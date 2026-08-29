// Package dagflows is the entrypoint for a dagflows node binary.
//
// The API is split by what the code does:
//
//	github.com/dagflows/sdk-go/authoring   declaring a workflow
//	github.com/dagflows/sdk-go/runtime     executing a node
//	github.com/dagflows/sdk-go/errors      signalling failure
//
// A node binary declares its workflow, registers its handlers, and hands
// control to [Main]:
//
//	func extract(ctx *runtime.Ctx, inputs *runtime.Inputs) (any, error) {
//		seed, err := inputs.One()
//		if err != nil {
//			return nil, err
//		}
//
//		return seed.Value()
//	}
//
//	func main() {
//		wf := authoring.NewWorkflow("demo", authoring.WorkflowOptions{})
//		wf.Node(extract, authoring.NodeOptions{})
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

	"github.com/dagflows/sdk-go/authoring"
	"github.com/dagflows/sdk-go/internal/cli"
)

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
