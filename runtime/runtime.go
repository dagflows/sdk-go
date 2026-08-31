// Package runtime is the runtime execution environment and node runner API.
//
// It reads the input envelope the guest agent writes, gives a handler its
// execution context and its parents' outputs, and writes what the handler
// returns back as the output envelope.
package runtime

import "iter"

// Environment variable names the guest agent sets inside the microVM.
const (
	InputEnv  = "DAGFLOWS_INPUT"
	OutputEnv = "DAGFLOWS_OUTPUT"
	// ReadyFDEnv names the descriptor the agent listens on for the runtime's
	// ready signal.
	ReadyFDEnv = "DAGFLOWS_READY_FD"
)

// Block types a payload entry in an envelope can carry.
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

// Rows is a stream of records of type T, returned by a handler that produces
// rows and accepted by a handler that consumes a parent's rows one at a time.
// It is a plain iter.Seq2, so a parent declared as one and a child declared as
// the other are the same type.
type Rows[T any] = iter.Seq2[T, error]

// rows represents an iterator over generic record values.
type rows = iter2[any]
