package artifactspool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/paths"
)

// Publish adopts a closed verified payload through a same-volume atomic
// no-replace primitive. Existing destinations are never touched.
func (h *Handle) Publish(ctx context.Context, expected app.ArtifactIdentity, target app.PublishTarget) (app.PublishedArtifact, error) {
	if h == nil || ctx == nil {
		return app.PublishedArtifact{}, app.ErrInvalidArtifactSpool
	}
	h.operationMu.Lock()
	defer h.operationMu.Unlock()
	lock, err := h.manager.acquireOwnerLock(ctx, h.descriptor.OwnerKind, string(h.descriptor.OperationID))
	if err != nil {
		return app.PublishedArtifact{}, err
	}
	defer lock.Close()
	return h.publishLocked(ctx, expected, target)
}

func (h *Handle) publishLocked(ctx context.Context, expected app.ArtifactIdentity, target app.PublishTarget) (app.PublishedArtifact, error) {
	if err := contextErr(ctx); err != nil {
		return app.PublishedArtifact{}, err
	}
	if err := expected.Validate(); err != nil {
		return app.PublishedArtifact{}, err
	}
	h.stateMu.Lock()
	state := h.descriptor.State
	verified := h.verified
	limits := h.descriptor.Limits
	owner := h.descriptor.OwnerKind
	h.stateMu.Unlock()
	if state != app.SpoolVerified || expected != verified || expected.SpoolID != h.descriptor.SpoolID || target.OwnerKind != owner {
		return app.PublishedArtifact{}, app.ErrSpoolNotReady
	}
	cleanTarget, cleanSource, err := validatePublishTarget(target, limits.MaxPathBytes)
	if err != nil {
		return app.PublishedArtifact{}, err
	}
	if cleanSource != "" && expected.Entries != 1 {
		return app.PublishedArtifact{}, app.ErrInvalidArtifactSpool
	}
	destination, err := h.manager.destinationPath(app.PublishTarget{OwnerKind: target.OwnerKind, RelativePath: cleanTarget, SourceRelativePath: cleanSource}, limits.MaxPathBytes)
	if err != nil {
		return app.PublishedArtifact{}, err
	}
	source := h.payloadPath
	if cleanSource != "" {
		source = filepath.Join(h.payloadPath, cleanSource)
		file, openErr := openVerifiedFile(h.payloadPath, cleanSource)
		if openErr != nil {
			return app.PublishedArtifact{}, openErr
		}
		closeErr := file.Close()
		if closeErr != nil {
			return app.PublishedArtifact{}, closeErr
		}
	} else if err := paths.EnsurePrivateDir(source); err != nil {
		return app.PublishedArtifact{}, err
	}
	if err := paths.EnsurePrivateDir(filepath.Dir(destination)); err != nil {
		return app.PublishedArtifact{}, err
	}
	if err := h.refreshMarker(); err != nil {
		return app.PublishedArtifact{}, err
	}
	h.marker.TargetDigest = digestID(string(target.OwnerKind), cleanTarget, cleanSource)
	if err := h.updateState(app.SpoolPublishing); err != nil {
		return app.PublishedArtifact{}, err
	}
	if err := renameNoReplace(source, destination); err != nil {
		_ = h.updateState(app.SpoolRecoveryRequired)
		if errors.Is(err, ErrDestinationExists) {
			return app.PublishedArtifact{}, app.ErrSpoolDestinationExists
		}
		if errors.Is(err, os.ErrExist) {
			return app.PublishedArtifact{}, app.ErrSpoolDestinationExists
		}
		return app.PublishedArtifact{}, err
	}
	if err := syncDirectory(filepath.Dir(destination)); err != nil {
		_ = h.updateState(app.SpoolRecoveryRequired)
		return app.PublishedArtifact{}, err
	}
	h.marker.ManifestHash = expected.ManifestHash
	h.marker.ManifestBytes = uint64(expected.Bytes)
	h.marker.ManifestEntries = uint64(expected.Entries)
	h.marker.VerifiedUnixNano = expected.VerifiedAt.UnixNano()
	if err := h.updateState(app.SpoolPublished); err != nil {
		return app.PublishedArtifact{}, app.ErrSpoolResidueAmbiguous
	}
	if err := removePublishedResidue(h.spoolPath, h.payloadPath); err != nil {
		return app.PublishedArtifact{}, err
	}
	h.setDescriptorState(app.SpoolPublished)
	return app.PublishedArtifact{Identity: expected, Target: app.PublishTarget{OwnerKind: target.OwnerKind, RelativePath: cleanTarget, SourceRelativePath: cleanSource}}, nil
}

func (m *Manager) destinationPath(target app.PublishTarget, maxPath app.ByteSize) (string, error) {
	clean, _, err := validatePublishTarget(target, maxPath)
	if err != nil {
		return "", err
	}
	ownerRoot := filepath.Join(m.publishRoot, digestID(string(target.OwnerKind)))
	if err := paths.EnsurePrivateDir(ownerRoot); err != nil {
		return "", err
	}
	destination := filepath.Join(ownerRoot, clean)
	if !pathContained(ownerRoot, destination) {
		return "", app.ErrInvalidArtifactSpool
	}
	return destination, nil
}

func validatePublishTarget(target app.PublishTarget, maxPath app.ByteSize) (string, string, error) {
	if target.OwnerKind == "" {
		return "", "", app.ErrInvalidArtifactSpool
	}
	cleanTarget, err := validateRelative(target.RelativePath, maxPath)
	if err != nil {
		return "", "", err
	}
	cleanSource := ""
	if target.SourceRelativePath != "" && target.SourceRelativePath != "." {
		cleanSource, err = validateRelative(target.SourceRelativePath, maxPath)
		if err != nil {
			return "", "", err
		}
	}
	return cleanTarget, cleanSource, nil
}

func pathContained(root, child string) bool {
	relative, err := filepath.Rel(root, child)
	return err == nil && relative != "" && relative != "." && relative != ".." && !isParentRelative(relative)
}

func removePublishedResidue(spoolPath, payloadPath string) error {
	if _, err := os.Lstat(payloadPath); err == nil {
		if err := removeEmptyTree(payloadPath); err != nil {
			return app.ErrSpoolResidueAmbiguous
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return app.ErrSpoolResidueAmbiguous
	}
	if err := removeMarkerOnly(spoolPath); err != nil {
		return err
	}
	if err := os.Remove(spoolPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return app.ErrSpoolResidueAmbiguous
	}
	return nil
}

func removeEmptyTree(root string) error {
	pathsToRemove := make([]string, 0, 4)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return app.ErrSpoolResidueAmbiguous
		}
		if !entry.IsDir() {
			return app.ErrSpoolResidueAmbiguous
		}
		if err := paths.EnsurePrivateDir(path); err != nil {
			return app.ErrSpoolResidueAmbiguous
		}
		pathsToRemove = append(pathsToRemove, path)
		return nil
	})
	if err != nil {
		return err
	}
	for index := len(pathsToRemove) - 1; index >= 0; index-- {
		if err := os.Remove(pathsToRemove[index]); err != nil {
			return app.ErrSpoolResidueAmbiguous
		}
	}
	return nil
}

func removeMarkerOnly(spoolPath string) error {
	path := filepath.Join(spoolPath, markerName)
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return app.ErrSpoolResidueAmbiguous
		}
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return app.ErrSpoolResidueAmbiguous
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncDirectory(spoolPath)
}

func verifyPublishedTarget(ctx context.Context, expected app.ArtifactIdentity, target app.PublishTarget, destination string, limits app.SpoolLimits) error {
	if err := expected.Validate(); err != nil {
		return err
	}
	cleanTarget, cleanSource, err := validatePublishTarget(target, limits.MaxPathBytes)
	if err != nil || cleanTarget == "" {
		return app.ErrSpoolResidueAmbiguous
	}
	if err := paths.EnsurePrivateDir(filepath.Dir(destination)); err != nil {
		return app.ErrSpoolResidueAmbiguous
	}
	var identity app.ArtifactIdentity
	if cleanSource == "" {
		manifest := sha256.New()
		var total app.ByteSize
		var entries app.Count
		var manifestBytes app.ByteSize
		if err := walkManifest(ctx, destination, limits, manifest, &total, &entries, &manifestBytes); err != nil {
			return err
		}
		identity = app.ArtifactIdentity{SpoolID: expected.SpoolID, ManifestHash: hex.EncodeToString(manifest.Sum(nil)), Bytes: total, Entries: entries, Complete: true, VerifiedAt: expected.VerifiedAt}
	} else {
		info, err := os.Lstat(destination)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return app.ErrSpoolResidueAmbiguous
		}
		file, err := os.Open(destination)
		if err != nil {
			return err
		}
		info, statErr := file.Stat()
		if statErr == nil {
			statErr = validateRegularNoHardLink(file, info)
		}
		digest, hashErr := hashFile(ctx, file, limits)
		closeErr := file.Close()
		if statErr != nil {
			return statErr
		}
		if hashErr != nil {
			return hashErr
		}
		if closeErr != nil {
			return closeErr
		}
		manifest := sha256.New()
		var bytes app.ByteSize
		bytes, err = bytes.Add(app.ByteSize(info.Size()))
		if err != nil {
			return err
		}
		writeManifestEntry(manifest, cleanSource, 'f', bytes, digest)
		identity = app.ArtifactIdentity{SpoolID: expected.SpoolID, ManifestHash: hex.EncodeToString(manifest.Sum(nil)), Bytes: bytes, Entries: 1, Complete: true, VerifiedAt: expected.VerifiedAt}
	}
	if identity.ManifestHash != expected.ManifestHash || identity.Bytes != expected.Bytes || identity.Entries != expected.Entries {
		return app.ErrSpoolResidueAmbiguous
	}
	return nil
}
