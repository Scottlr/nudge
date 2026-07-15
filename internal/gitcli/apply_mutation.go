package gitcli

import (
	"context"
	"errors"
	"fmt"

	"github.com/Scottlr/nudge/internal/app"
)

// ApplyPatchMutator executes only the exact T069 working-tree mutation. It
// never stages, commits, pushes, or supplies a path filter.
type ApplyPatchMutator struct {
	executable ApplyPatchChecker
}

// NewApplyPatchMutator constructs the mutation adapter from the same trusted
// executable and policy configuration used by apply check.
func NewApplyPatchMutator(config ApplyPatchCheckerConfig) (*ApplyPatchMutator, error) {
	checker, err := NewApplyPatchChecker(config)
	if err != nil {
		return nil, err
	}
	return &ApplyPatchMutator{executable: *checker}, nil
}

// Mutate applies the exact persisted patch to the working tree only.
func (m *ApplyPatchMutator) Mutate(ctx context.Context, request app.ApplyPatchMutationRequest) error {
	if m == nil || ctx == nil || request.Validate() != nil || request.ApplyPolicyVersion != m.executable.apply.Version {
		return app.ErrInvalidApplyPreflight
	}
	args, err := m.executable.apply.Args(ApplyMutationPhase)
	if err != nil {
		return app.ErrInvalidApplyPreflight
	}
	builder, err := NewCommandBuilder(CommandBuilderConfig{Executable: m.executable.executable, Runner: m.executable.runner, StartPath: request.Worktree.RootPath, Policy: m.executable.machine})
	if err != nil {
		return err
	}
	if _, err := builder.RunInput(ctx, request.Patch, args...); err != nil {
		var gitErr *GitError
		if errors.As(err, &gitErr) && gitErr.Code == ErrorInvalidInput {
			return err
		}
		return fmt.Errorf("%w: %w", app.ErrApplyMutationFailed, err)
	}
	return nil
}

var _ app.ApplyPatchMutator = (*ApplyPatchMutator)(nil)
