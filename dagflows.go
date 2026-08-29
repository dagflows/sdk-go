// Package dagflows authors and runs dagflows workflow nodes.
//
// A node binary declares its workflow, registers its handlers, and hands
// control to [Main]:
//
//	func extract(ctx *dagflows.Ctx, inputs *dagflows.Inputs) (any, error) {
//		seed, err := inputs.One()
//		if err != nil {
//			return nil, err
//		}
//
//		return seed.Value()
//	}
//
//	func main() {
//		wf := dagflows.NewWorkflow("demo", dagflows.WorkflowOptions{})
//		wf.Node(extract, dagflows.NodeOptions{})
//		dagflows.Main()
//	}
//
// Inside the platform microVM the binary executes the node named by the envelope.
// In local development or during builds, it emits the manifest (build manifest)
// and executes nodes locally (dev run, dev fixture).
package dagflows

import (
	"os"
	"runtime/debug"

	"github.com/dagflows/sdk-go/internal/authoring"
	"github.com/dagflows/sdk-go/internal/cli"
	"github.com/dagflows/sdk-go/internal/runtime"
)

// Failure categories supported by the execution platform.
const (
	PERMANENT      = runtime.PERMANENT
	INFRASTRUCTURE = runtime.INFRASTRUCTURE
	TIMEOUT        = runtime.TIMEOUT
	EXECUTION      = runtime.EXECUTION
)

// ContentType identifies the encoding format of an input or output payload.
type ContentType = runtime.ContentType

const (
	JSON   ContentType = runtime.JSON
	NDJSON ContentType = runtime.NDJSON
	CSV    ContentType = runtime.CSV
	TEXT   ContentType = runtime.TEXT
	BYTES  ContentType = runtime.BYTES
)

// Ctx provides runtime metadata, execution limits, and stream factories.
type Ctx = runtime.Ctx

// Inputs provides lazy access to upstream parent node outputs.
type Inputs = runtime.Inputs

// Input represents a single upstream node output handle.
type Input = runtime.Input

// Result holds the return value, routing instructions, and branch termination flag.
type Result = runtime.Result

// Fail signals an intentional node execution failure with category and retry directives.
type Fail = runtime.Fail

// InputTooLarge indicates an input payload exceeds available memory allocation.
type InputTooLarge = runtime.InputTooLarge

// InputUnavailable indicates an input cannot be delivered in the requested format.
type InputUnavailable = runtime.InputUnavailable

// OutputTooLarge indicates an output payload exceeds configured size thresholds.
type OutputTooLarge = runtime.OutputTooLarge

// OutputStream provides a write handle for streaming output payloads.
type OutputStream = runtime.OutputStream

// Written represents a finalized stream token returned by OutputStream.Close.
type Written = runtime.Written

// Multipart contains presigned URLs and configuration for multipart uploads.
type Multipart = runtime.Multipart

// Handler defines the function signature for workflow node execution.
type Handler = runtime.Handler

// Workflow manages node registration and manifest generation for a project.
type Workflow = authoring.Workflow

// WorkflowOptions configures workflow-level properties and execution limits.
type WorkflowOptions = authoring.WorkflowOptions

// NodeOptions configures an individual node registration.
type NodeOptions = authoring.NodeOptions

// NodeRef represents a reference handle to a registered workflow node.
type NodeRef = authoring.NodeRef

// ExecutionConfig defines resource requirements and timeout limits for a node.
type ExecutionConfig = authoring.ExecutionConfig

// TransferConfig defines how a node moves data in and out of storage.
type TransferConfig = authoring.TransferConfig

// RetryConfig defines how a failed node is retried.
type RetryConfig = authoring.RetryConfig

// RetryCategory is a failure category a node may ask to retry.
type RetryCategory = authoring.RetryCategory

// OnWarning is what a deploy does when the platform has to adjust a value the
// author declared.
type OnWarning = authoring.OnWarning

// Deploy policies for an adjusted value.
const (
	OnWarningAllow  = authoring.OnWarningAllow
	OnWarningReject = authoring.OnWarningReject
)

// FailureCategory is how a node failed, which decides whether a retry helps.
type FailureCategory = runtime.FailureCategory

// Retry categories that nodes can opt into retrying.
const (
	RetryOnInfrastructure = authoring.RetryOnInfrastructure
	RetryOnTimeout        = authoring.RetryOnTimeout
	RetryOnExecution      = authoring.RetryOnExecution
)

// NewWorkflow initializes and registers a new Workflow definition.
func NewWorkflow(name string, opts WorkflowOptions) *Workflow {
	return authoring.NewWorkflow(name, opts)
}

// Main is the binary entrypoint handling platform execution, build commands, and local dev.
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
