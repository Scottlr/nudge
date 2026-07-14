package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootCommandShowsHelp(t *testing.T) {
	var output bytes.Buffer
	command := NewRootCommand(BuildInfo{})
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"--help"})

	if err := command.Execute(); err != nil {
		t.Fatalf("execute root command: %v", err)
	}
	if !strings.Contains(output.String(), "Usage:") {
		t.Fatalf("root output does not contain usage: %q", output.String())
	}
}

func TestVersionCommandUsesInjectedBuildInfo(t *testing.T) {
	var output bytes.Buffer
	command := NewRootCommand(BuildInfo{
		Version: "0.1.0-dev",
		Commit:  "abc123",
		Date:    "2026-07-14",
	})
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"version"})

	if err := command.Execute(); err != nil {
		t.Fatalf("execute version command: %v", err)
	}
	want := "nudge version=0.1.0-dev commit=abc123 date=2026-07-14\n"
	if output.String() != want {
		t.Fatalf("version output = %q, want %q", output.String(), want)
	}
}

func TestRootCommandReturnsArgumentErrorWithoutPrintingIt(t *testing.T) {
	var output bytes.Buffer
	command := NewRootCommand(BuildInfo{})
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"one", "two"})

	err := command.Execute()
	if err == nil {
		t.Fatal("expected argument error")
	}
	if !strings.Contains(err.Error(), "accepts at most 1 arg(s), received 2") {
		t.Fatalf("argument error = %q", err)
	}
	if output.Len() != 0 {
		t.Fatalf("unexpected command output: %q", output.String())
	}
}
