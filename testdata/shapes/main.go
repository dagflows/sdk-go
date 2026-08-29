// Command shapes declares workflows with intentional variations to verify SDK error detection.
package main

import (
	"os"

	dagflows "github.com/dagflows/sdk-go"
	"github.com/dagflows/sdk-go/authoring"
	"github.com/dagflows/sdk-go/runtime"
)

func step(*runtime.Ctx, *runtime.Inputs) (any, error) {
	return map[string]any{}, nil
}

func main() {
	switch os.Getenv("SHAPE") {
	case "none":

	case "two":
		authoring.NewWorkflow("a", authoring.WorkflowOptions{})
		authoring.NewWorkflow("b", authoring.WorkflowOptions{})

	case "badkey":
		authoring.NewWorkflow("w", authoring.WorkflowOptions{}).Node(step, authoring.NodeOptions{
			Key: "9lives",
		})

	case "anonymous":
		authoring.NewWorkflow("w", authoring.WorkflowOptions{}).Node(
			func(*runtime.Ctx, *runtime.Inputs) (any, error) {
				return nil, nil
			},
			authoring.NodeOptions{},
		)

	case "empty":
		authoring.NewWorkflow("w", authoring.WorkflowOptions{})
	}

	dagflows.Main()
}
