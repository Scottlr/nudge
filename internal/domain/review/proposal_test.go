package review

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

func TestProposalIntentLimitsAndRawPathOrdering(t *testing.T) {
	now := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	base := proposalIntentFixture(now)
	tests := []struct {
		name   string
		mutate func(*ProposalIntent)
		valid  bool
	}{
		{name: "summary exact boundary", mutate: func(intent *ProposalIntent) { intent.Summary = strings.Repeat("s", MaxProposalSummaryBytes) }, valid: true},
		{name: "summary over boundary", mutate: func(intent *ProposalIntent) { intent.Summary = strings.Repeat("s", MaxProposalSummaryBytes+1) }},
		{name: "paths must be sorted", mutate: func(intent *ProposalIntent) {
			intent.ExpectedPaths = []repository.RepoPath{repository.RepoPath("z.go"), repository.RepoPath("a.go")}
		}},
		{name: "duplicate raw key", mutate: func(intent *ProposalIntent) {
			intent.ExpectedPaths = []repository.RepoPath{repository.RepoPath("a.go"), repository.RepoPath("a.go")}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			intent := base
			test.mutate(&intent)
			if got := intent.Validate() == nil; got != test.valid {
				t.Fatalf("Validate() = %v, want %v", got, test.valid)
			}
		})
	}

	paths := make([]repository.RepoPath, 32)
	for index := range paths {
		paths[index] = repository.RepoPath([]byte(strings.Repeat("a", 16-len(string(rune(index)))) + string(rune('0'+index)) + strings.Repeat("b", MaxProposalExpectedPathBytes-16)))
	}
	SortProposalPaths(paths)
	base.ExpectedPaths = paths
	if err := base.Validate(); err != nil {
		t.Fatalf("exact total path boundary rejected: %v", err)
	}
}

func TestProposalStateTransitionsAndImmutablePatchBytes(t *testing.T) {
	if WorkspaceReady.CanTransitionTo(WorkspaceTurnRunning) == false || WorkspaceReady.CanTransitionTo(WorkspaceCreating) {
		t.Fatal("workspace transition table is incorrect")
	}
	if ProposalVersionReady.CanTransitionTo(ProposalVersionApplied) || !ProposalVersionReady.CanTransitionTo(ProposalVersionApplying) {
		t.Fatal("proposal transition table permits an unsafe direct apply")
	}
	if ProposalVersionRejected.CanTransitionTo(ProposalVersionApplied) || ProposalVersionStale.CanTransitionTo(ProposalVersionApplied) {
		t.Fatal("terminal proposal state can become applied")
	}
	if ProposalAttemptDeriving.CanTransitionTo(ProposalAttemptNoChanges) || !ProposalAttemptNoChangesResetting.CanTransitionTo(ProposalAttemptNoChanges) {
		t.Fatal("no-change reset transition table is incorrect")
	}

	patch := proposalPatchFixture()
	original := append([]byte(nil), patch.PatchBytes...)
	created, err := NewProposedPatch(patch)
	if err != nil {
		t.Fatalf("NewProposedPatch: %v", err)
	}
	patch.PatchBytes[0] = 0
	if string(created.PatchBytes) != string(original) {
		t.Fatal("patch constructor did not retain immutable bytes")
	}
	created.PatchBytes[0] ^= 0xff
	if created.Validate() == nil {
		t.Fatal("tampered patch bytes passed its persisted hash")
	}
}

func proposalIntentFixture(now time.Time) ProposalIntent {
	return ProposalIntent{
		ID:              domain.ProposalID("proposal-1"),
		ThreadID:        domain.ReviewThreadID("thread-1"),
		Summary:         "adjust the return value",
		ExpectedPaths:   []repository.RepoPath{repository.RepoPath("a.go"), repository.RepoPath("b.go")},
		AnchorVersionID: 1,
		ConfirmedAgainst: GenerationProvenance{
			SessionID:  domain.ReviewSessionID("session-1"),
			Generation: 1,
			CaptureID:  capturePointer("capture-1"),
			Base:       repository.SnapshotRef{Kind: repository.SnapshotEmpty},
			Head:       repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: domain.WorktreeID("worktree-1"), Fingerprint: "head"},
		},
		ConfirmedAt: now,
	}
}

func proposalPatchFixture() ProposedPatch {
	now := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	data := []byte{0x00, 0xff, 0x0a}
	digest := sha256.Sum256(data)
	path := repository.RepoPath("internal/example.go")
	oldPath := repository.RepoPath("internal/example.go")
	return ProposedPatch{
		ProposalID:       domain.ProposalID("proposal-1"),
		WorkspaceID:      domain.WorkspaceID("workspace-1"),
		ThreadID:         domain.ReviewThreadID("thread-1"),
		AttemptID:        domain.OperationID("attempt-1"),
		SourceGeneration: proposalIntentFixture(now).ConfirmedAgainst,
		Baseline:         SnapshotIdentity{ID: domain.ReviewSnapshotID("baseline-1"), Ref: repository.SnapshotRef{Kind: repository.SnapshotEmpty}, ManifestHash: strings.Repeat("a", 64)},
		Result:           SnapshotIdentity{ID: domain.ReviewSnapshotID("result-1"), Ref: repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: domain.WorktreeID("worktree-1"), Fingerprint: "result"}, ManifestHash: strings.Repeat("b", 64)},
		Destination:      DestinationConstraints{TargetKind: repository.TargetLocal, WorktreeID: domain.WorktreeID("worktree-1"), ExpectedWorkingTreeFingerprint: "destination"},
		Version:          1,
		PatchFormat:      "git-binary-safe",
		PatchBytes:       data,
		PatchSHA256:      hex.EncodeToString(digest[:]),
		Files:            []ProposedFile{{Path: path, OldPath: &oldPath, OldKind: repository.FileKindRegular, Kind: repository.FileKindRegular, OldMode: 0o100644, Mode: 0o100644, Binary: true}},
		Preconditions:    []repository.PathPrecondition{{Path: path, MustExist: true, Kind: repository.FileKindRegular, Mode: 0o100644, ContentHash: strings.Repeat("c", 64)}},
		Scope:            ProposalScopeFocused,
		Status:           ProposalVersionReady,
		CreatedAt:        now,
	}
}

func capturePointer(value string) *domain.CaptureID {
	id := domain.CaptureID(value)
	return &id
}
