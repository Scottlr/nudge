package app

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

func TestApplyPreflightPersistsPreparedOperationAfterExactCheck(t *testing.T) {
	fixture := newApplyPreflightFixture(t)
	service := fixture.service(t)
	preparation, err := service.Prepare(context.Background(), fixture.request(threadTestGuard(), "apply-op-1"))
	if err != nil {
		t.Fatal(err)
	}
	defer preparation.Lease.Close()
	if fixture.inspector.calls != 2 || fixture.checker.calls != 1 || len(fixture.store.operations) != 1 {
		t.Fatalf("calls inspector=%d checker=%d operations=%d", fixture.inspector.calls, fixture.checker.calls, len(fixture.store.operations))
	}
	if preparation.Guard.ExpectedRevision != 2 || preparation.Operation.Phase != ApplyOperationPrepared {
		t.Fatalf("preparation = %#v", preparation)
	}
	if err := preparation.Operation.Validate(); err != nil {
		t.Fatalf("prepared operation: %v", err)
	}
}

func TestApplyPreflightRejectsDestinationRaceBeforeJournal(t *testing.T) {
	fixture := newApplyPreflightFixture(t)
	fixture.inspector.evidence[1].GlobalFingerprint = strings.Repeat("e", 64)
	fixture.inspector.evidence[1].EvidenceHash = applyEvidenceHash(fixture.inspector.evidence[1])
	service := fixture.service(t)
	_, err := service.Prepare(context.Background(), fixture.request(threadTestGuard(), "apply-op-race"))
	if !errors.Is(err, ErrApplyPreflightRace) || len(fixture.store.operations) != 0 || fixture.lease.closed != 1 {
		t.Fatalf("race error=%v operations=%d closed=%d", err, len(fixture.store.operations), fixture.lease.closed)
	}
}

func TestApplyPreflightRetryReturnsPreparedOperationIdempotently(t *testing.T) {
	fixture := newApplyPreflightFixture(t)
	service := fixture.service(t)
	first, err := service.Prepare(context.Background(), fixture.request(threadTestGuard(), "apply-op-idempotent"))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Lease.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := service.Prepare(context.Background(), fixture.request(first.Guard, "apply-op-idempotent"))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Lease.Close()
	if second.Operation.ID != first.Operation.ID || fixture.inspector.calls != 2 || fixture.checker.calls != 1 || len(fixture.store.operations) != 1 {
		t.Fatalf("retry operation=%#v inspector=%d checker=%d operations=%d", second.Operation, fixture.inspector.calls, fixture.checker.calls, len(fixture.store.operations))
	}
}

type applyPreflightFixture struct {
	store     *applyTestStore
	patch     review.ProposedPatch
	repo      repository.Repository
	worktree  repository.WorktreeRef
	inspector *applyInspectorFake
	checker   *applyCheckerFake
	lease     *applyLeaseFake
	locks     *applyLockFake
	clock     fixedClock
}

func newApplyPreflightFixture(t *testing.T) *applyPreflightFixture {
	t.Helper()
	derived := proposalDerivationFixture(t)
	patch, err := DeriveProposal(derived.input)
	if err != nil {
		t.Fatal(err)
	}
	patch.Artifact = review.ProposedPatchArtifactReference{}
	patch.PatchBytes = []byte("ok")
	patch.PatchSHA256 = proposalHash(patch.PatchBytes)
	patch.Destination.ExpectedWorkingTreeFingerprint = strings.Repeat("a", 64)
	if err := patch.Validate(); err != nil {
		t.Fatal(err)
	}
	aggregate := derived.input.Aggregate
	aggregate.Versions = []review.ProposedPatch{patch}
	version := patch.Version
	aggregate.Proposal.CurrentVersion = &version
	if err := aggregate.Validate(); err != nil {
		t.Fatal(err)
	}
	store := &applyTestStore{proposalTurnStore: newProposalTurnStore(threadTestGuard(), aggregate, ProposalWorkspaceLifecycle{}), operations: make(map[string]ApplyOperation)}
	now := time.Date(2026, time.July, 15, 16, 0, 0, 0, time.UTC)
	repo := repository.Repository{ID: "repo-1", CommonGitDir: `C:\repo\.git`, Binding: repository.RepositoryBindingEvidence{Version: 1, ObjectFormat: "sha1", CommonGitDir: `C:\repo\.git`, CommonGitDirIdentity: "repo-id"}, DisplayName: "repo", CreatedAt: now, UpdatedAt: now}
	worktree := repository.WorktreeRef{ID: "worktree-1", RepositoryID: repo.ID, RootPath: `C:\repo`, GitDir: `C:\repo\.git`, Binding: repository.WorktreeBindingEvidence{Version: 1, ObjectFormat: "sha1", RootPath: `C:\repo`, GitDir: `C:\repo\.git`, RootIdentity: "root-id", GitDirIdentity: "git-id"}}
	if err := repo.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := worktree.Validate(); err != nil {
		t.Fatal(err)
	}
	evidence := ApplyDestinationEvidence{Version: ApplyEvidenceVersion, RepositoryID: repo.ID, WorktreeID: worktree.ID, TargetKind: repository.TargetLocal, WorkingTreeFingerprint: patch.Destination.ExpectedWorkingTreeFingerprint, Paths: cloneApplyPreconditions(patch.Preconditions), ConversionPolicyVersion: 1, ConversionFingerprint: strings.Repeat("b", 64), Capability: ApplyCapabilityEvidence{Version: 1, RegisteredSupport: true, CanonicalContainment: true, NativePathExecutor: true, NoUnmergedIndex: true, NoUnsupportedIndexFlags: true, ConversionByteNeutral: true}, GlobalFingerprint: strings.Repeat("d", 64), ObservedAt: now}
	evidence.EvidenceHash = applyEvidenceHash(evidence)
	lease := &applyLeaseFake{}
	inspector := &applyInspectorFake{evidence: []ApplyDestinationEvidence{evidence, evidence}}
	checker := &applyCheckerFake{}
	locks := &applyLockFake{lease: lease}
	return &applyPreflightFixture{store: store, patch: patch, repo: repo, worktree: worktree, inspector: inspector, checker: checker, lease: lease, locks: locks, clock: fixedClock{when: now}}
}

func (f *applyPreflightFixture) service(t *testing.T) *ApplyPreflightService {
	t.Helper()
	service, err := NewApplyPreflightService(ApplyPreflightServiceConfig{Store: f.store, Proposals: f.store, Operations: f.store, Locks: f.locks, Inspector: f.inspector, Checker: f.checker, Clock: f.clock})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func (f *applyPreflightFixture) request(guard SessionWriteGuard, operationID domain.OperationID) ApplyPreflightRequest {
	return ApplyPreflightRequest{Guard: guard, OperationID: operationID, ProposalID: f.patch.ProposalID, ProposalVersion: f.patch.Version, ConfirmedReviewRevision: 1, IdempotencyKey: string(operationID), Repository: f.repo, Worktree: f.worktree}
}

type applyTestStore struct {
	*proposalTurnStore
	operations map[string]ApplyOperation
}

func (s *applyTestStore) LoadApplyOperation(_ context.Context, operationID domain.OperationID) (ApplyOperation, error) {
	operation, ok := s.operations[string(operationID)]
	if !ok {
		return ApplyOperation{}, ErrApplyOperationNotFound
	}
	return operation, nil
}

func (s *applyTestStore) LoadApplyOperationByKey(_ context.Context, _ domain.ReviewSessionID, key string) (ApplyOperation, error) {
	for _, operation := range s.operations {
		if operation.IdempotencyKey == key {
			return operation, nil
		}
	}
	return ApplyOperation{}, ErrApplyOperationNotFound
}

func (s *applyTestStore) LoadApplyOperationForProposal(_ context.Context, proposalID domain.ProposalID, version review.ProposalVersionNumber) (ApplyOperation, error) {
	for _, operation := range s.operations {
		if operation.ProposalID == proposalID && operation.ProposalVersion == version {
			return operation, nil
		}
	}
	return ApplyOperation{}, ErrApplyOperationNotFound
}

func (s *applyTestStore) WithSessionTx(_ context.Context, guard SessionWriteGuard, fn func(ReviewStoreTx) error) (SessionWriteGuard, error) {
	current := s.guards[guard.SessionID]
	if current != guard {
		return guard, ErrSessionRevisionConflict
	}
	tx := &applyTestTx{proposalTurnTx: &proposalTurnTx{store: s.proposalTurnStore, threadTestTx: &threadTestTx{threads: s.threads, messages: s.messages, sessionID: guard.SessionID}}, store: s}
	if err := fn(tx); err != nil {
		return guard, err
	}
	current.ExpectedRevision++
	s.guards[guard.SessionID] = current
	return current, nil
}

type applyTestTx struct {
	*proposalTurnTx
	store *applyTestStore
}

func (t *applyTestTx) PrepareApplyOperation(_ context.Context, operation ApplyOperation) error {
	if existing, ok := t.store.operations[string(operation.ID)]; ok {
		if reflect.DeepEqual(existing, operation) {
			return nil
		}
		return ErrApplyOperationConflict
	}
	for _, existing := range t.store.operations {
		if existing.IdempotencyKey == operation.IdempotencyKey || existing.ProposalID == operation.ProposalID && existing.ProposalVersion == operation.ProposalVersion {
			return ErrApplyOperationConflict
		}
	}
	t.store.operations[string(operation.ID)] = operation
	return nil
}

func (t *applyTestTx) TransitionApplyOperation(_ context.Context, operation ApplyOperation) error {
	existing, ok := t.store.operations[string(operation.ID)]
	if !ok {
		return ErrApplyOperationNotFound
	}
	if reflect.DeepEqual(existing, operation) {
		return nil
	}
	if existing.ProposalID != operation.ProposalID || existing.ProposalVersion != operation.ProposalVersion {
		return ErrApplyOperationConflict
	}
	t.store.operations[string(operation.ID)] = operation
	return nil
}

type applyInspectorFake struct {
	evidence []ApplyDestinationEvidence
	calls    int
}

func (f *applyInspectorFake) Inspect(context.Context, ApplyDestinationInspectionRequest) (ApplyDestinationEvidence, error) {
	value := f.evidence[f.calls]
	f.calls++
	return value, nil
}

type applyCheckerFake struct {
	calls int
	bytes []byte
}

func (f *applyCheckerFake) Check(_ context.Context, request ApplyPatchCheckRequest) error {
	f.calls++
	value, err := io.ReadAll(request.Patch)
	if err != nil {
		return err
	}
	f.bytes = value
	return nil
}

type applyLockFake struct {
	lease    *applyLeaseFake
	acquires int
}

func (f *applyLockFake) Acquire(context.Context, domain.RepositoryID, domain.WorktreeID) (ApplyExecutionLease, error) {
	f.acquires++
	return f.lease, nil
}

type applyLeaseFake struct {
	closed int
}

func (f *applyLeaseFake) Close() error {
	f.closed++
	return nil
}

var _ ApplyOperationStore = (*applyTestStore)(nil)
var _ ApplyOperationStoreTx = (*applyTestTx)(nil)
var _ ApplyDestinationLockManager = (*applyLockFake)(nil)
var _ ApplyDestinationInspector = (*applyInspectorFake)(nil)
var _ ApplyPatchChecker = (*applyCheckerFake)(nil)
var _ ApplyExecutionLease = (*applyLeaseFake)(nil)
