package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

func TestApproveProposalAppliesOnceAndDuplicateFollowsJournal(t *testing.T) {
	fixture := newProposalApplyFixture(t)
	executor := &proposalApplyExecutorFake{store: fixture.store, patch: fixture.patch, classification: ApplyExecutionApplied}
	preflight := &proposalApplyPreflightFake{store: fixture.store, operation: fixture.operation}
	service := newProposalApplyServiceForTest(t, fixture, preflight, executor)
	command := fixture.command(threadTestGuard())

	first, err := service.ApproveProposal(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if first.Outcome != ProposalApplyApplied || first.Version.Status != review.ProposalVersionApplied || first.Thread.Proposal != review.ProposalApplied || preflight.calls != 1 || executor.calls != 1 {
		t.Fatalf("first approval = %#v preflight=%d executor=%d", first, preflight.calls, executor.calls)
	}

	command.Guard = first.Guard
	second, err := service.ApproveProposal(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if second.Outcome != ProposalApplyApplied || preflight.calls != 1 || executor.calls != 1 {
		t.Fatalf("duplicate approval = %#v preflight=%d executor=%d", second, preflight.calls, executor.calls)
	}
}

func TestApproveProposalRejectsWrongReviewIdentityBeforeApplying(t *testing.T) {
	fixture := newProposalApplyFixture(t)
	preflight := &proposalApplyPreflightFake{store: fixture.store, operation: fixture.operation}
	executor := &proposalApplyExecutorFake{store: fixture.store, patch: fixture.patch, classification: ApplyExecutionApplied}
	service := newProposalApplyServiceForTest(t, fixture, preflight, executor)
	command := fixture.command(threadTestGuard())
	command.ReviewCompletenessIdentity = strings.Repeat("b", 64)

	if _, err := service.ApproveProposal(context.Background(), command); !errors.Is(err, ErrProposalApprovalConflict) {
		t.Fatalf("approval error = %v, want conflict", err)
	}
	if fixture.store.aggregate.Proposal.Status != review.ProposalVersionReady || preflight.calls != 0 || executor.calls != 0 {
		t.Fatalf("wrong identity mutated aggregate=%#v preflight=%d executor=%d", fixture.store.aggregate.Proposal, preflight.calls, executor.calls)
	}
}

func TestApproveProposalMapsPreflightStaleWithoutClaimingApply(t *testing.T) {
	fixture := newProposalApplyFixture(t)
	preflight := &proposalApplyPreflightFake{err: ErrApplyStale}
	service := newProposalApplyServiceForTest(t, fixture, preflight, &proposalApplyExecutorFake{store: fixture.store, patch: fixture.patch, classification: ApplyExecutionApplied})

	commit, err := service.ApproveProposal(context.Background(), fixture.command(threadTestGuard()))
	if err != nil {
		t.Fatal(err)
	}
	if commit.Outcome != ProposalApplyStale || commit.Version.Status != review.ProposalVersionStale || commit.Thread.Proposal != review.ProposalStale || preflight.calls != 1 {
		t.Fatalf("stale approval = %#v preflight=%d", commit, preflight.calls)
	}
}

func TestApproveProposalMapsT113RepairRequiredAndLeavesThreadOpen(t *testing.T) {
	fixture := newProposalApplyFixture(t)
	preflight := &proposalApplyPreflightFake{store: fixture.store, operation: fixture.operation}
	executor := &proposalApplyExecutorFake{store: fixture.store, patch: fixture.patch, classification: ApplyExecutionRepairRequired, failure: ApplyFailureMixedState}
	service := newProposalApplyServiceForTest(t, fixture, preflight, executor)

	commit, err := service.ApproveProposal(context.Background(), fixture.command(threadTestGuard()))
	if err != nil {
		t.Fatal(err)
	}
	if commit.Outcome != ProposalApplyRepairRequired || commit.Version.Status != review.ProposalVersionFailed || commit.Thread.Proposal != review.ProposalFailed || commit.Thread.Resolution != review.ResolutionOpen {
		t.Fatalf("repair approval = %#v", commit)
	}
}

type proposalApplyFixture struct {
	store     *applyTestStore
	patch     review.ProposedPatch
	operation ApplyOperation
	repo      repository.Repository
	worktree  repository.WorktreeRef
}

func newProposalApplyFixture(t *testing.T) *proposalApplyFixture {
	t.Helper()
	base := newApplyPreflightFixture(t)
	now := time.Unix(100, 0).UTC()
	thread := review.ReviewThread{ID: base.patch.ThreadID, SessionID: threadTestGuard().SessionID, Anchor: threadTestAnchor(t), Resolution: review.ResolutionOpen, Conversation: review.ConversationIdle, Proposal: review.ProposalReady, Read: review.Unread, CreatedAt: now, UpdatedAt: now}
	if err := thread.Validate(); err != nil {
		t.Fatal(err)
	}
	base.store.thread = thread
	base.store.aggregate.Proposal.Status = review.ProposalVersionReady
	base.store.aggregate.Proposal.CurrentVersion = proposalVersionPointer(base.patch.Version)
	if err := base.store.aggregate.Validate(); err != nil {
		t.Fatal(err)
	}

	prepared, err := base.service(t).Prepare(context.Background(), base.request(threadTestGuard(), domain.OperationID("seed-apply")))
	if err != nil {
		t.Fatal(err)
	}
	_ = prepared.Lease.Close()
	base.store.operations = make(map[string]ApplyOperation)
	base.store.guards[threadTestGuard().SessionID] = threadTestGuard()
	return &proposalApplyFixture{store: base.store, patch: base.patch, operation: prepared.Operation, repo: base.repo, worktree: base.worktree}
}

func (f *proposalApplyFixture) command(guard SessionWriteGuard) ApproveProposal {
	return ApproveProposal{Guard: guard, ThreadID: f.patch.ThreadID, ProposalID: f.patch.ProposalID, Version: f.patch.Version, PatchSHA256: f.patch.PatchSHA256, ConfirmedReviewRevision: 1, ReviewCompletenessIdentity: f.patch.PatchSHA256, Destination: f.patch.Destination, Repository: f.repo, Worktree: f.worktree, IdempotencyKey: string(f.operation.ID), OperationID: f.operation.ID, CorrelationID: "approve-correlation"}
}

func newProposalApplyServiceForTest(t *testing.T, fixture *proposalApplyFixture, preflight ProposalApplyPreflight, executor ProposalApplyExecutor) *ProposalApplyService {
	t.Helper()
	service, err := NewProposalApplyService(ProposalApplyServiceConfig{Store: fixture.store, Proposals: fixture.store, Operations: fixture.store, Preflight: preflight, Executor: executor, Clock: fixedClock{when: time.Date(2026, time.July, 15, 18, 0, 0, 0, time.UTC)}})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

type proposalApplyPreflightFake struct {
	store     *applyTestStore
	operation ApplyOperation
	err       error
	calls     int
}

func (f *proposalApplyPreflightFake) Prepare(_ context.Context, request ApplyPreflightRequest) (ApplyPreparation, error) {
	f.calls++
	if f.err != nil {
		return ApplyPreparation{}, f.err
	}
	f.operation.ID = request.OperationID
	f.operation.IdempotencyKey = request.IdempotencyKey
	f.store.operations[string(f.operation.ID)] = f.operation
	return ApplyPreparation{Operation: f.operation, Guard: request.Guard, Lease: &applyLeaseFake{}}, nil
}

type proposalApplyExecutorFake struct {
	store          *applyTestStore
	patch          review.ProposedPatch
	classification ApplyExecutionClassification
	failure        ApplyFailureCode
	calls          int
}

func (f *proposalApplyExecutorFake) Execute(_ context.Context, request ApplyExecutionRequest) (ApplyExecutionResult, error) {
	f.calls++
	operation := f.store.operations[string(request.OperationID)]
	if operation.ID == "" {
		operation = ApplyOperation{ID: request.OperationID, ProposalID: request.ProposalID, ProposalVersion: request.ProposalVersion, ProposalPatchSHA256: request.ProposalPatchSHA256}
	}
	operation.Phase = ApplyOperationApplied
	operation.FailureCode = f.failure
	completed := time.Date(2026, time.July, 15, 18, 0, 0, 0, time.UTC)
	operation.Verification = applyResultEvidence(operation, f.patch, ApplyObservationResult, strings.Repeat("f", 64), completed)
	operation.CompletedAt = &completed
	if f.classification == ApplyExecutionFailedClean {
		operation.Phase = ApplyOperationFailedClean
	}
	if f.classification == ApplyExecutionRepairRequired {
		operation.Phase = ApplyOperationRepairRequired
	}
	if err := operation.Validate(); err != nil {
		return ApplyExecutionResult{}, err
	}
	f.store.operations[string(operation.ID)] = operation
	return ApplyExecutionResult{Operation: operation, Classification: f.classification, Guard: request.Guard}, nil
}
