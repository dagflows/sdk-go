# dagflows

Go SDK for authoring and running Dagflows workflows.

## Installation

<!-- not-tested: fetches from the module proxy -->
```bash
go get github.com/dagflows/sdk-go
```

Requires Go 1.27+: `wf.Node` is a generic method, so an editor needs a gopls that knows 1.27 or it will underline what compiles. The module depends on nothing outside the standard library, because everything it imports is linked into every tenant's node.

## Quickstart

A Go node is a precompiled static binary. One binary declares the workflow, registers every node, and dispatches on the node key the platform names. One import gives you everything a node touches; `df` is the conventional alias:

```go
// file: main.go
package main

import df "github.com/dagflows/sdk-go"

type Order struct {
	ID     int64 `json:"id"`
	Amount int64 `json:"amount"`
}

type Orders struct {
	Orders []Order `json:"orders"`
}

type Totals struct {
	Total int64 `json:"total"`
}

func fetchOrders(*df.Ctx, df.None) (Orders, error) {
	return Orders{Orders: []Order{{ID: 1, Amount: 100}, {ID: 2, Amount: 250}}}, nil
}

func calculateTotals(_ *df.Ctx, orders Orders) (Totals, error) {
	var total int64
	for _, order := range orders.Orders {
		total += order.Amount
	}
	return Totals{Total: total}, nil
}

func main() {
	wf := df.NewWorkflow("order_pipeline", df.WorkflowOptions{MaxConcurrentNodes: 4})

	fetched := wf.Node(fetchOrders, df.Root, df.NodeOptions{Key: "fetch_orders"})
	wf.Node(calculateTotals, fetched, df.NodeOptions{Key: "calculate_totals"})

	df.Main()
}
```

`df.Main()` never returns. With `DAGFLOWS_INPUT` set it runs the node the envelope names and exits 0, reporting through `DAGFLOWS_OUTPUT`. Otherwise it answers the build and dev commands below.

### Handler signature

Every handler has one shape, and the compiler is the binding validator:

```go
func step(ctx *df.Ctx, in In) (Out, error)
```

`In` is what the node reads and `Out` what it produces. The second argument to `wf.Node` is the **edge**: where `In` comes from. A parent's handle is the edge of a one-parent node, and it carries the parent's output type, so `wf.Node(calculateTotals, fetched)` compiles only when `calculateTotals` takes what `fetchOrders` returns. Nothing is declared twice.

| the handler takes | the edge | what arrives |
| --- | --- | --- |
| `df.None` | `df.Root` | nothing: the node has no parents |
| a type such as `Orders` | the parent's handle | the parent's output, decoded into it |
| `df.Rows[Line]` | the parent's handle | the parent's records one at a time, each decoded, however large the parent is |
| `*df.Input[Orders]` | `df.Lazy(parent)` | the lazy handle: `Size()`, `Bytes()`, `Value()` |
| `[]byte` | the parent's handle | the parent's bytes, whole |
| `*df.Inputs` | `df.Depends(a, b, ...)` | the bag, one handle per parent |
| `any` | the parent's handle | the value as plain JSON; numbers are `json.Number` |

Name what you do not use `_`. The node key defaults to the function's name; set `Key` to choose another. Invalid keys, duplicate keys and negative settings are reported by `build manifest` and `build validate`, as one clean line at your terminal.

### Typed nodes

Both ends of every edge are reflected into the manifest, as a JSON Schema subset the builder checks before a run starts, including edges into nodes written in Python or TypeScript:

```go
// file: typed/main.go
package main

import df "github.com/dagflows/sdk-go"

type Order struct {
	ID     int64 `json:"id"`
	Amount int64 `json:"amount"`
}

type Orders struct {
	Orders []Order `json:"orders"`
}

type Totals struct {
	Total int64 `json:"total"`
}

type Line struct {
	ID    int64 `json:"id"`
	Cents int64 `json:"cents"`
}

type Report struct {
	Orders int   `json:"orders"`
	Total  int64 `json:"total"`
}

var wf = df.NewWorkflow("typed", df.WorkflowOptions{})

func fetchOrders(*df.Ctx, df.None) (Orders, error) {
	return Orders{Orders: []Order{{ID: 1, Amount: 100}, {ID: 2, Amount: 250}}}, nil
}

func calculateTotals(_ *df.Ctx, orders Orders) (Totals, error) {
	var total int64
	for _, order := range orders.Orders {
		total += order.Amount
	}
	return Totals{Total: total}, nil
}

func exportLines(_ *df.Ctx, orders Orders) (df.Rows[Line], error) {
	return func(yield func(Line, error) bool) {
		for _, order := range orders.Orders {
			if !yield(Line{ID: order.ID, Cents: order.Amount * 100}, nil) {
				return
			}
		}
	}, nil
}

func countLines(_ *df.Ctx, lines df.Rows[Line]) (int, error) {
	n := 0
	for _, err := range lines {
		if err != nil {
			return 0, err
		}
		n++
	}
	return n, nil
}

func peek(_ *df.Ctx, orders *df.Input[Orders]) (map[string]any, error) {
	value, err := orders.Value()
	if err != nil {
		return nil, err
	}
	return map[string]any{"bytes": orders.Size(), "first": value.Orders[0].ID}, nil
}

func report(_ *df.Ctx, inputs *df.Inputs) (Report, error) {
	orders, err := df.Get(inputs, fetched).Value() // Orders, typed by the handle
	if err != nil {
		return Report{}, err
	}
	totals, err := df.Get(inputs, totalled).Value() // Totals
	if err != nil {
		return Report{}, err
	}
	return Report{Orders: len(orders.Orders), Total: totals.Total}, nil
}

var (
	fetched  = wf.Node(fetchOrders, df.Root, df.NodeOptions{Key: "fetch_orders"})
	totalled = wf.Node(calculateTotals, fetched, df.NodeOptions{Key: "calculate_totals"})
	exported = wf.Node(exportLines, fetched, df.NodeOptions{Key: "export_lines"})
	_        = wf.Node(countLines, exported, df.NodeOptions{Key: "count_lines"})
	_        = wf.Node(peek, df.Lazy(fetched))
	_        = wf.Node(report, df.Depends(fetched, totalled))
)

func main() { df.Main() }
```

A handle is a `*df.NodeRef[T]` where `T` is what the node produces, so `fetched` is a `*df.NodeRef[Orders]` and every place it is used is typed by it: as the edge of `calculateTotals`, inside `df.Lazy`, and through `df.Get` in the bag. A node with several parents takes `*df.Inputs` and reaches each parent through its handle; `inputs.Get("fetch_orders")` still works and is untyped.

Decoding is `encoding/json` with numbers kept exact: struct tags name the fields, `omitempty`, `omitzero` and pointers make a field optional, a pointer is nullable, `time.Time` is RFC 3339, `[]byte` is base64, `df.Decimal` is an exact decimal as a string, and a named string type declares its values by implementing `DagflowsSchema() df.Schema` with `df.Enum`. A parent that does not fit the type fails the run naming the input and the field, rather than as a zero value three calls later. A handler over `any` is never refused; its types are simply "anything".

The manifest carries what each node expects and produces:

```json
"io": {
  "inputs": {"fetch_orders": {"shape": "value", "schema": {"type": "object", "title": "Orders", "...": "..."}}},
  "output": {"shape": "value", "content_type": "application/json", "schema": {"type": "object", "title": "Totals", "...": "..."}}
}
```

A type the manifest cannot carry is refused while it is emitted, so it fails the build rather than the first run: a struct that refers to itself, a map with non-string keys, a `df.Rows` inside a field, each named with the field.

## Node configuration

### Resource limits and retries

```go
// file: limits/main.go
package main

import df "github.com/dagflows/sdk-go"

func extractData(*df.Ctx, df.None) (map[string]any, error) {
	return map[string]any{"rows": 0}, nil
}

func main() {
	wf := df.NewWorkflow("data_pipeline", df.WorkflowOptions{
		// Every node inherits this; a node stating its own overrides it field
		// by field, so the settings it leaves out still apply.
		Retry: &df.Retry{MaxAttempts: new(3), InitialBackoffMs: new(1000)},
	})

	wf.Node(extractData, df.Root, df.NodeOptions{
		Key:       "extract_data",
		Execution: &df.Execution{Machine: "gp-8", TimeoutSecs: 300},
		// Only the platform's own failures are retried unless a node says
		// otherwise. Settings are pointers so leaving one out means "let the
		// platform decide" rather than zero, and new(5) states one inline.
		Retry: &df.Retry{
			MaxAttempts: new(5),
			RetryOn:     []df.RetryCategory{df.RetryOnInfrastructure, df.RetryOnTimeout},
		},
	})

	df.Main()
}
```

Use `Machine` to select a resource tier from the platform catalog (e.g. `"gp-1"`, `"gp-2"`, `"gp-4"`, `"gp-8"`, `"gp-16"`). The name carries the memory in GB, so `gp-8` is the 8 GB tier. Set `TimeoutSecs` to cap node execution duration in seconds, and declare `MaxOutputMB` if your node needs multipart upload capabilities for large outputs.

`MaxAttempts` counts the first run, so `1` means run once and do not retry. Backoff doubles per attempt up to `MaxBackoffMs`.

Leaving a setting out is not the same as setting it to zero, which is why they are pointers: unstated means the platform decides, and that is what lets a workflow default reach a node at all. `new(5)` states one inline.

By default only the platform's own failures are retried. `RetryOn` widens that, and `RetryCategory` names what can be asked for:

| constant | what it means |
| --- | --- |
| `RetryOnInfrastructure` | the platform could not run the node |
| `RetryOnTimeout` | the node ran out of its time budget |
| `RetryOnExecution` | the node itself failed |

There is no constant for `permanent` on purpose: the platform reports it when running the node again cannot change the outcome, so asking to retry it is refused.

### Cross-project dependencies

Edges take the handles `Node` returned, never strings, so a typo fails to compile. A node in another project is the one thing you name by key, and the type you give it is what this project expects of it, which the builder checks against what that project's SDK declared:

```go
inventory := wf.External[Inventory]("inventory_check")
wf.Node(fulfillOrder, df.Depends(fetched, inventory), df.NodeOptions{Key: "fulfill_order"})
```

A typed external handle is an edge like any other, so `wf.Node(crunch, wf.External[df.Rows[Record]]("extract"))` hands `crunch` the other project's rows one at a time. `wf.ExternalNode(key)` expects nothing.

## Working with inputs

A handle is lazy: nothing is fetched until asked.

```go
upstream := df.Get(inputs, fetched) // *df.Input[Orders]

// The complete value, decoded, for an input small enough to hold
orders, err := upstream.Value()

// One record at a time, whatever the input's size
for record, err := range df.Stream[Order](upstream) {
	if err != nil {
		return nil, err
	}
	process(record)
}

// Raw bytes for opaque content: a reader, not a byte slice
body, err := upstream.Bytes()
defer body.Close()
io.Copy(sink, body)
```

Reach for the rows first: `df.Rows[T]` as the handler's input, or `df.Stream[T]` on a handle. Either reads an inline handful of rows and a stored file of any size through the same loop, so a node written this way keeps working when its parent grows. `Value` is the convenience for an input you know is small: it holds the whole payload in memory and is refused when it exceeds the node's memory limit.

Iteration yields one **record** at a time for row oriented payloads, which is what lets a node read more than it can hold. A JSON input has no records, so it yields the whole document as a single item.

| content type | `for x, err := range handle.Iter()` yields |
| --- | --- |
| `application/x-ndjson`, `text/csv` | one row at a time |
| `application/json` | the whole document, once |

`Iter` yields records as plain JSON; `df.Stream[T]` decodes each into `T`. Iteration re-opens the source each pass, so looping twice costs a second read rather than silently yielding nothing the second time.

If a node has exactly one parent and takes the bag, use `inputs.One()`:

```go
single, err := inputs.One()
```

## Producing outputs

Return a value directly, or a `df.Result` for routing and metadata:

```go
// A plain value
return Totals{Total: 42}, nil

// Route downstream execution to a specific branch
return df.Result[Decision]{Output: decision, Next: []string{"process_payment"}}, nil

// Or to several; children not named are skipped
return df.Result[Decision]{Output: decision, Next: []string{"process_payment", "notify"}}, nil

// Halt this branch
return df.Result[Decision]{Stop: true}, nil

// Metadata stored with the run, for whoever reads it later
return df.Result[Decision]{Output: decision, Meta: map[string]any{"reviewer": "ana"}}, nil
```

`Next` absent means every child runs; a list runs exactly the children it names, in graph order, and skips the rest; `Stop` skips them all. A handler that returns a `Result` registers with `wf.NodeResult`, and its handle is still typed by the payload, not the wrapper:

```go
func approve(_ *df.Ctx, totals Totals) (df.Result[Decision], error) {
	return df.Result[Decision]{Output: Decision{Approved: totals.Total < 1000}, Next: []string{"process_payment"}}, nil
}

var approved = wf.NodeResult(approve, totalled) // *df.NodeRef[Decision]
```

### Rows

A handler that returns `df.Rows[T]` (an `iter.Seq2[T, error]`) produces rows: they are encoded lazily and offloaded to storage when they outgrow the inline limit. You never choose inline versus reference: the platform decides from what it offered the run and what the node returned. A handler over `any` may return any `iter.Seq[T]` or `iter.Seq2[T, error]` to the same effect.

Two rules apply to a rows output:

- **One yield is one row.** The output is NDJSON, even where a sequence yields a single value once. Return the value itself when a node produces one.
- **A rows node is atomic.** A failure at row 900 replays from row 0; rows already sent are discarded, never resumed.

Laziness on its own does not bound what the node holds. Declare `Transfer{MaxOutputMB: ...}` for that: it is what asks the platform for a multipart upload, which is what lets parts leave as they fill instead of the whole output being buffered for a single request.

```go
wf.Node(exportAll, fetched, df.NodeOptions{
	Transfer: &df.Transfer{MaxOutputMB: 4096},
})
```

Without it a large output is refused rather than truncated, naming `max_output_mb` as the remedy.

To decide routing from what was streamed, write through `ctx.Out`:

```go
type exported = df.Result[*df.Written]

func export(ctx *df.Ctx, parent *df.Input[Order]) (exported, error) {
	out := ctx.Out(df.NDJSON)
	defer out.Abort()

	written := 0
	for record, err := range parent.Iter() {
		if err != nil {
			return exported{}, err
		}
		if err := out.Write(record); err != nil {
			return exported{}, err
		}
		written++
	}
	if err := out.Close(); err != nil {
		return exported{}, err
	}

	ref, err := out.Ref()
	if err != nil {
		return exported{}, err
	}
	return exported{Output: ref, Next: []string{branchFor(written)}}, nil
}
```

`Close` commits; the deferred `Abort` discards a partial upload when the handler fails first, and does nothing after a successful `Close`.

## Error handling

Return a `*df.Fail` to signal a structured failure with retry instructions. Any other error is reported as permanent:

```go
return nil, &df.Fail{Message: "Payment gateway unavailable", Category: df.EXECUTION, RetryAfterMs: 30_000}

// A stable machine readable name and any JSON that explains it travel with the message
return nil, &df.Fail{Message: "card declined", Category: df.EXECUTION, Code: "card_declined", Details: map[string]any{"last4": "4242"}}
```

Naming a delay implies "retry me"; naming nothing aborts. `Abort: new(false)` with no delay leaves retry timing to the workflow's policy. Categories: `EXECUTION`, `INFRASTRUCTURE`, `TIMEOUT`, `PERMANENT`. Anything else collapses to permanent, because an unknown category must never silently become retryable.

Errors the SDK returns are matchable with `errors.AsType` and `errors.Is`: `InputTooLarge`, `InputUnavailable`, `OutputTooLarge`.

## The context

`ctx` is everything about the run that is not business data. Its groups are accessors, so a new capability arrives as a new accessor, never as a new handler parameter:

| accessor | what it is |
| --- | --- |
| `ctx.Run()` | `WorkflowRunID`, `NodeKey`, `Attempt`, `Language`, `RuntimeVersion` |
| `ctx.Limits()` | `MemoryMB`, `MilliCores`, `TimeoutMs` |
| `ctx.Config` | the node's own config block from the manifest |
| `ctx.Trigger()` | delivery metadata of an event-triggered run: `Kind`, `ID`, `ReceivedAt`, `Attributes`; nil otherwise |
| `ctx.Has("stream/v1")` | whether this platform offers a capability beyond the base contract |
| `ctx.Context()` | cancelled on SIGTERM or SIGINT, so a long node can stop cleanly |
| `ctx.Log()` | a `*slog.Logger` on the console, which is the log path |
| `ctx.Out(contentType)` | the output writer, for routing decided while writing |

The first stop signal reaches the handler as cancellation and nothing else, and `context.Cause(ctx.Context())` says so. A second one terminates the process as it otherwise would, so a node that ignores cancellation stays interruptible.

## Long-running nodes

A node whose handler takes rows in and yields rows out is written the same way whether it runs once or stays alive. `Mode: df.ModeStream` asks the platform to keep it running and feed it live, with backpressure and cancellation through `ctx.Context()`, on a platform that offers channels; on one that does not, it runs exactly as a `df.ModeOnce` node would, so the code is the same either way:

```go
// file: streaming/main.go
package main

import df "github.com/dagflows/sdk-go"

type Tick struct {
	Symbol string  `json:"symbol"`
	Price  float64 `json:"price"`
}

type Enriched struct {
	Symbol  string  `json:"symbol"`
	Price   float64 `json:"price"`
	Doubled float64 `json:"doubled"`
}

var wf = df.NewWorkflow("streaming", df.WorkflowOptions{})

func ticks(*df.Ctx, df.None) (df.Rows[Tick], error) {
	return func(yield func(Tick, error) bool) {
		for _, price := range []float64{1, 2} {
			if !yield(Tick{Symbol: "ABC", Price: price}, nil) {
				return
			}
		}
	}, nil
}

func enrich(ctx *df.Ctx, quotes df.Rows[Tick]) (df.Rows[Enriched], error) {
	return func(yield func(Enriched, error) bool) {
		for tick, err := range quotes {
			if err != nil {
				yield(Enriched{}, err)
				return
			}
			if ctx.Context().Err() != nil {
				return
			}
			if !yield(Enriched{Symbol: tick.Symbol, Price: tick.Price, Doubled: tick.Price * 2}, nil) {
				return
			}
		}
	}, nil
}

var (
	quoted = wf.Node(ticks, df.Root)
	_      = wf.Node(enrich, quoted, df.NodeOptions{Mode: df.ModeStream})
)

func main() { df.Main() }
```

## Event triggers

A trigger is a typed source in the graph. The event is the input of every node that depends on it, and its delivery metadata is `ctx.Trigger()`, so business data and platform metadata never mix:

```go
// file: events/main.go
package main

import df "github.com/dagflows/sdk-go"

type OrderPlaced struct {
	OrderID string `json:"order_id"`
	Amount  int64  `json:"amount"`
}

var wf = df.NewWorkflow("events", df.WorkflowOptions{})

var placed = wf.Trigger[OrderPlaced]("order_placed")

func onOrder(ctx *df.Ctx, event OrderPlaced) (map[string]any, error) {
	delivery := "local"
	if trigger := ctx.Trigger(); trigger != nil {
		delivery = trigger.ID
	}
	ctx.Log().Info("delivery", "id", delivery)
	return map[string]any{"received": event.OrderID}, nil
}

var _ = wf.Node(onOrder, placed, df.NodeOptions{Key: "on_order"})

func main() { df.Main() }
```

The manifest lists the trigger with its schema and the node as `triggered_by` it; which kinds of trigger exist and how they are bound is the platform's side.

## Command line interface

The node binary is the CLI. In the module, `go run . <command>`; once built, `./app <command>`.

### Manifest management

```bash
# Generate the manifest the builder consumes
go run . build manifest -o dagflows-manifest.json

# Check that a committed manifest is up to date
go run . build manifest -o dagflows-manifest.json --check

# Validate the declaration without writing anything
go run . build validate
```

The builder generates the manifest itself by default (`manifests: generate` in `dagflows.yaml`), by compiling your module root to `bin/app` and running `./bin/app build manifest -o dagflows-manifest.json`, the same command as above. To commit the manifest instead, set `manifests: committed` and keep the file honest in CI with `--check`.

### Local development

`dev fixture` writes a starting input envelope and prints the command that runs the node against it:

```bash
go run . dev fixture calculate_totals --input fetch_orders=orders.json -o m.json
```

```bash
DAGFLOWS_INPUT=m.json DAGFLOWS_OUTPUT=out.json go run .
```

`dev run` does both in one step, with no VM, no platform and no network. The input is keyed by the parent it stands in for, and a typed node decodes it exactly as it would in production:

```bash
go run . dev run calculate_totals --input fetch_orders=orders.json
```

Options worth knowing:

```text
--input users=rows.ndjson     a file, content type inferred from the suffix
--input users='{"n": 1}'      inline json
--memory-limit-mb 512         what the node believes it has
--inline-max-bytes 262144     when an output would offload
--keep-fixture <path>         write the envelope instead of discarding it
```

Every command takes `--json` for machine readable output. Exit codes are `0` success, `1` the operation failed, `2` the command was wrong. A node run by the platform exits `0` even when it failed: the failure, its category and its retry directive travel in the output envelope, which the worker reads only on a clean exit.

## Versioning

Releases are git tags. The module's major tracks the manifest version: v1.x while the manifest is `"v": 1`. `df.Version()` reports the module version a binary was built against.

## License

Apache-2.0
