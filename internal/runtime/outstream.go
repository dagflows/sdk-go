package runtime

import (
	"errors"
	"fmt"
)

// OutputStream buffers records and decides between inlining, uploading, or multipart streaming on Close.
type OutputStream struct {
	ContentType ContentType
	sink        *RowSink
	closed      bool
	written     *Written
}

// NewOutputStream creates a stream writer configured with inline thresholds and storage upload targets.
func NewOutputStream(contentType ContentType, inlineMaxBytes int, upload *Upload, memoryLimitMB int, multipart *Multipart) *OutputStream {
	limit := int64(inlineMaxBytes)
	if limit <= 0 {
		limit = DefaultInlineMaxBytes
	}

	return &OutputStream{
		ContentType: contentType,
		sink:        NewRowSink(contentType, limit, bufferCap(limit, upload, memoryLimitMB), upload, multipart),
	}
}

// Write buffers a single record to the stream.
func (o *OutputStream) Write(record any) error {
	if o.closed {
		return errors.New("write() after the stream closed; write every record before Close()")
	}

	if err := o.sink.Add(record); err != nil {
		o.abort()

		return err
	}

	return nil
}

// Close commits the stream, finalizing uploads and constructing the output block.
func (o *OutputStream) Close() error {
	if o.closed {
		return nil
	}

	o.closed = true

	block, err := o.sink.Block()
	if err != nil {
		o.sink.Abort()

		return err
	}

	o.written = &Written{
		block: block,
	}

	return nil
}

// Abort discards uncommitted stream data and aborts any active storage uploads.
func (o *OutputStream) Abort() {
	if o.written != nil {
		return
	}

	o.abort()
}

func (o *OutputStream) abort() {
	o.closed = true
	o.sink.Abort()
}

// Ref returns the final written output handle after Close has completed successfully.
func (o *OutputStream) Ref() (*Written, error) {
	if o.written == nil {
		return nil, errors.New("ref() before the stream closed; call it after Close(), and return it")
	}

	return o.written, nil
}

func (o *OutputStream) String() string {
	state := "open"

	switch {
	case o.written != nil:
		state = "written"

	case o.closed:
		state = "aborted"
	}

	return fmt.Sprintf("<OutputStream %s %s>", o.ContentType, state)
}
