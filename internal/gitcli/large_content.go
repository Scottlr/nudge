package gitcli

import (
	"context"
	"errors"
	"io"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

// ReadImmutableRange reads only from an accepted capture artifact or a pinned
// Git object. It deliberately does not expose a filesystem path to callers.
func (l *GitContentLoader) ReadImmutableRange(ctx context.Context, identity app.ContentIdentity, offset, maxBytes app.ByteSize) ([]byte, error) {
	if l == nil || ctx == nil || identity.Validate() != nil || maxBytes == 0 || maxBytes > 256*app.KiB || offset > identity.ByteLength || maxBytes > identity.ByteLength-offset {
		return nil, ErrInvalidContentLoader
	}
	if identity.Snapshot.Kind == repository.SnapshotWorkingTree {
		return l.readCapturedRange(ctx, identity, offset, maxBytes)
	}
	if identity.Snapshot.Kind == repository.SnapshotEmpty {
		if identity.ByteLength != 0 || offset != 0 {
			return nil, ErrContentNotFound
		}
		return []byte{}, nil
	}
	if identity.Snapshot.Kind != repository.SnapshotCommit && identity.Snapshot.Kind != repository.SnapshotTree || identity.Snapshot.ObjectID == "" {
		return nil, ErrContentNotFound
	}
	writer := &immutableRangeWriter{skip: offset, want: maxBytes}
	argument := string(identity.Snapshot.ObjectID) + ":" + string(identity.RepoPathKey)
	if _, err := l.builder.RunStream(ctx, writer, "cat-file", "blob", argument); err != nil {
		var gitErr *GitError
		if errors.As(err, &gitErr) && gitErr.Code == ErrorCommandFailed {
			return nil, ErrContentNotFound
		}
		return nil, err
	}
	if app.ByteSize(len(writer.data)) != maxBytes {
		return nil, ErrContentCorrupt
	}
	return append([]byte(nil), writer.data...), nil
}

func (l *GitContentLoader) readCapturedRange(ctx context.Context, identity app.ContentIdentity, offset, maxBytes app.ByteSize) ([]byte, error) {
	if l.manifests == nil || l.artifacts == nil || identity.CaptureID == "" {
		return nil, ErrInvalidContentLoader
	}
	manifest, err := l.captureManifest(ctx, identity.CaptureID)
	if err != nil {
		return nil, err
	}
	for _, entry := range manifest.Candidate.Entries {
		if entry.Change.NewPath == nil || repository.RepoPathKey(entry.Change.NewPath.Key()) != identity.RepoPathKey {
			continue
		}
		for _, blob := range entry.Blobs {
			if !captureSideMatches(identity.Side, blob.Side) || blob.Path.Key() != identity.RepoPathKey {
				continue
			}
			if app.ByteSize(blob.Artifact.Bytes) != identity.ByteLength || blob.Artifact.ContentSHA256 != identity.SHA256 {
				return nil, ErrContentCorrupt
			}
			value, readErr := l.artifacts.ReadPublishedRange(ctx, manifest.Blobs.Target, blob.Artifact.RelativePath, app.StreamIdentity{Bytes: app.ByteSize(blob.Artifact.Bytes), SHA256: blob.Artifact.ContentSHA256}, offset, maxBytes)
			if readErr != nil {
				return nil, readErr
			}
			if app.ByteSize(len(value)) != maxBytes {
				return nil, ErrContentCorrupt
			}
			return append([]byte(nil), value...), nil
		}
		return nil, ErrContentNotFound
	}
	return nil, ErrContentNotFound
}

func captureSideMatches(side app.ContentSide, candidate repository.CaptureBlobSide) bool {
	switch side {
	case app.ContentSideBase:
		return candidate == repository.CaptureBlobBase
	case app.ContentSideHead, app.ContentSideWorkingTree:
		return candidate == repository.CaptureBlobWorkingTree
	default:
		return false
	}
}

type immutableRangeWriter struct {
	skip app.ByteSize
	want app.ByteSize
	data []byte
	seen app.ByteSize
}

func (w *immutableRangeWriter) Write(value []byte) (int, error) {
	if w == nil {
		return 0, io.ErrClosedPipe
	}
	start := app.ByteSize(0)
	if w.seen < w.skip {
		remaining := w.skip - w.seen
		if remaining >= app.ByteSize(len(value)) {
			w.seen += app.ByteSize(len(value))
			return len(value), nil
		}
		start = remaining
		w.seen = w.skip
	}
	if w.want > app.ByteSize(len(w.data)) && start < app.ByteSize(len(value)) {
		remaining := w.want - app.ByteSize(len(w.data))
		available := app.ByteSize(len(value)) - start
		if available > remaining {
			available = remaining
		}
		w.data = append(w.data, value[int(start):int(start+available)]...)
	}
	w.seen += app.ByteSize(len(value)) - start
	return len(value), nil
}

var _ app.ImmutableContentSource = (*GitContentLoader)(nil)
