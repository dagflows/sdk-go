// Command app provides an example node binary for integration testing.
package main

import (
	"encoding/json"
	"fmt"

	"github.com/dagflows/sdk-go"
)

// count counts records received from the seed parent input.
func count(_ *dagflows.Ctx, inputs *dagflows.Inputs) (any, error) {
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
func compute(_ *dagflows.Ctx, inputs *dagflows.Inputs) (any, error) {
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
func export(ctx *dagflows.Ctx, inputs *dagflows.Inputs) (any, error) {
	seed, err := inputs.One()
	if err != nil {
		return nil, err
	}

	out := ctx.OutputStream(dagflows.NDJSON)
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

	return dagflows.Result{
		Output: ref,
		Next:   []string{next},
	}, nil
}

func fails(*dagflows.Ctx, *dagflows.Inputs) (any, error) {
	return nil, &dagflows.Fail{
		Message:    "upstream returned 503",
		Category:   dagflows.INFRASTRUCTURE,
		Abort:      new(false),
	}
}

func crashes(*dagflows.Ctx, *dagflows.Inputs) (any, error) {
	var rows []int

	return rows[3], nil
}

func version(*dagflows.Ctx, *dagflows.Inputs) (any, error) {
	return map[string]any{
		"sdk": dagflows.Version(),
	}, nil
}

func main() {
	wf := dagflows.NewWorkflow("demo", dagflows.WorkflowOptions{
		Version:            "1.26",
		MaxConcurrentNodes: 5,
	})

	counted := wf.Node(count, dagflows.NodeOptions{
		Execution: &dagflows.ExecutionConfig{
			MemoryLimitMB: 256,
		},
	})
	computed := wf.Node(compute, dagflows.NodeOptions{
		Depends: []*dagflows.NodeRef{counted},
		Execution: &dagflows.ExecutionConfig{
			Timeout:       30,
			MemoryLimitMB: 256,
		},
		Retry: &dagflows.RetryConfig{
			MaxAttempts: 2,
		},
	})
	wf.Node(export, dagflows.NodeOptions{
		Key:     "report",
		Depends: []*dagflows.NodeRef{computed, wf.ExternalNode("crunch")},
		Execution: &dagflows.ExecutionConfig{
			MaxOutputMB: 64,
		},
	})
	wf.Node(fails, dagflows.NodeOptions{})
	wf.Node(crashes, dagflows.NodeOptions{})
	wf.Node(version, dagflows.NodeOptions{})

	dagflows.Main()
}
