package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/diff"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

func TestDeriveProposalUsesArtifactIdentityAndFlagsBroaderScope(t *testing.T) {
	fixture := proposalDerivationFixture(t)
	patch, err := DeriveProposal(fixture.input)
	if err != nil {
		t.Fatalf("derive: %v aggregate=%v snapshot=%v destination=%v", err, fixture.input.Aggregate.Validate(), fixture.input.Snapshot.Validate(), fixture.input.Destination.Validate())
	}
	if patch.Artifact.ArtifactID != fixture.artifact.ID || len(patch.PatchBytes) != 0 || patch.PatchSHA256 != fixture.artifact.PatchSHA256 {
		t.Fatalf("patch artifact reference = %#v", patch.Artifact)
	}
	if patch.Scope != review.ProposalScopeBroader || len(patch.Files) != 2 || len(patch.Preconditions) != 2 {
		t.Fatalf("derived patch = %#v", patch)
	}
	preconditionsByPath := make(map[repository.RepoPathKey]repository.PathPrecondition, len(patch.Preconditions))
	for _, precondition := range patch.Preconditions {
		preconditionsByPath[precondition.Path.Key()] = precondition
	}
	if preconditionsByPath[repository.RepoPath("main.go").Key()].ContentHash != fixture.baselineEntry.SHA256 || preconditionsByPath[repository.RepoPath("extra.go").Key()].MustExist {
		t.Fatalf("derived preconditions = %#v", patch.Preconditions)
	}

	stale := fixture.input
	stale.DestinationPreconditions = append([]repository.PathPrecondition(nil), fixture.input.DestinationPreconditions...)
	stale.DestinationPreconditions[0].ContentHash = strings.Repeat("f", 64)
	if _, err := DeriveProposal(stale); !errors.Is(err, ErrProposalPublicationStale) {
		t.Fatalf("stale destination error = %v", err)
	}
}

func TestProposalPublicationNoChangesResetsBeforeTerminalOutcome(t *testing.T) {
	fixture := proposalDerivationFixture(t)
	emptySnapshot := emptyResultSnapshot(t, fixture)
	guard := SessionWriteGuard{SessionID: "session-1", LeaseID: "lease-1", WriterEpoch: 1, ExpectedRevision: 1}
	store := newProposalTurnStore(guard, fixture.input.Aggregate, ProposalWorkspaceLifecycle{})
	fake := &proposalPublicationFake{proposalTurnStore: store, snapshot: emptySnapshot}
	reset := &proposalResetFake{}
	service, err := NewProposalPublicationService(ProposalPublicationServiceConfig{
		Store: store, Proposals: fake, Snapshots: fake, Artifacts: fake, Resetter: reset,
		Clock: publicationTestClock{now: fixture.input.CreatedAt.Add(time.Minute)},
	})
	if err != nil {
		t.Fatal(err)
	}
	commit, err := service.Publish(context.Background(), ProposalPublicationRequest{
		Guard: guard, ProposalID: fixture.input.Aggregate.Proposal.ID, AttemptID: fixture.input.AttemptID,
		Destination: fixture.input.Destination,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !commit.NoChanges || commit.Patch != nil || commit.Attempt.Outcome != review.ProposalAttemptNoChanges || commit.Attempt.ResultDisposition != review.ProposalResultDiscarded || len(store.aggregate.Versions) != 0 {
		t.Fatalf("no-change publication = %#v", commit)
	}
	if reset.calls != 1 || reset.request.SessionID != guard.SessionID || reset.request.ProposalID != fixture.input.Aggregate.Proposal.ID || reset.request.WorkspaceID != fixture.input.Aggregate.Workspace.ID || reset.request.AttemptID != fixture.input.AttemptID || reset.request.Baseline != emptySnapshot.Baseline {
		t.Fatalf("reset request = %#v, calls=%d", reset.request, reset.calls)
	}
}

type proposalDerivationFixtureValue struct {
	input         ProposalDerivationInput
	artifact      ProposalPatchArtifact
	baselineEntry WorkspaceManifestEntry
}

func proposalDerivationFixture(t *testing.T) proposalDerivationFixtureValue {
	t.Helper()
	now := time.Date(2026, time.July, 15, 15, 0, 0, 0, time.UTC)
	mainBefore := []byte("before\n")
	mainAfter := []byte("after\n")
	extra := []byte("extra\n")
	mainPath := []byte("main.go")
	extraPath := []byte("extra.go")
	baselineEntry := WorkspaceManifestEntry{Path: mainPath, Kind: repository.FileKindRegular, Mode: 0o100644, Bytes: uint64(len(mainBefore)), SHA256: proposalHash(mainBefore)}
	baseline, err := NewWorkspaceManifest([]WorkspaceManifestEntry{baselineEntry})
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	resultManifest, err := NewResultManifest([]ResultSnapshotEntry{
		{Path: mainPath, Kind: repository.FileKindRegular, Mode: 0o100644, Bytes: uint64(len(mainAfter)), SHA256: proposalHash(mainAfter), NativeIdentityHash: strings.Repeat("1", 64), Complete: true},
		{Path: extraPath, Kind: repository.FileKindRegular, Mode: 0o100644, Bytes: uint64(len(extra)), SHA256: proposalHash(extra), NativeIdentityHash: strings.Repeat("2", 64), Complete: true},
	}, DefaultResourcePolicy().Version, true, ResultReasonNone)
	if err != nil {
		t.Fatalf("result manifest: %v", err)
	}
	delta, err := CompareResultManifest(baseline, resultManifest)
	if err != nil {
		t.Fatalf("delta: %v", err)
	}
	normalized, err := NewResultSnapshot(ResultSnapshot{
		SessionID: "session-1", ProposalID: "proposal-1", WorkspaceID: "workspace-1", WorktreeID: "worktree-1", AttemptID: "attempt-1", ThreadID: "thread-1", ProviderTurnID: "turn-1", ProviderTurnRef: "turn-ref",
		Baseline: review.SnapshotIdentity{ID: "baseline-1", Ref: repository.SnapshotRef{Kind: repository.SnapshotEmpty}, ManifestHash: baseline.Hash},
		Result:   review.SnapshotIdentity{ID: "result-1", Ref: repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: "worktree-1", Fingerprint: resultManifest.Hash}, ManifestHash: resultManifest.Hash},
		Manifest: resultManifest, Delta: delta, PolicyVersion: DefaultResourcePolicy().Version, IsolationVersion: 1, LeaseNonce: strings.Repeat("3", 64), State: ResultSnapshotReady, Reason: ResultReasonNone, CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("result snapshot: %v", err)
	}
	mainOld, mainNew, extraNew := repository.RepoPath(mainPath), repository.RepoPath(mainPath), repository.RepoPath(extraPath)
	entries := []diff.PatchIndexEntry{
		{Version: diff.PatchIndexVersion, SourceID: "patch-source", Index: 0, Offset: 0, Length: 1, HeaderLength: 1, File: repository.ChangedFile{OldPath: &mainOld, NewPath: &mainNew, Kind: repository.ChangeModified, OldFileKind: repository.FileKindRegular, NewFileKind: repository.FileKindRegular, OldMode: 0o100644, NewMode: 0o100644}, SHA256: strings.Repeat("4", 64)},
		{Version: diff.PatchIndexVersion, SourceID: "patch-source", Index: 1, Offset: 1, Length: 1, HeaderLength: 1, File: repository.ChangedFile{NewPath: &extraNew, Kind: repository.ChangeAdded, NewFileKind: repository.FileKindRegular, NewMode: 0o100644}, SHA256: strings.Repeat("5", 64)},
	}
	index, err := NewProposalReviewIndex(diff.PatchIndexIdentity{Version: diff.PatchIndexVersion, SourceID: "patch-source", Size: 2, SHA256: strings.Repeat("6", 64), FileCount: 2}, entries)
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	limits, err := DefaultSpoolLimits(DefaultResourcePolicy())
	if err != nil {
		t.Fatalf("limits: %v", err)
	}
	artifact, err := NewProposalPatchArtifact(ProposalPatchArtifact{
		SessionID: normalized.SessionID, ProposalID: normalized.ProposalID, WorkspaceID: normalized.WorkspaceID, AttemptID: normalized.AttemptID, ThreadID: normalized.ThreadID,
		Baseline: normalized.Baseline, Result: normalized.Result, BaselineSnapshotID: normalized.Baseline.ID, ResultSnapshotID: normalized.ID,
		PatchFormatVersion: ProposalPatchFormatVersion, RenamePolicyVersion: 1, ConversionPolicyVersion: 1, ConversionFingerprint: strings.Repeat("7", 64), ResourcePolicyVersion: DefaultResourcePolicy().Version,
		Published:   PublishedArtifact{Identity: ArtifactIdentity{SpoolID: "spool-1", ManifestHash: strings.Repeat("8", 64), Bytes: 2, Entries: 1, Complete: true, VerifiedAt: now}, Target: PublishTarget{OwnerKind: OwnerProposal, RelativePath: "patch", SourceRelativePath: "patch"}, Limits: limits},
		PatchSHA256: strings.Repeat("6", 64), Index: index, Summary: ProposalPatchSummary{FileCount: 2, PatchBytes: 2}, CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("artifact: %v", err)
	}
	generation := review.GenerationProvenance{SessionID: "session-1", Generation: 1, CaptureID: captureIDPointer("capture-1"), Base: repository.SnapshotRef{Kind: repository.SnapshotEmpty}, Head: repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: "worktree-1", Fingerprint: "head"}}
	aggregate := review.ProposalAggregate{
		Workspace: review.ProposalWorkspace{ID: "workspace-1", RepositoryID: "repo-1", WorktreeID: "worktree-1", SessionID: "session-1", SourceThreadID: "thread-1", SourceGeneration: generation, Roots: review.WorkspaceRoots{Baseline: "baseline", Admin: "admin", Result: "result", Destination: "destination"}, PolicyVersion: 1, State: review.WorkspaceReady, CreatedAt: now, UpdatedAt: now},
		Intent:    review.ProposalIntent{ID: "proposal-1", ThreadID: "thread-1", Summary: "change main", ExpectedPaths: []repository.RepoPath{repository.RepoPath(mainPath)}, AnchorVersionID: 1, ConfirmedAgainst: generation, ConfirmedAt: now},
		Proposal:  review.Proposal{ID: "proposal-1", WorkspaceID: "workspace-1", ThreadID: "thread-1", Status: review.ProposalVersionDeriving, CreatedAt: now, UpdatedAt: now},
		Attempts:  []review.ProposalAttempt{{ID: "attempt-1", ProposalID: "proposal-1", WorkspaceID: "workspace-1", ThreadID: "thread-1", SourceGeneration: generation, Outcome: review.ProposalAttemptDeriving, ResultDisposition: review.ProposalResultNone, StartedAt: now}},
	}
	destination := review.DestinationConstraints{TargetKind: repository.TargetLocal, WorktreeID: "worktree-1", ExpectedWorkingTreeFingerprint: "destination"}
	preconditions := []repository.PathPrecondition{
		{Path: repository.RepoPath(mainPath), MustExist: true, Kind: repository.FileKindRegular, Mode: 0o100644, ContentHash: baselineEntry.SHA256},
		{Path: repository.RepoPath(extraPath)},
	}
	return proposalDerivationFixtureValue{input: ProposalDerivationInput{Aggregate: aggregate, AttemptID: "attempt-1", Snapshot: normalized, Artifact: artifact, Destination: destination, DestinationPreconditions: preconditions, CreatedAt: now}, artifact: artifact, baselineEntry: baselineEntry}
}

func emptyResultSnapshot(t *testing.T, fixture proposalDerivationFixtureValue) ResultSnapshot {
	t.Helper()
	entry := ResultSnapshotEntry{Path: fixture.baselineEntry.Path, Kind: fixture.baselineEntry.Kind, Mode: fixture.baselineEntry.Mode, Bytes: fixture.baselineEntry.Bytes, SHA256: fixture.baselineEntry.SHA256, NativeIdentityHash: strings.Repeat("9", 64), Complete: true}
	manifest, err := NewResultManifest([]ResultSnapshotEntry{entry}, DefaultResourcePolicy().Version, true, ResultReasonNone)
	if err != nil {
		t.Fatalf("empty result manifest: %v", err)
	}
	delta, err := CompareResultManifest(mustWorkspaceManifest(t, fixture.baselineEntry), manifest)
	if err != nil {
		t.Fatalf("empty delta: %v", err)
	}
	snapshot, err := NewResultSnapshot(ResultSnapshot{
		SessionID: fixture.input.Snapshot.SessionID, ProposalID: fixture.input.Snapshot.ProposalID, WorkspaceID: fixture.input.Snapshot.WorkspaceID, WorktreeID: fixture.input.Snapshot.WorktreeID, AttemptID: fixture.input.Snapshot.AttemptID, ThreadID: fixture.input.Snapshot.ThreadID, ProviderTurnID: fixture.input.Snapshot.ProviderTurnID, ProviderTurnRef: fixture.input.Snapshot.ProviderTurnRef,
		Baseline: fixture.input.Snapshot.Baseline,
		Result:   review.SnapshotIdentity{ID: "result-empty", Ref: repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: fixture.input.Snapshot.WorktreeID, Fingerprint: manifest.Hash}, ManifestHash: manifest.Hash},
		Manifest: manifest, Delta: delta, PolicyVersion: DefaultResourcePolicy().Version, IsolationVersion: fixture.input.Snapshot.IsolationVersion, LeaseNonce: strings.Repeat("a", 64), State: ResultSnapshotReady, Reason: ResultReasonNone, CreatedAt: fixture.input.CreatedAt,
	})
	if err != nil {
		t.Fatalf("empty result snapshot: %v", err)
	}
	return snapshot
}

func mustWorkspaceManifest(t *testing.T, entry WorkspaceManifestEntry) WorkspaceManifest {
	t.Helper()
	manifest, err := NewWorkspaceManifest([]WorkspaceManifestEntry{entry})
	if err != nil {
		t.Fatalf("workspace manifest: %v", err)
	}
	return manifest
}

type proposalPublicationFake struct {
	*proposalTurnStore
	snapshot ResultSnapshot
}

func (f *proposalPublicationFake) LoadResultSnapshot(_ context.Context, id domain.ReviewSnapshotID) (ResultSnapshot, error) {
	if id != f.snapshot.ID {
		return ResultSnapshot{}, ErrResultSnapshotNotFound
	}
	return f.snapshot, nil
}

func (f *proposalPublicationFake) LoadResultSnapshotForAttempt(_ context.Context, id domain.OperationID) (ResultSnapshot, error) {
	if id != f.snapshot.AttemptID {
		return ResultSnapshot{}, ErrResultSnapshotNotFound
	}
	return f.snapshot, nil
}

func (f *proposalPublicationFake) LoadProposalPatchArtifact(_ context.Context, id string) (ProposalPatchArtifact, error) {
	return ProposalPatchArtifact{}, ErrProposalPatchArtifactNotFound
}

func (f *proposalPublicationFake) LoadProposalPatchArtifactForAttempt(_ context.Context, id domain.OperationID) (ProposalPatchArtifact, error) {
	return ProposalPatchArtifact{}, ErrProposalPatchArtifactNotFound
}

type proposalResetFake struct {
	request ProposalBaselineResetRequest
	calls   int
}

func (f *proposalResetFake) ResetToBaseline(_ context.Context, request ProposalBaselineResetRequest) error {
	f.request = request
	f.calls++
	return nil
}

type publicationTestClock struct{ now time.Time }

func (c publicationTestClock) Now() time.Time { return c.now }

func proposalHash(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func captureIDPointer(value string) *domain.CaptureID {
	id := domain.CaptureID(value)
	return &id
}
