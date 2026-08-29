package dagflows_test

import (
	"errors"
	"fmt"
	"testing"

	dagflows "github.com/dagflows/sdk-go"
	"github.com/dagflows/sdk-go/authoring"
	"github.com/dagflows/sdk-go/failure"
	"github.com/dagflows/sdk-go/runtime"
	"github.com/stretchr/testify/require"
)

var (
	_                     = failure.EXECUTION
	_                     = failure.INFRASTRUCTURE
	_                     = failure.PERMANENT
	_                     = failure.TIMEOUT
	_ runtime.ContentType = runtime.NDJSON
	_ *runtime.Ctx
	_ *authoring.Execution
	_ error = (*failure.Fail)(nil)
	_ *runtime.Input
	_ error = (*failure.InputTooLarge)(nil)
	_ error = (*failure.InputUnavailable)(nil)
	_ *runtime.Inputs
	_ *authoring.NodeRef
	_ error = (*failure.OutputTooLarge)(nil)
	_ runtime.Result
	_ *authoring.Retry
	_ *authoring.Workflow = authoring.NewWorkflow("surface", authoring.WorkflowOptions{})
	_ runtime.Handler     = handler
	_                     = dagflows.Main
)

func handler(_ *runtime.Ctx, _ *runtime.Inputs) (any, error) {
	return nil, nil
}

func TestTheContractStringsAreExact(t *testing.T) {
	// Converted rather than compared as strings: FailureCategory is a defined
	// type so a typo is a compile error, and the wire value is what this guards.
	require.Equal(t, "permanent", string(failure.PERMANENT))
	require.Equal(t, "infrastructure", string(failure.INFRASTRUCTURE))
	require.Equal(t, "timeout", string(failure.TIMEOUT))
	require.Equal(t, "execution", string(failure.EXECUTION))
	require.Equal(t, "application/json", runtime.JSON)
	require.Equal(t, "application/x-ndjson", runtime.NDJSON)
	require.Equal(t, "text/csv", runtime.CSV)
	require.Equal(t, "text/plain", runtime.TEXT)
	require.Equal(t, "application/octet-stream", runtime.BYTES)
}

func TestContentTypeIsAnOpenString(t *testing.T) {
	var custom runtime.ContentType = "application/vnd.example+json"

	require.Equal(t, "application/vnd.example+json", custom)
}

func TestErrorsMatchThroughWrappingWithAsTypeAndIs(t *testing.T) {
	err := fmt.Errorf("syncing: %w", &failure.Fail{
		Message:      "throttled",
		Category:     failure.TIMEOUT,
		RetryAfterMs: 5_000,
	})

	fail, ok := errors.AsType[*failure.Fail](err)
	require.True(t, ok)
	require.Equal(t, "throttled", fail.Message)
	require.EqualError(t, fail, "throttled")

	var tooLarge *failure.OutputTooLarge

	wrapped := fmt.Errorf("x: %w", &failure.OutputTooLarge{
		Message: "big",
	})
	require.ErrorAs(t, wrapped, &tooLarge)
	require.True(t, errors.Is(wrapped, &failure.OutputTooLarge{}))
	require.False(t, errors.Is(wrapped, &failure.InputTooLarge{}))
}

func TestVersionIsHonestAboutASourceTree(t *testing.T) {
	require.Equal(t, "(devel)", dagflows.Version())
}

func TestTheWorkflowDeclaresAndEmitsInProcess(t *testing.T) {
	wf := authoring.NewWorkflow("demo", authoring.WorkflowOptions{
		Version: "1.26",
	})
	first := wf.Node(handler, authoring.NodeOptions{
		Key: "first",
	})
	wf.Node(handler, authoring.NodeOptions{
		Key: "second",
		Depends: []*authoring.NodeRef{
			first,
			wf.ExternalNode("crunch"),
		},
		Execution: &authoring.Execution{
			Machine:     "m",
			TimeoutSecs: 30,
		},
		Transfer: &authoring.Transfer{
			MaxOutputMB: 1,
		},
		Retry: &authoring.Retry{
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
