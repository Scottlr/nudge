// Package gitcli contains the installed-Git adapter. Git remains the source
// of truth for repository, worktree, and revision semantics.
package gitcli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Scottlr/nudge/internal/process"
)

const (
	// MachineGitReadPolicyVersion identifies the exact read controls applied to
	// every repository-resolution command.
	MachineGitReadPolicyVersion uint32 = 1
	defaultGitStdoutLimit              = 256 * 1024 * 1024
	defaultGitStderrLimit              = 1 * 1024 * 1024
)

// MachineGitReadPolicyV1 centralizes non-mutating, non-interactive Git read
// behavior. A policy change requires a new version and review.
type MachineGitReadPolicyV1 struct {
	Version     uint32
	StdoutLimit int64
	StderrLimit int64
	Timeout     time.Duration
}

// DefaultMachineGitReadPolicyV1 returns the bounded repository-read policy.
func DefaultMachineGitReadPolicyV1() MachineGitReadPolicyV1 {
	return MachineGitReadPolicyV1{
		Version:     MachineGitReadPolicyVersion,
		StdoutLimit: defaultGitStdoutLimit,
		StderrLimit: defaultGitStderrLimit,
		Timeout:     30 * time.Second,
	}
}

func (p MachineGitReadPolicyV1) validate() error {
	if p.Version != MachineGitReadPolicyVersion || p.StdoutLimit <= 0 || p.StderrLimit <= 0 || p.Timeout <= 0 {
		return &GitError{Code: ErrorInvalidInput}
	}
	return nil
}

// ErrorCode identifies a stable Git-adapter failure category.
type ErrorCode string

const (
	ErrorInvalidInput              ErrorCode = "git_invalid_input"
	ErrorOutsideRepository         ErrorCode = "repository_not_found"
	ErrorBareRepository            ErrorCode = "repository_bare_unsupported"
	ErrorGitUnavailable            ErrorCode = "git_unavailable"
	ErrorPermission                ErrorCode = "git_permission_denied"
	ErrorTimeout                   ErrorCode = "git_timeout"
	ErrorCanceled                  ErrorCode = "git_canceled"
	ErrorCommandFailed             ErrorCode = "git_command_failed"
	ErrorMalformedOutput           ErrorCode = "git_malformed_output"
	ErrorNativeIdentityUnavailable ErrorCode = "repository_identity_unavailable"
	ErrorObjectUnavailableNoFetch  ErrorCode = "object_unavailable_no_fetch"
	ErrorOutputLimit               ErrorCode = "git_output_limit"
)

// GitError exposes only a stable safe code. Stderr and the process cause stay
// private for diagnostics and are never included in Error's text.
type GitError struct {
	Code     ErrorCode
	ExitCode int
	Stderr   string
	Cause    error
}

func (e *GitError) Error() string {
	if e == nil || e.Code == "" {
		return string(ErrorCommandFailed)
	}
	return string(e.Code)
}

func (e *GitError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *GitError) Is(target error) bool {
	other, ok := target.(*GitError)
	return ok && e != nil && other != nil && e.Code == other.Code
}

// CommandBuilder constructs every machine Git invocation with explicit argv,
// a trusted executable identity, and the central read policy.
type CommandBuilder struct {
	executable process.ExecutableIdentity
	runner     process.Runner
	startPath  string
	policy     MachineGitReadPolicyV1
}

// CommandBuilderConfig supplies the trusted executable and bounded runner.
type CommandBuilderConfig struct {
	Executable process.ExecutableIdentity
	Runner     process.Runner
	StartPath  string
	Policy     MachineGitReadPolicyV1
}

// NewCommandBuilder constructs a read-only Git command builder.
func NewCommandBuilder(config CommandBuilderConfig) (*CommandBuilder, error) {
	if err := config.Executable.Validate(); err != nil {
		return nil, &GitError{Code: ErrorGitUnavailable, Cause: err}
	}
	if config.Runner == nil {
		return nil, &GitError{Code: ErrorInvalidInput}
	}
	if err := config.Policy.validate(); err != nil {
		return nil, err
	}
	startPath, err := canonicalExistingDirectory(config.StartPath)
	if err != nil {
		return nil, &GitError{Code: ErrorInvalidInput, Cause: err}
	}
	return &CommandBuilder{
		executable: config.Executable,
		runner:     config.Runner,
		startPath:  startPath,
		policy:     config.Policy,
	}, nil
}

// Args returns the explicit argv after Git global read controls and -C.
func (b *CommandBuilder) Args(command ...string) []string {
	args := []string{
		"--no-optional-locks",
		"--no-pager",
		"-c", "color.ui=false",
		"-c", "core.pager=",
		"-c", "core.fsmonitor=false",
		"-c", "core.untrackedCache=false",
		"-c", "core.fscache=false",
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.attributesfile=",
		"-c", "core.excludesfile=",
		"-c", "diff.external=",
		"-c", "credential.helper=",
		"-c", "filter.lfs.process=",
		"-c", "filter.lfs.smudge=",
		"-c", "filter.lfs.clean=",
		"-c", "filter.lfs.required=false",
		"-c", "submodule.recurse=false",
		"-C", b.startPath,
	}
	return append(args, command...)
}

// Run executes one bounded Git read through the trusted process runner.
func (b *CommandBuilder) Run(ctx context.Context, command ...string) (process.Result, error) {
	if ctx == nil {
		return process.Result{}, &GitError{Code: ErrorInvalidInput}
	}
	if err := validateCommandArgs(command); err != nil {
		return process.Result{}, err
	}
	result, err := b.runner.Run(ctx, process.Spec{
		Executable:  b.executable,
		Args:        b.Args(command...),
		Environment: b.policy.EnvironmentPolicy(),
		Timeout:     b.policy.Timeout,
		StdoutLimit: b.policy.StdoutLimit,
		StderrLimit: b.policy.StderrLimit,
	})
	if err == nil {
		return result, nil
	}
	return result, classifyProcessError(err, result)
}

// RunInput executes one bounded Git read with an explicit stdin reader. It is
// used only for Git commands whose read-only semantics require a finite input,
// such as deriving the object-format-specific empty tree.
func (b *CommandBuilder) RunInput(ctx context.Context, input io.Reader, command ...string) (process.Result, error) {
	if ctx == nil || input == nil {
		return process.Result{}, &GitError{Code: ErrorInvalidInput}
	}
	if err := validateCommandArgs(command); err != nil {
		return process.Result{}, err
	}
	result, err := b.runner.Run(ctx, process.Spec{
		Executable:  b.executable,
		Args:        b.Args(command...),
		Environment: b.policy.EnvironmentPolicy(),
		Stdin:       input,
		Timeout:     b.policy.Timeout,
		StdoutLimit: b.policy.StdoutLimit,
		StderrLimit: b.policy.StderrLimit,
	})
	if err == nil {
		return result, nil
	}
	return result, classifyProcessError(err, result)
}

// RunStream executes one bounded Git read while writing stdout directly to an
// owner-controlled sink. Partial stream identities are never returned as
// complete by the process runner.
func (b *CommandBuilder) RunStream(ctx context.Context, stdout io.Writer, command ...string) (process.StreamResult, error) {
	if ctx == nil || stdout == nil {
		return process.StreamResult{}, &GitError{Code: ErrorInvalidInput}
	}
	if err := validateCommandArgs(command); err != nil {
		return process.StreamResult{}, err
	}
	result, err := b.runner.RunStream(ctx, process.Spec{
		Executable:  b.executable,
		Args:        b.Args(command...),
		Environment: b.policy.EnvironmentPolicy(),
		Timeout:     b.policy.Timeout,
		StdoutLimit: b.policy.StdoutLimit,
		StderrLimit: b.policy.StderrLimit,
	}, stdout)
	if err == nil {
		return result, nil
	}
	return result, classifyProcessError(err, process.Result{ExitCode: result.ExitCode, Stderr: result.StderrTail})
}

func validateCommandArgs(command []string) error {
	for _, arg := range command {
		if strings.IndexByte(arg, 0) >= 0 {
			return &GitError{Code: ErrorInvalidInput}
		}
	}
	return nil
}

func classifyProcessError(cause error, result process.Result) error {
	var timeout *process.TimeoutError
	if errors.As(cause, &timeout) {
		return &GitError{Code: ErrorTimeout, Cause: cause, Stderr: string(result.Stderr), ExitCode: result.ExitCode}
	}
	var canceled *process.CanceledError
	if errors.As(cause, &canceled) {
		return &GitError{Code: ErrorCanceled, Cause: cause, Stderr: string(result.Stderr), ExitCode: result.ExitCode}
	}
	var validation *process.ValidationError
	if errors.As(cause, &validation) {
		return &GitError{Code: ErrorInvalidInput, Cause: cause, Stderr: string(result.Stderr), ExitCode: result.ExitCode}
	}
	var spawn *process.SpawnError
	if errors.As(cause, &spawn) {
		return &GitError{Code: ErrorGitUnavailable, Cause: cause, Stderr: string(result.Stderr), ExitCode: result.ExitCode}
	}
	var limit *process.LimitError
	if errors.As(cause, &limit) {
		return &GitError{Code: ErrorOutputLimit, Cause: cause, Stderr: string(result.Stderr), ExitCode: result.ExitCode}
	}
	var exit *process.ExitError
	if errors.As(cause, &exit) {
		return &GitError{Code: ErrorCommandFailed, Cause: cause, Stderr: string(result.Stderr), ExitCode: exit.ExitCode}
	}
	return &GitError{Code: ErrorCommandFailed, Cause: cause, Stderr: string(result.Stderr), ExitCode: result.ExitCode}
}

func malformed(format string, args ...any) error {
	return &GitError{Code: ErrorMalformedOutput, Cause: fmt.Errorf(format, args...)}
}
