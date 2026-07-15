package app

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

func TestApplyExecutionMutatesVerifiesAndTerminalizesOnce(t *testing.T) {
	fixture := newApplyPreflightFixture(t)
	preflight := fixture.service(t)
	prepared, err := preflight.Prepare(context.Background(), fixture.request(threadTestGuard(), "apply-execute"))
	if err != nil {
		t.Fatal(err)
	}
	mutator := &applyMutatorFake{}
	verifier := &applyVerifierFake{evidence: applyResultEvidence(prepared.Operation, fixture.patch, ApplyObservationResult, strings.Repeat("f", 64), time.Now().UTC())}
	execution, err := NewApplyExecutionService(ApplyExecutionServiceConfig{Store: fixture.store, Proposals: fixture.store, Operations: fixture.store, Locks: fixture.locks, Mutator: mutator, Verifier: verifier, Clock: fixedClock{when: fixture.clock.when.Add(time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := execution.Execute(context.Background(), ApplyExecutionRequest{Guard: prepared.Guard, OperationID: prepared.Operation.ID, ProposalID: prepared.Operation.ProposalID, ProposalVersion: prepared.Operation.ProposalVersion, ProposalPatchSHA256: prepared.Operation.ProposalPatchSHA256, Repository: fixture.repo, Worktree: fixture.worktree, Lease: prepared.Lease})
	if err != nil {
		t.Fatal(err)
	}
	if result.Classification != ApplyExecutionApplied || result.Operation.Phase != ApplyOperationApplied || mutator.calls != 1 || verifier.calls != 1 || string(mutator.bytes) != "ok" {
		t.Fatalf("result=%#v mutator=%d verifier=%d bytes=%q", result, mutator.calls, verifier.calls, mutator.bytes)
	}
	duplicate, err := execution.Execute(context.Background(), ApplyExecutionRequest{Guard: result.Guard, OperationID: result.Operation.ID, ProposalID: result.Operation.ProposalID, ProposalVersion: result.Operation.ProposalVersion, ProposalPatchSHA256: result.Operation.ProposalPatchSHA256, Repository: fixture.repo, Worktree: fixture.worktree})
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.Classification != ApplyExecutionApplied || mutator.calls != 1 || verifier.calls != 1 {
		t.Fatalf("duplicate=%#v mutator=%d verifier=%d", duplicate, mutator.calls, verifier.calls)
	}
}

func TestApplyExecutionRecoveryClassifiesAllBaselineAsRetrySafe(t *testing.T) {
	fixture := newApplyPreflightFixture(t)
	preflight := fixture.service(t)
	prepared, err := preflight.Prepare(context.Background(), fixture.request(threadTestGuard(), "apply-recover"))
	if err != nil {
		t.Fatal(err)
	}
	operation := prepared.Operation
	operation.Phase = ApplyOperationMutating
	fixture.store.operations[string(operation.ID)] = operation
	baseline := applyResultEvidence(operation, fixture.patch, ApplyObservationBaseline, operation.Evidence.WorkingTreeFingerprint, time.Now().UTC())
	mutator := &applyMutatorFake{}
	verifier := &applyVerifierFake{evidence: baseline}
	execution, err := NewApplyExecutionService(ApplyExecutionServiceConfig{Store: fixture.store, Proposals: fixture.store, Operations: fixture.store, Locks: fixture.locks, Mutator: mutator, Verifier: verifier, Clock: fixedClock{when: fixture.clock.when.Add(time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := execution.Execute(context.Background(), ApplyExecutionRequest{Guard: prepared.Guard, OperationID: operation.ID, ProposalID: operation.ProposalID, ProposalVersion: operation.ProposalVersion, ProposalPatchSHA256: operation.ProposalPatchSHA256, Repository: fixture.repo, Worktree: fixture.worktree})
	if err != nil {
		t.Fatal(err)
	}
	if result.Classification != ApplyExecutionRetrySafe || result.Operation.Phase != ApplyOperationRetrySafe || mutator.calls != 0 || verifier.calls != 1 {
		t.Fatalf("recovery=%#v mutator=%d verifier=%d", result, mutator.calls, verifier.calls)
	}
}

func applyResultEvidence(operation ApplyOperation, patch review.ProposedPatch, state ApplyObservationState, workingTreeFingerprint string, now time.Time) ApplyVerificationEvidence {
	paths := make([]ApplyPathEvidence, 0, len(patch.Files))
	for _, file := range patch.Files {
		value := ApplyPathEvidence{Path: file.Path, Exists: !file.Deleted, Kind: file.Kind, Mode: file.Mode}
		if file.Kind == repository.FileKindSymlink {
			value.SymlinkTargetHash = file.ContentHash
		} else {
			value.ContentHash = file.ContentHash
		}
		paths = append(paths, value)
	}
	if state == ApplyObservationBaseline {
		paths = make([]ApplyPathEvidence, 0, len(operation.Preconditions))
		for _, precondition := range operation.Preconditions {
			paths = append(paths, ApplyPathEvidence{Path: precondition.Path, Exists: precondition.MustExist, Kind: precondition.Kind, Mode: precondition.Mode, ContentHash: precondition.ContentHash, SymlinkTargetHash: precondition.SymlinkTargetHash, NativeAlias: precondition.NativeAlias})
		}
	}
	evidence := ApplyVerificationEvidence{Version: 1, OperationID: operation.ID, RepositoryID: operation.Evidence.RepositoryID, WorktreeID: operation.Evidence.WorktreeID, TargetKind: operation.Evidence.TargetKind, State: state, Paths: paths, Head: operation.Evidence.Head, BranchName: operation.Evidence.BranchName, Detached: operation.Evidence.Detached, WorkingTreeFingerprint: workingTreeFingerprint, Index: operation.Evidence.Index, ConversionPolicyVersion: operation.Evidence.ConversionPolicyVersion, ConversionFingerprint: operation.Evidence.ConversionFingerprint, Capability: operation.Evidence.Capability, GlobalFingerprint: strings.Repeat("1", 64), ObservedAt: now}
	evidence.EvidenceHash = applyVerificationEvidenceHash(evidence)
	return evidence
}

type applyMutatorFake struct {
	calls int
	bytes []byte
}

func (f *applyMutatorFake) Mutate(_ context.Context, request ApplyPatchMutationRequest) error {
	f.calls++
	value, err := io.ReadAll(request.Patch)
	f.bytes = value
	return err
}

type applyVerifierFake struct {
	calls    int
	evidence ApplyVerificationEvidence
}

func (f *applyVerifierFake) Verify(context.Context, ApplyResultVerificationRequest) (ApplyVerificationEvidence, error) {
	f.calls++
	return f.evidence, nil
}

var _ ApplyPatchMutator = (*applyMutatorFake)(nil)
var _ ApplyResultVerifier = (*applyVerifierFake)(nil)
