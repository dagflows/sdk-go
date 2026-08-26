package runtime

import (
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"unicode/utf8"
)

// Handler defines the entrypoint signature for workflow node execution.
type Handler = func(ctx *Ctx, inputs *Inputs) (any, error)

// Failed represents the failure envelope payload written to DAGFLOWS_OUTPUT.
type Failed struct {
	Status string       `json:"status"`
	Error  FailureError `json:"error"`
	Retry  *Retry       `json:"retry,omitempty"`
}

type FailureError struct {
	Message  string `json:"message"`
	Category string `json:"category"`
}

type Retry struct {
	Abort        bool `json:"abort,omitempty"`
	AfterSeconds *int `json:"after_seconds,omitempty"`
}

// maxMessageBytes limits failure message length to avoid payload truncation by transport caps.
const maxMessageBytes = 8 * 1024

func bounded(message string) string {
	if len(message) <= maxMessageBytes {
		return message
	}

	cut := maxMessageBytes

	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}

	return message[:cut] + "...(truncated)"
}

// Failure converts an error into a structured Failed envelope with retry directives.
func Failure(err error) *Failed {
	fail, ok := errors.AsType[*Fail](err)
	if !ok {
		return &Failed{
			Status: "FAILED",
			Error: FailureError{
				Message:  bounded(err.Error()),
				Category: PERMANENT,
			},
			Retry: &Retry{
				Abort: true,
			},
		}
	}

	out := &Failed{
		Status: "FAILED",
		Error: FailureError{
			Message:  bounded(fail.Message),
			Category: fail.category(),
		},
	}

	retry := &Retry{
		Abort: fail.aborts(),
	}

	if fail.RetryAfter != 0 {
		retry.AfterSeconds = new(fail.RetryAfter)
	}

	if retry.Abort || retry.AfterSeconds != nil {
		out.Retry = retry
	}

	return out
}

// Write serializes the envelope to the specified path or DAGFLOWS_OUTPUT.
func Write(envelope any, path string) error {
	if path == "" {
		path = os.Getenv(OutputEnv)
	}

	if path == "" {
		return fmt.Errorf("%s is not set, run this node through the platform, or set it to a path to inspect what it would return", OutputEnv)
	}

	body, err := compact(envelope)
	if err != nil {
		return err
	}

	return os.WriteFile(path, body, 0o644)
}

// Report writes a failure envelope to DAGFLOWS_OUTPUT for an unhandled error.
func Report(err error) error {
	return Write(Failure(err), "")
}

// Run loads input context, executes the handler, and writes the output envelope.
func Run(handler Handler) error {
	ctx, inputs, err := Load("")
	if err != nil {
		return err
	}

	return Write(Execute(handler, ctx, inputs), "")
}

// Execute runs the handler, recovering panics and transforming returns to envelopes.
func Execute(handler Handler, ctx *Ctx, inputs *Inputs) (envelope any) {
	defer func() {
		if recovered := recover(); recovered != nil {
			fmt.Fprintf(os.Stderr, "panic: %v\n\n%s", recovered, debug.Stack())
			envelope = Failure(fmt.Errorf("panic: %v", recovered))
		}
	}()

	// The handler starts here, so this is where the node's timeout should begin
	// being charged. Everything above was the runtime starting up.
	signalReady()

	answer, err := handler(ctx, inputs)
	if err != nil {
		return Failure(err)
	}

	success, err := ToEnvelope(answer, ctx.InlineMaxBytes, ctx.upload(), ctx.MemoryLimitMB, ctx.Multipart())
	if err != nil {
		return Failure(err)
	}

	return success
}
