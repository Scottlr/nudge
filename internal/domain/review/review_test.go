package review

import (
	"errors"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

func TestThreadStatusAxesAreIndependent(t *testing.T) {
	now := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	anchor := testAnchor(now)
	proposalID := domain.ProposalID("proposal-1")
	thread, err := NewOpenReviewThread(domain.ReviewThreadID("thread-1"), domain.ReviewSessionID("session-1"), anchor, now)
	if err != nil {
		t.Fatalf("new thread: %v", err)
	}
	if err := thread.SetConversation(ConversationAwaitingRuntimeApproval, FailurePhaseNone, "", now); err != nil {
		t.Fatalf("set conversation: %v", err)
	}
	if err := thread.SetProposal(ProposalReady, &proposalID, now); err != nil {
		t.Fatalf("set proposal: %v", err)
	}
	if err := thread.SetAnchorState(AnchorRelocated, now); err != nil {
		t.Fatalf("set anchor state: %v", err)
	}
	status := thread.Status()
	if status.Resolution != ResolutionOpen || status.Conversation != ConversationAwaitingRuntimeApproval || status.Proposal != ProposalReady || status.Anchor != AnchorRelocated || status.Read != Unread {
		t.Fatalf("independent status axes = %#v", status)
	}

	if err := thread.Resolve(now); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	resolved := thread.Status()
	if resolved.Resolution != ResolutionResolved || resolved.Conversation != status.Conversation || resolved.Proposal != status.Proposal || resolved.Anchor != status.Anchor || resolved.Read != status.Read {
		t.Fatalf("resolve changed independent axes: before=%#v after=%#v", status, resolved)
	}
}

func TestCodeAnchorValidation(t *testing.T) {
	now := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	anchor := testAnchor(now)
	if _, err := NewCodeAnchor(anchor); err != nil {
		t.Fatalf("valid anchor: %v", err)
	}

	invalid := anchor
	invalid.TargetGeneration = 0
	if _, err := NewCodeAnchor(invalid); !errors.Is(err, ErrInvalidCodeAnchor) {
		t.Fatalf("zero generation error = %v, want ErrInvalidCodeAnchor", err)
	}

	invalid = anchor
	invalid.StartLine = 4
	invalid.EndLine = 3
	if _, err := NewCodeAnchor(invalid); !errors.Is(err, ErrInvalidCodeAnchor) {
		t.Fatalf("reversed range error = %v, want ErrInvalidCodeAnchor", err)
	}

	invalid = anchor
	invalid.Head = repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, ObjectID: repository.ObjectID("not-allowed"), WorktreeID: domain.WorktreeID("worktree-1"), Fingerprint: "head-fingerprint"}
	if _, err := NewCodeAnchor(invalid); !errors.Is(err, ErrInvalidCodeAnchor) {
		t.Fatalf("working-tree object ID error = %v, want ErrInvalidCodeAnchor", err)
	}
}

func TestResolveDoesNotChangeProposal(t *testing.T) {
	now := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	thread, err := NewOpenReviewThread(domain.ReviewThreadID("thread-1"), domain.ReviewSessionID("session-1"), testAnchor(now), now)
	if err != nil {
		t.Fatalf("new thread: %v", err)
	}
	proposalID := domain.ProposalID("proposal-1")
	if err := thread.SetProposal(ProposalReady, &proposalID, now); err != nil {
		t.Fatalf("set proposal: %v", err)
	}
	if err := thread.Resolve(now); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if thread.Proposal != ProposalReady || thread.LatestProposalID == nil || *thread.LatestProposalID != proposalID {
		t.Fatalf("proposal changed on resolve: %#v", thread)
	}
	if err := thread.Reopen(now); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if thread.Proposal != ProposalReady || thread.LatestProposalID == nil || *thread.LatestProposalID != proposalID {
		t.Fatalf("proposal changed on reopen: %#v", thread)
	}
}

func TestMessageLifecycle(t *testing.T) {
	now := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	m, err := NewPendingMessage(domain.MessageID("message-1"), domain.ReviewThreadID("thread-1"), RoleAssistant, 1, now)
	if err != nil {
		t.Fatalf("new pending message: %v", err)
	}
	if err := m.BeginStreaming(now); err != nil {
		t.Fatalf("begin streaming: %v", err)
	}
	if err := m.AppendContent("first\n", now); err != nil {
		t.Fatalf("append first delta: %v", err)
	}
	if err := m.AppendContent("second", now); err != nil {
		t.Fatalf("append second delta: %v", err)
	}
	if err := m.Complete(now); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if m.Status != MessageCompleted || m.Content != "first\nsecond" || m.CompletedAt == nil {
		t.Fatalf("completed message = %#v", m)
	}
	if err := m.AppendContent("late", now); !errors.Is(err, ErrInvalidStatusTransition) {
		t.Fatalf("late append error = %v, want ErrInvalidStatusTransition", err)
	}

	failed, err := NewPendingMessage(domain.MessageID("message-2"), domain.ReviewThreadID("thread-1"), RoleAssistant, 2, now)
	if err != nil {
		t.Fatalf("new second message: %v", err)
	}
	if err := failed.Fail(FailurePhaseProvider, ErrorCode("provider_disconnected"), now); err != nil {
		t.Fatalf("fail message: %v", err)
	}
	if failed.Status != MessageFailed || failed.FailurePhase != FailurePhaseProvider || failed.ErrorCode != ErrorCode("provider_disconnected") || failed.CompletedAt == nil {
		t.Fatalf("failed message = %#v", failed)
	}
}

func testAnchor(now time.Time) CodeAnchor {
	return CodeAnchor{
		Path:              repository.RepoPath([]byte("internal/example.go")),
		Side:              repository.DiffHead,
		StartLine:         3,
		EndLine:           4,
		TargetGeneration:  1,
		Base:              repository.SnapshotRef{Kind: repository.SnapshotEmpty},
		Head:              repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: domain.WorktreeID("worktree-1"), Fingerprint: "head-fingerprint"},
		HunkFingerprint:   "hunk-fingerprint",
		SelectionHash:     "selection-hash",
		SelectedText:      "return value",
		BeforeContextHash: "before-context",
		AfterContextHash:  "after-context",
		State:             AnchorValid,
		CreatedAt:         now,
	}
}
