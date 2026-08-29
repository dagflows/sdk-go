package failure

import "github.com/dagflows/sdk-go/runtime"

type (
	FailureCategory  = runtime.FailureCategory
	Fail             = runtime.Fail
	InputTooLarge    = runtime.InputTooLarge
	InputUnavailable = runtime.InputUnavailable
	OutputTooLarge   = runtime.OutputTooLarge
)

const (
	PERMANENT      = runtime.PERMANENT
	INFRASTRUCTURE = runtime.INFRASTRUCTURE
	TIMEOUT        = runtime.TIMEOUT
	EXECUTION      = runtime.EXECUTION
)
