package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

func TestEvaluateProposalValidityKeepsUnaffectedLocalProposalReady(t *testing.T) {
	path := mustValidityPath(t, "src/untouched.go")
	precondition := validityPrecondition(path, "a")
	candidate := ProposalValidityCandidate{
		ProposalID: "proposal-untouched", Version: 1, Status: review.ProposalVersionReady,
		Destination:   review.DestinationConstraints{TargetKind: repository.TargetLocal, WorktreeID: "worktree", ExpectedWorkingTreeFingerprint: strings.Repeat("b", 64)},
		Preconditions: []repository.PathPrecondition{precondition},
	}
	destination := PostApplyDestinationState{TargetKind: repository.TargetLocal, WorktreeID: "worktree", WorkingTreeFingerprint: strings.Repeat("c", 64), GlobalFingerprint: strings.Repeat("d", 64), Paths: []repository.PathPrecondition{precondition}}

	result := evaluateProposalValidity(candidate, destination, "apply-1", 2)
	if result.Outcome != ProposalValidityValid || result.ConflictPath != nil {
		t.Fatalf("validity result = %#v, want unaffected local proposal to remain valid", result)
	}
}

func TestEvaluateProposalValidityRecordsChangedPath(t *testing.T) {
	path := mustValidityPath(t, "src/changed.go")
	candidatePrecondition := validityPrecondition(path, "a")
	actualPrecondition := validityPrecondition(path, "b")
	candidate := ProposalValidityCandidate{
		ProposalID: "proposal-changed", Version: 2, Status: review.ProposalVersionReady,
		Destination:   review.DestinationConstraints{TargetKind: repository.TargetLocal, WorktreeID: "worktree", ExpectedWorkingTreeFingerprint: strings.Repeat("a", 64)},
		Preconditions: []repository.PathPrecondition{candidatePrecondition},
	}
	destination := PostApplyDestinationState{TargetKind: repository.TargetLocal, WorktreeID: "worktree", WorkingTreeFingerprint: strings.Repeat("b", 64), GlobalFingerprint: strings.Repeat("c", 64), Paths: []repository.PathPrecondition{actualPrecondition}}

	result := evaluateProposalValidity(candidate, destination, "apply-1", 2)
	if result.Outcome != ProposalValidityStale || result.Reason != "path_precondition_changed" || result.ConflictPath == nil || result.ConflictPath.Key() != path.Key() {
		t.Fatalf("validity result = %#v, want path-specific stale result", result)
	}
}

func TestPostApplyValiditySweepStagesBoundedPagesAndCompletesEpoch(t *testing.T) {
	path := mustValidityPath(t, "src/example.go")
	precondition := validityPrecondition(path, "a")
	service := &PostApplyReconciliationService{
		validity: &postApplyValiditySourceFake{pages: []ProposalValidityPage{
			{Items: []ProposalValidityCandidate{{ProposalID: "proposal-valid", Version: 1, Status: review.ProposalVersionReady, Destination: validityDestination(), Preconditions: []repository.PathPrecondition{precondition}}}, NextCursor: "next", EncodedBytes: 100},
			{Items: []ProposalValidityCandidate{{ProposalID: "proposal-stale", Version: 1, Status: review.ProposalVersionReady, Destination: validityDestination(), Preconditions: []repository.PathPrecondition{validityPrecondition(path, "b")}}}, Done: true, EncodedBytes: 100},
		}},
		journal: &postApplyValidityJournalFake{}, limits: DefaultProposalValidityBatchLimits(), clock: fixedClock{when: testTime},
	}
	destination := PostApplyDestinationState{TargetKind: repository.TargetLocal, WorktreeID: "worktree", WorkingTreeFingerprint: strings.Repeat("c", 64), GlobalFingerprint: strings.Repeat("d", 64), Paths: []repository.PathPrecondition{precondition}}
	target := testTarget(2)
	record := PostApplyReconciliationRecord{ApplyOperationID: "apply-1", SessionID: "session", WorkspaceID: "workspace", ProposalID: "proposal-applied", PreviousGeneration: 1, NewGeneration: 2, CaptureID: "capture-2", ManifestHash: strings.Repeat("a", 64), Provenance: ApplyReconciliationNudgeApplied, Target: target, Destination: destination, Phase: PostApplyPhaseValidityPending, ValidityEpoch: 1, StartedAt: testTime}

	guard := SessionWriteGuard{SessionID: "session", LeaseID: "lease", WriterEpoch: 1, ExpectedRevision: 1}
	next, events, err := service.runValiditySweep(context.Background(), guard, &record, "corr-1")
	if err != nil {
		t.Fatal(err)
	}
	if record.Phase != PostApplyPhaseBaselinePending || record.ValidityCursor != "" || record.ProcessedProposals != 2 || next.ExpectedRevision != 4 {
		t.Fatalf("record = %#v, guard = %#v", record, next)
	}
	fake := service.journal.(*postApplyValidityJournalFake)
	if len(fake.staged) != 2 || len(events) != 2 {
		t.Fatalf("staged=%d events=%d, want two bounded pages", len(fake.staged), len(events))
	}
}

func TestPostApplyReconciliationReplaysCompletedJournalWithoutRepeatingBaseline(t *testing.T) {
	fixture := newProposalApplyFixture(t)
	fixture.store.operations[string(fixture.operation.ID)] = fixture.operation
	executor := &proposalApplyExecutorFake{store: fixture.store, patch: fixture.patch, classification: ApplyExecutionApplied}
	execution, err := executor.Execute(context.Background(), ApplyExecutionRequest{Guard: threadTestGuard(), OperationID: fixture.operation.ID, ProposalID: fixture.operation.ProposalID, ProposalVersion: fixture.operation.ProposalVersion, ProposalPatchSHA256: fixture.operation.ProposalPatchSHA256, Repository: fixture.repo, Worktree: fixture.worktree})
	if err != nil {
		t.Fatal(err)
	}
	operation := execution.Operation

	target := testTarget(2)
	record := PostApplyReconciliationRecord{ApplyOperationID: operation.ID, SessionID: operation.SessionID, WorkspaceID: operation.WorkspaceID, ProposalID: operation.ProposalID, PreviousGeneration: 1, NewGeneration: 2, CaptureID: "capture-2", ManifestHash: strings.Repeat("a", 64), Provenance: ApplyReconciliationNudgeApplied, Target: target, Destination: PostApplyDestinationState{TargetKind: repository.TargetLocal, WorktreeID: fixture.worktree.ID, WorkingTreeFingerprint: strings.Repeat("c", 64), GlobalFingerprint: strings.Repeat("d", 64)}, Phase: PostApplyPhaseValidityPending, ValidityEpoch: 1, StartedAt: testTime}
	journal := &postApplyValidityJournalFake{loaded: &record}
	baseline := &postApplyBaselineFake{}
	service, err := NewPostApplyReconciliationService(PostApplyReconciliationServiceConfig{
		Operations: fixture.store,
		Refresh:    &postApplyRefreshFake{},
		Validity:   &postApplyValiditySourceFake{pages: []ProposalValidityPage{{Done: true}}},
		Journal:    journal,
		Baseline:   baseline,
		Clock:      fixedClock{when: testTime},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := PostApplyReconciliationRequest{Guard: threadTestGuard(), CorrelationID: "correlation", OperationID: operation.ID, Repository: fixture.repo, Worktree: fixture.worktree, PreviousTarget: testTarget(1), PreviousGeneration: testCaptureGeneration(1, "capture-1", "a"), WorkspaceID: operation.WorkspaceID, Provenance: ApplyReconciliationNudgeApplied, BaselineSource: &postApplyTreeSourceFake{}}
	first, err := service.Reconcile(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Applied || first.WorkspaceRepairRequired || baseline.calls != 1 || journal.completed != 1 {
		t.Fatalf("first reconciliation = %#v baseline=%d completed=%d", first, baseline.calls, journal.completed)
	}
	second, err := service.Reconcile(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Applied || baseline.calls != 1 || journal.completed != 1 {
		t.Fatalf("replayed reconciliation = %#v baseline=%d completed=%d", second, baseline.calls, journal.completed)
	}
}

func validityDestination() review.DestinationConstraints {
	return review.DestinationConstraints{TargetKind: repository.TargetLocal, WorktreeID: "worktree", ExpectedWorkingTreeFingerprint: strings.Repeat("f", 64)}
}

func mustValidityPath(t *testing.T, value string) repository.RepoPath {
	t.Helper()
	path, err := repository.NewRepoPath([]byte(value))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func validityPrecondition(path repository.RepoPath, marker string) repository.PathPrecondition {
	return repository.PathPrecondition{Path: path, MustExist: true, Kind: repository.FileKindRegular, Mode: 0o100644, ContentHash: strings.Repeat(marker, 64)}
}

type postApplyValiditySourceFake struct {
	pages []ProposalValidityPage
	index int
}

func (s *postApplyValiditySourceFake) PageProposalValidity(context.Context, ProposalValidityPageRequest) (ProposalValidityPage, error) {
	if s.index >= len(s.pages) {
		return ProposalValidityPage{}, errors.New("unexpected validity page")
	}
	page := s.pages[s.index]
	s.index++
	return page, nil
}

type postApplyValidityJournalFake struct {
	staged    []ProposalValidityResult
	loaded    *PostApplyReconciliationRecord
	completed int
}

func (j *postApplyValidityJournalFake) Load(context.Context, domain.OperationID) (PostApplyReconciliationRecord, error) {
	if j.loaded != nil {
		return *j.loaded, nil
	}
	return PostApplyReconciliationRecord{}, ErrReviewStoreNotFound
}

func (j *postApplyValidityJournalFake) Start(_ context.Context, guard SessionWriteGuard, _ PostApplyReconciliationRecord) (SessionWriteGuard, error) {
	guard.ExpectedRevision++
	return guard, nil
}

func (j *postApplyValidityJournalFake) RecordGeneration(_ context.Context, guard SessionWriteGuard, _ PostApplyReconciliationRecord) (SessionWriteGuard, error) {
	guard.ExpectedRevision++
	return guard, nil
}

func (j *postApplyValidityJournalFake) StageValidity(_ context.Context, guard SessionWriteGuard, _ PostApplyReconciliationRecord, results []ProposalValidityResult) (SessionWriteGuard, error) {
	j.staged = append(j.staged, results...)
	guard.ExpectedRevision++
	return guard, nil
}

func (j *postApplyValidityJournalFake) CompleteValidity(_ context.Context, guard SessionWriteGuard, _ PostApplyReconciliationRecord, _ time.Time) (SessionWriteGuard, error) {
	guard.ExpectedRevision++
	return guard, nil
}

func (j *postApplyValidityJournalFake) Complete(_ context.Context, guard SessionWriteGuard, record PostApplyReconciliationRecord, at time.Time) (SessionWriteGuard, error) {
	record.Phase = PostApplyPhaseCompleted
	record.CompletedAt = &at
	j.loaded = &record
	j.completed++
	return guard, nil
}

func (j *postApplyValidityJournalFake) Repair(context.Context, SessionWriteGuard, PostApplyReconciliationRecord, string, time.Time) (SessionWriteGuard, error) {
	return SessionWriteGuard{}, nil
}

type postApplyRefreshFake struct{}

func (postApplyRefreshFake) RefreshAfterApply(context.Context, PostApplyTargetRefreshRequest) (PostApplyTargetRefreshResult, error) {
	return PostApplyTargetRefreshResult{}, errors.New("refresh should not run for an already captured generation")
}

type postApplyBaselineFake struct {
	calls int
}

func (f *postApplyBaselineFake) AdvanceBaseline(context.Context, PostApplyBaselineRequest) (PostApplyBaselineResult, error) {
	f.calls++
	return PostApplyBaselineResult{}, nil
}

type postApplyTreeSourceFake struct{}

func (postApplyTreeSourceFake) Identity() WorkspaceSourceIdentity {
	return WorkspaceSourceIdentity{Kind: "accepted_capture", ID: "capture-2", ManifestHash: strings.Repeat("a", 64)}
}

func (postApplyTreeSourceFake) List(context.Context) ([]repository.TreeEntry, error) { return nil, nil }

func (postApplyTreeSourceFake) Open(context.Context, repository.TreeEntry) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
