package runtime

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// streamBufferBytes is what a stream gathers before it sends it as one chunk.
const streamBufferBytes = 64 << 10

// errAbandoned ends a stream's body without its last chunk, so the storage
// channel stores none of it.
var errAbandoned = errors.New("the node abandoned its output")

// StreamUpload sends one output to the platform's storage channel as it is
// written, in a PUT that names no length and that the channel stores in parts.
// A write that would take the output past what it may store is refused before
// any of it is sent. A stream abandoned, or failed, leaves the channel a body
// that never ends, so nothing of it is stored.
type StreamUpload struct {
	url      string
	maxBytes int64
	body     *io.PipeWriter
	buffer   *bufio.Writer
	cancel   context.CancelFunc
	sent     int64
	// err is why the stream ended, nil while it can be written.
	err error

	// done is closed once the channel answered or the request failed, which
	// result then says.
	done   chan struct{}
	result error
}

// OpenStream starts a stream of contentType to the storage channel at url,
// which may store maxBytes, 0 for no limit.
func OpenStream(url string, contentType ContentType, maxBytes int64) *StreamUpload {
	reader, writer := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())

	s := &StreamUpload{
		url:      url,
		maxBytes: maxBytes,
		body:     writer,
		buffer:   bufio.NewWriterSize(writer, streamBufferBytes),
		cancel:   cancel,
		done:     make(chan struct{}),
	}

	go func() {
		defer close(s.done)

		s.result = s.send(ctx, reader, contentType)
	}()

	return s
}

// send makes the stream's request, which waits however long the channel takes
// to answer: the channel bounds its own transfers to the storage.
func (s *StreamUpload) send(ctx context.Context, body *io.PipeReader, contentType ContentType) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, s.url, body)
	if err != nil {
		_ = body.CloseWithError(err)

		return uploadedNothing(s.url, err)
	}

	req.ContentLength = -1
	req.Header.Set("Content-Type", contentType)

	resp, err := httpClient.Do(req)
	if err != nil {
		return uploadedNothing(s.url, err)
	}
	defer resp.Body.Close()

	return answered(s.url, resp)
}

// Write sends chunk, and refuses it, before any of it is sent, when it would
// take the output past what it may store.
func (s *StreamUpload) Write(chunk []byte) error {
	if s.err != nil {
		return s.err
	}

	if s.maxBytes > 0 && s.sent+int64(len(chunk)) > s.maxBytes {
		s.Abort()
		s.err = &OutputTooLarge{
			Message: fmt.Sprintf(
				"the node's output passed the %d bytes it may store, the lowest of its plan's limit for one node's output, "+
					"its max_output_mb and the room left in its organization's storage",
				s.maxBytes,
			),
		}

		return s.err
	}

	if _, err := s.buffer.Write(chunk); err != nil {
		s.err = s.ended()
		return s.err
	}

	s.sent += int64(len(chunk))

	return nil
}

// Finish ends the stream and returns how many bytes it sent, once the channel
// has stored them.
func (s *StreamUpload) Finish() (int64, error) {
	if s.err != nil {
		return 0, s.err
	}

	if err := s.buffer.Flush(); err != nil {
		s.err = s.ended()
		return 0, s.err
	}

	_ = s.body.Close()
	<-s.done

	if s.result != nil {
		s.err = s.result
		return 0, s.err
	}

	return s.sent, nil
}

// Abort abandons the stream: its body never ends, so the channel stores none
// of it. A stream finished or abandoned already is left as it is.
func (s *StreamUpload) Abort() {
	_ = s.body.CloseWithError(errAbandoned)
	s.cancel()
	<-s.done

	if s.err == nil {
		s.err = errAbandoned
	}
}

// ended is why a write found the request over: the channel's answer, or the
// failure of the request.
func (s *StreamUpload) ended() error {
	s.cancel()
	<-s.done

	if s.result != nil {
		return s.result
	}

	return uploadedNothing(s.url, errors.New("the storage channel answered before the output was sent"))
}
