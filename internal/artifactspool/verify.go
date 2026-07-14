package artifactspool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/paths"
)

// CloseAndVerify closes the lifecycle phase only after every writer is closed
// and an independent no-follow reopen produces a complete manifest identity.
func (h *Handle) CloseAndVerify(ctx context.Context) (app.ArtifactIdentity, error) {
	if h == nil || ctx == nil {
		return app.ArtifactIdentity{}, app.ErrInvalidArtifactSpool
	}
	h.operationMu.Lock()
	defer h.operationMu.Unlock()
	lock, err := h.manager.acquireOwnerLock(ctx, h.descriptor.OwnerKind, string(h.descriptor.OperationID))
	if err != nil {
		return app.ArtifactIdentity{}, err
	}
	defer lock.Close()
	return h.closeAndVerifyLocked(ctx)
}

func (h *Handle) closeAndVerifyLocked(ctx context.Context) (app.ArtifactIdentity, error) {
	if err := contextErr(ctx); err != nil {
		return app.ArtifactIdentity{}, err
	}
	if err := h.requireNoActiveHandles(); err != nil {
		return app.ArtifactIdentity{}, err
	}
	h.stateMu.Lock()
	state := h.descriptor.State
	h.stateMu.Unlock()
	if state != app.SpoolOpen {
		return app.ArtifactIdentity{}, app.ErrSpoolNotReady
	}
	if err := h.refreshMarker(); err != nil {
		return app.ArtifactIdentity{}, err
	}
	if err := h.updateState(app.SpoolVerifying); err != nil {
		return app.ArtifactIdentity{}, err
	}
	identity, err := h.buildIdentity(ctx, h.payloadPath)
	if err != nil {
		_ = h.updateState(app.SpoolRecoveryRequired)
		return app.ArtifactIdentity{}, err
	}
	h.stateMu.Lock()
	h.verified = identity
	h.stateMu.Unlock()
	h.marker.ManifestHash = identity.ManifestHash
	h.marker.ManifestBytes = uint64(identity.Bytes)
	h.marker.ManifestEntries = uint64(identity.Entries)
	h.marker.VerifiedUnixNano = identity.VerifiedAt.UnixNano()
	if err := h.updateState(app.SpoolVerified); err != nil {
		_ = h.updateState(app.SpoolRecoveryRequired)
		return app.ArtifactIdentity{}, err
	}
	return identity, nil
}

func (h *Handle) buildIdentity(ctx context.Context, root string) (app.ArtifactIdentity, error) {
	h.stateMu.Lock()
	limits := h.descriptor.Limits
	spoolID := h.descriptor.SpoolID
	reserved := h.reserved.Load()
	h.stateMu.Unlock()
	manifest := sha256.New()
	var total app.ByteSize
	var entries app.Count
	var manifestBytes app.ByteSize
	if err := walkManifest(ctx, root, limits, manifest, &total, &entries, &manifestBytes); err != nil {
		return app.ArtifactIdentity{}, err
	}
	if uint64(total) != reserved {
		return app.ArtifactIdentity{}, app.ErrSpoolResidueAmbiguous
	}
	return app.ArtifactIdentity{
		SpoolID:      spoolID,
		ManifestHash: hex.EncodeToString(manifest.Sum(nil)),
		Bytes:        total,
		Entries:      entries,
		Complete:     true,
		VerifiedAt:   time.Now().UTC(),
	}, nil
}

func (h *Handle) updateState(state app.SpoolState) error {
	h.marker.State = string(state)
	if err := writeMarker(h.spoolPath, h.marker); err != nil {
		return err
	}
	h.setDescriptorState(state)
	return nil
}

func walkManifest(ctx context.Context, root string, limits app.SpoolLimits, manifest hash.Hash, total *app.ByteSize, entries *app.Count, manifestBytes *app.ByteSize) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := ensureDirectoryRoot(root); err != nil {
		return err
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return app.ErrSpoolResidueAmbiguous
		}
		if err := contextErr(ctx); err != nil {
			return err
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return app.ErrSpoolResidueAmbiguous
		}
		if _, err := validateRelative(relative, limits.MaxPathBytes); err != nil {
			return err
		}
		var kind byte
		var size app.ByteSize
		var digest [32]byte
		if entry.IsDir() {
			if err := ensureDirectoryRoot(path); err != nil {
				return err
			}
			kind = 'd'
		} else {
			file, err := openVerifiedFile(root, relative)
			if err != nil {
				return err
			}
			info, statErr := file.Stat()
			if statErr == nil {
				statErr = validateRegularNoHardLink(file, info)
			}
			if statErr == nil && info.Size() < 0 {
				statErr = app.ErrSpoolResidueAmbiguous
			}
			if statErr == nil {
				size = app.ByteSize(info.Size())
				digest, statErr = hashFile(ctx, file, limits)
			}
			closeErr := file.Close()
			if statErr != nil {
				return statErr
			}
			if closeErr != nil {
				return closeErr
			}
			kind = 'f'
			var addErr error
			*total, addErr = total.Add(size)
			if addErr != nil || *total > limits.MaxBytes {
				return app.ErrSpoolLimitExceeded
			}
		}
		var addErr error
		*entries, addErr = entries.Add(1)
		if addErr != nil || *entries > limits.MaxEntries {
			return app.ErrSpoolLimitExceeded
		}
		entryBytes, err := manifestEntryBytes(relative, size, kind)
		if err != nil {
			return err
		}
		*manifestBytes, addErr = manifestBytes.Add(entryBytes)
		if addErr != nil || *manifestBytes > limits.MaxManifestBytes {
			return app.ErrSpoolLimitExceeded
		}
		writeManifestEntry(manifest, relative, kind, size, digest)
		return nil
	})
}

func scanPayloadBytes(root string, limits app.SpoolLimits) (app.ByteSize, error) {
	var total app.ByteSize
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return app.ErrSpoolResidueAmbiguous
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return app.ErrSpoolResidueAmbiguous
		}
		if _, err := validateRelative(relative, limits.MaxPathBytes); err != nil {
			return err
		}
		if entry.IsDir() {
			return ensureDirectoryRoot(path)
		}
		file, err := openVerifiedFile(root, relative)
		if err != nil {
			return err
		}
		info, statErr := file.Stat()
		if statErr == nil {
			statErr = validateRegularNoHardLink(file, info)
		}
		if statErr == nil && info.Size() >= 0 {
			total, statErr = total.Add(app.ByteSize(info.Size()))
		}
		if statErr == nil && total > limits.MaxBytes {
			statErr = app.ErrSpoolLimitExceeded
		}
		closeErr := file.Close()
		if statErr != nil {
			return statErr
		}
		return closeErr
	})
	return total, err
}

func ensureDirectoryRoot(path string) error {
	return paths.EnsurePrivateDir(path)
}

func openVerifiedFile(root, relative string) (*os.File, error) {
	file, err := paths.OpenExistingProtectedFile(root, relative)
	if err != nil {
		return nil, app.ErrSpoolResidueAmbiguous
	}
	return file, nil
}

func hashFile(ctx context.Context, file *os.File, limits app.SpoolLimits) ([32]byte, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return [32]byte{}, err
	}
	bufferSize := limits.BufferBytes
	if limits.CheckEveryBytes < bufferSize {
		bufferSize = limits.CheckEveryBytes
	}
	if bufferSize > 1*app.MiB {
		bufferSize = 1 * app.MiB
	}
	buffer := make([]byte, int(bufferSize))
	hash := sha256.New()
	zeroReads := 0
	for {
		if err := contextErr(ctx); err != nil {
			return [32]byte{}, err
		}
		read, err := file.Read(buffer)
		if read > 0 {
			zeroReads = 0
			if _, writeErr := hash.Write(buffer[:read]); writeErr != nil {
				return [32]byte{}, writeErr
			}
		} else if err == nil {
			zeroReads++
			if zeroReads >= 100 {
				return [32]byte{}, io.ErrNoProgress
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return [32]byte{}, err
		}
	}
	var result [32]byte
	copy(result[:], hash.Sum(nil))
	return result, nil
}

func manifestEntryBytes(relative string, size app.ByteSize, kind byte) (app.ByteSize, error) {
	value := app.ByteSize(len(relative) + 1 + 8)
	if kind == 'f' {
		value, _ = value.Add(32)
	}
	if size > 0 && kind != 'f' {
		return 0, fmt.Errorf("invalid directory manifest size")
	}
	return value, nil
}

func writeManifestEntry(manifest hash.Hash, relative string, kind byte, size app.ByteSize, digest [32]byte) {
	var length [8]byte
	for index := range length {
		length[len(length)-index-1] = byte(uint64(len(relative)) >> (index * 8))
	}
	_, _ = manifest.Write(length[:])
	_, _ = manifest.Write([]byte(relative))
	_, _ = manifest.Write([]byte{kind})
	for index := range length {
		length[len(length)-index-1] = byte(uint64(size) >> (index * 8))
	}
	_, _ = manifest.Write(length[:])
	if kind == 'f' {
		_, _ = manifest.Write(digest[:])
	}
}
