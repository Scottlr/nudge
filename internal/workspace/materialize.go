package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/paths"
)

const materializeCopyBufferBytes = 64 * 1024

var (
	ErrInvalidWorkspaceSource   = errors.New("invalid proposal workspace source")
	ErrWorkspaceContentMismatch = errors.New("proposal workspace content mismatch")
)

// TrustedTreeSource supplies one accepted immutable tree. Open returns file
// bytes for regular files and the raw link target for symlinks; it never
// returns a live worktree path for the materializer to open.
type TrustedTreeSource interface {
	Identity() app.WorkspaceSourceIdentity
	List(context.Context) ([]repository.TreeEntry, error)
	Open(context.Context, repository.TreeEntry) (io.ReadCloser, error)
}

// MaterializeTree copies one accepted tree into an empty typed workspace root.
// It returns a logical source-mode manifest while storing all regular files
// with owner-only permissions.
func MaterializeTree(ctx context.Context, source TrustedTreeSource, root WorkspaceRoot, policy app.ResourcePolicy) (app.WorkspaceManifest, error) {
	if ctx == nil || source == nil || root.Kind() != RootBaseline && root.Kind() != RootResult || policy.Validate() != nil {
		return app.WorkspaceManifest{}, ErrInvalidWorkspaceSource
	}
	if err := source.Identity().Validate(); err != nil {
		return app.WorkspaceManifest{}, err
	}
	if err := ensureMaterializeRootEmpty(root.Path()); err != nil {
		return app.WorkspaceManifest{}, err
	}
	entries, err := source.List(ctx)
	if err != nil {
		return app.WorkspaceManifest{}, err
	}
	verifiedRoot, err := paths.NewVerifiedRoot(root.Path())
	if err != nil {
		return app.WorkspaceManifest{}, ErrWorkspaceRootMismatch
	}
	nativeResolver := paths.NewNativePathResolver()
	if app.Count(len(entries)) > policy.Artifact.SnapshotEntries {
		return app.WorkspaceManifest{}, app.ErrReviewSnapshotLimit
	}
	entries, err = withImplicitDirectories(entries)
	if err != nil {
		return app.WorkspaceManifest{}, err
	}
	if app.Count(len(entries)) > policy.Artifact.SnapshotEntries {
		return app.WorkspaceManifest{}, app.ErrReviewSnapshotLimit
	}
	sort.Slice(entries, func(i, j int) bool { return bytes.Compare(entries[i].Path.Bytes(), entries[j].Path.Bytes()) < 0 })
	seen := make(map[repository.RepoPathKey]struct{}, len(entries))
	manifestEntries := make([]app.WorkspaceManifestEntry, 0, len(entries))
	var total app.ByteSize
	for _, entry := range entries {
		if err := checkMaterializeContext(ctx); err != nil {
			return app.WorkspaceManifest{}, err
		}
		if err := entry.Validate(); err != nil {
			return app.WorkspaceManifest{}, ErrInvalidWorkspaceSource
		}
		key := entry.Path.Key()
		if _, exists := seen[key]; exists {
			return app.WorkspaceManifest{}, ErrInvalidWorkspaceSource
		}
		seen[key] = struct{}{}
		nativePath, err := nativeRepoPath(entry.Path)
		if err != nil {
			return app.WorkspaceManifest{}, ErrInvalidWorkspaceSource
		}
		if entry.Kind == repository.FileKindDirectory {
			if err := ensureMaterializeDirectory(root.Path(), nativePath); err != nil {
				return app.WorkspaceManifest{}, err
			}
			manifestEntries = append(manifestEntries, app.WorkspaceManifestEntry{Path: entry.Path.Bytes(), Kind: entry.Kind, Mode: entry.Mode})
			continue
		}
		if err := ensureMaterializeDirectory(root.Path(), filepath.Dir(nativePath)); err != nil {
			return app.WorkspaceManifest{}, err
		}
		reader, err := source.Open(ctx, entry)
		if err != nil {
			return app.WorkspaceManifest{}, err
		}
		if entry.Kind == repository.FileKindSymlink {
			linkTarget, readErr := readBounded(reader, uint64(policy.Symlink.TrackedBlobBytes))
			closeErr := reader.Close()
			if readErr != nil {
				return app.WorkspaceManifest{}, readErr
			}
			if closeErr != nil {
				return app.WorkspaceManifest{}, closeErr
			}
			if len(linkTarget) == 0 || uint64(len(linkTarget)) > uint64(policy.Symlink.TrackedBlobBytes) {
				return app.WorkspaceManifest{}, app.ErrReviewSnapshotLimit
			}
			if total > policy.Artifact.SnapshotBytes || app.ByteSize(len(linkTarget)) > policy.Artifact.SnapshotBytes-total {
				return app.WorkspaceManifest{}, app.ErrReviewSnapshotLimit
			}
			total += app.ByteSize(len(linkTarget))
			evidence, evidenceErr := nativeResolver.QualifySymlinkTarget(verifiedRoot, entry.Path, linkTarget)
			if evidenceErr != nil || !evidence.IsActionable() {
				return app.WorkspaceManifest{}, app.ErrReviewSnapshotUnsafe
			}
			if err := os.Symlink(string(linkTarget), filepath.Join(root.Path(), nativePath)); err != nil {
				return app.WorkspaceManifest{}, app.ErrReviewSnapshotUnsafe
			}
			manifestEntries = append(manifestEntries, app.WorkspaceManifestEntry{Path: entry.Path.Bytes(), Kind: entry.Kind, Mode: entry.Mode, Bytes: uint64(len(linkTarget)), SHA256: hashMaterializedBytes(linkTarget), LinkTarget: linkTarget, SymlinkEvidence: &evidence})
			continue
		}
		file, createErr := paths.OpenProtectedFile(root.Path(), nativePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if createErr != nil {
			_ = reader.Close()
			return app.WorkspaceManifest{}, app.ErrReviewSnapshotUnsafe
		}
		hash := sha256.New()
		classifier := repository.NewContentClassifierV1(false)
		textSemanticsWriter := repository.NewTextByteSemanticsWriter()
		written, copyErr := copyMaterializedStream(ctx, file, reader, hash, classifier, textSemanticsWriter, &total, policy.Artifact.SnapshotBytes)
		mode := os.FileMode(entry.Mode & 0o700)
		if mode == 0 {
			mode = 0o600
		}
		chmodErr := file.Chmod(mode)
		closeFileErr := file.Close()
		closeReaderErr := reader.Close()
		if copyErr != nil {
			return app.WorkspaceManifest{}, copyErr
		}
		if closeFileErr != nil {
			return app.WorkspaceManifest{}, closeFileErr
		}
		if closeReaderErr != nil {
			return app.WorkspaceManifest{}, closeReaderErr
		}
		if chmodErr != nil {
			return app.WorkspaceManifest{}, chmodErr
		}
		contentClass := classifier.Classify()
		var textSemantics *repository.TextByteSemantics
		if contentClass == repository.ContentClassRegularTextUTF8 {
			semantics, semanticsErr := textSemanticsWriter.Semantics(written)
			if semanticsErr != nil {
				return app.WorkspaceManifest{}, semanticsErr
			}
			textSemantics = &semantics
		}
		manifestEntries = append(manifestEntries, app.WorkspaceManifestEntry{Path: entry.Path.Bytes(), Kind: entry.Kind, Mode: entry.Mode, Bytes: written, SHA256: hex.EncodeToString(hash.Sum(nil)), ContentClass: contentClass, TextSemantics: textSemantics})
	}
	manifest, err := app.NewWorkspaceManifest(manifestEntries)
	if err != nil {
		return app.WorkspaceManifest{}, err
	}
	if err := verifyMaterializedManifest(root.Path(), manifest, policy); err != nil {
		return app.WorkspaceManifest{}, err
	}
	return manifest, nil
}

// CopyManifestToRoot reproduces a verified baseline manifest into an empty
// result root without sharing inodes, links, or provider-visible metadata.
func CopyManifestToRoot(ctx context.Context, source WorkspaceRoot, destination WorkspaceRoot, manifest app.WorkspaceManifest, policy app.ResourcePolicy) error {
	if ctx == nil || source.Kind() != RootBaseline || destination.Kind() != RootResult || manifest.Validate() != nil || policy.Validate() != nil {
		return ErrInvalidWorkspaceSource
	}
	if err := ensureMaterializeRootEmpty(destination.Path()); err != nil {
		return err
	}
	verifiedDestination, err := paths.NewVerifiedRoot(destination.Path())
	if err != nil {
		return ErrWorkspaceRootMismatch
	}
	nativeResolver := paths.NewNativePathResolver()
	var total app.ByteSize
	for _, entry := range manifest.Entries {
		if err := checkMaterializeContext(ctx); err != nil {
			return err
		}
		nativePath, err := nativeRepoPath(repository.RepoPath(entry.Path))
		if err != nil {
			return ErrInvalidWorkspaceSource
		}
		if entry.Kind == repository.FileKindDirectory {
			if err := ensureMaterializeDirectory(destination.Path(), nativePath); err != nil {
				return err
			}
			continue
		}
		if err := ensureMaterializeDirectory(destination.Path(), filepath.Dir(nativePath)); err != nil {
			return err
		}
		sourcePath := filepath.Join(source.Path(), nativePath)
		destinationPath := filepath.Join(destination.Path(), nativePath)
		switch entry.Kind {
		case repository.FileKindSymlink:
			linkTarget, err := os.Readlink(sourcePath)
			if err != nil || !bytes.Equal([]byte(linkTarget), entry.LinkTarget) {
				return ErrWorkspaceContentMismatch
			}
			if total > policy.Artifact.SnapshotBytes || app.ByteSize(len(entry.LinkTarget)) > policy.Artifact.SnapshotBytes-total {
				return app.ErrReviewSnapshotLimit
			}
			evidence, evidenceErr := nativeResolver.QualifySymlinkTarget(verifiedDestination, repository.RepoPath(entry.Path), entry.LinkTarget)
			if evidenceErr != nil || !evidence.IsActionable() {
				return app.ErrReviewSnapshotUnsafe
			}
			if err := os.Symlink(linkTarget, destinationPath); err != nil {
				return err
			}
			total += app.ByteSize(len(entry.LinkTarget))
		case repository.FileKindRegular:
			input, err := paths.OpenExistingProtectedFile(source.Path(), nativePath)
			if err != nil {
				return ErrWorkspaceContentMismatch
			}
			output, err := paths.OpenProtectedFile(destination.Path(), nativePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				_ = input.Close()
				return err
			}
			hash := sha256.New()
			written, copyErr := copyMaterializedStream(ctx, output, input, hash, nil, nil, &total, policy.Artifact.SnapshotBytes)
			mode := os.FileMode(entry.Mode & 0o700)
			if mode == 0 {
				mode = 0o600
			}
			chmodErr := output.Chmod(mode)
			closeOutputErr := output.Close()
			closeInputErr := input.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeOutputErr != nil {
				return closeOutputErr
			}
			if closeInputErr != nil || written != entry.Bytes || hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
				return ErrWorkspaceContentMismatch
			}
			if chmodErr != nil {
				return chmodErr
			}
		default:
			return ErrInvalidWorkspaceSource
		}
	}
	return verifyMaterializedManifest(destination.Path(), manifest, policy)
}

// ResetResultToBaseline removes only entries below the already verified typed
// result root, then reproduces the baseline manifest from independent files.
func ResetResultToBaseline(ctx context.Context, baseline WorkspaceRoot, result WorkspaceRoot, manifest app.WorkspaceManifest, policy app.ResourcePolicy) error {
	if ctx == nil || baseline.Kind() != RootBaseline || result.Kind() != RootResult || manifest.Validate() != nil {
		return ErrInvalidWorkspaceSource
	}
	if err := clearMaterializeRoot(result.Path()); err != nil {
		return err
	}
	return CopyManifestToRoot(ctx, baseline, result, manifest, policy)
}

// ResetToBaseline is the lifecycle-facing name for the verified result-root
// reset. It retains the same typed-root and manifest checks as the original
// helper so callers cannot turn rejection into arbitrary path cleanup.
func ResetToBaseline(ctx context.Context, baseline WorkspaceRoot, result WorkspaceRoot, manifest app.WorkspaceManifest, policy app.ResourcePolicy) error {
	return ResetResultToBaseline(ctx, baseline, result, manifest, policy)
}

func ensureMaterializeRootEmpty(root string) error {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrWorkspaceRootMismatch
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return ErrWorkspaceContentMismatch
	}
	return nil
}

func clearMaterializeRoot(root string) error {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrWorkspaceRootMismatch
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := removeMaterializedEntry(filepath.Join(root, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func removeMaterializedEntry(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
		if err := os.Chmod(path, 0o700); err != nil {
			return err
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := removeMaterializedEntry(filepath.Join(path, entry.Name())); err != nil {
				return err
			}
		}
		return os.Remove(path)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		_ = os.Chmod(path, 0o600)
	}
	return os.Remove(path)
}

func ensureMaterializeDirectory(root, relative string) error {
	if relative == "." || relative == "" {
		return nil
	}
	if filepath.IsAbs(relative) || filepath.Clean(relative) != relative || !containedPath(root, filepath.Join(root, relative)) {
		return ErrInvalidWorkspaceSource
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	current := root
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return ErrInvalidWorkspaceSource
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := paths.EnsurePrivateDir(current); err != nil {
				return err
			}
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrWorkspaceContentMismatch
		}
		if err := paths.EnsurePrivateDir(current); err != nil {
			return err
		}
	}
	return nil
}

func withImplicitDirectories(entries []repository.TreeEntry) ([]repository.TreeEntry, error) {
	byPath := make(map[repository.RepoPathKey]repository.TreeEntry, len(entries))
	for _, entry := range entries {
		if err := entry.Validate(); err != nil {
			return nil, ErrInvalidWorkspaceSource
		}
		byPath[entry.Path.Key()] = entry
	}
	for _, entry := range append([]repository.TreeEntry(nil), entries...) {
		fullPath := entry.Path.Bytes()
		for index := bytes.IndexByte(fullPath, '/'); index >= 0; {
			path := fullPath[:index]
			key := repository.RepoPath(path).Key()
			if _, exists := byPath[key]; !exists {
				value, err := repository.NewRepoPath(path)
				if err != nil {
					return nil, ErrInvalidWorkspaceSource
				}
				byPath[key] = treeEntryForPath(value, repository.FileKindDirectory, 0o40000)
			}
			next := bytes.IndexByte(fullPath[index+1:], '/')
			if next < 0 {
				break
			}
			index += next + 1
		}
	}
	result := make([]repository.TreeEntry, 0, len(byPath))
	for _, entry := range byPath {
		result = append(result, entry)
	}
	return result, nil
}

func verifyMaterializedManifest(root string, expected app.WorkspaceManifest, policy app.ResourcePolicy) error {
	actual, err := materializedManifest(root, policy)
	if err != nil {
		return err
	}
	if len(actual.Entries) != len(expected.Entries) {
		return ErrWorkspaceContentMismatch
	}
	for index, entry := range expected.Entries {
		observed := actual.Entries[index]
		if !bytes.Equal(entry.Path, observed.Path) || entry.Kind != observed.Kind || entry.Bytes != observed.Bytes || entry.SHA256 != observed.SHA256 || entry.ContentClass != observed.ContentClass || !sameTextSemantics(entry.TextSemantics, observed.TextSemantics) || !bytes.Equal(entry.LinkTarget, observed.LinkTarget) {
			return ErrWorkspaceContentMismatch
		}
		if entry.Kind == repository.FileKindRegular && entry.Mode&0o700 != observed.Mode&0o700 {
			return ErrWorkspaceContentMismatch
		}
	}
	return nil
}

func sameTextSemantics(left, right *repository.TextByteSemantics) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func materializedManifest(root string, policy app.ResourcePolicy) (app.WorkspaceManifest, error) {
	entries := make([]app.WorkspaceManifestEntry, 0)
	var total app.ByteSize
	verifiedRoot, err := paths.NewVerifiedRoot(root)
	if err != nil {
		return app.WorkspaceManifest{}, ErrWorkspaceRootMismatch
	}
	nativeResolver := paths.NewNativePathResolver()
	err = filepath.WalkDir(root, func(path string, dirEntry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || !containedPath(root, path) || relative == "." {
			return ErrWorkspaceContentMismatch
		}
		rawPath, err := repository.NewRepoPath([]byte(filepath.ToSlash(relative)))
		if err != nil {
			return ErrWorkspaceContentMismatch
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		entry := app.WorkspaceManifestEntry{Path: rawPath.Bytes()}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			entry.Kind = repository.FileKindSymlink
			entry.Mode = 0o120000
			entry.Bytes = uint64(len([]byte(target)))
			entry.LinkTarget = []byte(target)
			entry.SHA256 = hashMaterializedBytes(entry.LinkTarget)
			evidence, evidenceErr := nativeResolver.QualifySymlinkTarget(verifiedRoot, rawPath, entry.LinkTarget)
			if evidenceErr != nil {
				return app.ErrReviewSnapshotUnsafe
			}
			entry.SymlinkEvidence = &evidence
			if total > policy.Artifact.SnapshotBytes || app.ByteSize(entry.Bytes) > policy.Artifact.SnapshotBytes-total {
				return app.ErrReviewSnapshotLimit
			}
			total += app.ByteSize(entry.Bytes)
		case info.IsDir():
			entry.Kind = repository.FileKindDirectory
			entry.Mode = 0o40000
		default:
			entry.Kind = repository.FileKindRegular
			entry.Mode = snapshotLogicalMode(repository.FileKindRegular, uint32(info.Mode().Perm()))
			file, err := paths.OpenExistingProtectedFile(root, filepath.FromSlash(string(rawPath)))
			if err != nil {
				return err
			}
			hash := sha256.New()
			classifier := repository.NewContentClassifierV1(false)
			textSemanticsWriter := repository.NewTextByteSemanticsWriter()
			var size uint64
			buffer := make([]byte, materializeCopyBufferBytes)
			for {
				read, readErr := file.Read(buffer)
				if read > 0 {
					size += uint64(read)
					if size > uint64(policy.Artifact.SnapshotBytes) || app.ByteSize(read) > policy.Artifact.SnapshotBytes-total {
						_ = file.Close()
						return app.ErrReviewSnapshotLimit
					}
					total += app.ByteSize(read)
					_, _ = hash.Write(buffer[:read])
					_, _ = classifier.Write(buffer[:read])
					_, _ = textSemanticsWriter.Write(buffer[:read])
				}
				if errors.Is(readErr, io.EOF) {
					break
				}
				if readErr != nil {
					_ = file.Close()
					return readErr
				}
			}
			if err := file.Close(); err != nil {
				return err
			}
			entry.Bytes = size
			entry.SHA256 = hex.EncodeToString(hash.Sum(nil))
			entry.ContentClass = classifier.Classify()
			if entry.ContentClass == repository.ContentClassRegularTextUTF8 {
				semantics, semanticsErr := textSemanticsWriter.Semantics(size)
				if semanticsErr != nil {
					return semanticsErr
				}
				entry.TextSemantics = &semantics
			}
		}
		entries = append(entries, entry)
		if app.Count(len(entries)) > policy.Artifact.SnapshotEntries {
			return app.ErrReviewSnapshotLimit
		}
		return nil
	})
	if err != nil {
		return app.WorkspaceManifest{}, err
	}
	return app.NewWorkspaceManifest(entries)
}

func copyMaterializedStream(ctx context.Context, destination io.Writer, source io.Reader, hash io.Writer, classifier *repository.ContentClassifierV1, textSemantics io.Writer, total *app.ByteSize, limit app.ByteSize) (uint64, error) {
	buffer := make([]byte, materializeCopyBufferBytes)
	var written uint64
	for {
		if err := checkMaterializeContext(ctx); err != nil {
			return written, err
		}
		read, readErr := source.Read(buffer)
		if read > 0 {
			if *total > limit || app.ByteSize(read) > limit-*total {
				return written, app.ErrReviewSnapshotLimit
			}
			if _, err := destination.Write(buffer[:read]); err != nil {
				return written, err
			}
			_, _ = hash.Write(buffer[:read])
			if classifier != nil {
				_, _ = classifier.Write(buffer[:read])
			}
			if textSemantics != nil {
				if _, err := textSemantics.Write(buffer[:read]); err != nil {
					return written, err
				}
			}
			*total += app.ByteSize(read)
			written += uint64(read)
		}
		if errors.Is(readErr, io.EOF) {
			return written, nil
		}
		if readErr != nil {
			return written, readErr
		}
	}
}

func readBounded(reader io.Reader, limit uint64) ([]byte, error) {
	if limit == 0 {
		return nil, app.ErrReviewSnapshotLimit
	}
	data, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if uint64(len(data)) > limit {
		return nil, app.ErrReviewSnapshotLimit
	}
	return data, nil
}

func checkMaterializeContext(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func hashMaterializedBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

// FilesystemTreeSource exposes an already accepted immutable snapshot root as
// a trusted source without following symlinks during enumeration or reads.
type FilesystemTreeSource struct {
	root     string
	identity app.WorkspaceSourceIdentity
}

func NewFilesystemTreeSource(snapshot app.ReviewSnapshot) (*FilesystemTreeSource, error) {
	if snapshot.Validate() != nil || snapshot.Root == "" {
		return nil, ErrInvalidWorkspaceSource
	}
	info, err := os.Lstat(snapshot.Root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalidWorkspaceSource
	}
	return &FilesystemTreeSource{root: snapshot.Root, identity: app.WorkspaceSourceIdentity{Kind: "review_snapshot", ID: string(snapshot.ID), ManifestHash: snapshot.ManifestHash}}, nil
}

func (s *FilesystemTreeSource) Identity() app.WorkspaceSourceIdentity { return s.identity }

func (s *FilesystemTreeSource) List(ctx context.Context) ([]repository.TreeEntry, error) {
	if s == nil || ctx == nil {
		return nil, ErrInvalidWorkspaceSource
	}
	entries := make([]repository.TreeEntry, 0)
	err := filepath.WalkDir(s.root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := checkMaterializeContext(ctx); err != nil {
			return err
		}
		if path == s.root || entry.Name() == ".nudge-review-snapshot.json" {
			return nil
		}
		relative, err := filepath.Rel(s.root, path)
		if err != nil || !containedPath(s.root, path) {
			return ErrInvalidWorkspaceSource
		}
		repoPath, err := repository.NewRepoPath([]byte(filepath.ToSlash(relative)))
		if err != nil {
			return ErrInvalidWorkspaceSource
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		kind := repository.FileKindRegular
		mode := snapshotLogicalMode(repository.FileKindRegular, uint32(info.Mode().Perm()))
		if info.Mode()&os.ModeSymlink != 0 {
			kind, mode = repository.FileKindSymlink, 0o120000
		} else if info.IsDir() {
			kind, mode = repository.FileKindDirectory, 0o40000
		}
		entries = append(entries, treeEntryForPath(repoPath, kind, mode))
		return nil
	})
	return entries, err
}

func (s *FilesystemTreeSource) Open(ctx context.Context, entry repository.TreeEntry) (io.ReadCloser, error) {
	if s == nil || ctx == nil || entry.Validate() != nil || entry.Kind == repository.FileKindDirectory {
		return nil, ErrInvalidWorkspaceSource
	}
	if err := checkMaterializeContext(ctx); err != nil {
		return nil, err
	}
	nativePath, err := nativeRepoPath(entry.Path)
	if err != nil {
		return nil, ErrInvalidWorkspaceSource
	}
	if entry.Kind == repository.FileKindSymlink {
		target, err := os.Readlink(filepath.Join(s.root, nativePath))
		if err != nil {
			return nil, err
		}
		return io.NopCloser(strings.NewReader(target)), nil
	}
	return paths.OpenExistingProtectedFile(s.root, nativePath)
}

type captureSourceEntry struct {
	entry   repository.TreeEntry
	base    bool
	capture *repository.CaptureBlobRef
}

// CaptureTreeSource overlays one accepted local capture on its pinned base.
// The capture store remains the only reader of accepted blob bytes.
type CaptureTreeSource struct {
	manifest app.CaptureManifest
	captures app.LocalCaptureStore
	base     app.ReviewSnapshotBaseSource

	mu      sync.Mutex
	entries map[repository.RepoPathKey]captureSourceEntry
}

func NewCaptureTreeSource(manifest app.CaptureManifest, captures app.LocalCaptureStore, base app.ReviewSnapshotBaseSource) (*CaptureTreeSource, error) {
	if manifest.Validate() != nil || captures == nil || base == nil {
		return nil, ErrInvalidWorkspaceSource
	}
	return &CaptureTreeSource{manifest: manifest, captures: captures, base: base, entries: make(map[repository.RepoPathKey]captureSourceEntry)}, nil
}

func (s *CaptureTreeSource) Identity() app.WorkspaceSourceIdentity {
	if s == nil {
		return app.WorkspaceSourceIdentity{}
	}
	return app.WorkspaceSourceIdentity{Kind: "accepted_capture", ID: string(s.manifest.CaptureID), ManifestHash: s.manifest.ManifestHash}
}

func (s *CaptureTreeSource) List(ctx context.Context) ([]repository.TreeEntry, error) {
	if s == nil || ctx == nil || s.manifest.Validate() != nil {
		return nil, ErrInvalidWorkspaceSource
	}
	accepted, err := s.captures.OpenCaptureManifest(ctx, s.manifest.CaptureID)
	if err != nil || accepted.ManifestHash != s.manifest.ManifestHash || accepted.RepositoryID != s.manifest.RepositoryID || accepted.WorktreeID != s.manifest.WorktreeID || accepted.Candidate.Fingerprint != s.manifest.Candidate.Fingerprint || accepted.Validate() != nil {
		return nil, app.ErrCaptureCorrupt
	}
	baseEntries, err := s.base.ListBase(ctx, s.manifest.Candidate.Base)
	if err != nil {
		return nil, err
	}
	entries := make(map[repository.RepoPathKey]captureSourceEntry, len(baseEntries)+len(s.manifest.Candidate.Entries))
	for _, entry := range baseEntries {
		if err := entry.Validate(); err != nil {
			return nil, ErrInvalidWorkspaceSource
		}
		if entry.Kind == repository.FileKindUnknown || entry.Kind == repository.FileKindGitlink {
			continue
		}
		entries[entry.Path.Key()] = captureSourceEntry{entry: entry, base: true}
	}
	for _, captured := range s.manifest.Candidate.SortedEntries() {
		if err := captured.Validate(); err != nil {
			return nil, ErrInvalidWorkspaceSource
		}
		if captured.Change.OldPath != nil {
			removeCaptureSourceEntries(entries, captured.Change.OldPath)
		}
		if captured.Change.NewPath == nil {
			continue
		}
		if captured.Change.NewFileKind != repository.FileKindRegular && captured.Change.NewFileKind != repository.FileKindSymlink {
			continue
		}
		blob, found := captureWorkingBlob(captured, *captured.Change.NewPath)
		if !found {
			return nil, ErrInvalidWorkspaceSource
		}
		entry := treeEntryForPath(*captured.Change.NewPath, captured.Change.NewFileKind, captured.Change.NewMode)
		entries[entry.Path.Key()] = captureSourceEntry{entry: entry, capture: &blob}
	}
	result := make([]repository.TreeEntry, 0, len(entries))
	for _, value := range entries {
		result = append(result, value.entry)
	}
	s.mu.Lock()
	s.entries = entries
	s.mu.Unlock()
	sort.Slice(result, func(i, j int) bool { return bytes.Compare(result[i].Path.Bytes(), result[j].Path.Bytes()) < 0 })
	return result, nil
}

func (s *CaptureTreeSource) Open(ctx context.Context, entry repository.TreeEntry) (io.ReadCloser, error) {
	if s == nil || ctx == nil || entry.Validate() != nil {
		return nil, ErrInvalidWorkspaceSource
	}
	accepted, err := s.captures.OpenCaptureManifest(ctx, s.manifest.CaptureID)
	if err != nil || accepted.ManifestHash != s.manifest.ManifestHash || accepted.RepositoryID != s.manifest.RepositoryID || accepted.WorktreeID != s.manifest.WorktreeID || accepted.Candidate.Fingerprint != s.manifest.Candidate.Fingerprint || accepted.Validate() != nil {
		return nil, app.ErrCaptureCorrupt
	}
	s.mu.Lock()
	value, ok := s.entries[entry.Path.Key()]
	s.mu.Unlock()
	if !ok || value.entry.Kind != entry.Kind {
		return nil, ErrInvalidWorkspaceSource
	}
	if value.base {
		return s.base.OpenBase(ctx, s.manifest.Candidate.Base, value.entry)
	}
	if value.capture == nil {
		return nil, ErrInvalidWorkspaceSource
	}
	return &captureBlobReader{ctx: ctx, store: s.captures, manifest: accepted, blob: *value.capture}, nil
}

type captureBlobReader struct {
	ctx      context.Context
	store    app.LocalCaptureStore
	manifest app.CaptureManifest
	blob     repository.CaptureBlobRef
	offset   app.ByteSize
	chunk    []byte
}

func (r *captureBlobReader) Read(buffer []byte) (int, error) {
	if err := checkMaterializeContext(r.ctx); err != nil {
		return 0, err
	}
	if r.offset >= app.ByteSize(r.blob.Artifact.Bytes) {
		return 0, io.EOF
	}
	if len(r.chunk) == 0 {
		request := app.ByteSize(r.blob.Artifact.Bytes) - r.offset
		if request > 256*app.KiB {
			request = 256 * app.KiB
		}
		chunk, err := r.store.ReadBlobRange(r.ctx, app.CaptureBlobRead{CaptureID: r.manifest.CaptureID, ManifestHash: r.manifest.ManifestHash, RelativePath: r.blob.Artifact.RelativePath, Expected: app.StreamIdentity{Bytes: app.ByteSize(r.blob.Artifact.Bytes), SHA256: r.blob.Artifact.ContentSHA256}, Offset: r.offset, MaxBytes: request})
		if err != nil {
			return 0, err
		}
		if len(chunk) == 0 || app.ByteSize(len(chunk)) > request {
			return 0, app.ErrCaptureCorrupt
		}
		r.chunk = chunk
	}
	count := copy(buffer, r.chunk)
	r.chunk = r.chunk[count:]
	r.offset += app.ByteSize(count)
	return count, nil
}

func (r *captureBlobReader) Close() error { return nil }

func captureWorkingBlob(entry repository.LocalCaptureEntry, path repository.RepoPath) (repository.CaptureBlobRef, bool) {
	for _, blob := range entry.Blobs {
		if blob.Side == repository.CaptureBlobWorkingTree && bytes.Equal(blob.Path.Bytes(), path.Bytes()) {
			return blob, true
		}
	}
	return repository.CaptureBlobRef{}, false
}

func removeCaptureSourceEntries(entries map[repository.RepoPathKey]captureSourceEntry, path *repository.RepoPath) {
	if path == nil {
		return
	}
	key := string(path.Bytes())
	for candidate := range entries {
		value := string(candidate)
		if value == key || strings.HasPrefix(value, key+"/") {
			delete(entries, candidate)
		}
	}
}

func treeEntryForPath(path repository.RepoPath, kind repository.FileKind, mode uint32) repository.TreeEntry {
	value := path.Bytes()
	entry := repository.TreeEntry{Path: value, Kind: kind, Mode: mode, ModeClass: repository.ClassifyGitMode(mode)}
	separator := bytes.LastIndexByte(value, '/')
	if separator >= 0 {
		entry.Parent = append([]byte(nil), value[:separator]...)
		entry.Name = append([]byte(nil), value[separator+1:]...)
	} else {
		entry.Name = append([]byte(nil), value...)
	}
	return entry
}
