package workspace

import (
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

// GenerationConsistencyGuard is the authoritative reconciliation fence for
// one accepted local generation. It must fail when the session no longer
// treats the supplied generation as current.
type GenerationConsistencyGuard func(context.Context, app.CaptureGeneration) error

// LocalBaseline is an immutable accepted-capture source for proposal
// materialization. It is deliberately not a filesystem source: all bytes are
// obtained from the capture-backed TrustedTreeSource.
type LocalBaseline struct {
	request app.ProposalBaselineRequest
	source  TrustedTreeSource
	guard   GenerationConsistencyGuard
}

// NewLocalBaseline binds an accepted-capture source to a current-generation
// guard and the exact policy evaluation used for proposal readiness.
func NewLocalBaseline(request app.ProposalBaselineRequest, source TrustedTreeSource, guard GenerationConsistencyGuard) (*LocalBaseline, error) {
	if source == nil || guard == nil {
		return nil, app.ErrInvalidProposalBaseline
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	identity := source.Identity()
	if err := identity.Validate(); err != nil || identity.Kind != "accepted_capture" || identity.ID != string(request.Generation.CaptureID) || identity.ManifestHash != request.Generation.ManifestHash || identity.Generation != 0 && identity.Generation != request.Generation.Generation || identity.Fingerprint != "" && identity.Fingerprint != request.Generation.Fingerprint {
		return nil, app.ErrProposalBaselineStale
	}
	return &LocalBaseline{request: request, source: source, guard: guard}, nil
}

// Identity returns the full source-generation identity that is persisted with
// the proposal workspace lifecycle claim.
func (b *LocalBaseline) Identity() app.WorkspaceSourceIdentity {
	if b == nil {
		return app.WorkspaceSourceIdentity{}
	}
	return app.WorkspaceSourceIdentity{
		Kind:         "accepted_capture",
		ID:           string(b.request.Generation.CaptureID),
		ManifestHash: b.request.Generation.ManifestHash,
		Generation:   b.request.Generation.Generation,
		Fingerprint:  b.request.Generation.Fingerprint,
	}
}

// List preflights the complete accepted tree without mutating the workspace.
func (b *LocalBaseline) List(ctx context.Context) ([]repository.TreeEntry, error) {
	if b == nil || ctx == nil {
		return nil, app.ErrInvalidProposalBaseline
	}
	if err := b.checkGeneration(ctx); err != nil {
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
	var total app.ByteSize
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
		if entry.Kind != repository.FileKindRegular || entry.Mode&0o111 != 0 {
			return nil, app.ErrProposalBaselineUnsupported
		}
		reader, openErr := b.source.Open(ctx, entry)
		if openErr != nil {
			return nil, openErr
		}
		size, scanErr := countLocalBaselineContent(ctx, reader, b.request.ResourcePolicy.Artifact.ProposalFileBytes)
		closeErr := reader.Close()
		if scanErr != nil {
			return nil, scanErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if total > b.request.ResourcePolicy.Artifact.SnapshotBytes || size > b.request.ResourcePolicy.Artifact.SnapshotBytes-total {
			return nil, app.ErrProposalBaselineLimit
		}
		total += size
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
	return expanded, b.checkGeneration(ctx)
}

// Open reads one accepted tree entry without exposing a live worktree path.
func (b *LocalBaseline) Open(ctx context.Context, entry repository.TreeEntry) (io.ReadCloser, error) {
	if b == nil || ctx == nil || entry.Validate() != nil {
		return nil, app.ErrInvalidProposalBaseline
	}
	if err := b.checkGeneration(ctx); err != nil {
		return nil, err
	}
	return b.source.Open(ctx, entry)
}

// Materialize constructs and independently verifies the complete baseline
// manifest inside a typed T108 baseline root.
func (b *LocalBaseline) Materialize(ctx context.Context, root WorkspaceRoot) (app.ProposalBaseline, error) {
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
	if err := b.checkGeneration(buildCtx); err != nil {
		return app.ProposalBaseline{}, err
	}
	result := app.ProposalBaseline{Generation: b.request.Generation, Manifest: manifest}
	if err := result.Validate(); err != nil {
		return app.ProposalBaseline{}, err
	}
	return result, nil
}

func (b *LocalBaseline) checkGeneration(ctx context.Context) error {
	if err := b.guard(ctx, b.request.Generation); err != nil {
		return app.ErrProposalBaselineStale
	}
	return nil
}

func validateBaselineAncestors(entries []repository.TreeEntry) error {
	byPath := make(map[repository.RepoPathKey]repository.FileKind, len(entries))
	for _, entry := range entries {
		byPath[entry.Path.Key()] = entry.Kind
	}
	for _, entry := range entries {
		raw := entry.Path.Bytes()
		for index := bytes.IndexByte(raw, '/'); index >= 0; {
			ancestor := repository.RepoPath(raw[:index])
			if kind, exists := byPath[ancestor.Key()]; exists && kind != repository.FileKindDirectory {
				return app.ErrProposalBaselineUnsupported
			}
			next := bytes.IndexByte(raw[index+1:], '/')
			if next < 0 {
				break
			}
			index += next + 1
		}
	}
	return nil
}

func countLocalBaselineContent(ctx context.Context, reader io.Reader, limit app.ByteSize) (app.ByteSize, error) {
	if reader == nil || limit == 0 {
		return 0, app.ErrInvalidProposalBaseline
	}
	buffer := make([]byte, materializeCopyBufferBytes)
	var total app.ByteSize
	for {
		if err := checkMaterializeContext(ctx); err != nil {
			return total, err
		}
		read, readErr := reader.Read(buffer)
		if read > 0 {
			if total > limit || app.ByteSize(read) > limit-total {
				return total, app.ErrProposalBaselineLimit
			}
			total += app.ByteSize(read)
		}
		if errors.Is(readErr, io.EOF) {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}
