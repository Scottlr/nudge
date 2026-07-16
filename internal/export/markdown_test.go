package export

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

func TestWriteMarkdownStreamsAndSanitizesThreadBody(t *testing.T) {
	source, threadID := newExportThreadSource(t, "line one\n# heading\n\x1b[31mred")
	selection, err := app.SelectThread(context.Background(), source, threadID)
	if err != nil {
		t.Fatalf("select thread: %v", err)
	}
	var output bytes.Buffer
	if err := WriteMarkdown(context.Background(), selection, source, nil, &output); err != nil {
		t.Fatalf("write markdown: %v", err)
	}
	value := output.String()
	if !strings.Contains(value, "> line one\n> # heading") {
		t.Fatalf("body was not quoted: %q", value)
	}
	if strings.ContainsRune(value, '\x1b') || !strings.Contains(value, "�[31mred") {
		t.Fatalf("control sequence was not sanitized: %q", value)
	}
}

func TestSelectThreadRejectsActiveMessage(t *testing.T) {
	source, threadID := newExportThreadSource(t, "active")
	source.messages[0].Status = review.MessageStreaming
	if _, err := app.SelectThread(context.Background(), source, threadID); !errors.Is(err, app.ErrExportActiveMessage) {
		t.Fatalf("error = %v, want active-message refusal", err)
	}
}

type exportSource struct {
	thread   review.ReviewThread
	anchors  []app.AnchorVersionRecord
	messages []app.MessageSummary
	bodies   map[domain.MessageID][]byte
}

func newExportThreadSource(t *testing.T, body string) (*exportSource, domain.ReviewThreadID) {
	t.Helper()
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	anchor := review.CodeAnchor{
		Path:             repository.RepoPath("main.go"),
		Side:             repository.DiffBase,
		StartLine:        1,
		EndLine:          1,
		TargetGeneration: 1,
		Base:             repository.SnapshotRef{Kind: repository.SnapshotEmpty},
		Head:             repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: "worktree-1", Fingerprint: "fingerprint-1"},
		HunkFingerprint:  "hunk-1",
		SelectionHash:    "selection-1",
		State:            review.AnchorValid,
		CreatedAt:        now,
	}
	thread, err := review.NewOpenReviewThread("thread-1", "session-1", anchor, now)
	if err != nil {
		t.Fatalf("thread: %v", err)
	}
	if err := thread.AppendMessageID("message-1", now); err != nil {
		t.Fatalf("message identity: %v", err)
	}
	data := []byte(body)
	digest := sha256.Sum256(data)
	completed := now.Add(time.Minute)
	source := &exportSource{
		thread:   thread,
		anchors:  []app.AnchorVersionRecord{{ThreadID: thread.ID, Version: 1, Anchor: anchor, Method: review.AnchorVersionMethodInitial, CreatedAt: now}},
		messages: []app.MessageSummary{{ID: "message-1", ThreadID: thread.ID, Role: review.RoleUser, Status: review.MessageCompleted, Ordinal: 1, ByteLength: uint64(len(data)), SHA256: hex.EncodeToString(digest[:]), CreatedAt: now, UpdatedAt: completed, CompletedAt: &completed}},
		bodies:   map[domain.MessageID][]byte{"message-1": data},
	}
	return source, thread.ID
}

func (s *exportSource) LoadThread(context.Context, domain.ReviewThreadID) (review.ReviewThread, error) {
	return s.thread, nil
}

func (s *exportSource) ListAnchorVersions(context.Context, domain.ReviewThreadID) ([]app.AnchorVersionRecord, error) {
	return s.anchors, nil
}

func (s *exportSource) ListMessages(context.Context, domain.ReviewThreadID, app.MessagePage) (app.MessagePageResult, error) {
	return app.MessagePageResult{Items: s.messages, Revision: 1}, nil
}

func (s *exportSource) ReadMessageBody(_ context.Context, request app.BodyRange) (app.MessageBodyChunk, error) {
	data := s.bodies[request.MessageID]
	if request.Offset+request.Length > uint64(len(data)) {
		return app.MessageBodyChunk{}, app.ErrExportSource
	}
	chunk := append([]byte(nil), data[request.Offset:request.Offset+request.Length]...)
	return app.MessageBodyChunk{MessageID: request.MessageID, Offset: request.Offset, Bytes: chunk, TotalLength: uint64(len(data)), SHA256: request.ExpectedSHA256, Complete: request.Offset+request.Length == uint64(len(data))}, nil
}

func (*exportSource) LoadProposalAggregate(context.Context, domain.ProposalID) (review.ProposalAggregate, error) {
	return review.ProposalAggregate{}, app.ErrExportNotFound
}

func (*exportSource) LoadProposalPatchArtifactForAttempt(context.Context, domain.OperationID) (app.ProposalPatchArtifact, error) {
	return app.ProposalPatchArtifact{}, app.ErrExportNotFound
}
