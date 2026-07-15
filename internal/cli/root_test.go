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
	if command.Flags().Lookup("no-persist") == nil || !strings.Contains(output.String(), "--no-persist") {
		t.Fatalf("root output does not expose --no-persist: %q", output.String())
	}
	if command.Flags().Lookup("theme") == nil || !strings.Contains(output.String(), "--theme") {
		t.Fatalf("root output does not expose --theme: %q", output.String())
	}
	for _, flag := range []string{"local", "commit", "branch"} {
		if command.Flags().Lookup(flag) == nil || !strings.Contains(output.String(), "--"+flag) {
			t.Fatalf("root output does not expose --%s: %q", flag, output.String())
		}
	}
	found := false
	for _, child := range command.Commands() {
		if child.Name() == "doctor" {
			found = true
		}
	}
	if !found {
		t.Fatal("root command does not expose doctor")
	}
}

func TestRootCommandRejectsTargetFlagCombinationsBeforeRun(t *testing.T) {
	for _, args := range [][]string{{"--local", "--branch", "main"}, {"--commit", "HEAD", "--branch", "main"}, {"--branch"}} {
		command := NewRootCommand(BuildInfo{})
		command.SetArgs(args)
		if err := command.Execute(); err == nil {
			t.Fatalf("args %#v unexpectedly succeeded", args)
		}
	}
}

func TestRootCommandRejectsOptionLookingCommitBeforeApplicationStartup(t *testing.T) {
	command := NewRootCommand(BuildInfo{})
	command.SetArgs([]string{"--commit", "-main"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "invalid --commit revision expression") {
		t.Fatalf("leading-dash commit error = %v", err)
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
