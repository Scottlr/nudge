package app

import (
	"context"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
)

func TestWorkspaceRetentionReaperResumesRemovingPhase(t *testing.T) {
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	candidate := validWorkspaceRetentionCandidate(now.Add(-DefaultWorkspaceRetentionMinimumAge))
	decision, err := EvaluateWorkspaceRetention(DefaultWorkspaceRetentionPolicy(), candidate, now)
	if err != nil {
		t.Fatal(err)
	}
	retirement := WorkspaceRetirement{Version: 1, OperationID: "workspace-retirement-workspace-1-7", Candidate: candidate, Decision: decision, Phase: WorkspaceRetirementRemoving, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour)}
	store := &retirementTestStore{candidate: candidate, retirement: retirement, hasRetirement: true}
	executor := &retirementTestExecutor{proof: WorkspaceRetirementProof{WorkspaceID: candidate.WorkspaceID, OwnershipDigest: candidate.OwnershipDigest, MarkerNonce: candidate.MarkerNonce}}
	reaper, err := NewWorkspaceRetentionReaper(WorkspaceRetentionReaperConfig{Store: store, Executor: executor, Policy: DefaultWorkspaceRetentionPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	pass, err := reaper.Reap(context.Background(), WorkspaceRetentionPage{Limit: 1}, now)
	if err != nil {
		t.Fatal(err)
	}
	if pass.Retired != 1 || executor.verifyCalls != 0 || executor.removeCalls != 1 || store.retirement.Phase != WorkspaceRetirementRemoved {
		t.Fatalf("pass=%#v verify=%d remove=%d phase=%s", pass, executor.verifyCalls, executor.removeCalls, store.retirement.Phase)
	}
}

func TestWorkspaceRetentionReaperStopsOnRevisionChange(t *testing.T) {
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	candidate := validWorkspaceRetentionCandidate(now.Add(-DefaultWorkspaceRetentionMinimumAge))
	store := &retirementTestStore{candidate: candidate, reread: candidate}
	store.reread.EvaluatedRevision++
	executor := &retirementTestExecutor{}
	reaper, err := NewWorkspaceRetentionReaper(WorkspaceRetentionReaperConfig{Store: store, Executor: executor, Policy: DefaultWorkspaceRetentionPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	pass, err := reaper.Reap(context.Background(), WorkspaceRetentionPage{Limit: 1}, now)
	if err != nil {
		t.Fatal(err)
	}
	if pass.Blocked != 1 || executor.verifyCalls != 0 || executor.removeCalls != 0 {
		t.Fatalf("pass=%#v verify=%d remove=%d", pass, executor.verifyCalls, executor.removeCalls)
	}
}

type retirementTestStore struct {
	candidate      WorkspaceRetentionCandidate
	reread         WorkspaceRetentionCandidate
	retirement     WorkspaceRetirement
	hasRetirement  bool
	cursor         WorkspaceRetentionCursor
	hasCursor      bool
	requestedAfter domain.WorkspaceID
}

func (s *retirementTestStore) ListWorkspaceRetentionCandidates(_ context.Context, page WorkspaceRetentionPage) (WorkspaceRetentionPageResult, error) {
	s.requestedAfter = page.AfterID
	return WorkspaceRetentionPageResult{Candidates: []WorkspaceRetentionCandidate{s.candidate}}, nil
}

func (s *retirementTestStore) LoadWorkspaceRetentionCandidate(context.Context, domain.WorkspaceID) (WorkspaceRetentionCandidate, error) {
	if s.reread.WorkspaceID != "" {
		return s.reread, nil
	}
	return s.candidate, nil
}

func (s *retirementTestStore) LoadWorkspaceRetirement(context.Context, domain.WorkspaceID, domain.OperationID) (WorkspaceRetirement, error) {
	if !s.hasRetirement {
		return WorkspaceRetirement{}, ErrWorkspaceRetirementNotFound
	}
	return s.retirement, nil
}

func (s *retirementTestStore) SaveWorkspaceRetirement(_ context.Context, retirement WorkspaceRetirement) error {
	s.retirement = retirement
	s.hasRetirement = true
	return nil
}

func (s *retirementTestStore) LoadWorkspaceRetentionCursor(context.Context) (WorkspaceRetentionCursor, error) {
	if !s.hasCursor {
		return WorkspaceRetentionCursor{}, ErrWorkspaceRetentionCursorNotFound
	}
	return s.cursor, nil
}

func (s *retirementTestStore) SaveWorkspaceRetentionCursor(_ context.Context, cursor WorkspaceRetentionCursor) error {
	s.cursor = cursor
	s.hasCursor = true
	return nil
}

func TestWorkspaceRetentionReaperResumesDurableCursor(t *testing.T) {
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	candidate := validWorkspaceRetentionCandidate(now.Add(-DefaultWorkspaceRetentionMinimumAge))
	store := &retirementTestStore{candidate: candidate, cursor: WorkspaceRetentionCursor{AfterID: "workspace-0", UpdatedAt: now.Add(-time.Hour)}, hasCursor: true}
	reaper, err := NewWorkspaceRetentionReaper(WorkspaceRetentionReaperConfig{Store: store, Executor: &retirementTestExecutor{}, Policy: DefaultWorkspaceRetentionPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reaper.Reap(context.Background(), WorkspaceRetentionPage{Limit: 1}, now); err != nil {
		t.Fatal(err)
	}
	if store.requestedAfter != "workspace-0" || !store.hasCursor || !store.cursor.UpdatedAt.Equal(now) || store.cursor.AfterID != "" {
		t.Fatalf("cursor after reap = %#v, requested=%q", store.cursor, store.requestedAfter)
	}
}

type retirementTestExecutor struct {
	proof       WorkspaceRetirementProof
	verifyCalls int
	removeCalls int
}

func (e *retirementTestExecutor) Verify(context.Context, WorkspaceRetirement) (WorkspaceRetirementProof, error) {
	e.verifyCalls++
	return e.proof, nil
}

func (e *retirementTestExecutor) Remove(context.Context, WorkspaceRetirement) (WorkspaceRetirementProof, error) {
	e.removeCalls++
	return e.proof, nil
}
