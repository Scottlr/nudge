package gitcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/diff"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/process"
)

var (
	// ErrContentNotFound reports an absent path in an immutable snapshot.
	ErrContentNotFound = errors.New("content not found")
	// ErrContentCorrupt reports capture or source identity drift.
	ErrContentCorrupt = errors.New("content corrupt")
	// ErrInvalidContentLoader reports an incomplete adapter composition.
	ErrInvalidContentLoader = errors.New("invalid content loader")
	// ErrContentLimit is internal to the bounded stream sink.
	errContentLimit = errors.New("content limit exceeded")
)

// ContentLoader is the consumer-facing source/diff loading port.
type ContentLoader interface {
	LoadFile(context.Context, domain.CaptureID, repository.SnapshotRef, repository.RepoPath) (repository.FileContent, error)
	LoadDiff(context.Context, domain.CaptureID, repository.ChangedFile) (repository.FileDiff, error)
}

// ContentLoaderConfig supplies the trusted Git process and adopted-capture
// readers used by GitContentLoader.
type ContentLoaderConfig struct {
	Executable      process.ExecutableIdentity
	Runner          process.Runner
	StartPath       string
	Policy          MachineGitReadPolicyV1
	Manifests       app.CaptureManifestReader
	Artifacts       app.PublishedArtifactReader
	MaxContentBytes app.ByteSize
	PatchLimits     diff.PatchParseLimits
}

// GitContentLoader reads pinned Git objects and accepted local-capture bytes.
type GitContentLoader struct {
	builder         *CommandBuilder
	manifests       app.CaptureManifestReader
	artifacts       app.PublishedArtifactReader
	maxContentBytes app.ByteSize
	patchLimits     diff.PatchParseLimits
}

// NewContentLoader constructs a content loader with explicit Git policy and
// bounded content/diff limits.
func NewContentLoader(config ContentLoaderConfig) (*GitContentLoader, error) {
	policy := config.Policy
	if policy == (MachineGitReadPolicyV1{}) {
		policy = DefaultMachineGitReadPolicyV1()
	}
	if config.Runner == nil {
		config.Runner = process.NewRunner()
	}
	if config.MaxContentBytes == 0 {
		config.MaxContentBytes = 16 * app.MiB
	}
	if config.StartPath == "" {
		return nil, ErrInvalidContentLoader
	}
	builder, err := NewCommandBuilder(CommandBuilderConfig{
		Executable: config.Executable,
		Runner:     config.Runner,
		StartPath:  config.StartPath,
		Policy:     policy,
	})
	if err != nil {
		return nil, err
	}
	limits := config.PatchLimits
	return &GitContentLoader{
		builder:         builder,
		manifests:       config.Manifests,
		artifacts:       config.Artifacts,
		maxContentBytes: config.MaxContentBytes,
		patchLimits:     limits,
	}, nil
}

// LoadFile loads one exact path from a capture-backed local snapshot or a
// pinned Git object. It never falls back from an accepted capture to a live
// worktree path.
func (l *GitContentLoader) LoadFile(ctx context.Context, captureID domain.CaptureID, snapshot repository.SnapshotRef, path repository.RepoPath) (repository.FileContent, error) {
	if l == nil || ctx == nil || snapshot.Validate() != nil || path.Validate() != nil {
		return repository.FileContent{}, ErrInvalidContentLoader
	}
	if snapshot.Kind == repository.SnapshotWorkingTree {
		if captureID == "" {
			return repository.FileContent{}, ErrContentCorrupt
		}
		manifest, err := l.captureManifest(ctx, captureID)
		if err != nil {
			return repository.FileContent{}, err
		}
		if manifest.WorktreeID != snapshot.WorktreeID || manifest.Candidate.Fingerprint != snapshot.Fingerprint {
			return repository.FileContent{}, ErrContentCorrupt
		}
		return l.loadCaptureFile(ctx, snapshot, manifest, path)
	}
	return l.loadGitFile(ctx, snapshot, path)
}

// LoadDiff indexes the accepted patch and parses only the matching file
// section. The live worktree is never consulted.
func (l *GitContentLoader) LoadDiff(ctx context.Context, captureID domain.CaptureID, file repository.ChangedFile) (repository.FileDiff, error) {
	if l == nil || ctx == nil || captureID == "" || file.Validate() != nil || l.manifests == nil || l.artifacts == nil {
		return repository.FileDiff{}, ErrInvalidContentLoader
	}
	manifest, err := l.captureManifest(ctx, captureID)
	if err != nil {
		return repository.FileDiff{}, err
	}
	source, err := NewPublishedPatchSource(ctx, manifest, l.artifacts)
	if err != nil {
		return repository.FileDiff{}, err
	}
	sink := new(diff.MemoryPatchIndexSink)
	identity, err := diff.BuildPatchIndex(ctx, source, l.patchLimits, sink)
	if err != nil {
		return repository.FileDiff{}, err
	}
	if identity.SHA256 != manifest.Candidate.Patch.ContentSHA256 {
		return repository.FileDiff{}, ErrContentCorrupt
	}
	for _, entry := range sink.Entries() {
		if changedFileMatches(entry.File, file) {
			return diff.ParseFileDiff(ctx, source, entry, l.patchLimits)
		}
	}
	return repository.FileDiff{}, ErrContentNotFound
}

// LoadTargetDiff parses a complete immutable object-to-object diff and
// returns the requested file section. The current worktree and index are not
// consulted.
func (l *GitContentLoader) LoadTargetDiff(ctx context.Context, target repository.ResolvedTarget, file repository.ChangedFile) (repository.FileDiff, error) {
	if l == nil || ctx == nil || target.Validate() != nil || target.Spec.Kind == repository.TargetLocal || file.Validate() != nil || target.Base.ObjectID == "" || target.Head.ObjectID == "" {
		return repository.FileDiff{}, ErrInvalidContentLoader
	}
	result, err := l.builder.Run(ctx, "diff", "--no-ext-diff", "--no-textconv", "--binary", "--full-index", "--find-renames", string(target.Base.ObjectID), string(target.Head.ObjectID), "--")
	if err != nil {
		return repository.FileDiff{}, err
	}
	files, err := diff.ParsePatch(result.Stdout)
	if err != nil {
		return repository.FileDiff{}, err
	}
	for _, value := range files {
		if changedFileMatches(value.File, file) || changedFilePathsMatch(value.File, file) {
			return value, nil
		}
	}
	return repository.FileDiff{}, ErrContentNotFound
}

func changedFilePathsMatch(left, right repository.ChangedFile) bool {
	return sameRepoPath(left.OldPath, right.OldPath) && sameRepoPath(left.NewPath, right.NewPath)
}

func (l *GitContentLoader) captureManifest(ctx context.Context, captureID domain.CaptureID) (app.CaptureManifest, error) {
	if l.manifests == nil {
		return app.CaptureManifest{}, ErrInvalidContentLoader
	}
	manifest, err := l.manifests.OpenCaptureManifest(ctx, captureID)
	if err != nil {
		return app.CaptureManifest{}, err
	}
	if manifest.CaptureID != captureID || manifest.Validate() != nil {
		return app.CaptureManifest{}, ErrContentCorrupt
	}
	return manifest, nil
}

func (l *GitContentLoader) loadCaptureFile(ctx context.Context, snapshot repository.SnapshotRef, manifest app.CaptureManifest, path repository.RepoPath) (repository.FileContent, error) {
	if l.artifacts == nil {
		return repository.FileContent{}, ErrInvalidContentLoader
	}
	for _, entry := range manifest.Candidate.Entries {
		if entry.Change.NewPath == nil || !bytes.Equal(entry.Change.NewPath.Bytes(), path.Bytes()) {
			continue
		}
		for _, blob := range entry.Blobs {
			if blob.Side != repository.CaptureBlobWorkingTree || !bytes.Equal(blob.Path.Bytes(), path.Bytes()) {
				continue
			}
			content, err := l.readPublishedBlob(ctx, snapshot, manifest, entry.Change, blob)
			return content, err
		}
		return repository.FileContent{}, ErrContentNotFound
	}
	return repository.FileContent{}, ErrContentNotFound
}

func (l *GitContentLoader) readPublishedBlob(ctx context.Context, snapshot repository.SnapshotRef, manifest app.CaptureManifest, file repository.ChangedFile, blob repository.CaptureBlobRef) (repository.FileContent, error) {
	if blob.Artifact.Bytes == 0 || blob.Artifact.ContentSHA256 == "" {
		return repository.FileContent{}, ErrContentCorrupt
	}
	if app.ByteSize(blob.Artifact.Bytes) > l.maxContentBytes {
		return boundedFileContent(snapshot, *file.NewPath, file, nil, true, "content_limit")
	}
	data, err := l.readPublished(ctx, manifest.Blobs.Target, blob.Artifact.RelativePath, app.StreamIdentity{Bytes: app.ByteSize(blob.Artifact.Bytes), SHA256: blob.Artifact.ContentSHA256})
	if err != nil {
		return repository.FileContent{}, fmt.Errorf("%w: %v", ErrContentCorrupt, err)
	}
	if len(data) != int(blob.Artifact.Bytes) {
		return repository.FileContent{}, ErrContentCorrupt
	}
	return boundedFileContent(snapshot, *file.NewPath, file, data, false, "")
}

func (l *GitContentLoader) readPublished(ctx context.Context, target app.PublishTarget, relative string, expected app.StreamIdentity) ([]byte, error) {
	if l.artifacts == nil || uint64(expected.Bytes) > uint64(^uint(0)>>1) {
		return nil, ErrContentCorrupt
	}
	data := make([]byte, int(expected.Bytes))
	read := app.ByteSize(0)
	const maxRange = app.ByteSize(256 * app.KiB)
	for read < expected.Bytes {
		request := expected.Bytes - read
		if request > maxRange {
			request = maxRange
		}
		chunk, err := l.artifacts.ReadPublishedRange(ctx, target, relative, expected, read, request)
		if err != nil {
			return nil, err
		}
		if len(chunk) == 0 || app.ByteSize(len(chunk)) > request {
			return nil, ErrContentCorrupt
		}
		copy(data[int(read):], chunk)
		read += app.ByteSize(len(chunk))
		if app.ByteSize(len(chunk)) != request {
			return nil, ErrContentCorrupt
		}
	}
	return data, nil
}

func (l *GitContentLoader) loadGitFile(ctx context.Context, snapshot repository.SnapshotRef, path repository.RepoPath) (repository.FileContent, error) {
	if snapshot.Kind != repository.SnapshotCommit && snapshot.Kind != repository.SnapshotTree && snapshot.Kind != repository.SnapshotEmpty {
		return repository.FileContent{}, ErrInvalidContentLoader
	}
	if snapshot.Kind == repository.SnapshotEmpty {
		file := repository.ChangedFile{NewPath: &path, NewFileKind: repository.FileKindRegular, NewMode: 0o100644}
		return boundedFileContent(snapshot, path, file, nil, false, "")
	}
	if snapshot.ObjectID == "" {
		return repository.FileContent{}, ErrContentNotFound
	}
	writer := newLimitedContentWriter(l.maxContentBytes)
	argument := string(snapshot.ObjectID) + ":" + string(path.Bytes())
	_, err := l.builder.RunStream(ctx, writer, "cat-file", "blob", argument)
	if err != nil {
		if writer.limited {
			return boundedFileContent(snapshot, path, repository.ChangedFile{NewPath: &path, NewFileKind: repository.FileKindRegular, NewMode: 0o100644}, writer.Bytes(), true, "content_limit")
		}
		var gitErr *GitError
		if errors.As(err, &gitErr) && gitErr.Code == ErrorCommandFailed {
			return repository.FileContent{}, ErrContentNotFound
		}
		return repository.FileContent{}, err
	}
	file := repository.ChangedFile{NewPath: &path, NewFileKind: repository.FileKindRegular, NewMode: 0o100644}
	return boundedFileContent(snapshot, path, file, writer.Bytes(), false, "")
}

func boundedFileContent(snapshot repository.SnapshotRef, path repository.RepoPath, file repository.ChangedFile, data []byte, truncated bool, reason string) (repository.FileContent, error) {
	kind := file.NewFileKind
	mode := file.NewMode
	if kind == "" || kind == repository.FileKindUnknown {
		kind = repository.FileKindRegular
	}
	if mode == 0 {
		mode = 0o100644
	}
	hash := sha256.Sum256(data)
	content := repository.FileContent{
		Snapshot:    snapshot,
		Path:        repository.RepoPath(path.Bytes()),
		Kind:        kind,
		Mode:        mode,
		Bytes:       append([]byte(nil), data...),
		ContentHash: hex.EncodeToString(hash[:]),
		Binary:      bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data),
		Truncated:   truncated,
		LimitReason: reason,
	}
	if err := content.Validate(); err != nil {
		return repository.FileContent{}, err
	}
	return content, nil
}

func changedFileMatches(left, right repository.ChangedFile) bool {
	if left.Kind != right.Kind || left.OldMode != right.OldMode || left.NewMode != right.NewMode || left.OldFileKind != right.OldFileKind || left.NewFileKind != right.NewFileKind || left.Binary != right.Binary {
		return false
	}
	return sameRepoPath(left.OldPath, right.OldPath) && sameRepoPath(left.NewPath, right.NewPath)
}

func sameRepoPath(left, right *repository.RepoPath) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return bytes.Equal(left.Bytes(), right.Bytes())
}

type limitedContentWriter struct {
	limit   app.ByteSize
	data    []byte
	limited bool
}

func newLimitedContentWriter(limit app.ByteSize) *limitedContentWriter {
	return &limitedContentWriter{limit: limit}
}

func (w *limitedContentWriter) Write(data []byte) (int, error) {
	if w.limited {
		return 0, errContentLimit
	}
	remaining := w.limit - app.ByteSize(len(w.data))
	if app.ByteSize(len(data)) > remaining {
		if remaining > 0 {
			w.data = append(w.data, data[:int(remaining)]...)
		}
		w.limited = true
		return int(remaining), errContentLimit
	}
	w.data = append(w.data, data...)
	return len(data), nil
}

func (w *limitedContentWriter) Bytes() []byte {
	return append([]byte(nil), w.data...)
}

var _ ContentLoader = (*GitContentLoader)(nil)
