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

// stream initiates a GET request and returns a stream reader.
func stream(rawURL string) (io.ReadCloser, error) {
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
	}, nil
}

// streamBody maps read errors to infrastructure failures and cleans up the watchdog on close.
type streamBody struct {
	body io.ReadCloser
	stop func()
	url  string
}

func (s *streamBody) Read(p []byte) (int, error) {
	n, err := s.body.Read(p)
	if err != nil && err != io.EOF {
		return n, unreachable(s.url, err)
	}

	return n, err
}

func (s *streamBody) Close() error {
	err := s.body.Close()
	s.stop()

	return err
}

// send delivers a payload with an explicit Content-Length header required by object storage providers.
func send(method, rawURL string, body []byte, contentType string) (*http.Response, error) {
	req, err := http.NewRequest(method, rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, uploadedNothing(rawURL, err)
	}

	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", contentType)

	resp, stop, err := do(req)
	if err != nil {
		return nil, uploadedNothing(rawURL, err)
	}

	defer stop()
	defer resp.Body.Close()

	answer, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, uploadedNothing(rawURL, err)
	}

	resp.Body = io.NopCloser(bytes.NewReader(answer))

	return resp, nil
}

// put uploads a payload to a presigned PUT URL and returns the response ETag.
func put(rawURL string, body []byte, contentType string) (string, error) {
	resp, err := send(http.MethodPut, rawURL, body, contentType)
	if err != nil {
		return "", err
	}

	return resp.Header.Get("ETag"), nil
}

// post sends a payload using POST and returns the response body.
func post(rawURL string, body []byte, contentType string) ([]byte, error) {
	resp, err := send(http.MethodPost, rawURL, body, contentType)
	if err != nil {
		return nil, err
	}

	return io.ReadAll(resp.Body)
}

// del executes a best-effort DELETE request without failing the caller on error.
func del(rawURL string) {
	req, err := http.NewRequest(http.MethodDelete, rawURL, nil)
	if err != nil {
		return
	}

	resp, stop, err := do(req)
	if err != nil {
		return
	}

	defer stop()

	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}
