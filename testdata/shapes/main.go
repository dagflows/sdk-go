// Command shapes declares workflows with intentional variations to verify SDK error detection.
package main

import (
	"os"

	"github.com/dagflows/sdk-go"
)

func step(*dagflows.Ctx, *dagflows.Inputs) (any, error) {
	return map[string]any{}, nil
}

func main() {
	switch os.Getenv("SHAPE") {
	case "none":

	case "two":
		dagflows.NewWorkflow("a", dagflows.WorkflowOptions{})
		dagflows.NewWorkflow("b", dagflows.WorkflowOptions{})

	case "badkey":
		dagflows.NewWorkflow("w", dagflows.WorkflowOptions{}).Node(step, dagflows.NodeOptions{
			Key: "9lives",
		})

	case "anonymous":
		dagflows.NewWorkflow("w", dagflows.WorkflowOptions{}).Node(
			func(*dagflows.Ctx, *dagflows.Inputs) (any, error) {
				return nil, nil
			},
			dagflows.NodeOptions{},
		)

	case "empty":
		dagflows.NewWorkflow("w", dagflows.WorkflowOptions{})
	}

	dagflows.Main()
}
