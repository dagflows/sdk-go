package runtime

import (
	"errors"
	"fmt"
	"iter"
	"reflect"
	"strings"
)

// Result holds a node output, content type, routing instructions, and optional metadata.
type Result struct {
	Output      any
	ContentType ContentType
	Next        []string
	Stop        bool

	// Meta holds user-defined node execution metadata for platforms supporting metadata ingestion.
	Meta map[string]any
}

// Upload specifies the target destination for offloading large payloads.
type Upload struct {
	URL string
	Key string
}

func (u *Upload) Offered() bool {
	return u != nil && u.URL != "" && u.Key != ""
}

// Written represents the final output block produced by a closed OutputStream.
type Written struct {
	block *Block
}

// Block represents a payload entry in the output envelope.
type Block struct {
	Type        string      `json:"type"`
	Ref         string      `json:"ref,omitempty"`
	Size        int64       `json:"size,omitzero"`
	ContentType ContentType `json:"content_type"`
	Data        any         `json:"data,omitzero"`
}

// Envelope represents the top-level success payload written to DAGFLOWS_OUTPUT.
type Envelope struct {
	Status string   `json:"status"`
	Output *Block   `json:"output"`
	Next   []string `json:"next,omitempty"`
	Stop   bool     `json:"stop,omitempty"`
}

// bufferBudget defines the fraction of node memory allowed for buffering records in RAM.
const bufferBudget = 0.25

// normaliseNext validates downstream routing keys, rejecting empty strings.
func normaliseNext(next []string) ([]string, error) {
	for _, key := range next {
		if strings.TrimSpace(key) == "" {
			return nil, errors.New("a routing key must be a non-empty node key; use Stop: true to halt")
		}
	}

	return next, nil
}

// lazyRows adapts iterator functions to the standard runtime rows iterator.
func lazyRows(output any) (rows, bool) {
	switch v := output.(type) {
	case nil:
		return nil, false

	case iter.Seq2[any, error]:
		return v, true

	case iter.Seq[any]:
		return func(yield func(any, error) bool) {
			for item := range v {
				if !yield(item, nil) {
					return
				}
			}
		}, true
	}

	return reflectRows(reflect.ValueOf(output))
}

var errorType = reflect.TypeFor[error]()

// reflectRows dynamically adapts functions matching iterator signatures using reflection.
func reflectRows(fn reflect.Value) (rows, bool) {
	kind := fn.Type()
	if kind.Kind() != reflect.Func || kind.NumIn() != 1 || kind.NumOut() != 0 || kind.IsVariadic() {
		return nil, false
	}

	yieldType := kind.In(0)
	if yieldType.Kind() != reflect.Func || yieldType.NumOut() != 1 || yieldType.Out(0).Kind() != reflect.Bool {
		return nil, false
	}

	withError := yieldType.NumIn() == 2 && yieldType.In(1) == errorType
	if yieldType.NumIn() != 1 && !withError {
		return nil, false
	}

	return func(yield func(any, error) bool) {
		adapter := reflect.MakeFunc(yieldType, func(args []reflect.Value) []reflect.Value {
			var err error

			if withError && !args[1].IsNil() {
				err = args[1].Interface().(error)
			}

			return []reflect.Value{reflect.ValueOf(yield(args[0].Interface(), err))}
		})

		fn.Call([]reflect.Value{adapter})
	}, true
}

// inferContentType determines the MIME type, prioritizing explicit content types.
func inferContentType(output any, stated ContentType) ContentType {
	if stated != "" {
		return stated
	}

	if _, ok := output.([]byte); ok {
		return BYTES
	}

	if _, ok := lazyRows(output); ok {
		return NDJSON
	}

	return JSON
}

// isSequence reports whether output is an in-memory slice or array.
func isSequence(output any) bool {
	if _, ok := output.([]byte); ok {
		return false
	}

	switch reflect.ValueOf(output).Kind() {
	case reflect.Slice, reflect.Array:
		return true
	}

	return false
}

// sequenceRows converts an in-memory slice or array into a rows iterator.
func sequenceRows(output any) rows {
	if list, ok := output.([]any); ok {
		return seqOf(list)
	}

	v := reflect.ValueOf(output)

	return func(yield func(any, error) bool) {
		for i := range v.Len() {
			if !yield(v.Index(i).Interface(), nil) {
				return
			}
		}
	}
}

// payload extracts the envelope data value, rejecting raw bytes in JSON envelopes.
func payload(output any, contentType ContentType) (any, error) {
	if output == nil {
		return map[string]any{}, nil
	}

	if _, ok := output.([]byte); ok {
		return nil, &OutputTooLarge{
			Message: "raw bytes cannot travel in the JSON envelope; return text or records, or write the object yourself and return its key",
		}
	}

	if isRows(contentType) {
		return []any{output}, nil
	}

	if _, ok := lazyRows(output); ok {
		return nil, fmt.Errorf("a lazy iterator cannot be sent as %s; give it a row content type such as NDJSON, or materialise it with slices.Collect", contentType)
	}

	if contentType == TEXT {
		return text(output), nil
	}

	return output, nil
}

// bufferCap computes maximum allowed bytes for memory buffering before forcing a spill or error.
func bufferCap(limit int64, upload *Upload, memoryLimitMB int) int64 {
	if upload.Offered() && memoryLimitMB > 0 {
		heap := float64(memoryLimitMB) * 1024 * 1024 * bufferBudget

		return max(limit, int64(heap/ParseExpansion))
	}

	return limit
}

// offload uploads payload to presigned storage and returns a REFERENCE block.
func offload(data any, contentType ContentType, inlineSize, limit int64, upload *Upload) (*Block, error) {
	if !upload.Offered() {
		return nil, &OutputTooLarge{
			Message: fmt.Sprintf(
				"the encoded output is %d bytes and this run's inline limit is %d, but this run was given nowhere to upload it",
				inlineSize, limit,
			),
		}
	}

	var body []byte
	var err error

	if isRows(contentType) {
		body, err = encodeRows(sequenceRows(data), contentType)
	} else {
		body, err = encodeValue(data, contentType)
	}

	if err != nil {
		return nil, err
	}

	if _, err := put(upload.URL, body, contentType); err != nil {
		return nil, err
	}

	return &Block{
		Type:        REFERENCE,
		Ref:         upload.Key,
		Size:        int64(len(body)),
		ContentType: contentType,
	}, nil
}

// buildBlock returns an INLINE block if data fits within limit, or offloads to storage.
func buildBlock(data any, contentType ContentType, limit int64, upload *Upload) (*Block, error) {
	inlineSize, err := encodedLen(newCompactEncoder(), data)
	if err != nil {
		return nil, err
	}

	if inlineSize > limit {
		return offload(data, contentType, inlineSize, limit, upload)
	}

	return &Block{
		Type:        INLINE,
		ContentType: contentType,
		Data:        data,
	}, nil
}

// RowSink accumulates streaming records, inlining, uploading in single PUT, or spilling to multipart.
type RowSink struct {
	contentType ContentType
	limit       int64
	cap         int64
	upload      *Upload
	multipart   *Multipart

	collected []any
	measured  int64
	sizer     *compactEncoder
	encoder   *RowEncoder
	uploader  *PartUploader
}

func NewRowSink(contentType ContentType, limit, cap int64, upload *Upload, multipart *Multipart) *RowSink {
	return &RowSink{
		contentType: contentType,
		limit:       limit,
		cap:         cap,
		upload:      upload,
		multipart:   multipart,
		collected:   []any{},
		measured:    2,
		sizer:       newCompactEncoder(),
	}
}

func (s *RowSink) Add(row any) error {
	if s.uploader != nil {
		encoded, err := s.encoder.Encode(row)
		if err != nil {
			return err
		}

		return s.uploader.Write(encoded)
	}

	size, err := encodedLen(s.sizer, row)
	if err != nil {
		return err
	}

	if len(s.collected) > 0 {
		size++
	}

	s.measured += size
	s.collected = append(s.collected, row)

	if s.measured <= s.limit {
		return nil
	}

	if s.multipart != nil {
		return s.spill()
	}

	if s.measured > s.cap {
		return &OutputTooLarge{
			Message: fmt.Sprintf(
				"the output passed %d bytes after %d rows, which is as much as this node can buffer; declare max_output_mb on the node to stream instead",
				s.cap, len(s.collected),
			),
		}
	}

	return nil
}

// Block finalizes buffered or multipart rows into an output block.
func (s *RowSink) Block() (*Block, error) {
	if s.uploader == nil {
		return buildBlock(s.collected, s.contentType, s.limit, s.upload)
	}

	if err := s.uploader.Write(s.encoder.Finish()); err != nil {
		return nil, err
	}

	size, err := s.uploader.Finish()
	if err != nil {
		return nil, err
	}

	ref := ""
	if s.upload != nil {
		ref = s.upload.Key
	}

	return &Block{
		Type:        REFERENCE,
		Ref:         ref,
		Size:        size,
		ContentType: s.contentType,
	}, nil
}

// Abort cleans up any open multipart upload on cancellation or error.
func (s *RowSink) Abort() {
	if s.uploader != nil {
		s.uploader.Abort()
	}
}

func (s *RowSink) spill() error {
	s.encoder = NewRowEncoder(s.contentType)
	s.uploader = NewPartUploader(s.multipart, s.contentType)

	for _, held := range s.collected {
		encoded, err := s.encoder.Encode(held)
		if err != nil {
			return err
		}

		if err := s.uploader.Write(encoded); err != nil {
			return err
		}
	}

	s.collected = nil

	return nil
}

// streamRows consumes an iterator into a RowSink, ensuring incomplete uploads are aborted on panic or error.
func streamRows(output rows, contentType ContentType, limit, cap int64, upload *Upload, multipart *Multipart) (*Block, error) {
	sink := NewRowSink(contentType, limit, cap, upload, multipart)

	committed := false
	defer func() {
		if !committed {
			sink.Abort()
		}
	}()

	for row, err := range output {
		if err == nil {
			err = sink.Add(row)
		}

		if err != nil {
			return nil, err
		}
	}

	block, err := sink.Block()
	committed = err == nil

	return block, err
}

// ToEnvelope formats the handler return value into a success envelope.
func ToEnvelope(value any, inlineMaxBytes int, upload *Upload, memoryLimitMB int, multipart *Multipart) (*Envelope, error) {
	var result Result

	switch v := value.(type) {
	case Result:
		result = v

	case *Result:
		if v != nil {
			result = *v
		}

	default:
		result = Result{Output: value}
	}

	limit := int64(inlineMaxBytes)
	if limit <= 0 {
		limit = DefaultInlineMaxBytes
	}

	var block *Block
	var err error

	if written, ok := result.Output.(*Written); ok && written != nil {
		block = written.block
	} else {
		contentType := inferContentType(result.Output, result.ContentType)
		cap := bufferCap(limit, upload, memoryLimitMB)

		switch {
		case isRows(contentType) && isSequence(result.Output):
			block, err = streamRows(sequenceRows(result.Output), contentType, limit, cap, upload, multipart)

		case isRows(contentType) && isLazy(result.Output):
			lazy, _ := lazyRows(result.Output)
			block, err = streamRows(lazy, contentType, limit, cap, upload, multipart)

		default:
			var data any

			data, err = payload(result.Output, contentType)
			if err == nil {
				block, err = buildBlock(data, contentType, limit, upload)
			}
		}

		if err != nil {
			return nil, err
		}
	}

	next, err := normaliseNext(result.Next)
	if err != nil {
		return nil, err
	}

	return &Envelope{
		Status: "SUCCESS",
		Output: block,
		Next:   next,
		Stop:   result.Stop,
	}, nil
}

func isLazy(output any) bool {
	_, ok := lazyRows(output)

	return ok
}
