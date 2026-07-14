package gitcli

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/Scottlr/nudge/internal/process"
)

type recordingRunner struct {
	spec process.Spec
}

func (r *recordingRunner) Run(_ context.Context, spec process.Spec) (process.Result, error) {
	r.spec = spec
	return process.Result{Stdout: []byte("false\n")}, nil
}

func (*recordingRunner) RunStream(context.Context, process.Spec, io.Writer) (process.StreamResult, error) {
	return process.StreamResult{}, errors.New("not used")
}

func (*recordingRunner) Start(context.Context, process.Spec, process.StreamSink) (process.Process, error) {
	return nil, errors.New("not used")
}

func TestCommandBuilderAppliesMachineReadPolicy(t *testing.T) {
	start := t.TempDir()
	identity, err := process.NewExecutableResolver().Resolve(context.Background(), process.ResolveExecutableRequest{
		Kind:       process.ExecutableGit,
		SearchPath: os.Getenv("PATH"),
		CurrentDir: start,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	builder, err := NewCommandBuilder(CommandBuilderConfig{
		Executable: identity,
		Runner:     runner,
		StartPath:  start,
		Policy:     DefaultMachineGitReadPolicyV1(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := builder.Run(context.Background(), "rev-parse", "--is-bare-repository"); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(runner.spec.Args, "\x00")
	for _, expected := range []string{"--no-pager", "core.fsmonitor=false", "core.untrackedCache=false", "core.hooksPath=" + os.DevNull, "credential.helper=", "filter.lfs.process=", "rev-parse", "--is-bare-repository"} {
		if !strings.Contains(args, expected) {
			t.Fatalf("Git args %q do not contain %q", args, expected)
		}
	}
	for index, argument := range runner.spec.Args {
		if argument == "-C" {
			if index+1 >= len(runner.spec.Args) || runner.spec.Args[index+1] != builder.startPath {
				t.Fatalf("canonical -C argument = %#v, want %q", runner.spec.Args, builder.startPath)
			}
			break
		}
	}
	if runner.spec.Environment.Set["GIT_NO_LAZY_FETCH"] != "1" || runner.spec.Environment.Set["GIT_OPTIONAL_LOCKS"] != "0" || runner.spec.Environment.Set["GIT_TERMINAL_PROMPT"] != "0" {
		t.Fatalf("machine Git environment = %#v", runner.spec.Environment)
	}
	for _, key := range []string{"GIT_DIR", "GIT_INDEX_FILE", "GIT_EXTERNAL_DIFF", "GIT_EDITOR", "PAGER", "GIT_SSH_COMMAND"} {
		if !containsString(runner.spec.Environment.Remove, key) {
			t.Fatalf("environment removal missing %q: %#v", key, runner.spec.Environment.Remove)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
