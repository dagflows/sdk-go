// Package dagflows is the combined workflow authoring and runtime execution API
// for a node binary.
//
// It re-exports the authoring, runtime and failure declarations so that every
// type a workflow declares and every type a handler receives is reachable under
// a single import:
//
//	import df "github.com/dagflows/sdk-go"
//
//	type Order struct {
//		ID     int64 `json:"id"`
//		Amount int64 `json:"amount"`
//	}
//	type Orders struct{ Orders []Order `json:"orders"` }
//	type Totals struct{ Total int64 `json:"total"` }
//
//	var wf = df.NewWorkflow("order_pipeline", df.WorkflowOptions{MaxConcurrentNodes: 4})
//
//	func fetchOrders(*df.Ctx, df.None) (Orders, error) {
//		return Orders{Orders: []Order{{1, 100}, {2, 250}}}, nil
//	}
//
//	func calculateTotals(_ *df.Ctx, orders Orders) (Totals, error) {
//		var total int64
//		for _, o := range orders.Orders {
//			total += o.Amount
//		}
//		return Totals{Total: total}, nil
//	}
//
//	var (
//		fetched = wf.Node(fetchOrders, df.Root)             // no parents
//		totals  = wf.Node(calculateTotals, fetched)          // one parent: the handle is the edge, typed
//	)
//
//	func main() { df.Main() }
//
// Inside the platform microVM the binary executes the node named by the envelope.
// In local development or during builds, it emits the manifest (build manifest)
// and executes nodes locally (dev run, dev fixture).
//
// The runtime, authoring and failure packages remain importable on their own;
// this package aggregates them. A dot import gives the bare names (Ctx, Result,
// Node), which Go style reserves for tests, so the documentation writes df.
package dagflows

import (
	"os"
	"runtime/debug"

	"github.com/dagflows/sdk-go/authoring"
	"github.com/dagflows/sdk-go/internal/cli"
	"github.com/dagflows/sdk-go/runtime"
)

// Node execution types, re-exported from the runtime package.
type (
	// Ctx provides the node execution context: run identity, platform limits,
	// config, trigger metadata, cancellation, logging and the output writer.
	Ctx = runtime.Ctx
	// Inputs is the collection of parent outputs, keyed by node key.
	Inputs = runtime.Inputs
	// Input is one parent's output as a typed handle, resolved on demand.
	Input[T any] = runtime.Input[T]
	// Rows is a stream of T values, produced by a rows node and consumed by a
	// rows child.
	Rows[T any] = runtime.Rows[T]
	// Result carries an output with routing and metadata.
	Result[T any] = runtime.Result[T]
	// None is the input of a node with no parents.
	None = runtime.None
	// Decimal is an exact decimal carried as a string.
	Decimal = runtime.Decimal
	// RunInfo, Limits and TriggerInfo group the execution context's fields.
	RunInfo     = runtime.RunInfo
	Limits      = runtime.Limits
	TriggerInfo = runtime.TriggerInfo
	// OutputStream is the output writer, for a node that decides routing while
	// it writes; Written is the output block that closing it produces.
	OutputStream = runtime.OutputStream
	Written      = runtime.Written
	// ContentType identifies the payload MIME type an output travels as.
	ContentType = runtime.ContentType
	// Handler is the untyped handler signature the platform dispatches.
	Handler = runtime.Handler
)

// Workflow authoring types, re-exported from the authoring package.
type (
	Workflow        = authoring.Workflow
	WorkflowOptions = authoring.WorkflowOptions
	NodeOptions     = authoring.NodeOptions
	Execution       = authoring.Execution
	Transfer        = authoring.Transfer
	Retry           = authoring.Retry
	RetryCategory   = authoring.RetryCategory
	OnWarning       = authoring.OnWarning
	Mode            = authoring.Mode
	Manifest        = authoring.Manifest
	Schema          = authoring.Schema
	SchemaField     = authoring.SchemaField
	Schemer         = authoring.Schemer
	// NodeRef is a reference handle to a node, typed by what that node produces.
	NodeRef[T any] = runtime.NodeRef[T]
	// Ref is a node reference handle of any output type.
	Ref = runtime.Ref
	// Edge declares where a handler's input comes from.
	Edge[In any] = runtime.Edge[In]
)

// Failure signalling types, re-exported from the runtime package.
type (
	Fail             = runtime.Fail
	FailureCategory  = runtime.FailureCategory
	InputTooLarge    = runtime.InputTooLarge
	InputUnavailable = runtime.InputUnavailable
	OutputTooLarge   = runtime.OutputTooLarge
)

const (
	PERMANENT      = runtime.PERMANENT
	INFRASTRUCTURE = runtime.INFRASTRUCTURE
	TIMEOUT        = runtime.TIMEOUT
	EXECUTION      = runtime.EXECUTION

	JSON   = runtime.JSON
	NDJSON = runtime.NDJSON
	CSV    = runtime.CSV
	TEXT   = runtime.TEXT
	BYTES  = runtime.BYTES

	ModeOnce   = authoring.ModeOnce
	ModeStream = authoring.ModeStream

	OnWarningAllow  = authoring.OnWarningAllow
	OnWarningReject = authoring.OnWarningReject

	RetryOnInfrastructure = authoring.RetryOnInfrastructure
	RetryOnTimeout        = authoring.RetryOnTimeout
	RetryOnExecution      = authoring.RetryOnExecution
)

var (
	// NewWorkflow creates the workflow this binary registers its nodes on.
	NewWorkflow = authoring.NewWorkflow
	// Declared lists the workflows created so far in this process.
	Declared = authoring.Declared
	// Root is the edge of a node with no parents.
	Root = runtime.Root
	// Depends declares the edge for a node with several parents, whose handler
	// receives *Inputs.
	Depends = runtime.Depends
	// Enum is a ready-made schema for a named string type.
	Enum = authoring.Enum
)

// Lazy declares an edge that passes the handler a typed handle to its one
// parent instead of the decoded value.
func Lazy[T any](parent *NodeRef[T]) Edge[*Input[T]] {
	return runtime.Lazy(parent)
}

// Get returns one parent's input from the collection, typed by its handle.
func Get[T any](inputs *Inputs, ref *NodeRef[T]) *Input[T] {
	return runtime.Get(inputs, ref)
}

// Typed converts an untyped input handle into one that decodes into T.
func Typed[T any](in *Input[any]) *Input[T] {
	return runtime.Typed[T](in)
}

// Stream decodes an input handle's records into E, one at a time.
func Stream[E any, T any](in *Input[T]) Rows[E] {
	return runtime.Stream[E](in)
}

// Run executes one handler against the envelope in $DAGFLOWS_INPUT, for a
// binary that dispatches itself.
func Run(handler Handler) error {
	return runtime.Run(handler)
}

// Main executes the node the envelope names, or answers the build and dev
// commands. It never returns.
func Main() {
	os.Exit(cli.Main{
		Args:      os.Args[1:],
		Workflows: authoring.Declared(),
	}.Run())
}

// Version returns the SDK module version extracted from build metadata.
func Version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "(devel)"
	}

	const module = "github.com/dagflows/sdk-go"

	if info.Main.Path == module {
		return info.Main.Version
	}

	for _, dep := range info.Deps {
		if dep.Path == module {
			if dep.Replace != nil {
				return dep.Replace.Version
			}

			return dep.Version
		}
	}

	return "(devel)"
}
