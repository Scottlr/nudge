package gitcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

const maxCommitParents = 64

// ResolveCommitTarget resolves one revision expression to a frozen commit
// target. The original expression is retained only as a display label; all
// later target reads use the verified commit and comparison-parent objects.
func (r *Resolver) ResolveCommitTarget(ctx context.Context, request app.CommitTargetRequest) (repository.ResolvedTarget, error) {
	if r == nil || ctx == nil || request.Validate() != nil {
		return repository.ResolvedTarget{}, &GitError{Code: ErrorInvalidInput}
	}
	builder, err := r.builder(request.Worktree.RootPath)
	if err != nil {
		return repository.ResolvedTarget{}, err
	}
	commit, err := r.resolveExpression(ctx, builder, request.Expression)
	if err != nil {
		return repository.ResolvedTarget{}, &GitError{Code: ErrorCommitUnavailable, Cause: err}
	}
	parents, err := r.commitParents(ctx, builder, commit)
	if err != nil {
		return repository.ResolvedTarget{}, err
	}
	base, resolvedParent, parentLabel, err := r.commitBase(ctx, builder, parents)
	if err != nil {
		return repository.ResolvedTarget{}, err
	}
	editable := request.Worktree.CurrentObjectID != "" && request.Worktree.CurrentObjectID == commit
	var destination *domain.WorktreeID
	if editable {
		value := request.Worktree.ID
		destination = &value
	}
	spec, err := repository.NewCommitTargetSpec(request.Expression, "")
	if err != nil {
		return repository.ResolvedTarget{}, err
	}
	target := repository.ResolvedTarget{
		Spec:            spec,
		Generation:      request.Generation,
		Base:            base,
		Head:            repository.SnapshotRef{Kind: repository.SnapshotCommit, ObjectID: commit},
		ResolvedCommit:  commit,
		ResolvedParent:  resolvedParent,
		ParentLabel:     parentLabel,
		Editable:        editable,
		EditDestination: destination,
		Fingerprint:     commitTargetFingerprint(request.Repository.ID, request.Worktree.ID, commit, base.ObjectID, resolvedParent, parentLabel),
		ResolvedAt:      time.Now().UTC(),
	}
	return repository.NewResolvedTarget(target)
}

func (r *Resolver) commitParents(ctx context.Context, builder *CommandBuilder, commit repository.ObjectID) ([]repository.ObjectID, error) {
	result, err := builder.Run(ctx, "rev-list", "--parents", "--max-count=1", "--no-walk", string(commit))
	if err != nil {
		return nil, &GitError{Code: ErrorCommitParentUnavailable, Cause: err}
	}
	fields := bytes.Fields(result.Stdout)
	if len(fields) == 0 || len(fields)-1 > maxCommitParents {
		return nil, malformed("invalid commit parent output")
	}
	resolved, err := repository.NewObjectID(string(fields[0]))
	if err != nil || resolved != commit {
		return nil, malformed("commit parent output does not match commit")
	}
	parents := make([]repository.ObjectID, 0, len(fields)-1)
	for _, field := range fields[1:] {
		parent, parentErr := repository.NewObjectID(string(field))
		if parentErr != nil {
			return nil, malformed("invalid commit parent object")
		}
		parents = append(parents, parent)
	}
	return parents, nil
}

func (r *Resolver) commitBase(ctx context.Context, builder *CommandBuilder, parents []repository.ObjectID) (repository.SnapshotRef, repository.ObjectID, string, error) {
	if len(parents) == 0 {
		emptyTree, err := r.emptyTree(ctx, builder)
		if err != nil {
			return repository.SnapshotRef{}, "", "", err
		}
		return repository.SnapshotRef{Kind: repository.SnapshotEmpty, ObjectID: emptyTree}, "", "root (empty tree)", nil
	}
	label := "parent 1"
	if len(parents) > 1 {
		label = "parent 1 (first-parent v1)"
	}
	return repository.SnapshotRef{Kind: repository.SnapshotCommit, ObjectID: parents[0]}, parents[0], label, nil
}

func (r *Resolver) emptyTree(ctx context.Context, builder *CommandBuilder) (repository.ObjectID, error) {
	result, err := builder.RunInput(ctx, strings.NewReader(""), EmptyTreeArgs()...)
	if err != nil {
		return "", &GitError{Code: ErrorEmptyTreeUnavailable, Cause: err}
	}
	value, err := parseOutputLine("empty tree", result.Stdout, false)
	if err != nil {
		return "", &GitError{Code: ErrorEmptyTreeUnavailable, Cause: err}
	}
	objectID, err := repository.NewObjectID(value)
	if err != nil || strings.Trim(value, "0") == "" {
		return "", &GitError{Code: ErrorEmptyTreeUnavailable, Cause: errors.New("invalid empty tree object")}
	}
	return objectID, nil
}

func commitTargetFingerprint(repoID domain.RepositoryID, worktreeID domain.WorktreeID, commit, base, parent repository.ObjectID, label string) string {
	hash := sha256.New()
	for _, value := range []string{string(repoID), string(worktreeID), string(commit), string(base), string(parent), label} {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}
