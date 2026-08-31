//go:build unix

package runtime

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Tests that a SIGTERM signal triggers handler context cancellation for graceful shutdown.
func TestATerminateSignalReachesTheHandlerAsCancellation(t *testing.T) {
	ctx := CtxFromRaw(map[string]any{"node_key": "n", "inline_max_bytes": 1 << 20})

	running := make(chan struct{})
	killed := make(chan error, 1)

	handler := func(c *Ctx, _ *Inputs) (any, error) {
		close(running)

		select {
		case <-c.Context().Done():
			return map[string]any{"cause": context.Cause(c.Context()).Error()}, nil
		case <-time.After(30 * time.Second):
			return nil, errors.New("the signal never reached the handler")
		}
	}

	go func() {
		// Wait until the handler starts and signal watching is active before delivering SIGTERM.
		<-running
		killed <- syscall.Kill(syscall.Getpid(), syscall.SIGTERM)
	}()

	envelope := Execute(handler, ctx, NewInputs(nil, 0))
	require.NoError(t, <-killed)

	success, ok := envelope.(*Envelope)
	require.True(t, ok, "a handler that returns after being stopped still succeeds: %#v", envelope)
	require.Equal(t, map[string]any{"cause": ErrStopped.Error()}, success.Output.Data)
}
