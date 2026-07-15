package workspace

import (
	"context"
	"errors"
	"io"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

// TargetConsistencyGuard rechecks the frozen target against the live
// destination before a proposal baseline is read or materialized.
type TargetConsistencyGuard func(context.Context, repository.ResolvedTarget, repository.WorktreeRef) error

// PinnedBaseline is a writable-proposal source backed by an immutable T056
// review snapshot. Its root is separately materialized by T108/T035; the
// provider never receives the review snapshot root as a writable directory.
type PinnedBaseline struct {
	request app.ProposalTargetBaselineRequest
	source  TrustedTreeSource
	guard   TargetConsistencyGuard
}

// NewPinnedBaseline binds a proposal baseline to one eligible current-HEAD
// target and its independently verified immutable review snapshot.
func NewPinnedBaseline(request app.ProposalTargetBaselineRequest, source TrustedTreeSource, guard TargetConsistencyGuard) (*PinnedBaseline, error) {
	if source == nil || guard == nil {
		return nil, app.ErrProposalTargetUnavailable
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	identity := source.Identity()
	if identity.Validate() != nil || identity.Kind != "review_snapshot" || identity.ID != string(request.Snapshot.ID) || identity.ManifestHash != request.Snapshot.ManifestHash {
		return nil, app.ErrProposalTargetUnavailable
	}
	return &PinnedBaseline{request: request, source: source, guard: guard}, nil
}

// Identity records the pinned target object and immutable review manifest,
// distinct from the destination constraints used during apply.
func (b *PinnedBaseline) Identity() app.WorkspaceSourceIdentity {
	if b == nil {
		return app.WorkspaceSourceIdentity{}
	}
	return app.WorkspaceSourceIdentity{
		Kind:         "pinned_object",
		ID:           string(b.request.Target.Head.ObjectID),
		ManifestHash: b.request.Snapshot.ManifestHash,
		Generation:   b.request.Target.Generation,
		Fingerprint:  b.request.Target.Fingerprint,
	}
}

// List preflights the complete pinned tree and rechecks destination HEAD.
func (b *PinnedBaseline) List(ctx context.Context) ([]repository.TreeEntry, error) {
	if b == nil || ctx == nil {
		return nil, app.ErrInvalidProposalBaseline
	}
	if err := b.check(ctx); err != nil {
		return nil, err
	}
	entries, err := b.source.List(ctx)
	if err != nil {
		return nil, err
	}
	if app.Count(len(entries)) > b.request.ResourcePolicy.Artifact.SnapshotEntries {
		return nil, app.ErrProposalBaselineLimit
	}
	seenRaw := make(map[repository.RepoPathKey]struct{}, len(entries))
	seenNative := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if err := entry.Validate(); err != nil {
			return nil, app.ErrInvalidProposalBaseline
		}
		if _, exists := seenRaw[entry.Path.Key()]; exists {
			return nil, app.ErrProposalBaselineUnsupported
		}
		seenRaw[entry.Path.Key()] = struct{}{}
		native, err := nativeRepoPath(entry.Path)
		if err != nil {
			return nil, app.ErrProposalBaselineUnsupported
		}
		nativeKey := comparisonPath(native)
		if _, exists := seenNative[nativeKey]; exists {
			return nil, app.ErrProposalBaselineUnsupported
		}
		seenNative[nativeKey] = struct{}{}
		if entry.Kind == repository.FileKindDirectory {
			continue
		}
		if entry.Kind != repository.FileKindRegular && entry.Kind != repository.FileKindSymlink {
			return nil, app.ErrProposalBaselineUnsupported
		}
		if entry.Kind == repository.FileKindRegular && entry.Mode&0o111 != 0 {
			return nil, app.ErrProposalBaselineUnsupported
		}
		reader, err := b.source.Open(ctx, entry)
		if err != nil {
			return nil, err
		}
		limit := b.request.ResourcePolicy.Artifact.ProposalFileBytes
		if entry.Kind == repository.FileKindSymlink {
			limit = b.request.ResourcePolicy.Symlink.TrackedBlobBytes
		}
		size, scanErr := countLocalBaselineContent(ctx, reader, limit)
		closeErr := reader.Close()
		if scanErr != nil {
			return nil, scanErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if size > b.request.ResourcePolicy.Artifact.SnapshotBytes {
			return nil, app.ErrProposalBaselineLimit
		}
	}
	expanded, err := withImplicitDirectories(entries)
	if err != nil {
		return nil, app.ErrInvalidProposalBaseline
	}
	if app.Count(len(expanded)) > b.request.ResourcePolicy.Artifact.SnapshotEntries {
		return nil, app.ErrProposalBaselineLimit
	}
	if err := validateBaselineAncestors(expanded); err != nil {
		return nil, err
	}
	return expanded, b.check(ctx)
}

// Open reads one pinned object-tree entry after rechecking destination HEAD.
func (b *PinnedBaseline) Open(ctx context.Context, entry repository.TreeEntry) (io.ReadCloser, error) {
	if b == nil || ctx == nil || entry.Validate() != nil {
		return nil, app.ErrInvalidProposalBaseline
	}
	if err := b.check(ctx); err != nil {
		return nil, err
	}
	return b.source.Open(ctx, entry)
}

// Materialize creates the complete proposal baseline in the separately owned
// baseline root and retains the frozen branch/commit target identity.
func (b *PinnedBaseline) Materialize(ctx context.Context, root WorkspaceRoot) (app.ProposalBaseline, error) {
	if b == nil || ctx == nil {
		return app.ProposalBaseline{}, app.ErrInvalidProposalBaseline
	}
	buildCtx, cancel := context.WithTimeout(ctx, b.request.ResourcePolicy.Artifact.SnapshotDeadline)
	defer cancel()
	manifest, err := MaterializeTree(buildCtx, b, root, b.request.ResourcePolicy)
	if err != nil {
		if errors.Is(err, app.ErrReviewSnapshotLimit) || errors.Is(err, context.DeadlineExceeded) {
			return app.ProposalBaseline{}, app.ErrProposalBaselineLimit
		}
		return app.ProposalBaseline{}, err
	}
	if err := b.check(buildCtx); err != nil {
		return app.ProposalBaseline{}, err
	}
	target := b.request.Target
	result := app.ProposalBaseline{Target: &target, Manifest: manifest}
	if err := result.Validate(); err != nil {
		return app.ProposalBaseline{}, err
	}
	return result, nil
}

func (b *PinnedBaseline) check(ctx context.Context) error {
	if b == nil || ctx == nil || b.request.Validate() != nil {
		return app.ErrInvalidProposalBaseline
	}
	if err := b.guard(ctx, b.request.Target, b.request.Worktree); err != nil {
		return app.ErrProposalBaselineStale
	}
	return nil
}
