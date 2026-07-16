package artifactspool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/paths"
)

const maxPublishedRangeBytes app.ByteSize = 256 * app.KiB

// CleanupOwner adapts exact published-artifact removal to T060. The manager
// revalidates owner-root containment and the complete manifest before removal.
type CleanupOwner struct {
	Manager *Manager
}

func (o CleanupOwner) Remove(ctx context.Context, resource app.CleanupResource) error {
	if o.Manager == nil || resource.Published.Identity.SpoolID == "" || resource.Kind != app.CleanupResourceCapture && resource.Kind != app.CleanupResourceProposal {
		return app.ErrCleanupInvalid
	}
	if resource.ManifestHash != resource.Published.Identity.ManifestHash || resource.Published.Target.OwnerKind == "" {
		return app.ErrCleanupConflict
	}
	return o.Manager.RemovePublished(ctx, resource.Published)
}

// ReadProposalPatchRange verifies and returns one bounded range from an
// adopted proposal patch. The caller supplies the persisted target and full
// stream identity; this adapter never resolves an arbitrary path.
func (m *Manager) ReadProposalPatchRange(ctx context.Context, request app.ProposalPatchRangeRequest) (app.ProposalPatchRange, error) {
	if request.Validate() != nil {
		return app.ProposalPatchRange{}, app.ErrProposalPatchRangeInvalid
	}
	if request.MaxBytes > maxPublishedRangeBytes {
		return app.ProposalPatchRange{}, app.ErrProposalPatchRangeInvalid
	}
	data, err := m.ReadPublishedRange(ctx, request.Published.Target, "", app.StreamIdentity{Bytes: request.PatchBytes, SHA256: request.PatchSHA256}, request.Offset, request.MaxBytes)
	if err != nil {
		return app.ProposalPatchRange{}, err
	}
	digest := sha256.Sum256(data)
	result := app.ProposalPatchRange{ArtifactID: request.ArtifactID, PatchSHA256: request.PatchSHA256, Offset: request.Offset, Length: app.ByteSize(len(data)), SHA256: hex.EncodeToString(digest[:]), Bytes: append([]byte(nil), data...), Complete: true}
	if result.Validate(request) != nil {
		return app.ProposalPatchRange{}, app.ErrCaptureCorrupt
	}
	return result, nil
}

// ReadPublishedRange verifies one accepted file against its immutable stream
// identity and returns only the requested bounded range. Callers never receive
// the protected absolute path.
func (m *Manager) ReadPublishedRange(ctx context.Context, target app.PublishTarget, relative string, expected app.StreamIdentity, offset, maxBytes app.ByteSize) ([]byte, error) {
	if m == nil || ctx == nil || expected.Bytes == 0 || expected.SHA256 == "" || maxBytes == 0 || maxBytes > maxPublishedRangeBytes || offset > expected.Bytes {
		return nil, app.ErrInvalidArtifactSpool
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	cleanTarget, cleanSource, err := validatePublishTarget(target, maxPublishedRangeBytes)
	if err != nil {
		return nil, err
	}
	cleanRelative := ""
	if cleanSource != "" {
		if relative != "" {
			return nil, app.ErrInvalidArtifactSpool
		}
		cleanRelative = cleanTarget
	} else {
		cleanRelative, err = validateRelative(relative, maxPublishedRangeBytes)
		if err != nil {
			return nil, err
		}
		cleanRelative = filepath.Join(cleanTarget, cleanRelative)
	}
	ownerRoot := filepath.Join(m.publishRoot, digestID(string(target.OwnerKind)))
	file, err := paths.OpenExistingProtectedFile(ownerRoot, cleanRelative)
	if err != nil {
		return nil, app.ErrCaptureNotFound
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 0 || app.ByteSize(info.Size()) != expected.Bytes {
		return nil, app.ErrCaptureCorrupt
	}
	if err := validateRegularNoHardLink(file, info); err != nil {
		return nil, app.ErrCaptureCorrupt
	}
	hashValue := sha256.New()
	buffer := make([]byte, 64*1024)
	for {
		if err := contextErr(ctx); err != nil {
			return nil, err
		}
		read, readErr := file.Read(buffer)
		if read > 0 {
			_, _ = hashValue.Write(buffer[:read])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, app.ErrCaptureCorrupt
		}
	}
	if hex.EncodeToString(hashValue.Sum(nil)) != expected.SHA256 {
		return nil, app.ErrCaptureCorrupt
	}
	if offset == expected.Bytes {
		return []byte{}, nil
	}
	remaining := expected.Bytes - offset
	if maxBytes > remaining {
		maxBytes = remaining
	}
	if _, err := file.Seek(int64(offset), io.SeekStart); err != nil {
		return nil, app.ErrCaptureCorrupt
	}
	data := make([]byte, int(maxBytes))
	if _, err := io.ReadFull(file, data); err != nil {
		return nil, app.ErrCaptureCorrupt
	}
	return data, nil
}

// RemovePublished removes one newly published artifact only after rechecking
// its exact protected target and verified identity. It is intentionally not a
// general cleanup primitive.
func (m *Manager) RemovePublished(ctx context.Context, published app.PublishedArtifact) error {
	if m == nil || ctx == nil || published.Target.OwnerKind == "" {
		return app.ErrInvalidArtifactSpool
	}
	if err := published.Identity.Validate(); err != nil || published.Limits.Validate() != nil {
		return app.ErrInvalidArtifactSpool
	}
	lock, err := m.acquireOwnerLock(ctx, published.Target.OwnerKind, "published-"+digestID(string(published.Target.OwnerKind), published.Target.RelativePath, published.Target.SourceRelativePath))
	if err != nil {
		return err
	}
	defer lock.Close()
	destination, err := m.destinationPath(published.Target, published.Limits.MaxPathBytes)
	if err != nil {
		return err
	}
	if err := verifyPublishedTarget(ctx, published.Identity, published.Target, destination, published.Limits); err != nil {
		return app.ErrSpoolResidueAmbiguous
	}
	if published.Target.SourceRelativePath != "" {
		info, err := os.Lstat(destination)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return app.ErrSpoolResidueAmbiguous
		}
		if err := os.Remove(destination); err != nil {
			return app.ErrSpoolResidueAmbiguous
		}
	} else {
		info, err := os.Lstat(destination)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return app.ErrSpoolResidueAmbiguous
		}
		if err := removePayloadTree(destination); err != nil {
			return err
		}
		if err := os.Remove(destination); err != nil {
			return app.ErrSpoolResidueAmbiguous
		}
	}
	if err := syncDirectory(filepath.Dir(destination)); err != nil {
		return app.ErrSpoolResidueAmbiguous
	}
	return nil
}

var _ app.PublishedArtifactReader = (*Manager)(nil)
var _ app.PublishedArtifactReleaser = (*Manager)(nil)
var _ app.ProposalPatchReader = (*Manager)(nil)
