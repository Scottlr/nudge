package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/mattn/go-runewidth"
)

var (
	ErrInvalidContentIdentity     = errors.New("invalid content identity")
	ErrInvalidLargeContentRequest = errors.New("invalid large content request")
	ErrLargeContentConfirmation   = errors.New("large content confirmation required")
	ErrLargeContentLimit          = errors.New("large content explicit limit exceeded")
	ErrLargeContentNotFound       = errors.New("large content open not found")
	ErrLargeContentClosed         = errors.New("large content open is closed")
	ErrLargeContentStale          = errors.New("large content result is stale")
	ErrLargeContentOutOfBounds    = errors.New("large content range is out of bounds")
	ErrLargeContentOverlap        = errors.New("large content range overlaps an active request")
	ErrLargeContentUnavailable    = errors.New("immutable large content is unavailable")
	ErrLargeContentCorrupt        = errors.New("immutable large content identity mismatch")
	ErrLargeContentWindowLimit    = errors.New("large content window limit reached")
)

// ContentSide identifies the raw immutable side independently from labels.
type ContentSide string

const (
	ContentSideBase        ContentSide = "base"
	ContentSideHead        ContentSide = "head"
	ContentSideWorkingTree ContentSide = "working_tree"
)

// ContentMode identifies the lossless display fallback selected before bytes
// are read. T072 intentionally has no write-back or alternate-encoding mode.
type ContentMode string

const (
	ContentModeText      ContentMode = "text"
	ContentModePlainByte ContentMode = "plain_bytes"
)

// ContentIdentity is the complete identity of one immutable file side. Raw
// path bytes are retained in RepoPathKey and are never replaced by a label.
type ContentIdentity struct {
	Generation  repository.TargetGeneration
	Snapshot    repository.SnapshotRef
	CaptureID   domain.CaptureID
	RepoPathKey repository.RepoPathKey
	Side        ContentSide
	Mode        ContentMode
	ByteLength  ByteSize
	SHA256      string
}

// Validate checks every field that can change the meaning of a range result.
func (i ContentIdentity) Validate() error {
	if i.Generation == 0 || i.Snapshot.Validate() != nil || i.RepoPathKey == "" || i.Side == "" || i.Mode == "" || !validLargeContentHash(i.SHA256) {
		return ErrInvalidContentIdentity
	}
	if _, err := i.RepoPathKey.Path(); err != nil {
		return ErrInvalidContentIdentity
	}
	switch i.Side {
	case ContentSideBase, ContentSideHead, ContentSideWorkingTree:
	default:
		return ErrInvalidContentIdentity
	}
	switch i.Mode {
	case ContentModeText, ContentModePlainByte:
	default:
		return ErrInvalidContentIdentity
	}
	if i.Snapshot.Kind == repository.SnapshotWorkingTree && i.CaptureID == "" || i.Snapshot.Kind != repository.SnapshotWorkingTree && i.CaptureID != "" {
		return ErrInvalidContentIdentity
	}
	if i.Snapshot.Kind == repository.SnapshotWorkingTree && i.Side == ContentSideBase || i.Snapshot.Kind != repository.SnapshotWorkingTree && i.Side == ContentSideWorkingTree {
		return ErrInvalidContentIdentity
	}
	return nil
}

// Digest is a stable opaque identity for open-lease reuse and result binding.
func (i ContentIdentity) Digest() (string, error) {
	if err := i.Validate(); err != nil {
		return "", err
	}
	h := sha256.New()
	writeLargeContentPart(h, fmt.Sprint(uint64(i.Generation)))
	writeLargeContentPart(h, string(i.Snapshot.Kind))
	writeLargeContentPart(h, string(i.Snapshot.ObjectID))
	writeLargeContentPart(h, string(i.Snapshot.WorktreeID))
	writeLargeContentPart(h, i.Snapshot.Fingerprint)
	writeLargeContentPart(h, string(i.CaptureID))
	writeLargeContentPart(h, string(i.RepoPathKey))
	writeLargeContentPart(h, string(i.Side))
	writeLargeContentPart(h, string(i.Mode))
	writeLargeContentPart(h, fmt.Sprint(uint64(i.ByteLength)))
	writeLargeContentPart(h, i.SHA256)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// LargeContentOpenID identifies one application-scoped open lease.
type LargeContentOpenID string

func (id LargeContentOpenID) Validate() error {
	if len(id) != sha256.Size*2 || !validLargeContentHash(string(id)) {
		return ErrInvalidLargeContentRequest
	}
	return nil
}

// LargeContentPhase is the bounded lifecycle reported to the frontend.
type LargeContentPhase string

const (
	LargeContentVerifying LargeContentPhase = "verifying"
	LargeContentIndexing  LargeContentPhase = "indexing"
	LargeContentReady     LargeContentPhase = "ready"
	LargeContentCancelled LargeContentPhase = "cancelled"
	LargeContentFailed    LargeContentPhase = "failed"
)

// ContentProgress is replaceable progress evidence for one cancellable open
// or line-window operation. CheckedBytes never exceeds KnownTotal.
type ContentProgress struct {
	OperationID  domain.OperationID
	Generation   repository.TargetGeneration
	Phase        LargeContentPhase
	CheckedBytes ByteSize
	KnownTotal   ByteSize
	IndexBytes   ByteSize
}

func (p ContentProgress) Validate() error {
	if p.OperationID == "" || p.Generation == 0 || p.KnownTotal < p.CheckedBytes || p.Phase == "" {
		return ErrInvalidLargeContentRequest
	}
	switch p.Phase {
	case LargeContentVerifying, LargeContentIndexing, LargeContentReady, LargeContentCancelled, LargeContentFailed:
		return nil
	default:
		return ErrInvalidLargeContentRequest
	}
}

// ContentMetadata is safe to show before bytes are opened. A rejected file
// remains represented by metadata and an explicit reason.
type ContentMetadata struct {
	Identity             ContentIdentity
	Openable             bool
	Verified             bool
	PlainTextFallback    bool
	HighlightingDisabled bool
	LimitReason          string
}

func (m ContentMetadata) Validate() error {
	if err := m.Identity.Validate(); err != nil || !m.PlainTextFallback || !m.HighlightingDisabled {
		return ErrInvalidLargeContentRequest
	}
	if m.Openable && m.LimitReason != "" || !m.Openable && m.LimitReason == "" {
		return ErrInvalidLargeContentRequest
	}
	return nil
}

// OpenLargeContent is the only operation that can grant an explicit open
// lease. Confirmation is intentionally part of the typed request.
type OpenLargeContent struct {
	Identity              ContentIdentity
	ExpectedQueryRevision uint64
	OperationID           domain.OperationID
	Confirmed             bool
}

// LargeContentOpen is the immutable result of a verified open lease.
type LargeContentOpen struct {
	ID            LargeContentOpenID
	Identity      ContentIdentity
	QueryRevision uint64
	Metadata      ContentMetadata
	PlainTextOnly bool
}

func (o LargeContentOpen) Validate() error {
	if err := o.ID.Validate(); err != nil || o.Identity.Validate() != nil || o.QueryRevision == 0 || o.Metadata.Validate() != nil || o.Metadata.Identity != o.Identity || !o.PlainTextOnly {
		return ErrInvalidLargeContentRequest
	}
	return nil
}

// ByteRange is a checked half-open range in one ContentIdentity.
type ByteRange struct {
	Start ByteSize
	End   ByteSize
}

func (r ByteRange) Validate(size ByteSize) error {
	if r.Start >= r.End || r.End > size {
		return ErrLargeContentOutOfBounds
	}
	return nil
}

// LargeContentRangeRequest asks for one bounded, identity-bound byte range.
type LargeContentRangeRequest struct {
	OpenID                LargeContentOpenID
	Identity              ContentIdentity
	ExpectedQueryRevision uint64
	OperationID           domain.OperationID
	Range                 ByteRange
}

// ContentRange is complete only when all requested bytes were returned.
type ContentRange struct {
	OpenID   LargeContentOpenID
	Identity ContentIdentity
	Range    ByteRange
	Bytes    []byte
	Complete bool
}

// LineWindow asks for complete logical lines where practical. A pathological
// line may return bounded continuation segments and Complete=false.
type LineWindow struct {
	StartLine       uint64
	MaxLines        Count
	MaxEncodedBytes ByteSize
}

func (w LineWindow) Validate(policy LargeContentLimits) error {
	if w.MaxLines == 0 || w.MaxLines > policy.LineWindowLines || w.MaxEncodedBytes == 0 || w.MaxEncodedBytes > policy.LineWindowBytes {
		return ErrInvalidLargeContentRequest
	}
	return nil
}

type LargeContentWindowRequest struct {
	OpenID                LargeContentOpenID
	Identity              ContentIdentity
	ExpectedQueryRevision uint64
	OperationID           domain.OperationID
	Window                LineWindow
}

// LargeContentQueryPort is the consumer-owned read boundary used by the
// application client. Lease creation and release stay explicit operations.
type LargeContentQueryPort interface {
	ReadRange(context.Context, LargeContentRangeRequest) (ContentRange, error)
	ReadLines(context.Context, LargeContentWindowRequest) (ContentWindow, error)
}

// LargeContentRangeQuery is a bounded immutable range query.
type LargeContentRangeQuery struct {
	Request LargeContentRangeRequest
}

func (LargeContentRangeQuery) isQuery() {}

// LargeContentWindowQuery is a bounded immutable line-window query.
type LargeContentWindowQuery struct {
	Request LargeContentWindowRequest
}

func (LargeContentWindowQuery) isQuery() {}

// ContentSegment is a bounded, display-only projection of one raw line part.
// Raw offsets remain authoritative; Text is never write-back data.
type ContentSegment struct {
	Identity           ContentIdentity
	Line               uint64
	Ordinal            uint64
	Range              ByteRange
	Text               string
	TerminalCells      Count
	ContinuationBefore bool
	ContinuationAfter  bool
	InvalidEncoding    bool
	ElidedBytes        ByteSize
}

func (s ContentSegment) Validate() error {
	if s.Identity.Validate() != nil || s.Range.End > s.Identity.ByteLength || s.Range.Start > s.Range.End || !utf8.ValidString(s.Text) || s.TerminalCells == 0 && s.Text != "" {
		return ErrInvalidLargeContentRequest
	}
	if s.Range.Start == s.Range.End && s.Text != "" {
		return ErrInvalidLargeContentRequest
	}
	return nil
}

type ContentWindow struct {
	OpenID        LargeContentOpenID
	Identity      ContentIdentity
	Window        LineWindow
	Segments      []ContentSegment
	NextLine      uint64
	CompleteLines Count
	Complete      bool
}

func (w ContentWindow) Validate() error {
	if w.OpenID.Validate() != nil || w.Identity.Validate() != nil || w.Window.MaxLines == 0 || w.NextLine < w.Window.StartLine {
		return ErrInvalidLargeContentRequest
	}
	for _, segment := range w.Segments {
		if segment.Validate() != nil || segment.Identity != w.Identity {
			return ErrInvalidLargeContentRequest
		}
	}
	return nil
}

// CloseLargeContent releases the disposable open/index lease.
type CloseLargeContent struct {
	ID                    LargeContentOpenID
	Identity              ContentIdentity
	ExpectedQueryRevision uint64
}

// ImmutableContentSource is the only byte source accepted by the large
// content service. Implementations read captures or pinned objects, never live
// worktree paths, and must return exactly the requested bytes.
type ImmutableContentSource interface {
	ReadImmutableRange(context.Context, ContentIdentity, ByteSize, ByteSize) ([]byte, error)
}

// LargeContentProgressSink receives bounded replaceable progress snapshots.
type LargeContentProgressSink func(ContentProgress)

// LargeContentService owns application-scoped leases and disposable sparse
// indexes. The mutex protects only lease/index metadata; source I/O and
// progress callbacks happen outside it.
type LargeContentService struct {
	policy   ResourcePolicy
	source   ImmutableContentSource
	progress LargeContentProgressSink

	mu    sync.Mutex
	opens map[LargeContentOpenID]*largeContentLease
}

type largeContentLease struct {
	open        LargeContentOpen
	checkpoints []largeContentCheckpoint
	active      []ByteRange
	indexBytes  ByteSize
	interval    ByteSize
	closed      bool
}

type largeContentCheckpoint struct {
	Line   uint64
	Offset ByteSize
}

// NewLargeContentService creates a bounded immutable content service.
func NewLargeContentService(policy ResourcePolicy, source ImmutableContentSource, progress LargeContentProgressSink) (*LargeContentService, error) {
	if policy == (ResourcePolicy{}) {
		policy = DefaultResourcePolicy()
	}
	if err := policy.Validate(); err != nil || source == nil {
		return nil, ErrInvalidLargeContentRequest
	}
	return &LargeContentService{policy: policy, source: source, progress: progress, opens: make(map[LargeContentOpenID]*largeContentLease)}, nil
}

// Metadata returns immutable pre-open evidence, including an explicit limit
// reason for files that cannot receive a lease.
func (s *LargeContentService) Metadata(identity ContentIdentity) (ContentMetadata, error) {
	if s == nil || identity.Validate() != nil {
		return ContentMetadata{}, ErrInvalidContentIdentity
	}
	metadata := ContentMetadata{Identity: identity, Openable: identity.ByteLength <= s.policy.LargeContent.ExplicitOpenBytes, PlainTextFallback: true, HighlightingDisabled: true}
	if !metadata.Openable {
		metadata.LimitReason = "explicit_content_limit"
	}
	return metadata, nil
}

// Open verifies the full immutable byte identity using bounded reads before
// publishing a ready lease. It never allocates the full content.
func (s *LargeContentService) Open(ctx context.Context, request OpenLargeContent) (LargeContentOpen, error) {
	if s == nil || ctx == nil || request.Identity.Validate() != nil || request.ExpectedQueryRevision == 0 || request.OperationID == "" {
		return LargeContentOpen{}, ErrInvalidLargeContentRequest
	}
	if !request.Confirmed {
		return LargeContentOpen{}, ErrLargeContentConfirmation
	}
	metadata, err := s.Metadata(request.Identity)
	if err != nil {
		return LargeContentOpen{}, err
	}
	if !metadata.Openable {
		return LargeContentOpen{}, ErrLargeContentLimit
	}
	digest, err := request.Identity.Digest()
	if err != nil {
		return LargeContentOpen{}, err
	}
	openID := LargeContentOpenID(digest)
	s.mu.Lock()
	if existing, ok := s.opens[openID]; ok && !existing.closed {
		existing.open.QueryRevision = request.ExpectedQueryRevision
		result := existing.open
		indexBytes := existing.indexBytes
		s.mu.Unlock()
		s.emit(ContentProgress{OperationID: request.OperationID, Generation: request.Identity.Generation, Phase: LargeContentReady, CheckedBytes: request.Identity.ByteLength, KnownTotal: request.Identity.ByteLength, IndexBytes: indexBytes})
		return result, nil
	}
	s.mu.Unlock()

	s.emit(ContentProgress{OperationID: request.OperationID, Generation: request.Identity.Generation, Phase: LargeContentVerifying, KnownTotal: request.Identity.ByteLength})
	hash := sha256.New()
	checked := ByteSize(0)
	for checked < request.Identity.ByteLength {
		if err := ctx.Err(); err != nil {
			s.emit(ContentProgress{OperationID: request.OperationID, Generation: request.Identity.Generation, Phase: LargeContentCancelled, CheckedBytes: checked, KnownTotal: request.Identity.ByteLength})
			return LargeContentOpen{}, err
		}
		amount := request.Identity.ByteLength - checked
		if amount > s.policy.LargeContent.ReadBytes {
			amount = s.policy.LargeContent.ReadBytes
		}
		chunk, readErr := s.source.ReadImmutableRange(ctx, request.Identity, checked, amount)
		if readErr != nil {
			s.emit(ContentProgress{OperationID: request.OperationID, Generation: request.Identity.Generation, Phase: LargeContentFailed, CheckedBytes: checked, KnownTotal: request.Identity.ByteLength})
			return LargeContentOpen{}, fmt.Errorf("%w: %v", ErrLargeContentUnavailable, readErr)
		}
		if ByteSize(len(chunk)) != amount {
			return LargeContentOpen{}, ErrLargeContentCorrupt
		}
		_, _ = hash.Write(chunk)
		checked += amount
		s.emit(ContentProgress{OperationID: request.OperationID, Generation: request.Identity.Generation, Phase: LargeContentVerifying, CheckedBytes: checked, KnownTotal: request.Identity.ByteLength})
	}
	if hex.EncodeToString(hash.Sum(nil)) != request.Identity.SHA256 {
		return LargeContentOpen{}, ErrLargeContentCorrupt
	}
	metadata.Verified = true
	open := LargeContentOpen{ID: openID, Identity: request.Identity, QueryRevision: request.ExpectedQueryRevision, Metadata: metadata, PlainTextOnly: true}
	lease := &largeContentLease{open: open, interval: s.policy.LargeContent.CheckpointInterval}
	s.mu.Lock()
	s.opens[openID] = lease
	s.mu.Unlock()
	s.emit(ContentProgress{OperationID: request.OperationID, Generation: request.Identity.Generation, Phase: LargeContentReady, CheckedBytes: request.Identity.ByteLength, KnownTotal: request.Identity.ByteLength})
	return open, nil
}

// ReadRange reads one exact bounded byte interval without returning partial
// evidence as complete.
func (s *LargeContentService) ReadRange(ctx context.Context, request LargeContentRangeRequest) (ContentRange, error) {
	lease, err := s.validateRequest(request.OpenID, request.Identity, request.ExpectedQueryRevision)
	if err != nil || ctx == nil || request.OperationID == "" {
		if err != nil {
			return ContentRange{}, err
		}
		return ContentRange{}, ErrInvalidLargeContentRequest
	}
	if rangeErr := request.Range.Validate(request.Identity.ByteLength); rangeErr != nil {
		return ContentRange{}, rangeErr
	}
	if request.Range.End-request.Range.Start > s.policy.LargeContent.ReadBytes {
		return ContentRange{}, ErrLimitExceeded
	}
	if err := ctx.Err(); err != nil {
		return ContentRange{}, err
	}
	value, err := s.readImmutableRange(ctx, lease, request.OpenID, request.Identity, request.ExpectedQueryRevision, request.Range)
	if err != nil {
		return ContentRange{}, err
	}
	if err := s.validateLease(lease, request.OpenID, request.Identity, request.ExpectedQueryRevision); err != nil {
		return ContentRange{}, err
	}
	return ContentRange{OpenID: request.OpenID, Identity: request.Identity, Range: request.Range, Bytes: append([]byte(nil), value...), Complete: true}, nil
}

// ReadLines scans only bounded chunks and retains sparse line checkpoints.
func (s *LargeContentService) ReadLines(ctx context.Context, request LargeContentWindowRequest) (ContentWindow, error) {
	lease, err := s.validateRequest(request.OpenID, request.Identity, request.ExpectedQueryRevision)
	if err != nil {
		return ContentWindow{}, err
	}
	if ctx == nil || request.OperationID == "" || request.Window.Validate(s.policy.LargeContent) != nil {
		return ContentWindow{}, ErrInvalidLargeContentRequest
	}
	checkpoint := s.checkpoint(lease, request.Window.StartLine)
	line := checkpoint.Line
	offset := checkpoint.Offset
	lineStart := offset
	segmentStart := offset
	buffer := make([]byte, 0, int(s.policy.LargeContent.LineSegmentBytes))
	segments := make([]ContentSegment, 0, minInt(int(request.Window.MaxLines), 64))
	projectionBytes := ByteSize(0)
	completeLines := Count(0)
	complete := true
	for offset < request.Identity.ByteLength {
		if err := ctx.Err(); err != nil {
			s.emit(ContentProgress{OperationID: request.OperationID, Generation: request.Identity.Generation, Phase: LargeContentCancelled, CheckedBytes: offset, KnownTotal: request.Identity.ByteLength, IndexBytes: s.indexBytes(lease)})
			return ContentWindow{}, err
		}
		if err := s.validateLease(lease, request.OpenID, request.Identity, request.ExpectedQueryRevision); err != nil {
			return ContentWindow{}, err
		}
		amount := request.Identity.ByteLength - offset
		if amount > s.policy.LargeContent.ReadBytes {
			amount = s.policy.LargeContent.ReadBytes
		}
		chunk, readErr := s.readImmutableRange(ctx, lease, request.OpenID, request.Identity, request.ExpectedQueryRevision, ByteRange{Start: offset, End: offset + amount})
		if readErr != nil {
			return ContentWindow{}, readErr
		}
		for index, value := range chunk {
			if value == '\n' {
				if len(buffer) > 0 && buffer[len(buffer)-1] == '\r' {
					buffer = buffer[:len(buffer)-1]
				}
				if line >= request.Window.StartLine {
					segment, segmentErr := s.makeSegment(request.Identity, line, segmentStart, buffer, segmentStart != lineStart, false, projectionBytes, request.Window.MaxEncodedBytes)
					if segmentErr != nil {
						complete = false
						break
					}
					segments = append(segments, segment)
					projectionBytes += ByteSize(len(segment.Text))
					completeLines++
					if completeLines >= request.Window.MaxLines {
						offset += ByteSize(index + 1)
						s.addCheckpoint(lease, line+1, offset)
						return ContentWindow{OpenID: request.OpenID, Identity: request.Identity, Window: request.Window, Segments: segments, NextLine: line + 1, CompleteLines: completeLines, Complete: true}, nil
					}
				}
				line++
				offset = lineStart + ByteSize(index+1)
				lineStart = offset
				segmentStart = offset
				buffer = buffer[:0]
				s.addCheckpoint(lease, line, offset)
				continue
			}
			buffer = append(buffer, value)
			if ByteSize(len(buffer)) >= s.policy.LargeContent.LineSegmentBytes {
				prefix := safeUTF8Prefix(buffer, int(s.policy.LargeContent.LineSegmentBytes))
				if prefix == 0 {
					prefix = int(s.policy.LargeContent.LineSegmentBytes)
				}
				if line >= request.Window.StartLine {
					segment, segmentErr := s.makeSegment(request.Identity, line, segmentStart, buffer[:prefix], segmentStart != lineStart, true, projectionBytes, request.Window.MaxEncodedBytes)
					if segmentErr != nil {
						complete = false
						break
					}
					segments = append(segments, segment)
					projectionBytes += ByteSize(len(segment.Text))
				}
				segmentStart += ByteSize(prefix)
				buffer = append(buffer[:0], buffer[prefix:]...)
			}
		}
		if !complete {
			break
		}
		offset += amount
		s.emit(ContentProgress{OperationID: request.OperationID, Generation: request.Identity.Generation, Phase: LargeContentIndexing, CheckedBytes: offset, KnownTotal: request.Identity.ByteLength, IndexBytes: s.indexBytes(lease)})
	}
	if complete && (len(buffer) > 0 || segmentStart > lineStart || lineStart == request.Identity.ByteLength) {
		if line >= request.Window.StartLine && completeLines < request.Window.MaxLines {
			if len(buffer) > 0 || segmentStart == lineStart {
				segment, segmentErr := s.makeSegment(request.Identity, line, segmentStart, buffer, segmentStart != lineStart, false, projectionBytes, request.Window.MaxEncodedBytes)
				if segmentErr == nil {
					segments = append(segments, segment)
					completeLines++
				} else {
					complete = false
				}
			} else {
				completeLines++
			}
		}
	}
	if !complete {
		return ContentWindow{OpenID: request.OpenID, Identity: request.Identity, Window: request.Window, Segments: segments, NextLine: line, CompleteLines: completeLines, Complete: false}, nil
	}
	return ContentWindow{OpenID: request.OpenID, Identity: request.Identity, Window: request.Window, Segments: segments, NextLine: line + 1, CompleteLines: completeLines, Complete: true}, nil
}

// Close releases only the disposable lease and its sparse index.
func (s *LargeContentService) Close(request CloseLargeContent) error {
	if s == nil || request.ID.Validate() != nil || request.Identity.Validate() != nil || request.ExpectedQueryRevision == 0 {
		return ErrInvalidLargeContentRequest
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lease, ok := s.opens[request.ID]
	if !ok || lease.closed {
		return ErrLargeContentNotFound
	}
	if lease.open.Identity != request.Identity || lease.open.QueryRevision != request.ExpectedQueryRevision {
		return ErrLargeContentStale
	}
	lease.closed = true
	delete(s.opens, request.ID)
	return nil
}

func (s *LargeContentService) validateRequest(id LargeContentOpenID, identity ContentIdentity, revision uint64) (*largeContentLease, error) {
	if s == nil || id.Validate() != nil || identity.Validate() != nil || revision == 0 {
		return nil, ErrInvalidLargeContentRequest
	}
	s.mu.Lock()
	lease, ok := s.opens[id]
	if !ok || lease.closed {
		s.mu.Unlock()
		return nil, ErrLargeContentNotFound
	}
	if lease.open.Identity != identity || lease.open.QueryRevision != revision {
		s.mu.Unlock()
		return nil, ErrLargeContentStale
	}
	s.mu.Unlock()
	return lease, nil
}

func (s *LargeContentService) validateLease(lease *largeContentLease, id LargeContentOpenID, identity ContentIdentity, revision uint64) error {
	_, err := s.validateRequest(id, identity, revision)
	return err
}

func (s *LargeContentService) readImmutableRange(ctx context.Context, lease *largeContentLease, id LargeContentOpenID, identity ContentIdentity, revision uint64, request ByteRange) ([]byte, error) {
	if err := s.reserveRange(lease, id, identity, revision, request); err != nil {
		return nil, err
	}
	value, err := s.source.ReadImmutableRange(ctx, identity, request.Start, request.End-request.Start)
	s.releaseRange(lease, request)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLargeContentUnavailable, err)
	}
	if ByteSize(len(value)) != request.End-request.Start {
		return nil, ErrLargeContentCorrupt
	}
	return append([]byte(nil), value...), nil
}

func (s *LargeContentService) reserveRange(lease *largeContentLease, id LargeContentOpenID, identity ContentIdentity, revision uint64, request ByteRange) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.opens[id]
	if !ok || current != lease || lease.closed {
		return ErrLargeContentNotFound
	}
	if lease.open.Identity != identity || lease.open.QueryRevision != revision {
		return ErrLargeContentStale
	}
	for _, active := range lease.active {
		if request.Start < active.End && active.Start < request.End {
			return ErrLargeContentOverlap
		}
	}
	lease.active = append(lease.active, request)
	return nil
}

func (s *LargeContentService) releaseRange(lease *largeContentLease, request ByteRange) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index, active := range lease.active {
		if active == request {
			lease.active = append(lease.active[:index], lease.active[index+1:]...)
			return
		}
	}
}

func (s *LargeContentService) checkpoint(lease *largeContentLease, line uint64) largeContentCheckpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	best := largeContentCheckpoint{}
	for _, checkpoint := range lease.checkpoints {
		if checkpoint.Line <= line && checkpoint.Line >= best.Line {
			best = checkpoint
		}
	}
	return best
}

func (s *LargeContentService) addCheckpoint(lease *largeContentLease, line uint64, offset ByteSize) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if lease.closed || len(lease.checkpoints) > 0 && lease.checkpoints[len(lease.checkpoints)-1].Line == line {
		return
	}
	if len(lease.checkpoints) > 0 && offset < lease.checkpoints[len(lease.checkpoints)-1].Offset+lease.interval {
		return
	}
	const checkpointBytes = ByteSize(24)
	if lease.indexBytes+checkpointBytes > s.policy.LargeContent.IndexBytes {
		if lease.interval <= math.MaxUint64/2 {
			lease.interval *= 2
		}
		return
	}
	lease.checkpoints = append(lease.checkpoints, largeContentCheckpoint{Line: line, Offset: offset})
	lease.indexBytes += checkpointBytes
}

func (s *LargeContentService) indexBytes(lease *largeContentLease) ByteSize {
	s.mu.Lock()
	defer s.mu.Unlock()
	return lease.indexBytes
}

func (s *LargeContentService) emit(progress ContentProgress) {
	if s.progress != nil {
		s.progress(progress)
	}
}

func (s *LargeContentService) makeSegment(identity ContentIdentity, line uint64, start ByteSize, raw []byte, before, after bool, projection, limit ByteSize) (ContentSegment, error) {
	if projection+ByteSize(len(raw)) > limit {
		return ContentSegment{}, ErrLargeContentWindowLimit
	}
	text, cells, invalid, elided := renderLargeContent(raw, s.policy.LargeContent.LineSegmentCells)
	end := start + ByteSize(len(raw))
	segment := ContentSegment{Identity: identity, Line: line, Ordinal: uint64(start), Range: ByteRange{Start: start, End: end}, Text: text, TerminalCells: cells, ContinuationBefore: before, ContinuationAfter: after, InvalidEncoding: invalid, ElidedBytes: elided}
	if len(raw) == 0 {
		segment.TerminalCells = 1
	}
	return segment, nil
}

func renderLargeContent(raw []byte, cellLimit Count) (string, Count, bool, ByteSize) {
	var builder strings.Builder
	var cells Count
	var elided ByteSize
	invalid := false
	for offset := 0; offset < len(raw); {
		runeValue, size := utf8.DecodeRune(raw[offset:])
		if runeValue == utf8.RuneError && size == 1 {
			invalid = true
		}
		width := runewidth.RuneWidth(runeValue)
		if width < 1 {
			width = 1
		}
		if cells+Count(width) > cellLimit {
			elided += ByteSize(len(raw) - offset)
			break
		}
		if runeValue == 0 || unicode.IsControl(runeValue) || unicode.Is(unicode.Bidi_Control, runeValue) {
			runeValue = '\uFFFD'
		}
		builder.WriteRune(runeValue)
		cells += Count(width)
		offset += size
	}
	return builder.String(), cells, invalid, elided
}

func safeUTF8Prefix(value []byte, maximum int) int {
	if len(value) <= maximum {
		return len(value)
	}
	end := maximum
	for end > 0 && end < len(value) && value[end]&0xC0 == 0x80 {
		end--
	}
	if end == 0 {
		return maximum
	}
	return end
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func validLargeContentHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func writeLargeContentPart(hash interface{ Write([]byte) (int, error) }, value string) {
	var length [8]byte
	for index := range length {
		length[len(length)-index-1] = byte(uint64(len(value)) >> (index * 8))
	}
	_, _ = hash.Write(length[:])
	_, _ = hash.Write([]byte(value))
}
