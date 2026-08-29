package runtime

import "iter"

// Environment variables set by the guest agent inside the microVM.
const (
	InputEnv  = "DAGFLOWS_INPUT"
	OutputEnv = "DAGFLOWS_OUTPUT"
	// ReadyFDEnv names the descriptor the agent listens on for the runtime's
	// ready signal.
	ReadyFDEnv = "DAGFLOWS_READY_FD"
)

// Block types used in payload envelopes.
const (
	INLINE    = "INLINE"
	REFERENCE = "REFERENCE"
)

// DefaultInlineMaxBytes is the fallback limit for inline payloads when none is specified.
const DefaultInlineMaxBytes = 256 * 1024

// chunkBytes is the read buffer size for streaming reference payloads.
const chunkBytes = 64 * 1024

// iter2 is an iterator yielding values with per-element error handling.
type iter2[T any] = iter.Seq2[T, error]

// rows represents an iterator over generic record values.
type rows = iter2[any]
