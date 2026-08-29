package runtime

import (
	"os"
	"strconv"
)

// Notify the guest agent that runtime initialization is complete and the handler is starting.
// The node's execution timeout begins counting from this signal, ensuring that
// interpreter startup and module imports do not consume the user's execution budget.
// The signal dynamically tightens the execution deadline. If never sent, the agent
// terminates the process at the hard ceiling (init allowance + timeout).
// This function is idempotent and safe to call multiple times. If the ready file
// descriptor is unset or unavailable, it silently no-ops.
func signalReady() {
	fd, err := strconv.Atoi(os.Getenv(ReadyFDEnv))
	if err != nil || fd <= 0 {
		return
	}

	// Borrowed, not owned: the descriptor is the agent's, and closing it here
	// would take away a channel this SDK was only lent.
	pipe := os.NewFile(uintptr(fd), "dagflows-ready")
	if pipe == nil {
		return
	}

	// A write that fails means nothing is listening. The node runs either way,
	// only its own deadline is affected.
	_, _ = pipe.Write([]byte("."))
}
