package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
	"github.com/Scottlr/nudge/internal/paths"
)

var (
	ErrResultFreezeUnavailable = errors.New("proposal result freeze unavailable")
	ErrResultRootChanged       = errors.New("proposal result root changed during freeze")
)

// ResultFreezeRequest binds one freeze to the exact proposal attempt and the
// trusted baseline that T035 installed. The caller must hold the workspace
// lease for the complete request lifetime.
type ResultFreezeRequest struct {
	SessionID        domain.ReviewSessionID
	ProposalID       domain.ProposalID
	AttemptID        domain.OperationID
	ThreadID         domain.ReviewThreadID
	ProviderTurnID   domain.ProviderTurnID
	ProviderTurnRef  string
	Baseline         app.WorkspaceManifest
	BaselineIdentity review.SnapshotIdentity
	Isolation        IsolationContract
	Quiescence       QuiescenceProof
	Policy           app.ResourcePolicy
	Now              time.Time
}

// AdoptResultSnapshot records one complete freeze through the session-fenced
// transaction. Filesystem evidence is already immutable by this point; the
// transaction only adopts its identity and bounded manifest/delta artifact.
func AdoptResultSnapshot(ctx context.Context, store SessionTransactionStore, guard app.SessionWriteGuard, snapshot app.ResultSnapshot) (app.SessionWriteGuard, error) {
	if ctx == nil || store == nil || guard.Validate() != nil || snapshot.Validate() != nil || !snapshot.Manifest.Complete || !snapshot.Delta.Complete {
		return guard, ErrResultFreezeUnavailable
	}
	return store.WithSessionTx(ctx, guard, func(tx app.ReviewStoreTx) error {
		adopter, ok := tx.(app.ResultSnapshotStoreTx)
		if !ok {
			return ErrResultFreezeUnavailable
		}
		return adopter.AdoptResultSnapshot(ctx, snapshot)
	})
}

func (r ResultFreezeRequest) validate(handle WorkspaceHandle) error {
	if handle.WorkspaceID == "" || handle.WorktreeID == "" || handle.ThreadID == "" || handle.Nonce == "" || handle.Roots.Result.Path() == "" || r.SessionID == "" || r.ProposalID == "" || r.AttemptID == "" || r.ThreadID == "" || r.ProviderTurnID == "" || r.ProviderTurnRef == "" || r.Baseline.Validate() != nil || r.BaselineIdentity.Validate() != nil || r.BaselineIdentity.ManifestHash != r.Baseline.Hash || r.Policy.Validate() != nil || r.Now.IsZero() {
		return ErrResultFreezeUnavailable
	}
	if r.Isolation.RequireProposalTurn() != nil || r.Isolation.Roots.Result != handle.Roots.Result.Path() || r.Isolation.Growth.Validate() != nil {
		return ErrResultFreezeUnavailable
	}
	if err := r.Isolation.VerifyQuiescence(r.Quiescence); err != nil {
		return err
	}
	if r.Isolation.Roots.Baseline != handle.Roots.Baseline.Path() || r.Isolation.Roots.Admin != handle.Roots.Admin.Path() || r.Isolation.Roots.Destination != handle.Roots.Destination.Path() {
		return ErrResultFreezeUnavailable
	}
	return nil
}

// FreezeResult revokes the provider result lease at the caller boundary,
// independently walks the held result root, and returns only a complete
// immutable evidence value. It never generates a patch or mutates the root.
func FreezeResult(ctx context.Context, lease *WorkspaceLease, request ResultFreezeRequest) (app.ResultSnapshot, error) {
	if lease == nil || ctx == nil {
		return app.ResultSnapshot{}, ErrResultFreezeUnavailable
	}
	handle := lease.Handle()
	if err := request.validate(handle); err != nil {
		return app.ResultSnapshot{}, err
	}
	if handle.Nonce == "" || handle.Roots.Result.NativeIdentity() == "" {
		return app.ResultSnapshot{}, ErrResultFreezeUnavailable
	}
	freezeCtx, cancel := context.WithTimeout(ctx, request.Policy.Artifact.SnapshotDeadline)
	defer cancel()
	root := handle.Roots.Result.Path()
	before, err := paths.NativeDirectoryIdentity(root)
	if err != nil || before != handle.Roots.Result.NativeIdentity() {
		return app.ResultSnapshot{}, ErrResultRootChanged
	}
	entries, reason, err := enumerateResultRoot(freezeCtx, root, request.Policy)
	if err != nil {
		return app.ResultSnapshot{}, err
	}
	after, err := paths.NativeDirectoryIdentity(root)
	if err != nil || after != before {
		return app.ResultSnapshot{}, ErrResultRootChanged
	}
	manifest, err := app.NewResultManifest(entries, request.Policy.Version, true, reason)
	if err != nil {
		return app.ResultSnapshot{}, err
	}
	delta, err := app.CompareResultManifest(request.Baseline, manifest)
	if err != nil {
		return app.ResultSnapshot{}, err
	}
	snapshotReason := manifest.Reason
	if snapshotReason == app.ResultReasonNone {
		snapshotReason = delta.Reason
	}
	state := app.ResultSnapshotReady
	if snapshotReason != app.ResultReasonNone || !manifest.Complete || !delta.Complete {
		state = app.ResultSnapshotNonReady
	}
	resultIdentity := review.SnapshotIdentity{
		ID:           domain.ReviewSnapshotID("result-" + manifest.Hash),
		Ref:          repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: handle.WorktreeID, Fingerprint: manifest.Hash},
		ManifestHash: manifest.Hash,
	}
	snapshot := app.ResultSnapshot{
		Version:          app.ResultSnapshotVersion,
		SessionID:        request.SessionID,
		ProposalID:       request.ProposalID,
		WorkspaceID:      handle.WorkspaceID,
		WorktreeID:       handle.WorktreeID,
		AttemptID:        request.AttemptID,
		ThreadID:         request.ThreadID,
		ProviderTurnID:   request.ProviderTurnID,
		ProviderTurnRef:  request.ProviderTurnRef,
		Baseline:         request.BaselineIdentity,
		Result:           resultIdentity,
		Manifest:         manifest,
		Delta:            delta,
		PolicyVersion:    request.Policy.Version,
		IsolationVersion: handle.IsolationVersion,
		LeaseNonce:       handle.Nonce,
		State:            state,
		Reason:           snapshotReason,
		CreatedAt:        request.Now.UTC(),
	}
	return app.NewResultSnapshot(snapshot)
}

func enumerateResultRoot(ctx context.Context, root string, policy app.ResourcePolicy) ([]app.ResultSnapshotEntry, app.ResultSnapshotReason, error) {
	entries := make([]app.ResultSnapshotEntry, 0)
	seenNative := make(map[string]int)
	reason := app.ResultReasonNone
	verifiedRoot, rootErr := paths.NewVerifiedRoot(root)
	if rootErr != nil {
		return nil, app.ResultReasonRootChanged, rootErr
	}
	nativeResolver := paths.NewNativePathResolver()
	err := filepath.WalkDir(root, func(path string, dirEntry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := checkFreezeContext(ctx); err != nil {
			return err
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == "." || !containedPath(root, path) {
			return ErrResultRootChanged
		}
		rawPath, err := repository.NewRepoPath([]byte(filepath.ToSlash(relative)))
		if err != nil || len(rawPath) > int(policy.Input.RepoPathBytes) {
			return app.ErrReviewSnapshotLimit
		}
		components := strings.Split(filepath.ToSlash(relative), "/")
		if app.Count(len(components)) > policy.Symlink.NormalizedRelativeDepth {
			return app.ErrReviewSnapshotLimit
		}
		if hasGitAdminComponent(components) {
			entries = append(entries, unsupportedResultEntry(rawPath, app.ResultReasonGitAdminPath))
			reason = firstFreezeReason(reason, app.ResultReasonGitAdminPath)
			if dirEntry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		nativePath, err := nativeRepoPath(rawPath)
		if err != nil {
			entries = append(entries, unsupportedResultEntry(rawPath, app.ResultReasonPathAlias))
			reason = firstFreezeReason(reason, app.ResultReasonPathAlias)
			return nil
		}
		nativeKey := comparisonPath(filepath.Join(root, nativePath))
		duplicateNative := false
		if previous, exists := seenNative[nativeKey]; exists {
			entries[previous].Reason = app.ResultReasonPathAlias
			duplicateNative = true
			reason = firstFreezeReason(reason, app.ResultReasonPathAlias)
		}
		seenNative[nativeKey] = len(entries)
		info, err := os.Lstat(path)
		if err != nil {
			return ErrResultRootChanged
		}
		switch {
		case info.IsDir() && info.Mode()&os.ModeSymlink == 0:
			identity, identityErr := paths.NativeDirectoryIdentity(path)
			if identityErr != nil {
				entries = append(entries, unsupportedResultEntry(rawPath, app.ResultReasonNativeIdentity))
				reason = firstFreezeReason(reason, app.ResultReasonNativeIdentity)
				return filepath.SkipDir
			}
			entries = append(entries, app.ResultSnapshotEntry{Path: rawPath.Bytes(), Kind: repository.FileKindDirectory, Mode: 0o40000, NativeIdentityHash: hashNativeIdentity(identity), Complete: true})
		case info.Mode()&os.ModeSymlink != 0:
			target, readErr := os.Readlink(path)
			if readErr != nil {
				return ErrResultRootChanged
			}
			afterInfo, statErr := os.Lstat(path)
			afterTarget, targetErr := os.Readlink(path)
			if statErr != nil || targetErr != nil || !os.SameFile(info, afterInfo) || afterTarget != target {
				entries = append(entries, unsupportedResultEntry(rawPath, app.ResultReasonResultRace))
				reason = firstFreezeReason(reason, app.ResultReasonResultRace)
				return nil
			}
			linkTarget := []byte(target)
			if app.ByteSize(len(linkTarget)) > policy.Symlink.TrackedBlobBytes {
				return app.ErrReviewSnapshotLimit
			}
			entryReason := app.ResultReasonUnsupportedEntry
			if symlinkEscapes(root, path, target) {
				entryReason = app.ResultReasonPathAlias
			}
			entries = append(entries, app.ResultSnapshotEntry{Path: rawPath.Bytes(), Kind: repository.FileKindSymlink, Mode: 0o120000, Bytes: uint64(len(linkTarget)), SHA256: hashResultBytes(linkTarget), LinkTarget: linkTarget, Complete: true, Reason: entryReason})
			reason = firstFreezeReason(reason, entryReason)
		case info.Mode().IsRegular():
			entry, entryErr := scanResultFile(ctx, root, nativePath, rawPath, info, policy, nativeResolver, verifiedRoot)
			if entryErr != nil {
				if errors.Is(entryErr, app.ErrReviewSnapshotLimit) {
					return entryErr
				}
				entries = append(entries, unsupportedResultEntry(rawPath, app.ResultReasonResultRace))
				reason = firstFreezeReason(reason, app.ResultReasonResultRace)
				return nil
			}
			entries = append(entries, entry)
			if entry.Reason != app.ResultReasonNone {
				reason = firstFreezeReason(reason, entry.Reason)
			}
		default:
			entries = append(entries, unsupportedResultEntry(rawPath, app.ResultReasonUnsupportedEntry))
			reason = firstFreezeReason(reason, app.ResultReasonUnsupportedEntry)
		}
		if duplicateNative && len(entries) > 0 {
			entries[len(entries)-1].Reason = firstFreezeReason(entries[len(entries)-1].Reason, app.ResultReasonPathAlias)
		}
		if app.Count(len(entries)) > policy.Artifact.SnapshotEntries {
			return app.ErrReviewSnapshotLimit
		}
		return nil
	})
	if err != nil {
		return nil, reason, err
	}
	var totalBytes uint64
	for _, entry := range entries {
		if entry.Kind == repository.FileKindDirectory {
			continue
		}
		if totalBytes > ^uint64(0)-entry.Bytes || totalBytes+entry.Bytes > uint64(policy.Artifact.SnapshotBytes) {
			return nil, reason, app.ErrReviewSnapshotLimit
		}
		totalBytes += entry.Bytes
	}
	return entries, reason, nil
}

func scanResultFile(ctx context.Context, root, nativePath string, rawPath repository.RepoPath, initial os.FileInfo, policy app.ResourcePolicy, nativeResolver *paths.NativePathResolver, verifiedRoot paths.VerifiedRoot) (app.ResultSnapshotEntry, error) {
	identity, err := paths.NativeFileIdentity(filepath.Join(root, nativePath))
	if err != nil {
		return unsupportedResultEntry(rawPath, app.ResultReasonNativeIdentity), nil
	}
	file, err := paths.OpenExistingProtectedFile(root, nativePath)
	if err != nil {
		return app.ResultSnapshotEntry{}, err
	}
	statBefore, err := file.Stat()
	if err != nil || !statBefore.Mode().IsRegular() {
		_ = file.Close()
		return unsupportedResultEntry(rawPath, app.ResultReasonResultRace), nil
	}
	hash := sha256.New()
	classifier := repository.NewContentClassifierV1(false)
	textSemanticsWriter := repository.NewTextByteSemanticsWriter()
	buffer := make([]byte, 64*1024)
	var size uint64
	for {
		if err := checkFreezeContext(ctx); err != nil {
			_ = file.Close()
			return app.ResultSnapshotEntry{}, err
		}
		read, readErr := file.Read(buffer)
		if read > 0 {
			if size > uint64(policy.Artifact.ProposalFileBytes) || uint64(read) > uint64(policy.Artifact.ProposalFileBytes)-size {
				_ = file.Close()
				return unsupportedResultEntry(rawPath, app.ResultReasonLimit), nil
			}
			size += uint64(read)
			if _, err := hash.Write(buffer[:read]); err != nil {
				_ = file.Close()
				return app.ResultSnapshotEntry{}, err
			}
			_, _ = classifier.Write(buffer[:read])
			_, _ = textSemanticsWriter.Write(buffer[:read])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			_ = file.Close()
			return app.ResultSnapshotEntry{}, readErr
		}
	}
	statAfter, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil || closeErr != nil || statAfter.Size() != int64(size) || statBefore.Size() != statAfter.Size() || !statBefore.ModTime().Equal(statAfter.ModTime()) {
		return unsupportedResultEntry(rawPath, app.ResultReasonResultRace), nil
	}
	afterIdentity, identityErr := paths.NativeFileIdentity(filepath.Join(root, nativePath))
	if identityErr != nil || afterIdentity != identity || initial.Size() != int64(size) || initial.Mode() != statAfter.Mode() {
		return unsupportedResultEntry(rawPath, app.ResultReasonResultRace), nil
	}
	contentClass := classifier.Classify()
	var textSemantics *repository.TextByteSemantics
	if contentClass == repository.ContentClassRegularTextUTF8 {
		semantics, semanticsErr := textSemanticsWriter.Semantics(size)
		if semanticsErr != nil {
			return app.ResultSnapshotEntry{}, semanticsErr
		}
		textSemantics = &semantics
	}
	entry := app.ResultSnapshotEntry{Path: rawPath.Bytes(), Kind: repository.FileKindRegular, Mode: snapshotLogicalMode(repository.FileKindRegular, uint32(initial.Mode().Perm())), Bytes: size, SHA256: hex.EncodeToString(hash.Sum(nil)), ContentClass: contentClass, TextSemantics: textSemantics, NativeIdentityHash: identity.FileIdentityHash, NativeAlias: &identity, Complete: true}
	if nativeResolver != nil {
		token, evidence, resolveErr := nativeResolver.Resolve(ctx, verifiedRoot, rawPath, repository.NativeReadExisting)
		if resolveErr != nil {
			if errors.Is(resolveErr, paths.ErrNativePathReviewOnly) {
				return app.ResultSnapshotEntry{Path: rawPath.Bytes(), Kind: repository.FileKindUnknown, Complete: false, Reason: app.ResultReasonPathAlias}, nil
			}
			return app.ResultSnapshotEntry{}, resolveErr
		}
		_ = token.Close()
		entry.NativePath = &evidence
	}
	if identity.LinkCount > 1 {
		entry.Reason = app.ResultReasonSharedIdentity
	}
	return entry, nil
}

func unsupportedResultEntry(path repository.RepoPath, reason app.ResultSnapshotReason) app.ResultSnapshotEntry {
	return app.ResultSnapshotEntry{Path: path.Bytes(), Kind: repository.FileKindUnknown, Complete: false, Reason: reason}
}

func hasGitAdminComponent(components []string) bool {
	for _, component := range components {
		if strings.EqualFold(component, ".git") {
			return true
		}
	}
	return false
}

func symlinkEscapes(root, path, target string) bool {
	if filepath.IsAbs(target) || filepath.VolumeName(target) != "" {
		return true
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(path), target))
	return !containedPath(root, resolved)
}

func hashNativeIdentity(identity repository.NativeIdentity) string {
	digest := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(digest[:])
}

func hashResultBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func firstFreezeReason(current, next app.ResultSnapshotReason) app.ResultSnapshotReason {
	if current != app.ResultReasonNone {
		return current
	}
	return next
}

func checkFreezeContext(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
