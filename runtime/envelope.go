package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"maps"
	"os"
	"os/signal"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"
)

// RunInfo is which run and which node this is.
type RunInfo struct {
	WorkflowRunID  string
	NodeKey        string
	Attempt        int
	Language       string
	RuntimeVersion string
}

// Limits is what the platform allocated to this node.
type Limits struct {
	MemoryMB   int
	MilliCores int
	TimeoutMs  int64
}

// TriggerInfo is how an event-triggered run was started. The event itself is
// an input, keyed by the trigger.
type TriggerInfo struct {
	Kind       string
	ID         string
	ReceivedAt string
	Attributes map[string]any
}

// Ctx is the execution metadata for this node run.
//
// The exported fields are the wire's, and Raw preserves the platform fields
// this SDK does not model. Run, Limits and Trigger group them, Context is the
// cancellation signal, Log the console logger and Out the output writer. New
// capabilities arrive as new accessors here, never as new handler parameters.
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
	// ConnTimeoutMs and IdleTimeoutMs bound one network operation each,
	// never the whole transfer.
	ConnTimeoutMs int64
	IdleTimeoutMs int64
	// Capabilities is what this platform offers beyond the base contract,
	// such as "stream/v1".
	Capabilities []string
	Raw          map[string]any

	background context.Context
	cancel     context.CancelCauseFunc
}

// ErrStopped indicates that the platform requested the node to stop, accessible via context.Cause.
var ErrStopped = errors.New("the platform asked the node to stop")

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

	var capabilities []string
	if offered, ok := raw["capabilities"].([]any); ok {
		for _, item := range offered {
			capabilities = append(capabilities, Str(item))
		}
	}

	background, cancel := context.WithCancelCause(context.Background())

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
		MaxOutputMB:   int64(Num(transfer["max_output_mb"], 0)),
		ConnTimeoutMs: int64(Num(transfer["conn_timeout_ms"], DefaultConnTimeoutMs)),
		IdleTimeoutMs: int64(Num(transfer["idle_timeout_ms"], DefaultIdleTimeoutMs)),
		Capabilities:  capabilities,
		Raw:           raw,
		background:    background,
		cancel:        cancel,
	}
}

// Run returns which run and which node this is.
func (c *Ctx) Run() RunInfo {
	return RunInfo{
		WorkflowRunID:  c.WorkflowRunID,
		NodeKey:        c.NodeKey,
		Attempt:        c.Attempt,
		Language:       c.Language,
		RuntimeVersion: c.RuntimeVersion,
	}
}

// Limits returns what the platform allocated to this node.
func (c *Ctx) Limits() Limits {
	return Limits{MemoryMB: c.MemoryMB, MilliCores: c.MilliCores, TimeoutMs: c.TimeoutMs}
}

// Has reports whether the platform running this node offers a capability.
func (c *Ctx) Has(capability string) bool {
	return slices.Contains(c.Capabilities, capability)
}

// Trigger returns the delivery metadata of an event-triggered run, or nil when
// the run was not event-triggered.
func (c *Ctx) Trigger() *TriggerInfo {
	raw, ok := c.Raw["trigger"].(map[string]any)
	if !ok {
		return nil
	}

	attributes, _ := raw["attributes"].(map[string]any)

	return &TriggerInfo{
		Kind:       Str(raw["kind"]),
		ID:         Str(raw["id"]),
		ReceivedAt: Str(raw["received_at"]),
		Attributes: attributes,
	}
}

// Context returns the run's cancellation context. A handler that runs long
// polls it, or hands it to whatever it calls.
func (c *Ctx) Context() context.Context {
	if c.background == nil {
		return context.Background()
	}
	return c.background
}

// Cancel requests the run to stop with ErrStopped.
func (c *Ctx) Cancel() {
	if c == nil || c.cancel == nil {
		return
	}

	c.cancel(ErrStopped)
}

// watchSignals cancels the execution context on SIGINT or SIGTERM and returns a teardown function.
func (c *Ctx) watchSignals() func() {
	if c == nil || c.cancel == nil {
		return func() {}
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)

	watching := make(chan struct{})

	go func() {
		select {
		case <-signals:
			signal.Stop(signals)
			c.Cancel()
		case <-watching:
		}
	}()

	var once sync.Once

	return func() {
		once.Do(func() {
			signal.Stop(signals)
			close(watching)
		})
	}
}

var (
	logOnce sync.Once
	logger  *slog.Logger
)

// Log returns the node's logger. The console is the log path, so it writes to stdout.
func (c *Ctx) Log() *slog.Logger {
	logOnce.Do(func() {
		logger = slog.New(slog.NewTextHandler(os.Stdout, nil))
	})
	return logger
}

// Multipart returns the presigned multipart upload configuration if available.
func (c *Ctx) Multipart() *Multipart {
	return multipartFromCtx(c.Raw["output_multipart"])
}

// Out returns the output writer, for a node that decides routing while it writes.
func (c *Ctx) Out(contentType ContentType) *OutputStream {
	return c.OutputStream(contentType)
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

// Input is a parent node's output, resolved lazily on demand and decoded into T.
//
// Reached through the parent's handle, Get(inputs, parent), T is what that node
// declared it produces; reached through a key it is any.
type Input[T any] struct {
	key           string
	entry         map[string]any
	memoryLimitMB int
	held          any
	hasValue      bool
	missing       error
}

// Typed returns the same input, decoded into T.
func Typed[T any](in *Input[any]) *Input[T] {
	return &Input[T]{
		key:           in.key,
		entry:         in.entry,
		memoryLimitMB: in.memoryLimitMB,
		held:          in.held,
		hasValue:      in.hasValue,
		missing:       in.missing,
	}
}

// ValueType returns T, the type this handle decodes into. The manifest emitter
// calls it by name through reflection, which is how a lazy handle states the
// element type it expects of its parent.
func (in *Input[T]) ValueType() reflect.Type {
	return reflect.TypeFor[T]()
}

// Key returns the parent node key this input came from.
func (in *Input[T]) Key() string {
	return in.key
}

// URL returns where a reference block's payload is stored, empty for an inline block.
func (in *Input[T]) URL() string {
	return Str(in.entry["url"])
}

// Type returns the block type, defaulting to INLINE.
func (in *Input[T]) Type() string {
	if kind := Str(in.entry["type"]); kind != "" {
		return kind
	}

	return INLINE
}

// ContentType returns the payload content type, defaulting to JSON.
func (in *Input[T]) ContentType() ContentType {
	if kind := Str(in.entry["content_type"]); kind != "" {
		return kind
	}

	return JSON
}

// Size returns the payload size in bytes as the envelope states it, 0 when unstated.
func (in *Input[T]) Size() int64 {
	return Num(in.entry["size"], 0)
}

func (in *Input[T]) isReference() bool {
	return in.Type() == REFERENCE
}

// refuseUnknownType rejects a block type this runtime does not know. Reading an
// unknown type as inline would hand the handler the missing data field, so a
// node baked against an older SDK would report success on nothing.
func (in *Input[T]) refuseUnknownType() error {
	if in.missing != nil {
		return in.missing
	}

	kind := in.Type()
	if kind == INLINE || kind == REFERENCE {
		return nil
	}

	return &InputUnavailable{
		Message: fmt.Sprintf(
			"'%s' is %s, which this runtime does not understand; rebuild the node against a newer SDK",
			in.key, kind,
		),
	}
}

// Value materializes the input, decoded into T. Refuses references that
// exceed memory limits before downloading.
func (in *Input[T]) Value() (T, error) {
	var zero T

	raw, err := in.raw()
	if err != nil {
		return zero, err
	}

	if reflect.TypeFor[T]() == anyType {
		return any(raw).(T), nil
	}

	return convert[T](raw, in.key)
}

func (in *Input[T]) raw() (any, error) {
	if err := in.refuseUnknownType(); err != nil {
		return nil, err
	}

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

func (in *Input[T]) readAll() ([]byte, error) {
	body, err := in.Bytes()
	if err != nil {
		return nil, err
	}

	defer body.Close()

	return io.ReadAll(body)
}

// Bytes returns a stream reader for the raw payload.
func (in *Input[T]) Bytes() (io.ReadCloser, error) {
	if err := in.refuseUnknownType(); err != nil {
		return nil, err
	}

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

// Iter yields records from the input as decoded JSON values, streaming
// on-demand for references. Rows[E] on a handler's input is the typed form.
func (in *Input[T]) Iter() rows {
	return func(yield func(any, error) bool) {
		if err := in.refuseUnknownType(); err != nil {
			yield(nil, err)
			return
		}

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

// Stream returns the input's records decoded into E, one at a time. It is the
// typed counterpart of Iter for a handle to a rows parent.
func Stream[E any, T any](in *Input[T]) Rows[E] {
	return func(yield func(E, error) bool) {
		for row, err := range in.Iter() {
			var item E
			if err == nil {
				err = convertInto(row, &item, in.key)
			}
			if !yield(item, err) {
				return
			}
		}
	}
}

// records iterates an already materialized payload using stream rules.
func (in *Input[T]) records(stored any, kind ContentType, reference bool) rows {
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

// refuseIfTooBig refuses a reference whose decoded form would not fit the
// node's memory budget, measured as its stated size times ParseExpansion.
func (in *Input[T]) refuseIfTooBig() error {
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

func (in *Input[T]) String() string {
	if in.isReference() {
		return fmt.Sprintf("<Input '%s' REFERENCE %dB %s>", in.key, in.Size(), in.ContentType())
	}

	return fmt.Sprintf("<Input '%s' INLINE %s>", in.key, in.ContentType())
}

// Inputs is the collection of parent node outputs, keyed by node key.
type Inputs struct {
	entries       map[string]map[string]any
	memoryLimitMB int
}

// NewInputs returns the parent collection for the envelope's input entries,
// carrying the node's memory limit into every handle it hands out.
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

// Get returns the untyped input handle for the given node key.
func (in *Inputs) Get(key string) (*Input[any], error) {
	entry, ok := in.entries[key]
	if !ok {
		return nil, fmt.Errorf("no input named '%s', this node's parents are: %s", key, in.available())
	}

	return &Input[any]{
		key:           key,
		entry:         entry,
		memoryLimitMB: in.memoryLimitMB,
	}, nil
}

// One returns the single input for nodes with exactly one parent.
func (in *Inputs) One() (*Input[any], error) {
	if len(in.entries) != 1 {
		return nil, fmt.Errorf("one() needs exactly one parent, this node has: %s", in.available())
	}

	for key := range in.entries {
		return in.Get(key)
	}

	return nil, errors.New("unreachable")
}

// Len returns how many parents this node has.
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

// Load reads and parses the input envelope, defaulting to the path named by
// DAGFLOWS_INPUT when none is given.
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
	configureTransfer(ctx.ConnTimeoutMs, ctx.IdleTimeoutMs)

	payload, _ := envelope["payload"].(map[string]any)
	entries, _ := payload["inputs"].(map[string]any)

	return ctx, NewInputs(entries, ctx.MemoryMB), nil
}
