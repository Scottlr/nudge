package gitcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

const maxMergeBaseCandidates = 64

// BaseDiscoverySource identifies the local-ref candidate that supplied a
// discovered base. It is explanatory metadata; the resolved object ID is the
// frozen generation identity.
type BaseDiscoverySource string

const (
	BaseDiscoveryUpstream     BaseDiscoverySource = "upstream"
	BaseDiscoveryOriginHead   BaseDiscoverySource = "origin_head"
	BaseDiscoveryLocalMain    BaseDiscoverySource = "local_main"
	BaseDiscoveryRemoteMain   BaseDiscoverySource = "remote_main"
	BaseDiscoveryLocalMaster  BaseDiscoverySource = "local_master"
	BaseDiscoveryRemoteMaster BaseDiscoverySource = "remote_master"
)

// DiscoverBaseBranch returns the first usable repository-local base candidate
// in deterministic order. It never fetches or updates refs.
func (r *Resolver) DiscoverBaseBranch(ctx context.Context, repo repository.Repository, worktree repository.WorktreeRef) (app.BaseBranchDiscovery, error) {
	if r == nil || ctx == nil || repo.Validate() != nil || worktree.Validate() != nil || worktree.RepositoryID != repo.ID {
		return app.BaseBranchDiscovery{}, &GitError{Code: ErrorInvalidInput}
	}
	builder, err := r.builder(worktree.RootPath)
	if err != nil {
		return app.BaseBranchDiscovery{}, err
	}
	if worktree.Upstream != nil {
		ref := worktree.Upstream.RefName
		if _, resolveErr := r.resolveExpression(ctx, builder, ref); resolveErr == nil {
			return discoveredBase(ref, ref, BaseDiscoveryUpstream)
		} else if !isMissingRevision(resolveErr) {
			return app.BaseBranchDiscovery{}, resolveErr
		}
	}

	if value, present, lineErr := r.optionalLine(ctx, builder, "origin default branch", "symbolic-ref", "--short", "-q", "refs/remotes/origin/HEAD"); lineErr != nil {
		return app.BaseBranchDiscovery{}, lineErr
	} else if present && strings.HasPrefix(value, "origin/") && len(value) > len("origin/") {
		ref := "refs/remotes/" + value
		if _, resolveErr := r.resolveExpression(ctx, builder, ref); resolveErr == nil {
			return discoveredBase(ref, ref, BaseDiscoveryOriginHead)
		} else if !isMissingRevision(resolveErr) {
			return app.BaseBranchDiscovery{}, resolveErr
		}
	}

	candidates := []struct {
		ref    string
		source BaseDiscoverySource
	}{
		{ref: "refs/heads/main", source: BaseDiscoveryLocalMain},
		{ref: "refs/remotes/origin/main", source: BaseDiscoveryRemoteMain},
		{ref: "refs/heads/master", source: BaseDiscoveryLocalMaster},
		{ref: "refs/remotes/origin/master", source: BaseDiscoveryRemoteMaster},
	}
	for _, candidate := range candidates {
		if _, resolveErr := r.resolveExpression(ctx, builder, candidate.ref); resolveErr == nil {
			return discoveredBase(candidate.ref, candidate.ref, candidate.source)
		} else if !isMissingRevision(resolveErr) {
			return app.BaseBranchDiscovery{}, resolveErr
		}
	}
	return app.BaseBranchDiscovery{}, &GitError{Code: ErrorBaseDiscoveryUnavailable}
}

func discoveredBase(expression, ref string, source BaseDiscoverySource) (app.BaseBranchDiscovery, error) {
	result := app.BaseBranchDiscovery{Expression: expression, RefName: ref, Source: string(source), NoFetch: true}
	if err := result.Validate(); err != nil {
		return app.BaseBranchDiscovery{}, err
	}
	return result, nil
}

// ResolveBranchTarget resolves and freezes the current branch, base commit,
// and merge-base. Branch content remains object-backed even when the selected
// worktree has unrelated dirty files.
func (r *Resolver) ResolveBranchTarget(ctx context.Context, request app.BranchTargetRequest) (repository.ResolvedTarget, error) {
	if r == nil || ctx == nil || request.Validate() != nil {
		return repository.ResolvedTarget{}, &GitError{Code: ErrorInvalidInput}
	}
	builder, err := r.builder(request.Worktree.RootPath)
	if err != nil {
		return repository.ResolvedTarget{}, err
	}
	branchRef, err := r.currentBranchRef(ctx, builder)
	if err != nil {
		return repository.ResolvedTarget{}, err
	}
	head, err := r.resolveExpression(ctx, builder, "HEAD")
	if err != nil {
		return repository.ResolvedTarget{}, &GitError{Code: ErrorHeadUnavailable, Cause: err}
	}
	base, err := r.ResolveBase(ctx, builder, request.Selection.Expression)
	if err != nil {
		return repository.ResolvedTarget{}, err
	}
	candidates, err := r.mergeBases(ctx, builder, base, head)
	if err != nil {
		return repository.ResolvedTarget{}, err
	}
	mergeBase, err := selectMergeBase(candidates, request.MergeBaseSelection)
	if err != nil {
		return repository.ResolvedTarget{}, err
	}
	dirty, err := r.worktreeDirty(ctx, builder)
	if err != nil {
		return repository.ResolvedTarget{}, err
	}
	spec, err := repository.NewBranchTargetSpec(request.Selection.Expression)
	if err != nil {
		return repository.ResolvedTarget{}, err
	}
	editable := request.Worktree.CurrentObjectID != "" && request.Worktree.CurrentObjectID == head
	var destination *domain.WorktreeID
	if editable {
		value := request.Worktree.ID
		destination = &value
	}
	target := repository.ResolvedTarget{
		Spec:             spec,
		Generation:       request.Generation,
		Base:             repository.SnapshotRef{Kind: repository.SnapshotCommit, ObjectID: mergeBase},
		Head:             repository.SnapshotRef{Kind: repository.SnapshotCommit, ObjectID: head},
		ResolvedCommit:   head,
		ResolvedBaseRef:  base,
		MergeBase:        mergeBase,
		BaseBranchSource: string(request.Selection.Source),
		BranchRef:        branchRef,
		BaseBranchRef:    request.Discovery.RefName,
		DirtyWorktree:    dirty,
		NoFetchWarning:   request.Selection.Source == app.BaseFromDiscovery && request.Discovery.NoFetch,
		Editable:         editable,
		EditDestination:  destination,
		Fingerprint:      branchTargetFingerprint(request.Repository.ID, request.Worktree.ID, branchRef, base, mergeBase, head),
		ResolvedAt:       time.Now().UTC(),
	}
	return repository.NewResolvedTarget(target)
}

// ResolveBase verifies one user-selected revision expression as a commit.
// Leading-dash expressions are rejected before the Git process boundary.
func (r *Resolver) ResolveBase(ctx context.Context, builder *CommandBuilder, expression string) (repository.ObjectID, error) {
	if ctx == nil || builder == nil || app.ValidateBaseBranchExpression(expression) != nil {
		return "", &GitError{Code: ErrorInvalidInput}
	}
	value, err := r.resolveExpression(ctx, builder, expression)
	if err != nil {
		if isMissingRevision(err) {
			return "", &GitError{Code: ErrorBaseUnavailable, Cause: err}
		}
		return "", err
	}
	return value, nil
}

// ResolveBaseExpression is the path-oriented convenience form used by
// frontends that have not yet built a branch target request.
func (r *Resolver) ResolveBaseExpression(ctx context.Context, startPath, expression string) (repository.ObjectID, error) {
	if r == nil {
		return "", &GitError{Code: ErrorInvalidInput}
	}
	builder, err := r.builder(startPath)
	if err != nil {
		return "", err
	}
	return r.ResolveBase(ctx, builder, expression)
}

func (r *Resolver) currentBranchRef(ctx context.Context, builder *CommandBuilder) (string, error) {
	result, err := r.run(ctx, builder, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		var gitErr *GitError
		if errors.As(err, &gitErr) && gitErr.ExitCode == 1 {
			return "", &GitError{Code: ErrorBranchDetached, Cause: err}
		}
		return "", err
	}
	value, err := parseOutputLine("current branch", result.Stdout, false)
	if err != nil || !strings.HasPrefix(value, "refs/heads/") || len(value) == len("refs/heads/") {
		return "", malformed("invalid current branch ref")
	}
	return value, nil
}

func (r *Resolver) mergeBases(ctx context.Context, builder *CommandBuilder, base, head repository.ObjectID) ([]repository.ObjectID, error) {
	result, err := r.run(ctx, builder, "merge-base", "--all", string(base), string(head))
	if err != nil {
		var gitErr *GitError
		if errors.As(err, &gitErr) && gitErr.ExitCode == 1 {
			return nil, &GitError{Code: ErrorMergeBaseMissing, Cause: err}
		}
		return nil, err
	}
	candidates, err := parseObjectIDs(result.Stdout)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, &GitError{Code: ErrorMergeBaseMissing}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i] < candidates[j] })
	return candidates, nil
}

func selectMergeBase(candidates []repository.ObjectID, selected repository.ObjectID) (repository.ObjectID, error) {
	if len(candidates) == 0 {
		return "", &GitError{Code: ErrorMergeBaseMissing}
	}
	if len(candidates) > maxMergeBaseCandidates {
		return "", &GitError{Code: ErrorMergeBaseLimit}
	}
	if selected != "" {
		for _, candidate := range candidates {
			if candidate == selected {
				return selected, nil
			}
		}
		return "", &GitError{Code: ErrorMergeBaseSelectionInvalid, Candidates: append([]repository.ObjectID(nil), candidates...)}
	}
	if len(candidates) != 1 {
		return "", &GitError{Code: ErrorMergeBaseAmbiguous, Candidates: append([]repository.ObjectID(nil), candidates...)}
	}
	return candidates[0], nil
}

func (r *Resolver) resolveExpression(ctx context.Context, builder *CommandBuilder, expression string) (repository.ObjectID, error) {
	if expression != "HEAD" && app.ValidateBaseBranchExpression(expression) != nil {
		return "", &GitError{Code: ErrorInvalidInput}
	}
	result, err := r.run(ctx, builder, "rev-parse", "--verify", "--end-of-options", expression+"^{commit}")
	if err != nil {
		return "", err
	}
	value, err := parseOutputLine("commit object", result.Stdout, false)
	if err != nil {
		return "", err
	}
	objectID, err := repository.NewObjectID(value)
	if err != nil {
		return "", malformed("invalid commit object")
	}
	return objectID, nil
}

func parseObjectIDs(data []byte) ([]repository.ObjectID, error) {
	parts := bytes.FieldsFunc(data, func(r rune) bool { return r == '\x00' || r == '\n' || r == '\r' })
	if len(parts) > maxMergeBaseCandidates {
		return nil, &GitError{Code: ErrorMergeBaseLimit}
	}
	result := make([]repository.ObjectID, 0, len(parts))
	seen := make(map[repository.ObjectID]struct{}, len(parts))
	for _, part := range parts {
		value, err := repository.NewObjectID(string(part))
		if err != nil {
			return nil, malformed("invalid merge-base object")
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func (r *Resolver) worktreeDirty(ctx context.Context, builder *CommandBuilder) (bool, error) {
	result, err := r.run(ctx, builder, "status", "--porcelain=v2", "-z", "--untracked-files=all", "--ignore-submodules=all")
	if err != nil {
		return false, err
	}
	return len(result.Stdout) != 0, nil
}

func (r *Resolver) builder(startPath string) (*CommandBuilder, error) {
	return NewCommandBuilder(CommandBuilderConfig{Executable: r.executable, Runner: r.runner, StartPath: startPath, Policy: r.policy})
}

func isMissingRevision(err error) bool {
	var gitErr *GitError
	return errors.As(err, &gitErr) && gitErr.Code == ErrorCommandFailed && (gitErr.ExitCode == 1 || gitErr.ExitCode == 128)
}

func branchTargetFingerprint(repoID domain.RepositoryID, worktreeID domain.WorktreeID, branch string, base, mergeBase, head repository.ObjectID) string {
	hash := sha256.New()
	for _, value := range []string{string(repoID), string(worktreeID), branch, string(base), string(mergeBase), string(head)} {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}
