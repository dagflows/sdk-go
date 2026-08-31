// Command shapes declares workflows with intentional variations to verify SDK error detection.
package main

import (
	"os"

	df "github.com/dagflows/sdk-go"
)

func step(*df.Ctx, df.None) (map[string]any, error) {
	return map[string]any{}, nil
}

func main() {
	switch os.Getenv("SHAPE") {
	case "none":

	case "two":
		df.NewWorkflow("a", df.WorkflowOptions{})
		df.NewWorkflow("b", df.WorkflowOptions{})

	case "badkey":
		df.NewWorkflow("w", df.WorkflowOptions{}).Node(step, df.Root, df.NodeOptions{
			Key: "9lives",
		})

	case "anonymous":
		df.NewWorkflow("w", df.WorkflowOptions{}).Node(
			func(*df.Ctx, df.None) (any, error) {
				return nil, nil
			},
			df.Root,
		)

	case "empty":
		df.NewWorkflow("w", df.WorkflowOptions{})
	}

	df.Main()
}
