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
// Setting RetryAfter implies retry. Leaving Abort nil defaults to aborting
// unless a RetryAfter delay is provided.
type Fail struct {
	Message string

	// Category defaults to PERMANENT if empty or unrecognized.
	Category string

	// RetryAfter is the retry delay in seconds. Zero means no delay was specified.
	RetryAfter int

	// Abort controls retry behavior. Nil defaults to false if RetryAfter is set, true otherwise.
	Abort *bool
}

func (f *Fail) Error() string {
	return f.Message
}

// category returns the validated category, falling back to PERMANENT.
func (f *Fail) category() string {
	if knownCategory(f.Category) {
		return f.Category
	}

	return PERMANENT
}

// aborts returns whether the failure should abort execution without retrying.
func (f *Fail) aborts() bool {
	if f.Abort != nil {
		return *f.Abort
	}

	return f.RetryAfter == 0
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
