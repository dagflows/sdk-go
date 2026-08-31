package dagflows_test

import (
	"errors"
	"fmt"
	"testing"

	df "github.com/dagflows/sdk-go"
	"github.com/dagflows/sdk-go/authoring"
	"github.com/dagflows/sdk-go/failure"
	"github.com/dagflows/sdk-go/runtime"
	"github.com/stretchr/testify/require"
)

// The facade is the whole surface a node author needs. Every name the README
// uses is pinned here, as a compile-time fact.
type (
	surfaceIn struct {
		N int64 `json:"n"`
	}
	surfaceOut struct {
		Total int64 `json:"total"`
	}
)

var (
	_ df.FailureCategory = df.EXECUTION
	_ df.FailureCategory = df.INFRASTRUCTURE
	_ df.FailureCategory = df.PERMANENT
	_ df.FailureCategory = df.TIMEOUT
	_ df.ContentType     = df.NDJSON
	_ *df.Ctx
	_ *df.Inputs
	_ *df.Input[surfaceIn]
	_ df.Rows[surfaceIn]
	_ df.Result[surfaceOut]
	_ df.None
	_ df.Decimal
	_ *df.NodeRef[surfaceOut]
	_ df.Ref              = (*df.NodeRef[surfaceOut])(nil)
	_ df.Edge[surfaceIn]  = (*df.NodeRef[surfaceIn])(nil)
	_ df.Edge[*df.Inputs] = df.Depends()
	_ df.Edge[df.None]    = df.Root
	_ error               = (*df.Fail)(nil)
	_ error               = (*df.InputTooLarge)(nil)
	_ error               = (*df.InputUnavailable)(nil)
	_ error               = (*df.OutputTooLarge)(nil)
	_ *df.Execution
	_ *df.Transfer
	_ *df.Retry
	_ df.RetryCategory = df.RetryOnTimeout
	_ df.OnWarning     = df.OnWarningReject
	_ df.Mode          = df.ModeStream
	_ *df.Workflow     = df.NewWorkflow("surface", df.WorkflowOptions{})
	_ df.Handler       = handler
	_                  = df.Main
	_                  = df.Version

	// The halves the facade is made of remain importable.
	_ *runtime.Ctx     = (*df.Ctx)(nil)
	_ *authoring.Retry = (*df.Retry)(nil)
	_ *failure.Fail    = (*df.Fail)(nil)
	_ runtime.Handler  = handler
)

func handler(_ *df.Ctx, _ *df.Inputs) (any, error) {
	return nil, nil
}

func rootHandler(*df.Ctx, df.None) (surfaceIn, error) {
	return surfaceIn{N: 1}, nil
}

func typedHandler(_ *df.Ctx, in surfaceIn) (surfaceOut, error) {
	return surfaceOut{Total: in.N}, nil
}

func lazyHandler(_ *df.Ctx, in *df.Input[surfaceIn]) (int64, error) {
	value, err := in.Value()
	if err != nil {
		return 0, err
	}

	return value.N, nil
}

func resultHandler(_ *df.Ctx, in surfaceIn) (df.Result[surfaceOut], error) {
	return df.Result[surfaceOut]{Output: surfaceOut{Total: in.N}, Next: []string{"bagHandler"}}, nil
}

func bagHandler(_ *df.Ctx, in *df.Inputs) (map[string]any, error) {
	return map[string]any{"parents": in.Len()}, nil
}

func rowsHandler(_ *df.Ctx, rows df.Rows[surfaceIn]) (surfaceOut, error) {
	var total int64

	for row, err := range rows {
		if err != nil {
			return surfaceOut{}, err
		}

		total += row.N
	}

	return surfaceOut{Total: total}, nil
}

func TestTheContractStringsAreExact(t *testing.T) {
	// Converted rather than compared as strings: FailureCategory is a defined
	// type so a typo is a compile error, and the wire value is what this guards.
	require.Equal(t, "permanent", string(df.PERMANENT))
	require.Equal(t, "infrastructure", string(df.INFRASTRUCTURE))
	require.Equal(t, "timeout", string(df.TIMEOUT))
	require.Equal(t, "execution", string(df.EXECUTION))
	require.Equal(t, "application/json", df.JSON)
	require.Equal(t, "application/x-ndjson", df.NDJSON)
	require.Equal(t, "text/csv", df.CSV)
	require.Equal(t, "text/plain", df.TEXT)
	require.Equal(t, "application/octet-stream", df.BYTES)
	require.Equal(t, "once", string(df.ModeOnce))
	require.Equal(t, "stream", string(df.ModeStream))
}

func TestContentTypeIsAnOpenString(t *testing.T) {
	var custom df.ContentType = "application/vnd.example+json"

	require.Equal(t, "application/vnd.example+json", custom)
}

func TestErrorsMatchThroughWrappingWithAsTypeAndIs(t *testing.T) {
	err := fmt.Errorf("syncing: %w", &df.Fail{
		Message:      "throttled",
		Category:     df.TIMEOUT,
		RetryAfterMs: 5_000,
		Code:         "throttled",
		Details:      map[string]any{"after_ms": 5_000},
	})

	fail, ok := errors.AsType[*df.Fail](err)
	require.True(t, ok)
	require.Equal(t, "throttled", fail.Message)
	require.Equal(t, "throttled", fail.Code)
	require.EqualError(t, fail, "throttled")

	var tooLarge *df.OutputTooLarge

	wrapped := fmt.Errorf("x: %w", &df.OutputTooLarge{
		Message: "big",
	})
	require.ErrorAs(t, wrapped, &tooLarge)
	require.True(t, errors.Is(wrapped, &df.OutputTooLarge{}))
	require.False(t, errors.Is(wrapped, &df.InputTooLarge{}))
}

func TestVersionIsHonestAboutASourceTree(t *testing.T) {
	require.Equal(t, "(devel)", df.Version())
}

func TestTheWorkflowDeclaresAndEmitsInProcess(t *testing.T) {
	wf := df.NewWorkflow("demo", df.WorkflowOptions{
		Version: "1.27",
	})
	first := wf.Node(rootHandler, df.Root, df.NodeOptions{
		Key: "first",
	})
	wf.Node(handler, df.Depends(first, wf.External[surfaceOut]("crunch")), df.NodeOptions{
		Key: "second",
		Execution: &df.Execution{
			Machine:     "m",
			TimeoutSecs: 30,
		},
		Transfer: &df.Transfer{
			MaxOutputMB: 1,
		},
		Retry: &df.Retry{
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
	require.Equal(t, []string{"first"}, manifest.Nodes[1].Depends)
	require.Equal(t, []string{"crunch"}, manifest.Nodes[1].ExternalDepends)
}

// Tests that every edge form types the handle it produces, as the compiler
// sees it: a wrong assignment here is a build failure, not a test failure.
func TestEveryEdgeFormTypesItsHandle(t *testing.T) {
	wf := df.NewWorkflow("edges", df.WorkflowOptions{})

	root := wf.Node(rootHandler, df.Root)
	typed := wf.Node(typedHandler, root)
	lazy := wf.Node(lazyHandler, df.Lazy(root))
	routed := wf.NodeResult(resultHandler, root)
	bag := wf.Node(bagHandler, df.Depends(typed, lazy, routed, wf.External[surfaceOut]("crunch")))
	rows := wf.Node(rowsHandler, wf.External[df.Rows[surfaceIn]]("extract"))
	event := wf.Node(typedHandler, wf.Trigger[surfaceIn]("ping"), df.NodeOptions{Key: "on_ping", Mode: df.ModeStream})

	var (
		_ *df.NodeRef[surfaceIn]      = root
		_ *df.NodeRef[surfaceOut]     = typed
		_ *df.NodeRef[int64]          = lazy
		_ *df.NodeRef[surfaceOut]     = routed
		_ *df.NodeRef[map[string]any] = bag
		_ *df.NodeRef[surfaceOut]     = rows
		_ *df.NodeRef[surfaceOut]     = event
	)

	manifest, err := wf.Manifest()
	require.NoError(t, err)
	require.Equal(t, []string{"rootHandler", "typedHandler", "lazyHandler", "resultHandler", "bagHandler", "rowsHandler", "on_ping"}, manifest.Keys())

	byKey := map[string]*authoring.NodeManifest{}
	for _, node := range manifest.Nodes {
		byKey[node.Key] = node
	}

	require.Equal(t, []string{"rootHandler"}, byKey["typedHandler"].Depends)
	require.Equal(t, []string{"rootHandler"}, byKey["lazyHandler"].Depends)
	require.Equal(t, []string{"typedHandler", "lazyHandler", "resultHandler"}, byKey["bagHandler"].Depends)
	require.Equal(t, []string{"crunch"}, byKey["bagHandler"].ExternalDepends)
	require.Equal(t, []string{"extract"}, byKey["rowsHandler"].ExternalDepends)
	require.Equal(t, []string{"ping"}, byKey["on_ping"].TriggeredBy)
	require.Equal(t, "stream", byKey["on_ping"].Mode)
	require.Len(t, manifest.Triggers, 1)
	require.Equal(t, "ping", manifest.Triggers[0].Key)
}

// Tests that a typed handler runs against a real envelope through the same
// dispatch the platform uses, decoding its parent into the declared type.
func TestATypedHandlerDecodesItsParentWhenDispatched(t *testing.T) {
	wf := df.NewWorkflow("dispatch", df.WorkflowOptions{})
	root := wf.Node(rootHandler, df.Root, df.NodeOptions{Key: "root"})
	wf.Node(typedHandler, root, df.NodeOptions{Key: "typed"})
	wf.Node(lazyHandler, df.Lazy(root), df.NodeOptions{Key: "lazy"})
	wf.NodeResult(resultHandler, root, df.NodeOptions{Key: "routed"})

	inputs := runtime.NewInputs(map[string]any{
		"root": map[string]any{"type": "INLINE", "content_type": "application/json", "data": map[string]any{"n": 21}},
	}, 0)

	run := func(key string) any {
		t.Helper()

		fn, ok := wf.Handler(key)
		require.True(t, ok)

		out, err := fn(&df.Ctx{NodeKey: key}, inputs)
		require.NoError(t, err)

		return out
	}

	require.Equal(t, surfaceOut{Total: 21}, run("typed"))
	require.Equal(t, int64(21), run("lazy"))
	require.Equal(t, df.Result[surfaceOut]{Output: surfaceOut{Total: 21}, Next: []string{"bagHandler"}}, run("routed"))

	fn, _ := wf.Handler("typed")
	_, err := fn(&df.Ctx{NodeKey: "typed"}, runtime.NewInputs(map[string]any{
		"root": map[string]any{"type": "INLINE", "content_type": "application/json", "data": map[string]any{"n": "twenty-one"}},
	}, 0))
	require.ErrorContains(t, err, "root")
	require.ErrorContains(t, err, "n")
}
