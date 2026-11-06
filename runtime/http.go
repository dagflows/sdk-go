package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Fallbacks for a host that sent no transfer block. The platform always does,
// so these only cover a local run against a hand written envelope.
const (
	DefaultConnTimeoutMs = 10_000
	DefaultIdleTimeoutMs = 60_000
)

// httpTimeout limits idle wait duration per network operation rather than the entire transfer.
var httpTimeout = time.Duration(DefaultIdleTimeoutMs) * time.Millisecond

// configureTransfer adopts the host's transfer timeouts. Called once, before
// the node runs.
func configureTransfer(connectMs, idleMs int64) {
	if idleMs > 0 {
		httpTimeout = time.Duration(idleMs) * time.Millisecond
	}

	if connectMs > 0 {
		dialer := &net.Dialer{Timeout: time.Duration(connectMs) * time.Millisecond, KeepAlive: 30 * time.Second}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.DialContext = dialer.DialContext
		httpClient.Transport = transport
	}
}

var httpClient = &http.Client{
	Transport: http.DefaultTransport,
}

// safe strips query parameters so signatures and auth tokens do not leak into logs.
func safe(rawURL string) string {
	before, _, _ := strings.Cut(rawURL, "?")

	return before
}

// describe extracts the underlying error description without embedded URLs.
func describe(err error) string {
	if urlErr, ok := errors.AsType[*url.Error](err); ok {
		err = urlErr.Err
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Sprintf("no data for %s", httpTimeout)
	}

	return err.Error()
}

// unreachable wraps read errors as retryable infrastructure failures.
func unreachable(rawURL string, err error) error {
	return &Fail{
		Message:  fmt.Sprintf("reading %s failed: %s", safe(rawURL), describe(err)),
		Category: INFRASTRUCTURE,
		Abort:    new(false),
	}
}

// notAsRecorded is a body that is not the length recorded for it, as a
// retryable infrastructure failure.
func notAsRecorded(rawURL, what string) error {
	return &Fail{
		Message:  fmt.Sprintf("reading %s failed: %s", safe(rawURL), what),
		Category: INFRASTRUCTURE,
		Abort:    new(false),
	}
}

// uploadedNothing wraps upload errors as retryable infrastructure failures.
func uploadedNothing(rawURL string, err error) error {
	return &Fail{
		Message:  fmt.Sprintf("uploading this node's output to %s failed: %s", safe(rawURL), describe(err)),
		Category: INFRASTRUCTURE,
		Abort:    new(false),
	}
}

// do executes an HTTP request with an idle watchdog timer and checks for status errors.
func do(req *http.Request) (*http.Response, func(), error) {
	ctx, cancel := context.WithCancel(req.Context())
	watchdog := time.AfterFunc(httpTimeout, cancel)

	stop := func() {
		watchdog.Stop()
		cancel()
	}

	resp, err := httpClient.Do(req.WithContext(ctx))
	if err != nil {
		stop()

		return nil, nil, err
	}

	if resp.StatusCode >= 400 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		stop()

		return nil, nil, statusError(resp.Status, detail, req.URL)
	}

	resp.Body = &idleBody{
		ReadCloser: resp.Body,
		watchdog:   watchdog,
	}

	return resp, stop, nil
}

// statusError includes provider error responses while scrubbing any signed query parameters.
func statusError(status string, body []byte, u *url.URL) error {
	text := strings.TrimSpace(string(body))

	for _, values := range u.Query() {
		for _, value := range values {
			if len(value) >= 8 {
				text = strings.ReplaceAll(text, value, "[scrubbed]")
			}
		}
	}

	if text == "" {
		return fmt.Errorf("HTTP %s", status)
	}

	return fmt.Errorf("HTTP %s: %s", status, text)
}

// idleBody resets the watchdog timer on each read to detect stalled connections.
type idleBody struct {
	io.ReadCloser
	watchdog *time.Timer
}

func (b *idleBody) Read(p []byte) (int, error) {
	b.watchdog.Reset(httpTimeout)

	return b.ReadCloser.Read(p)
}

// stream initiates a GET request and returns a stream reader. A body of size
// bytes, when size is above zero, fails as the platform's, to be retried,
// should it end short of it or run past it.
func stream(rawURL string, size int64) (io.ReadCloser, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, unreachable(rawURL, err)
	}

	resp, stop, err := do(req)
	if err != nil {
		return nil, unreachable(rawURL, err)
	}

	return &streamBody{
		body: resp.Body,
		stop: stop,
		url:  rawURL,
		size: size,
	}, nil
}

// streamBody maps read errors to infrastructure failures, holds the body to the
// size recorded for it, and cleans up the watchdog on close.
type streamBody struct {
	body io.ReadCloser
	stop func()
	url  string
	// size is the body's recorded size, 0 when none was recorded, and read is
	// how much of it was read.
	size int64
	read int64
}

func (s *streamBody) Read(p []byte) (int, error) {
	n, err := s.body.Read(p)
	s.read += int64(n)

	switch {
	case s.size > 0 && s.read > s.size:
		return n, notAsRecorded(s.url, fmt.Sprintf("it ran past the %d bytes recorded for it", s.size))
	case err == io.EOF && s.size > 0 && s.read < s.size:
		return n, notAsRecorded(s.url, fmt.Sprintf("it ended after %d of its %d bytes", s.read, s.size))
	case err != nil && err != io.EOF:
		return n, unreachable(s.url, err)
	}

	return n, err
}

func (s *streamBody) Close() error {
	err := s.body.Close()
	s.stop()

	return err
}

// store PUTs body, with its length, to the platform's storage channel at
// rawURL. It waits however long the channel takes to answer: the channel
// bounds its own transfers to the storage.
func store(rawURL string, body []byte, contentType ContentType) error {
	req, err := http.NewRequest(http.MethodPut, rawURL, bytes.NewReader(body))
	if err != nil {
		return uploadedNothing(rawURL, err)
	}

	req.Header.Set("Content-Type", contentType)

	resp, err := httpClient.Do(req)
	if err != nil {
		return uploadedNothing(rawURL, err)
	}
	defer resp.Body.Close()

	return answered(rawURL, resp)
}

// answered is what the storage channel's answer to an upload means: nothing
// once it stored the output, OutputTooLarge when it refused the output as over
// what it may store, and a retryable infrastructure failure otherwise.
func answered(rawURL string, resp *http.Response) error {
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusRequestEntityTooLarge:
		message := strings.TrimSpace(string(detail))
		if message == "" {
			message = "the platform refused the node's output as over what it may store"
		}

		return &OutputTooLarge{Message: message}
	}

	return uploadedNothing(rawURL, statusError(resp.Status, detail, resp.Request.URL))
}
