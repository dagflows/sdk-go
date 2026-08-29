package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"maps"
	"os"
	"slices"
	"strings"
)

// Ctx holds execution metadata for the node run. Raw preserves unmodeled platform fields.
type Ctx struct {
	WorkflowRunID   string
	NodeKey         string
	Language        string
	RuntimeVersion  string
	ABI             string
	Entrypoint      string
	Config          map[string]any
	TimeoutMs       int64
	Attempt         int
	MemoryMB        int
	MilliCores      int
	InlineMaxBytes  int
	OutputUploadURL string
	OutputKey       string
	// MaxOutputMB is how much this node may emit, 0 when it declared nothing.
	MaxOutputMB int64
	// ConnectTimeoutMs and IdleTimeoutMs bound one network operation each,
	// never the whole transfer.
	ConnectTimeoutMs int64
	IdleTimeoutMs    int64
	Raw              map[string]any
}

// CtxFromRaw parses the raw context map, applying fallback defaults for missing fields.
func CtxFromRaw(raw map[string]any) *Ctx {
	if raw == nil {
		raw = map[string]any{}
	}

	config, _ := raw["config"].(map[string]any)
	if config == nil {
		config = map[string]any{}
	}

	transfer, _ := raw["transfer"].(map[string]any)

	return &Ctx{
		WorkflowRunID:   Str(raw["workflow_run_id"]),
		NodeKey:         Str(raw["node_key"]),
		Language:        Str(raw["language"]),
		RuntimeVersion:  Str(raw["runtime_version"]),
		ABI:             Str(raw["abi"]),
		Entrypoint:      Str(raw["entrypoint"]),
		Config:          config,
		TimeoutMs:       int64(Num(raw["timeout_ms"], 0)),
		Attempt:         int(Num(raw["attempt"], 0)),
		MemoryMB:        int(Num(raw["memory_mb"], 0)),
		MilliCores:      int(Num(raw["milli_cores"], 0)),
		InlineMaxBytes:  int(Num(raw["inline_max_bytes"], DefaultInlineMaxBytes)),
		OutputUploadURL: Str(raw["output_upload_url"]),
		OutputKey:       Str(raw["output_key"]),
		// A host that sent no transfer block is a local run; the defaults here
		// stand in for what the platform always states.
		MaxOutputMB:      int64(Num(transfer["max_output_mb"], 0)),
		ConnectTimeoutMs: int64(Num(transfer["connect_timeout_ms"], DefaultConnectTimeoutMs)),
		IdleTimeoutMs:    int64(Num(transfer["idle_timeout_ms"], DefaultIdleTimeoutMs)),
		Raw:              raw,
	}
}

// Multipart returns the presigned multipart upload configuration if available.
func (c *Ctx) Multipart() *Multipart {
	return multipartFromCtx(c.Raw["output_multipart"])
}

// OutputStream creates a streaming writer for nodes that emit dynamic output.
func (c *Ctx) OutputStream(contentType ContentType) *OutputStream {
	return NewOutputStream(contentType, c.InlineMaxBytes, c.upload(), c.MemoryMB, c.Multipart())
}

func (c *Ctx) upload() *Upload {
	return &Upload{
		URL: c.OutputUploadURL,
		Key: c.OutputKey,
	}
}

// Num converts an int, int64, float, or json.Number to int64, falling back to a default value.
func Num(value any, fallback int64) int64 {
	switch v := value.(type) {
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return n
		}

		if f, err := v.Float64(); err == nil {
			return int64(f)
		}

	case float64:
		return int64(v)

	case int:
		return int64(v)

	case int64:
		return v
	}

	return fallback
}

// Str formats a JSON value as a string, returning empty for nil.
func Str(value any) string {
	switch v := value.(type) {
	case nil:
		return ""

	case string:
		return v

	case json.Number:
		return v.String()
	}

	return fmt.Sprint(value)
}

// Input represents an upstream node output, resolved on demand.
type Input struct {
	key           string
	entry         map[string]any
	memoryLimitMB int
	held          any
	hasValue      bool
}

func (in *Input) Key() string {
	return in.key
}

func (in *Input) URL() string {
	return Str(in.entry["url"])
}

// Type returns the block type, defaulting to INLINE.
func (in *Input) Type() string {
	if kind := Str(in.entry["type"]); kind != "" {
		return kind
	}

	return INLINE
}

// ContentType returns the payload content type, defaulting to JSON.
func (in *Input) ContentType() ContentType {
	if kind := Str(in.entry["content_type"]); kind != "" {
		return kind
	}

	return JSON
}

func (in *Input) Size() int64 {
	return Num(in.entry["size"], 0)
}

func (in *Input) isReference() bool {
	return in.Type() == REFERENCE
}

// Value materializes the input. Refuses references that exceed memory limits before downloading.
func (in *Input) Value() (any, error) {
	if !in.isReference() {
		return in.entry["data"], nil
	}

	if in.hasValue {
		return in.held, nil
	}

	if err := in.refuseIfTooBig(); err != nil {
		return nil, err
	}

	kind := in.ContentType()

	var value any

	switch {
	case isRows(kind):
		collected := []any{}

		for row, err := range in.Iter() {
			if err != nil {
				return nil, err
			}

			collected = append(collected, row)
		}

		value = collected

	case kind == TEXT:
		raw, err := in.readAll()
		if err != nil {
			return nil, err
		}

		value = string(raw)

	case kind == JSON:
		body, err := in.Bytes()
		if err != nil {
			return nil, err
		}

		defer body.Close()

		parsed, err := DecodeJSONFrom(body)
		if err != nil {
			return nil, err
		}

		value = parsed

	default:
		return nil, &InputUnavailable{
			Message: fmt.Sprintf("'%s' is %s, which is not JSON; read it with .bytes()", in.key, kind),
		}
	}

	in.held, in.hasValue = value, true

	return value, nil
}

func (in *Input) readAll() ([]byte, error) {
	body, err := in.Bytes()
	if err != nil {
		return nil, err
	}

	defer body.Close()

	return io.ReadAll(body)
}

// Bytes returns a stream reader for the raw payload.
func (in *Input) Bytes() (io.ReadCloser, error) {
	if in.isReference() {
		return stream(in.URL())
	}

	stored := in.entry["data"]

	if text, ok := stored.(string); ok && in.ContentType() == TEXT {
		return io.NopCloser(strings.NewReader(text)), nil
	}

	encoded, err := compact(stored)
	if err != nil {
		return nil, err
	}

	return io.NopCloser(bytes.NewReader(encoded)), nil
}

// Iter yields records from the input, streaming on-demand for references.
func (in *Input) Iter() rows {
	return func(yield func(any, error) bool) {
		kind := in.ContentType()

		if !in.isReference() {
			in.records(in.entry["data"], kind, false)(yield)
			return
		}

		if in.hasValue {
			in.records(in.held, kind, true)(yield)
			return
		}

		var decode func(io.Reader) rows

		switch kind {
		case NDJSON:
			decode = iterNDJSON

		case CSV:
			decode = iterCSV

		case JSON:
			decode = iterJSONArray

		default:
			yield(nil, &InputUnavailable{
				Message: fmt.Sprintf("'%s' is %s, which has no records to iterate; read it with .bytes()", in.key, kind),
			})

			return
		}

		body, err := stream(in.URL())
		if err != nil {
			yield(nil, err)
			return
		}

		defer body.Close()

		decode(body)(yield)
	}
}

// records iterates an already materialized payload using stream rules.
func (in *Input) records(stored any, kind ContentType, reference bool) rows {
	if isRows(kind) {
		if list, ok := stored.([]any); ok {
			return seqOf(list)
		}

		return seqOf([]any{stored})
	}

	if reference && kind == JSON {
		if list, ok := stored.([]any); ok {
			return seqOf(list)
		}

		return func(yield func(any, error) bool) {
			yield(nil, fmt.Errorf("'%s' is a JSON object, which cannot be streamed as rows; send ndjson or read it with .value()", in.key))
		}
	}

	return seqOf([]any{stored})
}

func seqOf(items []any) rows {
	return func(yield func(any, error) bool) {
		for _, item := range items {
			if !yield(item, nil) {
				return
			}
		}
	}
}

func (in *Input) refuseIfTooBig() error {
	budget := int64(in.memoryLimitMB) * 1024 * 1024

	if budget > 0 && in.Size()*ParseExpansion > budget {
		return &InputTooLarge{
			Message: fmt.Sprintf(
				"'%s' is %d bytes and this node has %d MB, so materialising it would not fit; iterate it instead, or read it with .bytes()",
				in.key, in.Size(), in.memoryLimitMB,
			),
		}
	}

	return nil
}

func (in *Input) String() string {
	if in.isReference() {
		return fmt.Sprintf("<Input '%s' REFERENCE %dB %s>", in.key, in.Size(), in.ContentType())
	}

	return fmt.Sprintf("<Input '%s' INLINE %s>", in.key, in.ContentType())
}

// Inputs contains parent node outputs, keyed by node name.
type Inputs struct {
	entries       map[string]map[string]any
	memoryLimitMB int
}

// NewInputs wraps input entries with node memory limits.
func NewInputs(entries map[string]any, memoryLimitMB int) *Inputs {
	in := &Inputs{
		entries:       make(map[string]map[string]any, len(entries)),
		memoryLimitMB: memoryLimitMB,
	}

	for key, raw := range entries {
		entry, _ := raw.(map[string]any)
		if entry == nil {
			entry = map[string]any{}
		}

		in.entries[key] = entry
	}

	return in
}

// Get returns the input handle for the given node key.
func (in *Inputs) Get(key string) (*Input, error) {
	entry, ok := in.entries[key]
	if !ok {
		return nil, fmt.Errorf("no input named '%s', this node's parents are: %s", key, in.available())
	}

	return &Input{
		key:           key,
		entry:         entry,
		memoryLimitMB: in.memoryLimitMB,
	}, nil
}

// One returns the single input for nodes with exactly one parent.
func (in *Inputs) One() (*Input, error) {
	if len(in.entries) != 1 {
		return nil, fmt.Errorf("one() needs exactly one parent, this node has: %s", in.available())
	}

	for key := range in.entries {
		return in.Get(key)
	}

	return nil, errors.New("unreachable")
}

func (in *Inputs) Len() int {
	return len(in.entries)
}

// Keys yields parent keys in sorted order.
func (in *Inputs) Keys() iter.Seq[string] {
	return slices.Values(slices.Sorted(maps.Keys(in.entries)))
}

func (in *Inputs) available() string {
	if len(in.entries) == 0 {
		return "none"
	}

	return strings.Join(slices.Sorted(maps.Keys(in.entries)), ", ")
}

// Load reads and parses the input envelope from the filesystem.
func Load(path string) (*Ctx, *Inputs, error) {
	if path == "" {
		path = os.Getenv(InputEnv)
	}

	if path == "" {
		return nil, nil, fmt.Errorf("%s is not set, run this node through the platform, or set it to a fixture file to reproduce a run locally", InputEnv)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}

	var envelope map[string]any

	if len(bytes.TrimSpace(body)) > 0 {
		parsed, err := DecodeJSON(body)
		if err != nil {
			return nil, nil, fmt.Errorf("%s is not a JSON envelope: %w", path, err)
		}

		envelope, _ = parsed.(map[string]any)
	}

	rawCtx, _ := envelope["ctx"].(map[string]any)
	ctx := CtxFromRaw(rawCtx)
	// The transport adopts the host's limits before the node moves any bytes.
	configureTransfer(ctx.ConnectTimeoutMs, ctx.IdleTimeoutMs)

	payload, _ := envelope["payload"].(map[string]any)
	entries, _ := payload["inputs"].(map[string]any)

	return ctx, NewInputs(entries, ctx.MemoryMB), nil
}
