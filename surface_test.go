package dagflows_test

import (
	"errors"
	"fmt"
	"testing"

	dagflows "github.com/dagflows/sdk-go"
	"github.com/stretchr/testify/require"
)

var (
	_                      = dagflows.EXECUTION
	_                      = dagflows.INFRASTRUCTURE
	_                      = dagflows.PERMANENT
	_                      = dagflows.TIMEOUT
	_ dagflows.ContentType = dagflows.NDJSON
	_ *dagflows.Ctx
	_ *dagflows.ExecutionConfig
	_ error = (*dagflows.Fail)(nil)
	_ *dagflows.Input
	_ error = (*dagflows.InputTooLarge)(nil)
	_ error = (*dagflows.InputUnavailable)(nil)
	_ *dagflows.Inputs
	_ *dagflows.NodeRef
	_ error = (*dagflows.OutputTooLarge)(nil)
	_ dagflows.Result
	_ *dagflows.RetryConfig
	_ *dagflows.Workflow = dagflows.NewWorkflow("surface", dagflows.WorkflowOptions{})
	_ dagflows.Handler   = handler
	_                    = dagflows.Main
)

func handler(_ *dagflows.Ctx, _ *dagflows.Inputs) (any, error) {
	return nil, nil
}

func TestTheContractStringsAreExact(t *testing.T) {
	// Converted rather than compared as strings: FailureCategory is a defined
	// type so a typo is a compile error, and the wire value is what this guards.
	require.Equal(t, "permanent", string(dagflows.PERMANENT))
	require.Equal(t, "infrastructure", string(dagflows.INFRASTRUCTURE))
	require.Equal(t, "timeout", string(dagflows.TIMEOUT))
	require.Equal(t, "execution", string(dagflows.EXECUTION))
	require.Equal(t, "application/json", dagflows.JSON)
	require.Equal(t, "application/x-ndjson", dagflows.NDJSON)
	require.Equal(t, "text/csv", dagflows.CSV)
	require.Equal(t, "text/plain", dagflows.TEXT)
	require.Equal(t, "application/octet-stream", dagflows.BYTES)
}

func TestContentTypeIsAnOpenString(t *testing.T) {
	var custom dagflows.ContentType = "application/vnd.example+json"

	require.Equal(t, "application/vnd.example+json", custom)
}

func TestErrorsMatchThroughWrappingWithAsTypeAndIs(t *testing.T) {
	err := fmt.Errorf("syncing: %w", &dagflows.Fail{
		Message:      "throttled",
		Category:     dagflows.TIMEOUT,
		RetryAfterMs: 5_000,
	})

	fail, ok := errors.AsType[*dagflows.Fail](err)
	require.True(t, ok)
	require.Equal(t, "throttled", fail.Message)
	require.EqualError(t, fail, "throttled")

	var tooLarge *dagflows.OutputTooLarge

	wrapped := fmt.Errorf("x: %w", &dagflows.OutputTooLarge{
		Message: "big",
	})
	require.ErrorAs(t, wrapped, &tooLarge)
	require.True(t, errors.Is(wrapped, &dagflows.OutputTooLarge{}))
	require.False(t, errors.Is(wrapped, &dagflows.InputTooLarge{}))
}

func TestVersionIsHonestAboutASourceTree(t *testing.T) {
	require.Equal(t, "(devel)", dagflows.Version())
}

func TestTheWorkflowDeclaresAndEmitsInProcess(t *testing.T) {
	wf := dagflows.NewWorkflow("demo", dagflows.WorkflowOptions{
		Version: "1.26",
	})
	first := wf.Node(handler, dagflows.NodeOptions{
		Key: "first",
	})
	wf.Node(handler, dagflows.NodeOptions{
		Key: "second",
		Depends: []*dagflows.NodeRef{
			first,
			wf.ExternalNode("crunch"),
		},
		Execution: &dagflows.ExecutionConfig{
			Machine:     "m",
			TimeoutSecs: 30,
		},
		Transfer: &dagflows.TransferConfig{
			MaxOutputMB: 1,
		},
		Retry: &dagflows.RetryConfig{
			MaxAttempts:      new(3),
			InitialBackoffMs: new(500),
		},
		Config: map[string]any{
			"region": "eu",
		},
		Type: "task",
	})

	manifest, err := wf.Manifest()
	require.NoError(t, err)
	require.Equal(t, []string{"first", "second"}, manifest.Keys())
	require.Equal(t, "app", manifest.Nodes[1].Entrypoint)
}
