// Command app provides an example node binary for integration testing.
package main

import (
	"encoding/json"
	"fmt"

	dagflows "github.com/dagflows/sdk-go"
	"github.com/dagflows/sdk-go/authoring"
	"github.com/dagflows/sdk-go/failure"
	"github.com/dagflows/sdk-go/runtime"
)

// count counts records received from the seed parent input.
func count(_ *runtime.Ctx, inputs *runtime.Inputs) (any, error) {
	fmt.Println("working")

	seed, err := inputs.Get("seed")
	if err != nil {
		return nil, err
	}

	rows := 0

	for _, err := range seed.Iter() {
		if err != nil {
			return nil, err
		}

		rows++
	}

	return map[string]any{"rows": rows}, nil
}

// compute performs arithmetic multiplication on the seed factor input.
func compute(_ *runtime.Ctx, inputs *runtime.Inputs) (any, error) {
	seed, err := inputs.Get("seed")
	if err != nil {
		return nil, err
	}

	value, err := seed.Value()
	if err != nil {
		return nil, err
	}

	factor, err := value.(map[string]any)["factor"].(json.Number).Int64()
	if err != nil {
		return nil, err
	}

	return map[string]any{"value": 14 * factor}, nil
}

// export streams processed rows to the output writer and determines next-hop routing.
func export(ctx *runtime.Ctx, inputs *runtime.Inputs) (any, error) {
	seed, err := inputs.One()
	if err != nil {
		return nil, err
	}

	out := ctx.OutputStream(runtime.NDJSON)
	defer out.Abort()

	written := 0

	for row, err := range seed.Iter() {
		if err != nil {
			return nil, err
		}

		if err := out.Write(row); err != nil {
			return nil, err
		}

		written++
	}

	if err := out.Close(); err != nil {
		return nil, err
	}

	ref, err := out.Ref()
	if err != nil {
		return nil, err
	}

	next := "process"
	if written == 0 {
		next = "empty"
	}

	return runtime.Result{
		Output: ref,
		Next:   []string{next},
	}, nil
}

func fails(*runtime.Ctx, *runtime.Inputs) (any, error) {
	return nil, &failure.Fail{
		Message:  "upstream returned 503",
		Category: failure.INFRASTRUCTURE,
		Abort:    new(false),
	}
}

func crashes(*runtime.Ctx, *runtime.Inputs) (any, error) {
	var rows []int

	return rows[3], nil
}

func version(*runtime.Ctx, *runtime.Inputs) (any, error) {
	return map[string]any{
		"sdk": dagflows.Version(),
	}, nil
}

func main() {
	wf := authoring.NewWorkflow("demo", authoring.WorkflowOptions{
		Version:            "1.26",
		MaxConcurrentNodes: 5,
	})

	counted := wf.Node(count, authoring.NodeOptions{
		Execution: &authoring.Execution{
			Machine: "m",
		},
	})
	computed := wf.Node(compute, authoring.NodeOptions{
		Depends: []*authoring.NodeRef{counted},
		Execution: &authoring.Execution{
			Machine:     "m",
			TimeoutSecs: 30,
		},
		Retry: &authoring.Retry{
			MaxAttempts: new(2),
			RetryOn:     []authoring.RetryCategory{authoring.RetryOnInfrastructure, authoring.RetryOnTimeout},
		},
	})
	wf.Node(export, authoring.NodeOptions{
		Key:     "report",
		Depends: []*authoring.NodeRef{computed, wf.ExternalNode("crunch")},
		Transfer: &authoring.Transfer{
			MaxOutputMB: 64,
		},
	})
	wf.Node(fails, authoring.NodeOptions{})
	wf.Node(crashes, authoring.NodeOptions{})
	wf.Node(version, authoring.NodeOptions{})

	dagflows.Main()
}
