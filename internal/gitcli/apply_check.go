package gitcli

import (
	"context"
	"errors"
	"fmt"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/process"
)

// ApplyPatchCheckerConfig supplies the trusted Git process and the exact
// machine-read and apply-check policies.
type ApplyPatchCheckerConfig struct {
	Executable process.ExecutableIdentity
	Runner     process.Runner
	Machine    MachineGitReadPolicyV1
	Apply      ApplyPolicyV1
}

// ApplyPatchChecker runs only Git's non-mutating apply check. It never passes
// user-controlled flags, whitespace settings, paths, or index options.
type ApplyPatchChecker struct {
	executable process.ExecutableIdentity
	runner     process.Runner
	machine    MachineGitReadPolicyV1
	apply      ApplyPolicyV1
}

// NewApplyPatchChecker constructs the exact v1 apply-check adapter.
func NewApplyPatchChecker(config ApplyPatchCheckerConfig) (*ApplyPatchChecker, error) {
	if err := config.Executable.Validate(); err != nil || config.Runner == nil {
		return nil, &GitError{Code: ErrorInvalidInput, Cause: err}
	}
	if err := config.Machine.validate(); err != nil {
		return nil, err
	}
	if err := config.Apply.Validate(); err != nil {
		return nil, err
	}
	return &ApplyPatchChecker{executable: config.Executable, runner: config.Runner, machine: config.Machine, apply: config.Apply}, nil
}

// Check runs `git apply --check` with the fixed v1 policy and patch bytes
// supplied by the immutable proposal artifact reader.
func (c *ApplyPatchChecker) Check(ctx context.Context, request app.ApplyPatchCheckRequest) error {
	if c == nil || ctx == nil || request.Validate() != nil || request.ApplyPolicyVersion != c.apply.Version {
		return app.ErrInvalidApplyPreflight
	}
	args, err := c.apply.Args(ApplyCheckPhase)
	if err != nil {
		return app.ErrInvalidApplyPreflight
	}
	builder, err := NewCommandBuilder(CommandBuilderConfig{Executable: c.executable, Runner: c.runner, StartPath: request.Worktree.RootPath, Policy: c.machine})
	if err != nil {
		return err
	}
	if _, err := builder.RunInput(ctx, request.Patch, args...); err != nil {
		var gitErr *GitError
		if errors.As(err, &gitErr) && gitErr.Code == ErrorInvalidInput {
			return err
		}
		return fmt.Errorf("%w: %w", app.ErrApplyPatchCheckFailed, err)
	}
	return nil
}

var _ app.ApplyPatchChecker = (*ApplyPatchChecker)(nil)
