package runtime

// Failure categories that the worker recognizes.
const (
	PERMANENT      = "permanent"
	INFRASTRUCTURE = "infrastructure"
	TIMEOUT        = "timeout"
	EXECUTION      = "execution"
)

// knownCategory checks if the worker retry policy understands this category.
func knownCategory(category string) bool {
	switch category {
	case PERMANENT, INFRASTRUCTURE, TIMEOUT, EXECUTION:
		return true
	}

	return false
}

// Fail signals a deliberate node failure with custom retry behavior.
//
// Setting RetryAfterMs implies retry, so a failure that names a delay is not
// treated as permanent unless the caller says so. Leaving Abort nil defaults to
// aborting unless a delay is provided.
type Fail struct {
	Message string

	// Category defaults to PERMANENT if empty or unrecognized, except when a
	// delay is named: a permanent failure is never retried, so defaulting to it
	// would silently discard the delay the caller asked for.
	Category string

	// RetryAfterMs is the retry delay in milliseconds, the unit the platform's
	// backoff settings use and the one it is capped against. Zero means no
	// delay was specified.
	RetryAfterMs int

	// Abort controls retry behavior. Nil defaults to false if RetryAfterMs is set, true otherwise.
	Abort *bool
}

func (f *Fail) Error() string {
	return f.Message
}

// category returns the validated category, omitting it when a delay is set to avoid defaulting to permanent.
func (f *Fail) category() string {
	if knownCategory(f.Category) {
		return f.Category
	}

	if f.Category == "" && f.RetryAfterMs > 0 {
		return ""
	}

	return PERMANENT
}

// aborts returns whether the failure should abort execution without retrying.
func (f *Fail) aborts() bool {
	if f.Abort != nil {
		return *f.Abort
	}

	return f.RetryAfterMs == 0
}

// OutputTooLarge means the encoded output exceeded what the node can hold.
type OutputTooLarge struct {
	Message string
}

func (e *OutputTooLarge) Error() string {
	return e.Message
}

// Is lets errors.Is match any OutputTooLarge error.
func (e *OutputTooLarge) Is(target error) bool {
	_, ok := target.(*OutputTooLarge)

	return ok
}

// InputUnavailable means the requested input could not be delivered in this format.
type InputUnavailable struct {
	Message string
}

func (e *InputUnavailable) Error() string {
	return e.Message
}

// Is lets errors.Is match any InputUnavailable error.
func (e *InputUnavailable) Is(target error) bool {
	_, ok := target.(*InputUnavailable)

	return ok
}

// InputTooLarge means loading this input into memory would exceed the node RAM limit.
type InputTooLarge struct {
	Message string
}

func (e *InputTooLarge) Error() string {
	return e.Message
}

// Is lets errors.Is match any InputTooLarge error.
func (e *InputTooLarge) Is(target error) bool {
	_, ok := target.(*InputTooLarge)

	return ok
}
