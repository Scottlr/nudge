package app

// ErrorCode identifies a stable application failure category.
type ErrorCode string

const (
	// CodeInvalidInput identifies input that cannot be accepted.
	CodeInvalidInput ErrorCode = "invalid_input"
	// CodeCancelled identifies an operation cancelled by its owner.
	CodeCancelled ErrorCode = "cancelled"
	// CodeDeadlineExceeded identifies an operation that reached its deadline.
	CodeDeadlineExceeded ErrorCode = "deadline_exceeded"
	// CodeUnsupported identifies a capability or input not supported by this build.
	CodeUnsupported ErrorCode = "unsupported"
	// CodeInternal identifies an unexpected application failure.
	CodeInternal ErrorCode = "internal"
)

// AppError is an application failure with a safe user-facing surface and
// optional private diagnostic detail. Error and Unwrap never expose Detail or
// the text of Cause through normal rendering.
type AppError struct {
	Code        ErrorCode
	Operation   string
	UserMessage string
	Detail      string
	Retryable   bool
	Cause       error
}

// Error returns only the safe user-facing message or stable error category.
func (e *AppError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.UserMessage != "" {
		return e.UserMessage
	}
	if e.Code != "" {
		return string(e.Code)
	}
	return "application error"
}

// Unwrap returns the private cause for errors.Is and errors.As traversal.
func (e *AppError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
