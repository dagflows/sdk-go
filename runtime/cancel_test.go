package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Tests that cancelling a context sets ErrStopped as the cause.
func TestCancelStopsTheRunContext(t *testing.T) {
	ctx := CtxFromRaw(map[string]any{"node_key": "n"})

	require.NoError(t, ctx.Context().Err(), "a run that nobody stopped is not cancelled")

	ctx.Cancel()

	require.ErrorIs(t, ctx.Context().Err(), context.Canceled)
	require.ErrorIs(t, context.Cause(ctx.Context()), ErrStopped,
		"a handler can tell why it was stopped, not only that it was")

	// Calling Cancel again preserves the original cancellation cause.
	ctx.Cancel()
	require.ErrorIs(t, context.Cause(ctx.Context()), ErrStopped)
}

// Tests that uninitialized or nil Ctx instances do not panic on cancellation or signal watching.
func TestAContextThisSDKDidNotBuildIsInert(t *testing.T) {
	zero := &Ctx{}

	require.Equal(t, context.Background(), zero.Context())
	require.NotPanics(t, zero.Cancel)
	require.NotPanics(t, func() { zero.watchSignals()() })

	var absent *Ctx

	require.NotPanics(t, absent.Cancel)
	require.NotPanics(t, func() { absent.watchSignals()() })
}

// Tests that signal watching teardown functions can be called multiple times safely.
func TestWatchingStopsIdempotently(t *testing.T) {
	ctx := CtxFromRaw(map[string]any{"node_key": "n"})

	stop := ctx.watchSignals()

	require.NotPanics(t, stop)
	require.NotPanics(t, stop)
	require.NoError(t, ctx.Context().Err(), "stopping the watch does not stop the run")
}
