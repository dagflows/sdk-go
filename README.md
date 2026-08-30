# dagflows

Go SDK for authoring and running Dagflows workflows.

## Installation

<!-- not-tested: fetches from the module proxy -->
```bash
go get github.com/dagflows/sdk-go
```

Requires Go 1.27+. The module depends on nothing outside the standard
library, because everything it imports is linked into every tenant's node.

## Quickstart

A Go node is a precompiled static binary. One binary declares the workflow,
registers every node, and dispatches on the node key the platform names:

```go
// file: main.go
package main

import (
	"encoding/json"

	dagflows "github.com/dagflows/sdk-go"
	"github.com/dagflows/sdk-go/authoring"
	"github.com/dagflows/sdk-go/runtime"
)

func fetchOrders(_ *runtime.Ctx, _ *runtime.Inputs) (any, error) {
	return map[string]any{"orders": []any{
		map[string]any{"id": 1, "amount": 100},
		map[string]any{"id": 2, "amount": 250},
	}}, nil
}

func calculateTotals(_ *runtime.Ctx, inputs *runtime.Inputs) (any, error) {
	parent, err := inputs.Get("fetch_orders")
	if err != nil {
		return nil, err
	}
	value, err := parent.Value()
	if err != nil {
		return nil, err
	}

	var total int64
	for _, order := range value.(map[string]any)["orders"].([]any) {
		amount, err := order.(map[string]any)["amount"].(json.Number).Int64()
		if err != nil {
			return nil, err
		}
		total += amount
	}
	return map[string]any{"total": total}, nil
}

func main() {
	wf := authoring.NewWorkflow("order_pipeline", authoring.WorkflowOptions{MaxConcurrentNodes: 4})

	fetched := wf.Node(fetchOrders, authoring.NodeOptions{Key: "fetch_orders"})
	wf.Node(calculateTotals, authoring.NodeOptions{Key: "calculate_totals", Depends: []*authoring.NodeRef{fetched}})

	dagflows.Main()
}
```

`dagflows.Main()` never returns. With `DAGFLOWS_INPUT` set it runs the node the envelope names and exits 0, reporting through `DAGFLOWS_OUTPUT`. Otherwise it answers the build and dev commands below.

### Handler signature

Every handler has one signature, and the compiler is the binding validator:

```go
func step(ctx *runtime.Ctx, inputs *runtime.Inputs) (any, error)
```

Name what you do not use `_`. Numbers in decoded inputs are `json.Number`, so an integer beyond float64 precision survives a trip through a node.

The node key defaults to the function's name; set `Key` to choose another. Invalid keys, duplicate keys and negative settings are reported by `build manifest` and `build validate`, as one clean line at your terminal.

## Node configuration

### Resource limits and retries

```go
// file: limits/main.go
package main

import (
	dagflows "github.com/dagflows/sdk-go"
	"github.com/dagflows/sdk-go/authoring"
	"github.com/dagflows/sdk-go/runtime"
)

func extractData(_ *runtime.Ctx, _ *runtime.Inputs) (any, error) {
	return map[string]any{"rows": 0}, nil
}

func main() {
	wf := authoring.NewWorkflow("data_pipeline", authoring.WorkflowOptions{
		// Every node inherits this; a node stating its own overrides it field
		// by field, so the settings it leaves out still apply.
		Retry: &authoring.Retry{MaxAttempts: new(3), InitialBackoffMs: new(1000)},
	})

	wf.Node(extractData, authoring.NodeOptions{
		Key:       "extract_data",
		Execution: &authoring.Execution{Machine: "l", TimeoutSecs: 300},
		// Only the platform's own failures are retried unless a node says
		// otherwise. Settings are pointers so leaving one out means "let the
		// platform decide" rather than zero, and new(5) states one inline.
		Retry: &authoring.Retry{
			MaxAttempts: new(5),
			RetryOn:     []authoring.RetryCategory{authoring.RetryOnInfrastructure, authoring.RetryOnTimeout},
		},
	})

	dagflows.Main()
}
```

Use `Machine` to select a resource tier from the platform catalog (e.g. `"xs"`, `"s"`, `"m"`, `"l"`, `"xl"`). Set `TimeoutSecs` to cap node execution duration in seconds, and declare `MaxOutputMB` if your node needs multipart upload capabilities for large outputs.

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

`Depends` takes the handles `Node` returned, never strings, so a typo fails to compile. A node in another project is the one thing you name by key:

```go
wf.Node(fulfillOrder, authoring.NodeOptions{
	Key:     "fulfill_order",
	Depends: []*authoring.NodeRef{fetched, wf.ExternalNode("inventory_check")},
})
```

## Working with inputs

`inputs.Get(key)` returns a lazy handle: nothing is fetched until asked.

```go
upstream, err := inputs.Get("fetch_orders")

// One record at a time, whatever the input's size
for record, err := range upstream.Iter() {
	if err != nil {
		return nil, err
	}
	process(record)
}

// The complete value, for an input small enough to hold
data, err := upstream.Value()

// Raw bytes for opaque content: a reader, not a byte slice
body, err := upstream.Bytes()
defer body.Close()
io.Copy(sink, body)
```

Reach for `Iter` first. It reads an inline handful of rows and a stored file of any size through the same loop, so a node written this way keeps working when its parent grows. `Value` is the convenience for an input you know is small: it holds the whole payload in memory and is refused when it exceeds the node's memory limit.

Iteration yields one **record** at a time for row oriented payloads, which is what lets a node read more than it can hold. A JSON input has no records, so it yields the whole document as a single item.

| content type | `for x, err := range handle.Iter()` yields |
| --- | --- |
| `application/x-ndjson`, `text/csv` | one row at a time |
| `application/json` | the whole document, once |

Iteration re-opens the source each pass, so looping twice costs a second read rather than silently yielding nothing the second time.

If a node has exactly one parent, use `inputs.One()`:

```go
single, err := inputs.One()
```

## Producing outputs

Return a value directly, or a `runtime.Result` for routing and formatting:

```go
// A plain value
return map[string]any{"count": 42}, nil

// Route downstream execution to a specific branch
return runtime.Result{Output: map[string]any{"status": "approved"}, Next: []string{"process_payment"}}, nil

// Halt this branch
return runtime.Result{Output: map[string]any{}, Stop: true}, nil

// Stream rows: an iterator is rows, a slice is a value
return runtime.Result{Output: parent.Iter(), ContentType: runtime.NDJSON}, nil
```

A handler may return any `iter.Seq[T]` or `iter.Seq2[T, error]`, its rows are encoded lazily and offloaded to storage when they outgrow the inline limit. You never choose inline versus reference: the platform decides from what it offered the run and what the node returned.

Two rules apply to an iterator output:

- **One yield is one row.** The output is NDJSON, even where a sequence yields a single value once. Return the value itself when a node produces one.
- **An iterator node is atomic.** A failure at row 900 replays from row 0; rows already sent are discarded, never resumed.

Laziness on its own does not bound what the node holds. Declare `Transfer{MaxOutputMB: ...}` for that: it is what asks the platform for a multipart upload, which is what lets parts leave as they fill instead of the whole output being buffered for a single request.

```go
wf.Node(exportAll, authoring.NodeOptions{
	Transfer: &authoring.Transfer{MaxOutputMB: 4096},
})
```

Without it a large output is refused rather than truncated, naming `max_output_mb` as the remedy.

To decide routing from what was streamed, write through `ctx.OutputStream`:

```go
out := ctx.OutputStream(runtime.NDJSON)
defer out.Abort()
for record, err := range parent.Iter() {
	if err != nil {
		return nil, err
	}
	if err := out.Write(record); err != nil {
		return nil, err
	}
	written++
}
if err := out.Close(); err != nil {
	return nil, err
}
ref, err := out.Ref()
return runtime.Result{Output: ref, Next: []string{branchFor(written)}}, nil
```

`Close` commits; the deferred `Abort` discards a partial upload when the handler fails first, and does nothing after a successful `Close`.

## Error handling

Return a `*failure.Fail` to signal a structured failure with retry instructions. Any other error is reported as permanent:

```go
return nil, &failure.Fail{Message: "Payment gateway unavailable", Category: failure.EXECUTION, RetryAfterMs: 30_000}
```

Naming a delay implies "retry me"; naming nothing aborts. `Abort: new(false)` with no delay leaves retry timing to the workflow's policy. Categories: `EXECUTION`, `INFRASTRUCTURE`, `TIMEOUT`, `PERMANENT`. Anything else collapses to permanent, because an unknown category must never silently
become retryable.

Errors the SDK returns are matchable with `errors.AsType` and `errors.Is`: `InputTooLarge`, `InputUnavailable`, `OutputTooLarge`.

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

`dev run` does both in one step, with no VM, no platform and no network:

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

Releases are git tags. The module's major tracks the manifest version: v1.x while the manifest is `"v": 1`. `dagflows.Version()` reports the module version a binary was built against.

## License

Apache-2.0
