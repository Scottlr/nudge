package process

import (
	"context"
	"errors"
	"fmt"
)

// ErrorKind identifies a stable process-runner failure category.
type ErrorKind string

const (
	ErrorInvalidInput ErrorKind = "invalid_input"
	ErrorSpawn        ErrorKind = "spawn"
	ErrorExit         ErrorKind = "exit"
	ErrorCanceled     ErrorKind = "canceled"
	ErrorTimeout      ErrorKind = "timeout"
	ErrorLimit        ErrorKind = "limit"
	ErrorSink         ErrorKind = "sink"
	ErrorStdin        ErrorKind = "stdin"
)

// Stream identifies one child-process output stream.
type Stream string

const (
	StreamStdout Stream = "stdout"
	StreamStderr Stream = "stderr"
)

// ValidationError reports a rejected process specification without exposing
// the supplied executable, arguments, environment, or paths.
type ValidationError struct {
	Field string
}

func (e *ValidationError) Error() string {
	if e == nil || e.Field == "" {
		return "invalid process specification"
	}
	return fmt.Sprintf("invalid process specification: %s", e.Field)
}

func (e *ValidationError) Kind() ErrorKind { return ErrorInvalidInput }

// SpawnError reports that the child could not be started. The underlying
// error remains available to callers that need platform-specific inspection,
// while Error deliberately omits command paths and arguments.
type SpawnError struct{ Cause error }

func (e *SpawnError) Error() string {
	if e == nil {
		return "process could not be started"
	}
	return "process could not be started"
}

func (e *SpawnError) Kind() ErrorKind { return ErrorSpawn }
func (e *SpawnError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// ExitError reports a child that exited unsuccessfully. ExitCode is also
// copied into the returned Result or StreamResult when one is available.
type ExitError struct {
	ExitCode int
	Cause    error
}

func (e *ExitError) Error() string {
	if e == nil {
		return "process exited unsuccessfully"
	}
	return fmt.Sprintf("process exited with status %d", e.ExitCode)
}

func (e *ExitError) Kind() ErrorKind { return ErrorExit }
func (e *ExitError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// CanceledError reports cancellation by the caller, its context, or the
// process owner. Cause is normally context.Canceled or context.DeadlineExceeded.
type CanceledError struct {
	Cause error
}

func (e *CanceledError) Error() string {
	if e != nil && errors.Is(e.Cause, context.DeadlineExceeded) {
		return "process deadline exceeded"
	}
	return "process canceled"
}

func (e *CanceledError) Kind() ErrorKind { return ErrorCanceled }
func (e *CanceledError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// TimeoutError reports a process operation that reached its deadline.
type TimeoutError struct{ Cause error }

func (e *TimeoutError) Error() string   { return "process deadline exceeded" }
func (e *TimeoutError) Kind() ErrorKind { return ErrorTimeout }
func (e *TimeoutError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// CancelledError is an alternate spelling retained for callers using the
// product's British-English terminology.
type CancelledError = CanceledError

// CancellationError is a descriptive alias for callers that prefer the
// longer name.
type CancellationError = CanceledError

func contextFailure(cause error) error {
	if errors.Is(cause, context.DeadlineExceeded) {
		return &TimeoutError{Cause: cause}
	}
	return &CanceledError{Cause: cause}
}

// LimitError reports a hard output limit. Observed is bounded to avoid
// integer overflow and is never used to retain unbounded child output.
type LimitError struct {
	Stream   Stream
	Limit    int64
	Observed int64
}

func (e *LimitError) Error() string {
	if e == nil {
		return "process output limit exceeded"
	}
	return fmt.Sprintf("process %s output limit exceeded", e.Stream)
}

func (e *LimitError) Kind() ErrorKind { return ErrorLimit }

// SinkError reports a caller-owned stream sink failure. The sink cause is
// available through Unwrap but is not rendered by default.
type SinkError struct {
	Stream Stream
	Cause  error
}

func (e *SinkError) Error() string {
	if e == nil {
		return "process output sink failed"
	}
	return fmt.Sprintf("process %s sink failed", e.Stream)
}

func (e *SinkError) Kind() ErrorKind { return ErrorSink }
func (e *SinkError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// StdinError reports writes after managed stdin has been closed or after the
// child has exited.
type StdinError struct{ Cause error }

func (e *StdinError) Error() string   { return "managed stdin is closed" }
func (e *StdinError) Kind() ErrorKind { return ErrorStdin }
func (e *StdinError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func invalid(field string) error { return &ValidationError{Field: field} }
