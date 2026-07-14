package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

func TestLargeContentServiceVerifiesAndBoundsImmutableRanges(t *testing.T) {
	data := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	source := &largeContentMemorySource{data: data}
	service, err := NewLargeContentService(DefaultResourcePolicy(), source, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity := largeContentTestIdentity(data)
	opened, err := service.Open(context.Background(), OpenLargeContent{Identity: identity, ExpectedQueryRevision: 7, OperationID: domain.OperationID("open"), Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	if !opened.Metadata.Verified {
		t.Fatal("metadata should claim verification after the open result")
	}
	if len(source.calls) == 0 || source.maxCall > int(DefaultResourcePolicy().LargeContent.ReadBytes) {
		t.Fatalf("bounded verification calls = %d, max = %d", len(source.calls), source.maxCall)
	}
	result, err := service.ReadRange(context.Background(), LargeContentRangeRequest{
		OpenID: opened.ID, Identity: identity, ExpectedQueryRevision: 7, OperationID: domain.OperationID("range"), Range: ByteRange{Start: 3, End: 11},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Bytes) != string(data[3:11]) || !result.Complete {
		t.Fatalf("range = %#v", result)
	}
	if _, err := service.ReadRange(context.Background(), LargeContentRangeRequest{
		OpenID: opened.ID, Identity: identity, ExpectedQueryRevision: 8, OperationID: domain.OperationID("stale"), Range: ByteRange{Start: 0, End: 1},
	}); !errors.Is(err, ErrLargeContentStale) {
		t.Fatalf("stale range error = %v", err)
	}
	if err := service.Close(CloseLargeContent{ID: opened.ID, Identity: identity, ExpectedQueryRevision: 7}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReadRange(context.Background(), LargeContentRangeRequest{
		OpenID: opened.ID, Identity: identity, ExpectedQueryRevision: 7, OperationID: domain.OperationID("closed"), Range: ByteRange{Start: 0, End: 1},
	}); !errors.Is(err, ErrLargeContentNotFound) {
		t.Fatalf("closed range error = %v", err)
	}
}

func TestLargeContentServiceReturnsBoundedPathologicalLineSegments(t *testing.T) {
	data := append([]byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), '\n')
	data = append(data, []byte("second")...)
	source := &largeContentMemorySource{data: data}
	policy := DefaultResourcePolicy()
	policy.LargeContent.ReadBytes = 32
	policy.LargeContent.LineSegmentBytes = 16
	policy.LargeContent.LineWindowBytes = 256
	policy.LargeContent.LineWindowLines = 8
	policy.LargeContent.LineSegmentCells = 16
	service, err := NewLargeContentService(policy, source, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity := largeContentTestIdentity(data)
	opened, err := service.Open(context.Background(), OpenLargeContent{Identity: identity, ExpectedQueryRevision: 1, OperationID: domain.OperationID("open"), Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	window, err := service.ReadLines(context.Background(), LargeContentWindowRequest{
		OpenID: opened.ID, Identity: identity, ExpectedQueryRevision: 1, OperationID: domain.OperationID("lines"), Window: LineWindow{MaxLines: 2, MaxEncodedBytes: 256},
	})
	if err != nil {
		t.Fatal(err)
	}
	if window.CompleteLines != 2 || !window.Complete || len(window.Segments) < 9 {
		t.Fatalf("window = %#v", window)
	}
	if window.Segments[0].ContinuationBefore || !window.Segments[0].ContinuationAfter || !window.Segments[1].ContinuationBefore {
		t.Fatalf("continuation evidence = %#v", window.Segments[:2])
	}
	for _, segment := range window.Segments {
		if segment.Range.End-segment.Range.Start > policy.LargeContent.LineSegmentBytes || segment.TerminalCells > policy.LargeContent.LineSegmentCells {
			t.Fatalf("unbounded segment = %#v", segment)
		}
	}
}

func TestLargeContentServiceCancellationDoesNotPublishReadyWindow(t *testing.T) {
	data := []byte("a large immutable file that is still bounded")
	source := &largeContentMemorySource{data: data}
	progress := make([]ContentProgress, 0, 8)
	service, err := NewLargeContentService(DefaultResourcePolicy(), source, func(value ContentProgress) { progress = append(progress, value) })
	if err != nil {
		t.Fatal(err)
	}
	identity := largeContentTestIdentity(data)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Open(ctx, OpenLargeContent{Identity: identity, ExpectedQueryRevision: 1, OperationID: domain.OperationID("cancel"), Confirmed: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled open error = %v", err)
	}
	for _, value := range progress {
		if value.Phase == LargeContentReady {
			t.Fatal("cancelled open published ready")
		}
	}
}

type largeContentMemorySource struct {
	data    []byte
	calls   []ByteRange
	maxCall int
}

func (s *largeContentMemorySource) ReadImmutableRange(ctx context.Context, identity ContentIdentity, offset, maxBytes ByteSize) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if identity.ByteLength != ByteSize(len(s.data)) || offset > identity.ByteLength || maxBytes > identity.ByteLength-offset {
		return nil, ErrLargeContentCorrupt
	}
	if int(maxBytes) > s.maxCall {
		s.maxCall = int(maxBytes)
	}
	s.calls = append(s.calls, ByteRange{Start: offset, End: offset + maxBytes})
	return append([]byte(nil), s.data[int(offset):int(offset+maxBytes)]...), nil
}

func largeContentTestIdentity(data []byte) ContentIdentity {
	hash := sha256.Sum256(data)
	return ContentIdentity{Generation: 1, Snapshot: repository.SnapshotRef{Kind: repository.SnapshotCommit, ObjectID: repository.ObjectID("commit")}, RepoPathKey: repository.RepoPathKey("file.txt"), Side: ContentSideHead, Mode: ContentModeText, ByteLength: ByteSize(len(data)), SHA256: hex.EncodeToString(hash[:])}
}
