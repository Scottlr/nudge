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

func TestMarkProposalStaleRetainsImmutablePatchAndReason(t *testing.T) {
	fixture := newProposalTurnFixture(t)
	now := time.Now().UTC()
	patch := readyProposalForDisposition(fixture, now)
	originalBytes := append([]byte(nil), patch.PatchBytes...)

	if err := MarkProposalStale(&patch, StaleReasonPathPreconditionChanged, now.Add(time.Minute)); err != nil {
		t.Fatalf("MarkProposalStale() error = %v", err)
	}
	if patch.Status != review.ProposalVersionStale || patch.StatusReason != string(StaleReasonPathPreconditionChanged) || string(patch.PatchBytes) != string(originalBytes) {
		t.Fatalf("stale patch = %#v, bytes changed or reason missing", patch)
	}
	if err := MarkProposalStale(&patch, StaleReasonTargetHeadChanged, now.Add(2*time.Minute)); !errors.Is(err, ErrProposalValidityTransition) {
		t.Fatalf("rewriting stale reason error = %v, want transition error", err)
	}
}

func TestProposalRefreshReusesConversationAndRetainsOldVersion(t *testing.T) {
	fixture := newProposalTurnFixture(t)
	now := time.Unix(3, 0).UTC()
	patch := readyProposalForDisposition(fixture, now)
	if err := MarkProposalStale(&patch, StaleReasonPathPreconditionChanged, now); err != nil {
		t.Fatal(err)
	}
	fixture.store.aggregate.Versions = []review.ProposedPatch{patch}
	fixture.store.aggregate.Proposal.Status = review.ProposalVersionStale
	fixture.store.aggregate.Proposal.CurrentVersion = proposalVersionPointer(patch.Version)
	newCapture := domain.CaptureID("capture-2")
	provenance := fixture.store.aggregate.Intent.ConfirmedAgainst
	provenance.Generation = 2
	provenance.CaptureID = &newCapture
	provenance.Head.Fingerprint = "fingerprint-2"
	intent := fixture.store.aggregate.Intent
	intent.ConfirmedAgainst = provenance
	intent.ConfirmedAt = now
	workspace := fixture.store.aggregate.Workspace
	workspace.SourceGeneration = provenance
	lifecycle := fixture.store.lifecycle
	lifecycle.OperationID = "refresh-lifecycle"
	lifecycle.Purpose = WorkspacePurposeRefreshBaseline
	lifecycle.Source = WorkspaceSourceIdentity{Kind: "accepted_capture", ID: "capture-2", ManifestHash: strings.Repeat("a", 64), Generation: 2, Fingerprint: strings.Repeat("b", 64)}
	lifecycle.UpdatedAt = now
	refreshWorkspace := &proposalRefreshWorkspaceFake{result: ProposalRefreshWorkspaceResult{Workspace: workspace, Lifecycle: lifecycle}}
	turns, err := NewProposalTurnService(ProposalTurnServiceConfig{Store: fixture.store, Proposals: fixture.store, Lifecycle: fixture.store, Conversation: fixture.conversation, Workspace: fixture.workspace, Clock: fixedClock{when: now}})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewProposalRefreshService(ProposalRefreshServiceConfig{Store: fixture.store, Proposals: fixture.store, Lifecycle: fixture.store, Workspace: refreshWorkspace, Turns: turns, Clock: fixedClock{when: now}})
	if err != nil {
		t.Fatal(err)
	}
	command := RefreshProposal{Guard: threadTestGuard(), ThreadID: intent.ThreadID, ProposalID: intent.ID, Version: patch.Version, ConversationID: *fixture.store.thread.ProviderConversationID, Intent: intent, Context: fixture.request().Context, Provenance: provenance, OperationID: "refresh-operation", CorrelationID: "refresh-correlation"}
	source := refreshTreeSource{identity: WorkspaceSourceIdentity{Kind: "accepted_capture", ID: "capture-2", ManifestHash: strings.Repeat("a", 64)}}
	commit, err := service.Refresh(context.Background(), command, source)
	if err != nil {
		t.Fatal(err)
	}
	if commit.ConversationID != command.ConversationID || commit.Turn.Turn == nil || fixture.provider.startTurns != 1 || refreshWorkspace.calls != 1 {
		t.Fatalf("refresh commit = %#v, starts=%d calls=%d", commit, fixture.provider.startTurns, refreshWorkspace.calls)
	}
	if len(fixture.store.aggregate.Versions) != 1 || fixture.store.aggregate.Versions[0].Status != review.ProposalVersionStale || fixture.store.aggregate.Intent.ConfirmedAgainst.Generation != 2 {
		t.Fatalf("aggregate after refresh = %#v", fixture.store.aggregate)
	}
}

type proposalRefreshWorkspaceFake struct {
	result ProposalRefreshWorkspaceResult
	calls  int
}

func (f *proposalRefreshWorkspaceFake) RefreshProposalWorkspace(_ context.Context, _ ProposalRefreshWorkspaceRequest) (ProposalRefreshWorkspaceResult, error) {
	f.calls++
	return f.result, nil
}

type refreshTreeSource struct{ identity WorkspaceSourceIdentity }

func (s refreshTreeSource) Identity() WorkspaceSourceIdentity { return s.identity }
func (s refreshTreeSource) List(context.Context) ([]repository.TreeEntry, error) {
	return nil, nil
}
func (s refreshTreeSource) Open(context.Context, repository.TreeEntry) (io.ReadCloser, error) {
	return nil, errors.New("not used")
}
