package app

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain/repository"
)

func TestCanProposeRequiresExactCurrentEditableHead(t *testing.T) {
	_, worktree := branchTargetTestBinding(t)
	target := editableBranchProposalTarget(t, worktree)
	eligible := CanPropose(target, worktree, true, true)
	if !eligible.Eligible || eligible.Validate() != nil || ProposalTargetLabel(eligible) != "Request change" {
		t.Fatalf("eligible target = %#v", eligible)
	}

	moved := worktree
	moved.CurrentObjectID = "moved-head"
	decision := CanPropose(target, moved, true, true)
	if decision.Eligible || decision.Reason != ProposalHeadMoved || !strings.Contains(ProposalTargetLabel(decision), "HEAD moved") {
		t.Fatalf("moved target = %#v", decision)
	}

	historical := target
	historical.Editable = false
	historical.EditDestination = nil
	decision = CanPropose(historical, worktree, true, true)
	if decision.Eligible || decision.Reason != ProposalHistoricalTarget {
		t.Fatalf("historical target = %#v", decision)
	}

	detached := worktree
	detached.Detached = true
	detached.BranchName = ""
	decision = CanPropose(target, detached, true, true)
	if decision.Eligible || decision.Reason != ProposalNonCurrentWorktree {
		t.Fatalf("detached branch target = %#v", decision)
	}

	decision = CanPropose(target, worktree, false, true)
	if decision.Eligible || decision.Reason != ProposalObjectsUnavailable {
		t.Fatalf("missing objects target = %#v", decision)
	}
	decision = CanPropose(target, worktree, true, false)
	if decision.Eligible || decision.Reason != ProposalIsolationUnavailable {
		t.Fatalf("missing isolation target = %#v", decision)
	}
}

func TestProposalTargetBaselineRequestBindsReviewSnapshot(t *testing.T) {
	repo, worktree := branchTargetTestBinding(t)
	target := editableBranchProposalTarget(t, worktree)
	snapshot := ReviewSnapshot{
		ID: "review-snapshot", RepositoryID: repo.ID, TargetKind: repository.TargetBranch,
		HeadObjectID: target.Head.ObjectID, BaseObjectID: target.Base.ObjectID, ObjectFormat: "sha1", FormatVersion: ReviewSnapshotFormatVersion,
		Root: filepath.Join(t.TempDir(), "snapshot"), MarkerNonce: strings.Repeat("a", 64), ManifestHash: strings.Repeat("b", 64),
		PolicyVersion: CurrentResourcePolicyVersion, EvidenceVersion: CurrentCapabilityEvidenceVersion, State: ReviewSnapshotReady, CreatedAt: time.Now().UTC(),
	}
	request := ProposalTargetBaselineRequest{Target: target, Snapshot: snapshot, Worktree: worktree, ResourcePolicy: DefaultResourcePolicy(), ObjectsAvailable: true, IsolationSupported: true}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	missing := request
	missing.ObjectsAvailable = false
	if !errors.Is(missing.Validate(), ErrProposalTargetUnavailable) {
		t.Fatalf("missing object validation = %v", missing.Validate())
	}
	wrongSnapshot := request
	wrongSnapshot.Snapshot.HeadObjectID = repository.ObjectID(strings.Repeat("c", 40))
	if !errors.Is(wrongSnapshot.Validate(), ErrProposalTargetUnavailable) {
		t.Fatalf("wrong snapshot validation = %v", wrongSnapshot.Validate())
	}
}

func editableBranchProposalTarget(t *testing.T, worktree repository.WorktreeRef) repository.ResolvedTarget {
	t.Helper()
	spec, err := repository.NewBranchTargetSpec("main")
	if err != nil {
		t.Fatal(err)
	}
	destination := worktree.ID
	target, err := repository.NewResolvedTarget(repository.ResolvedTarget{
		Spec: spec, Generation: 1,
		Base:           repository.SnapshotRef{Kind: repository.SnapshotCommit, ObjectID: "merge-base"},
		Head:           repository.SnapshotRef{Kind: repository.SnapshotCommit, ObjectID: worktree.CurrentObjectID},
		ResolvedCommit: worktree.CurrentObjectID, ResolvedBaseRef: "base-ref", MergeBase: "merge-base",
		BaseBranchSource: "explicit_branch_flag", BranchRef: "refs/heads/" + worktree.BranchName,
		Editable: true, EditDestination: &destination, Fingerprint: strings.Repeat("d", 64), ResolvedAt: time.Unix(20, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return target
}
