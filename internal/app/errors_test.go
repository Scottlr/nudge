package app

import (
	"errors"
	"strings"
	"testing"
)

func TestAppErrorPreservesCauseWithoutLeakingPrivateDetail(t *testing.T) {
	cause := errors.New("private cause")
	err := &AppError{
		Code:        CodeInternal,
		Operation:   "load review state",
		UserMessage: "Review state is unavailable.",
		Detail:      "database path and private diagnostic",
		Cause:       cause,
	}

	if got := err.Error(); got != err.UserMessage {
		t.Fatalf("error text = %q, want %q", got, err.UserMessage)
	}
	if strings.Contains(err.Error(), err.Detail) || strings.Contains(err.Error(), cause.Error()) {
		t.Fatalf("error text leaked private data: %q", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Fatal("errors.Is did not find the wrapped cause")
	}
	var typed *AppError
	if !errors.As(err, &typed) || typed != err {
		t.Fatal("errors.As did not recover AppError")
	}
}

func TestAppErrorFallsBackToStableCode(t *testing.T) {
	err := &AppError{Code: CodeUnsupported}
	if got, want := err.Error(), string(CodeUnsupported); got != want {
		t.Fatalf("error text = %q, want %q", got, want)
	}
}
