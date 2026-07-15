package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

func TestRejectProposalRecordsExactVersionBeforeReset(t *testing.T) {
	fixture := newProposalTurnFixture(t)
	now := time.Unix(40, 0).UTC()
	version := readyProposalForDisposition(fixture, now)
	fixture.store.aggregate.Proposal.Status = review.ProposalVersionReady
	fixture.store.aggregate.Proposal.CurrentVersion = proposalVersionPointer(version.Version)
	fixture.store.aggregate.Versions = []review.ProposedPatch{version}
	thread := fixture.store.thread
	if err := thread.SetProposal(review.ProposalReady, proposalIDPointer(version.ProposalID), now); err != nil {
		t.Fatal(err)
	}
	fixture.store.thread = thread
	reset := &proposalDispositionResetFake{}
	service, err := NewProposalDispositionService(ProposalDispositionServiceConfig{Store: fixture.store, Proposals: fixture.store, Lifecycle: fixture.store, Resetter: reset, Clock: fixedClock{when: now.Add(time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}
	commit, err := service.RejectProposal(context.Background(), RejectProposal{Guard: threadTestGuard(), ThreadID: version.ThreadID, ProposalID: version.ProposalID, Version: version.Version, Reason: "not wanted", OperationID: domain.OperationID("reject-op"), CorrelationID: CorrelationID("reject-correlation")})
	if err != nil {
		t.Fatal(err)
	}
	if reset.calls != 1 || commit.Proposal.Status != review.ProposalVersionRejected || commit.Thread.Proposal != review.ProposalRejected || commit.Workspace.State != review.WorkspaceReady {
		t.Fatalf("rejection commit = %#v reset calls=%d", commit, reset.calls)
	}
	if len(commit.Events) != 1 {
		t.Fatalf("rejection events = %#v", commit.Events)
	}
	event, ok := commit.Events[0].(ProposalRejected)
	if !ok || event.Version != version.Version || event.Reason != "not wanted" {
		t.Fatalf("rejection event = %#v", commit.Events[0])
	}
}

func TestDiscardProposalResultResetsAndRetainsFailedAttempt(t *testing.T) {
	fixture := newProposalTurnFixture(t)
	now := time.Unix(20, 0).UTC()
	finished := now.Add(time.Minute)
	attempt := review.ProposalAttempt{
		ID:                domain.OperationID("attempt-failed"),
		ProposalID:        fixture.store.aggregate.Proposal.ID,
		WorkspaceID:       fixture.store.aggregate.Workspace.ID,
		ThreadID:          fixture.store.aggregate.Proposal.ThreadID,
		SourceGeneration:  fixture.store.aggregate.Intent.ConfirmedAgainst,
		Outcome:           review.ProposalAttemptFailed,
		ResultDisposition: review.ProposalResultPresent,
		FailurePhase:      review.ProposalFailureDerivation,
		Reason:            "unsupported result entry",
		StartedAt:         now,
		FinishedAt:        &finished,
	}
	fixture.store.aggregate.Proposal.Status = review.ProposalVersionFailed
	fixture.store.aggregate.Attempts = []review.ProposalAttempt{attempt}
	if err := fixture.store.aggregate.Validate(); err != nil {
		t.Fatal(err)
	}
	reset := &proposalDispositionResetFake{}
	service, err := NewProposalDispositionService(ProposalDispositionServiceConfig{Store: fixture.store, Proposals: fixture.store, Lifecycle: fixture.store, Resetter: reset, Clock: fixedClock{when: now.Add(2 * time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}
	commit, err := service.DiscardProposalResult(context.Background(), DiscardProposalResult{Guard: threadTestGuard(), ProposalID: attempt.ProposalID, AttemptID: attempt.ID, Reason: "discard this failed result", OperationID: domain.OperationID("discard-op"), CorrelationID: CorrelationID("discard-correlation")})
	if err != nil {
		t.Fatal(err)
	}
	if reset.calls != 1 || reset.request.BaselineManifest.Hash != fixture.store.lifecycle.Baseline.Hash {
		t.Fatalf("reset evidence = calls %d request %#v", reset.calls, reset.request)
	}
	if commit.Attempt == nil || commit.Attempt.ResultDisposition != review.ProposalResultDiscarded || commit.Attempt.ResultDispositionReason != "discard this failed result" {
		t.Fatalf("discarded attempt = %#v", commit.Attempt)
	}
	if commit.Workspace.State != review.WorkspaceReady || len(commit.Events) != 1 {
		t.Fatalf("discard commit = %#v", commit)
	}
	if _, ok := commit.Events[0].(ProposalResultDiscarded); !ok {
		t.Fatalf("event type = %T", commit.Events[0])
	}
}

func TestDiscardProposalResultRecoveryRetriesInterruptedReset(t *testing.T) {
	fixture := newProposalTurnFixture(t)
	now := time.Unix(30, 0).UTC()
	finished := now.Add(time.Minute)
	attempt := review.ProposalAttempt{ID: domain.OperationID("attempt-interrupted"), ProposalID: fixture.store.aggregate.Proposal.ID, WorkspaceID: fixture.store.aggregate.Workspace.ID, ThreadID: fixture.store.aggregate.Proposal.ThreadID, SourceGeneration: fixture.store.aggregate.Intent.ConfirmedAgainst, Outcome: review.ProposalAttemptFailed, ResultDisposition: review.ProposalResultPresent, FailurePhase: review.ProposalFailureProvider, Reason: "provider cancelled", StartedAt: now, FinishedAt: &finished}
	fixture.store.aggregate.Proposal.Status = review.ProposalVersionFailed
	fixture.store.aggregate.Attempts = []review.ProposalAttempt{attempt}
	reset := &proposalDispositionResetFake{err: errors.New("reset interrupted")}
	service, err := NewProposalDispositionService(ProposalDispositionServiceConfig{Store: fixture.store, Proposals: fixture.store, Lifecycle: fixture.store, Resetter: reset, Clock: fixedClock{when: now.Add(2 * time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := service.DiscardProposalResult(context.Background(), DiscardProposalResult{Guard: threadTestGuard(), ProposalID: attempt.ProposalID, AttemptID: attempt.ID, OperationID: domain.OperationID("discard-op"), CorrelationID: CorrelationID("discard-correlation")})
	if err == nil || failed.Workspace.State != review.WorkspaceRepairRequired {
		t.Fatalf("interrupted reset = commit %#v err=%v", failed, err)
	}
	if failed.Attempt == nil || failed.Attempt.ResultDisposition != review.ProposalResultDiscarding {
		t.Fatalf("pending discard = %#v", failed.Attempt)
	}
	reset.err = nil
	recovered, err := service.DiscardProposalResult(context.Background(), DiscardProposalResult{Guard: failed.Guard, ProposalID: attempt.ProposalID, AttemptID: attempt.ID, OperationID: domain.OperationID("discard-op"), CorrelationID: CorrelationID("discard-correlation")})
	if err != nil {
		t.Fatal(err)
	}
	if reset.calls != 2 || recovered.Workspace.State != review.WorkspaceReady || recovered.Attempt == nil || recovered.Attempt.ResultDisposition != review.ProposalResultDiscarded {
		t.Fatalf("recovered discard = calls %d commit %#v", reset.calls, recovered)
	}
}

type proposalDispositionResetFake struct {
	calls   int
	request ProposalBaselineResetRequest
	err     error
}

func readyProposalForDisposition(fixture *proposalTurnFixture, now time.Time) review.ProposedPatch {
	path := repository.RepoPath("main.go")
	patchBytes := []byte("diff --git a/main.go b/main.go\n")
	digest := sha256.Sum256(patchBytes)
	return review.ProposedPatch{
		ProposalID: fixture.store.aggregate.Proposal.ID, WorkspaceID: fixture.store.aggregate.Workspace.ID, ThreadID: fixture.store.aggregate.Proposal.ThreadID, AttemptID: domain.OperationID("attempt-published"),
		SourceGeneration: fixture.store.aggregate.Intent.ConfirmedAgainst,
		Baseline:         review.SnapshotIdentity{ID: "baseline-1", Ref: repository.SnapshotRef{Kind: repository.SnapshotEmpty}, ManifestHash: fixture.store.lifecycle.Baseline.Hash},
		Result:           review.SnapshotIdentity{ID: "result-1", Ref: repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: fixture.store.aggregate.Workspace.WorktreeID, Fingerprint: "result-fingerprint"}, ManifestHash: strings.Repeat("d", 64)},
		Destination:      review.DestinationConstraints{TargetKind: repository.TargetLocal, WorktreeID: fixture.store.aggregate.Workspace.WorktreeID, ExpectedWorkingTreeFingerprint: "destination-fingerprint"},
		Version:          1, PatchFormat: "git-binary-v1", PatchBytes: patchBytes, PatchSHA256: hex.EncodeToString(digest[:]),
		Files: []review.ProposedFile{{Path: path, OldPath: &path, OldKind: repository.FileKindRegular, Kind: repository.FileKindRegular, OldMode: 0o100644, Mode: 0o100644}},
		Scope: review.ProposalScopeFocused, ScopeReason: "within request", Status: review.ProposalVersionReady, StatusReason: "ready", CreatedAt: now,
	}
}

func (f *proposalDispositionResetFake) ResetToBaseline(_ context.Context, request ProposalBaselineResetRequest) error {
	f.calls++
	f.request = request
	if request.BaselineManifest.Validate() != nil || request.SessionID == "" || request.ProposalID == "" || request.WorkspaceID == "" || request.AttemptID == "" {
		return errors.New("invalid reset request")
	}
	return f.err
}

func (t *proposalTurnTx) TransitionProposalResultDisposition(_ context.Context, value review.ProposalAttempt) error {
	for index := range t.store.aggregate.Attempts {
		if t.store.aggregate.Attempts[index].ID != value.ID {
			continue
		}
		if !t.store.aggregate.Attempts[index].ResultDisposition.CanTransitionTo(value.ResultDisposition) && t.store.aggregate.Attempts[index].ResultDisposition != value.ResultDisposition {
			return review.ErrProposalConflict
		}
		t.store.aggregate.Attempts[index] = value
		return nil
	}
	return ErrReviewStoreNotFound
}

var _ ProposalResultDispositionStoreTx = (*proposalTurnTx)(nil)
