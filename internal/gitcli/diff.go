package gitcli

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/diff"
)

// PublishedPatchSource exposes one accepted patch through the bounded
// artifact-reader capability. It never exposes the protected absolute path.
type PublishedPatchSource struct {
	ctx      context.Context
	reader   app.PublishedArtifactReader
	target   app.PublishTarget
	expected app.StreamIdentity
	id       string
	size     int64
}

// NewPublishedPatchSource constructs an identity-bound source for an adopted
// capture manifest.
func NewPublishedPatchSource(ctx context.Context, manifest app.CaptureManifest, reader app.PublishedArtifactReader) (*PublishedPatchSource, error) {
	if ctx == nil || reader == nil || manifest.Validate() != nil || manifest.Patch.Identity.Bytes == 0 || manifest.Candidate.Patch.ContentSHA256 == "" {
		return nil, app.ErrCaptureCorrupt
	}
	return newPublishedPatchSource(ctx, reader, manifest.Patch.Target, app.StreamIdentity{Bytes: app.ByteSize(manifest.Candidate.Patch.Bytes), SHA256: manifest.Candidate.Patch.ContentSHA256}, string(manifest.CaptureID)+":"+manifest.Patch.Identity.ManifestHash)
}

// NewPublishedProposalPatchSource binds a published T111 patch to the
// identity-checked artifact reader without exposing its protected path.
func NewPublishedProposalPatchSource(ctx context.Context, published app.PublishedArtifact, patch app.StreamIdentity, reader app.PublishedArtifactReader) (*PublishedPatchSource, error) {
	if ctx == nil || reader == nil || published.Identity.Validate() != nil || published.Identity.Bytes == 0 || published.Target.OwnerKind != app.OwnerProposal || patch.Bytes != published.Identity.Bytes || !validPatchSHA256(patch.SHA256) {
		return nil, app.ErrInvalidProposalPatchArtifact
	}
	return newPublishedPatchSource(ctx, reader, published.Target, patch, published.Identity.SpoolID+":"+patch.SHA256)
}

func validPatchSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func newPublishedPatchSource(ctx context.Context, reader app.PublishedArtifactReader, target app.PublishTarget, expected app.StreamIdentity, id string) (*PublishedPatchSource, error) {
	if ctx == nil || reader == nil || expected.Bytes == 0 || expected.SHA256 == "" || id == "" {
		return nil, app.ErrInvalidArtifactSpool
	}
	return &PublishedPatchSource{ctx: ctx, reader: reader, target: target, expected: expected, id: id, size: int64(expected.Bytes)}, nil
}

// ID returns the stable capture-bound source identity.
func (s *PublishedPatchSource) ID() string {
	if s == nil {
		return ""
	}
	return s.id
}

// Size returns the exact accepted patch byte count.
func (s *PublishedPatchSource) Size() int64 {
	if s == nil {
		return -1
	}
	return s.size
}

// Open returns a bounded section reader over the accepted patch.
func (s *PublishedPatchSource) Open() (io.ReadCloser, error) {
	if s == nil || s.reader == nil {
		return nil, app.ErrCaptureCorrupt
	}
	return io.NopCloser(io.NewSectionReader(s, 0, s.size)), nil
}

// ReadAt reads one bounded range and preserves io.ReaderAt semantics.
func (s *PublishedPatchSource) ReadAt(buffer []byte, offset int64) (int, error) {
	if s == nil || s.reader == nil || offset < 0 {
		return 0, app.ErrCaptureCorrupt
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	if offset >= s.size {
		return 0, io.EOF
	}
	short := int64(len(buffer)) > s.size-offset
	if short {
		buffer = buffer[:s.size-offset]
	}
	read := 0
	const maxArtifactRange = app.ByteSize(256 * app.KiB)
	for read < len(buffer) {
		remaining := len(buffer) - read
		request := remaining
		if app.ByteSize(request) > maxArtifactRange {
			request = int(maxArtifactRange)
		}
		value, err := s.reader.ReadPublishedRange(s.ctx, s.target, "", s.expected, app.ByteSize(offset+int64(read)), app.ByteSize(request))
		if err != nil {
			return read, fmt.Errorf("read accepted patch: %w", err)
		}
		if len(value) == 0 || len(value) > request {
			return read, app.ErrCaptureCorrupt
		}
		copy(buffer[read:], value)
		read += len(value)
		if len(value) != request {
			return read, io.EOF
		}
	}
	if int64(read) < s.size-offset {
		return read, io.EOF
	}
	if short {
		return read, io.EOF
	}
	return read, nil
}

var _ diff.PatchSource = (*PublishedPatchSource)(nil)
