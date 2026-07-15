package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
	"github.com/Scottlr/nudge/internal/provider"
)

func TestBuildProposalPromptPreservesIntentAndSafetyDirectives(t *testing.T) {
	intent := proposalPromptIntent()
	context := ProposalTurnContext{Target: "working tree", Path: repository.RepoPath("main.go"), Side: repository.DiffHead, Lines: DiscussionLineRange{Start: 4, End: 6}, SelectedText: "return value", UserConcern: "Handle the error before returning."}
	prompt, err := BuildProposalPrompt(context, intent)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"Request change",
		intent.Summary,
		"Expected paths (scope warnings only",
		"Edit only the isolated proposal result root",
		"Do not touch refs, the index, baseline, admin state, the source worktree, or the destination",
		"complete resulting patch before approval",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt does not contain %q:\n%s", required, prompt)
		}
	}
}

func TestProposalTurnPermissionProfileIsSingleRootAndNoNetwork(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	outside := filepath.Clean(t.TempDir())
	profile := ProposalTurnPermissionProfile{
		ResultRoot:   root,
		RuntimeRoots: []provider.PermissionRoot{{Path: filepath.Join(root, "bin")}},
		Containment:  provider.ContainmentEvidence{CanonicalRead: true, CanonicalWrite: true, NoSymlinkEscape: true, NoJunctionEscape: true, NoMountEscape: true, NoHardLinkAlias: true, HandlesQuiescent: true},
	}
	policy, workingDir, err := profile.Policy()
	if err != nil {
		t.Fatal(err)
	}
	if workingDir != root || policy.Network != provider.NetworkDisabled || len(policy.ReadableRoots) != 1 || len(policy.WritableRoots) != 1 || policy.ReadableRoots[0].Path != root || policy.WritableRoots[0].Path != root {
		t.Fatalf("proposal policy = %+v, cwd=%q", policy, workingDir)
	}
	profile.RuntimeRoots = []provider.PermissionRoot{{Path: outside}}
	if _, _, err := profile.Policy(); !errors.Is(err, ErrProposalTurnUnavailable) {
		t.Fatalf("outside runtime root error = %v, want unavailable", err)
	}
}

func TestValidateProposalRuntimeApprovalRejectsExpansionAndNetwork(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	profile := ProposalTurnPermissionProfile{ResultRoot: root, Containment: provider.ContainmentEvidence{CanonicalRead: true, CanonicalWrite: true, NoSymlinkEscape: true, NoJunctionEscape: true, NoMountEscape: true, NoHardLinkAlias: true, HandlesQuiescent: true}}
	policy, _, err := profile.Policy()
	if err != nil {
		t.Fatal(err)
	}
	inside := RuntimeApproval{Kind: provider.RuntimeApprovalCommand, RequestedScopeID: provider.RuntimeApprovalScope{Kind: provider.RuntimeApprovalCommand, Executable: filepath.Join(root, "tool"), ArgumentsDigest: strings.Repeat("a", 64)}}
	if err := ValidateProposalRuntimeApproval(inside, policy); err != nil {
		t.Fatal(err)
	}
	outside := inside
	outside.RequestedScopeID.Executable = filepath.Join(filepath.Dir(root), "tool")
	if err := ValidateProposalRuntimeApproval(outside, policy); !errors.Is(err, ErrProposalRuntimeApproval) {
		t.Fatalf("outside command error = %v, want denied", err)
	}
	network := inside
	network.NetworkTarget = "example.invalid"
	if err := ValidateProposalRuntimeApproval(network, policy); !errors.Is(err, ErrProposalRuntimeApproval) {
		t.Fatalf("network command error = %v, want denied", err)
	}
}

func TestProposalTurnServiceStartsOnceAndPublishesOnlyAfterQuiescence(t *testing.T) {
	fixture := newProposalTurnFixture(t)
	service, err := NewProposalTurnService(ProposalTurnServiceConfig{Store: fixture.store, Proposals: fixture.store, Lifecycle: fixture.store, Conversation: fixture.conversation, Workspace: fixture.workspace, Clock: fixedClock{when: time.Unix(10, 0).UTC()}})
	if err != nil {
		t.Fatal(err)
	}
	command := fixture.request()
	started, err := service.Start(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.provider.startTurns != 1 || started.Turn == nil || len(started.Events) == 0 {
		t.Fatalf("start commit = %#v, provider starts = %d", started, fixture.provider.startTurns)
	}
	if _, err := service.Start(context.Background(), command); !errors.Is(err, ErrProposalTurnConflict) {
		t.Fatalf("duplicate start error = %v, want conflict", err)
	}
	finished, err := service.Finish(context.Background(), FinishProposalTurn{AttemptID: started.Attempt.ID, ProposalID: started.Attempt.ProposalID, ThreadID: started.Attempt.ThreadID, TurnID: started.Turn.ID, OperationID: command.OperationID, CorrelationID: command.CorrelationID, Outcome: ProposalTurnSucceeded})
	if err != nil {
		t.Fatal(err)
	}
	if finished.Workspace.State != review.WorkspaceResultReady || fixture.lease.closeCount != 1 || fixture.lease.quiesceCount != 1 {
		t.Fatalf("finish = %#v, lease close/quiesce = %d/%d", finished, fixture.lease.closeCount, fixture.lease.quiesceCount)
	}
	if _, ok := finished.Events[0].(ProposalResultReady); !ok {
		t.Fatalf("terminal event = %T, want ProposalResultReady", finished.Events[0])
	}
	if _, err := service.Finish(context.Background(), FinishProposalTurn{AttemptID: started.Attempt.ID, ProposalID: started.Attempt.ProposalID, ThreadID: started.Attempt.ThreadID, TurnID: started.Turn.ID, OperationID: command.OperationID, CorrelationID: command.CorrelationID, Outcome: ProposalTurnSucceeded}); !errors.Is(err, ErrProposalTurnNotFound) {
		t.Fatalf("duplicate finish error = %v, want not found", err)
	}
}

func TestProposalTurnRequiresCurrentHeadEligibilityForPinnedTargets(t *testing.T) {
	fixture := newProposalTurnFixture(t)
	commitGeneration := review.GenerationProvenance{
		SessionID: fixture.store.aggregate.Intent.ConfirmedAgainst.SessionID, Generation: 2,
		Base: repository.SnapshotRef{Kind: repository.SnapshotEmpty}, Head: repository.SnapshotRef{Kind: repository.SnapshotCommit, ObjectID: "head-pinned"},
	}
	fixture.store.aggregate.Intent.ConfirmedAgainst = commitGeneration
	fixture.store.aggregate.Workspace.SourceGeneration = commitGeneration
	command := fixture.request()
	command.Intent = fixture.store.aggregate.Intent
	if err := validateProposalAggregateForRequest(fixture.store.aggregate, command); !errors.Is(err, ErrProposalTurnUnavailable) {
		t.Fatalf("missing target eligibility error = %v", err)
	}
	command.Eligibility = &ProposalEligibility{Eligible: true, Reason: ProposalEligible, TargetKind: repository.TargetCommit, WorktreeID: fixture.store.aggregate.Workspace.WorktreeID, ExpectedHead: "head-pinned", ObservedHead: "head-pinned"}
	if err := validateProposalAggregateForRequest(fixture.store.aggregate, command); err != nil {
		t.Fatalf("eligible target validation error = %v", err)
	}
}

type proposalTurnFixture struct {
	store        *proposalTurnStore
	provider     *proposalProviderFake
	conversation *ProviderConversationService
	workspace    *proposalWorkspaceFake
	lease        *proposalLeaseFake
}

func newProposalTurnFixture(t *testing.T) *proposalTurnFixture {
	t.Helper()
	intent := proposalPromptIntent()
	now := time.Unix(2, 0).UTC()
	resultRoot := filepath.Clean(filepath.Join(t.TempDir(), "result"))
	workspaceID := domain.WorkspaceID("workspace-1")
	workspace := review.ProposalWorkspace{ID: workspaceID, RepositoryID: domain.RepositoryID("repo-1"), WorktreeID: domain.WorktreeID("worktree-1"), SessionID: intent.ConfirmedAgainst.SessionID, SourceThreadID: intent.ThreadID, SourceGeneration: intent.ConfirmedAgainst, Roots: review.WorkspaceRoots{Baseline: filepath.Clean(filepath.Join(t.TempDir(), "baseline")), Admin: filepath.Clean(filepath.Join(t.TempDir(), "admin")), Result: resultRoot, Destination: filepath.Clean(filepath.Join(t.TempDir(), "destination"))}, PolicyVersion: 1, State: review.WorkspaceReady, CreatedAt: now, UpdatedAt: now}
	proposal := review.Proposal{ID: intent.ID, WorkspaceID: workspace.ID, ThreadID: intent.ThreadID, Status: review.ProposalVersionDeriving, CreatedAt: now, UpdatedAt: now}
	manifest, err := NewWorkspaceManifest([]WorkspaceManifestEntry{{Path: []byte("main.go"), Kind: repository.FileKindRegular, Mode: 0o100644, Bytes: 1, SHA256: strings.Repeat("c", 64)}})
	if err != nil {
		t.Fatal(err)
	}
	thread := review.ReviewThread{ID: intent.ThreadID, SessionID: intent.ConfirmedAgainst.SessionID, Anchor: threadTestAnchor(t), Resolution: review.ResolutionOpen, Conversation: review.ConversationIdle, Proposal: review.ProposalNone, Read: review.Unread, CreatedAt: now, UpdatedAt: now}
	if err := thread.Validate(); err != nil {
		t.Fatal(err)
	}
	lifecycle := ProposalWorkspaceLifecycle{WorkspaceID: workspace.ID, RepositoryID: workspace.RepositoryID, WorktreeID: workspace.WorktreeID, SessionID: workspace.SessionID, ThreadID: workspace.SourceThreadID, OperationID: domain.OperationID("lifecycle-1"), Owner: "test-owner", Nonce: strings.Repeat("a", 64), CapacityReservationMarker: "capacity-1", Purpose: WorkspacePurposeInstallBaseline, Phase: WorkspaceLifecycleReady, Source: WorkspaceSourceIdentity{Kind: "accepted_capture", ID: "capture-1", ManifestHash: strings.Repeat("a", 64), Generation: 1, Fingerprint: strings.Repeat("b", 64)}, Baseline: manifest, Result: manifest.Clone(), CreatedAt: now, UpdatedAt: now}
	if err := lifecycle.Validate(); err != nil {
		t.Fatal(err)
	}
	store := newProposalTurnStore(threadTestGuard(), review.ProposalAggregate{Workspace: workspace, Intent: intent, Proposal: proposal}, lifecycle)
	store.thread = thread
	providerFake := &proposalProviderFake{}
	conversation, err := NewProviderConversationService(ProviderConversationServiceConfig{Provider: providerFake, Persistence: PersistenceNoPersist, ProviderVersion: "codex-test"})
	if err != nil {
		t.Fatal(err)
	}
	conversationCommit, err := conversation.EnsureConversation(context.Background(), EnsureProviderConversation{Guard: threadTestGuard(), ThreadID: thread.ID, Thread: thread, Mode: provider.TurnDiscuss, WorkingDir: "", Permissions: provider.TurnPermissionPolicy{Filesystem: provider.FilesystemPromptOnly, Network: provider.NetworkDisabled, RuntimeApprovals: provider.RuntimeApprovalsDisabled}, OperationID: domain.OperationID("conversation-op"), CorrelationID: CorrelationID("conversation-correlation")})
	if err != nil {
		t.Fatal(err)
	}
	conversationID := conversationCommit.Conversation.ID
	thread.ProviderConversationID = &conversationID
	store.thread = thread
	lease := &proposalLeaseFake{profile: ProposalTurnPermissionProfile{ResultRoot: resultRoot, Containment: provider.ContainmentEvidence{CanonicalRead: true, CanonicalWrite: true, NoSymlinkEscape: true, NoJunctionEscape: true, NoMountEscape: true, NoHardLinkAlias: true, HandlesQuiescent: true}}}
	return &proposalTurnFixture{store: store, provider: providerFake, conversation: conversation, workspace: &proposalWorkspaceFake{lease: lease}, lease: lease}
}

func (f *proposalTurnFixture) request() RequestProposal {
	intent := f.store.aggregate.Intent
	return RequestProposal{Guard: threadTestGuard(), ThreadID: intent.ThreadID, ProposalID: intent.ID, ConversationID: *f.store.thread.ProviderConversationID, Intent: intent, Context: ProposalTurnContext{Target: "working tree", Path: repository.RepoPath("main.go"), Side: repository.DiffHead, Lines: DiscussionLineRange{Start: 4, End: 6}, SelectedText: "return value", UserConcern: intent.Summary}, OperationID: domain.OperationID("attempt-1"), CorrelationID: CorrelationID("proposal-correlation")}
}

type proposalProviderFake struct {
	mu         sync.Mutex
	startTurns int
}

func (f *proposalProviderFake) Probe(context.Context) (ProviderStatus, error) {
	gate := NewProviderDataDisclosureGate()
	_ = gate.Acknowledge(ProviderDisclosureVersionV1, time.Unix(1, 0), DisclosureProcessOnly)
	return ProviderStatus{Connection: ProviderConnected, Account: ProviderAccountStatus{State: ProviderAccountAuthenticated}, Disclosure: gate}, nil
}
func (f *proposalProviderFake) StartConversation(context.Context, provider.StartConversationRequest) (provider.ProviderConversationRef, error) {
	return provider.ProviderConversationRef("remote-conversation"), nil
}
func (f *proposalProviderFake) ResumeConversation(context.Context, provider.ProviderConversationRef) error {
	return nil
}
func (f *proposalProviderFake) StartTurn(_ context.Context, _ provider.ProviderConversationRef, request provider.TurnRequest) (provider.ProviderTurnRef, error) {
	f.mu.Lock()
	f.startTurns++
	f.mu.Unlock()
	if request.Mode != provider.TurnPropose || request.Permissions.Filesystem != provider.FilesystemProposalResult {
		return "", provider.ErrInvalidPermission
	}
	return provider.ProviderTurnRef("remote-turn"), nil
}
func (f *proposalProviderFake) SteerTurn(context.Context, provider.ProviderTurnRef, string) error {
	return nil
}
func (f *proposalProviderFake) CancelTurn(context.Context, provider.ProviderTurnRef) error {
	return nil
}

type proposalWorkspaceFake struct{ lease ProposalTurnLease }

func (f *proposalWorkspaceFake) AcquireProposalTurn(context.Context, review.ProposalWorkspace) (ProposalTurnLease, error) {
	return f.lease, nil
}

type proposalLeaseFake struct {
	profile      ProposalTurnPermissionProfile
	closeCount   int
	quiesceCount int
	terminated   int
}

func (f *proposalLeaseFake) PermissionProfile() ProposalTurnPermissionProfile { return f.profile }
func (f *proposalLeaseFake) Quiesce(context.Context) (ProposalQuiescenceProof, error) {
	f.quiesceCount++
	return ProposalQuiescenceProof{DescendantsEmpty: true, WritableHandlesClosed: true, ResultRootStable: true}, nil
}
func (f *proposalLeaseFake) Terminate(context.Context) error { f.terminated++; return nil }
func (f *proposalLeaseFake) Close() error                    { f.closeCount++; return nil }

type proposalTurnStore struct {
	*threadTestStore
	aggregate review.ProposalAggregate
	lifecycle ProposalWorkspaceLifecycle
	thread    review.ReviewThread
}

func newProposalTurnStore(guard SessionWriteGuard, aggregate review.ProposalAggregate, lifecycle ProposalWorkspaceLifecycle) *proposalTurnStore {
	base := newThreadTestStore()
	base.guards[guard.SessionID] = guard
	return &proposalTurnStore{threadTestStore: base, aggregate: aggregate, lifecycle: lifecycle}
}

func (s *proposalTurnStore) LoadThread(context.Context, domain.ReviewThreadID) (review.ReviewThread, error) {
	return s.thread, nil
}
func (s *proposalTurnStore) LoadProposalAggregate(context.Context, domain.ProposalID) (review.ProposalAggregate, error) {
	return s.aggregate, nil
}
func (s *proposalTurnStore) LoadProposalWorkspaceLifecycle(context.Context, domain.WorkspaceID, domain.OperationID) (ProposalWorkspaceLifecycle, error) {
	return s.lifecycle, nil
}
func (s *proposalTurnStore) LoadLatestProposalWorkspaceLifecycle(context.Context, domain.WorkspaceID) (ProposalWorkspaceLifecycle, error) {
	return s.lifecycle, nil
}
func (s *proposalTurnStore) WithSessionTx(_ context.Context, guard SessionWriteGuard, fn func(ReviewStoreTx) error) (SessionWriteGuard, error) {
	current := s.guards[guard.SessionID]
	if current != guard {
		return guard, ErrSessionRevisionConflict
	}
	tx := &proposalTurnTx{store: s, threadTestTx: &threadTestTx{threads: s.threads, messages: s.messages, sessionID: guard.SessionID}}
	if err := fn(tx); err != nil {
		return guard, err
	}
	current.ExpectedRevision++
	s.guards[guard.SessionID] = current
	return current, nil
}

type proposalTurnTx struct {
	*threadTestTx
	store *proposalTurnStore
}

func (t *proposalTurnTx) SaveThread(_ context.Context, value review.ReviewThread) error {
	t.store.thread = value
	return nil
}
func (t *proposalTurnTx) RecordProposalAttempt(_ context.Context, value review.ProposalAttempt) error {
	for index := range t.store.aggregate.Attempts {
		if t.store.aggregate.Attempts[index].ID == value.ID {
			t.store.aggregate.Attempts[index] = value
			return nil
		}
	}
	t.store.aggregate.Attempts = append(t.store.aggregate.Attempts, value)
	return nil
}
func (t *proposalTurnTx) CreateWorkspace(context.Context, review.ProposalWorkspace, review.ProposalIntent, review.Proposal) error {
	return nil
}
func (t *proposalTurnTx) RecordNoChanges(context.Context, review.ProposalAttempt) error { return nil }
func (t *proposalTurnTx) PublishProposal(context.Context, review.ProposedPatch) error   { return nil }
func (t *proposalTurnTx) TransitionProposal(_ context.Context, value review.ProposalTransition) error {
	for index := range t.store.aggregate.Versions {
		if t.store.aggregate.Versions[index].Version != value.Version || t.store.aggregate.Versions[index].ProposalID != value.ProposalID {
			continue
		}
		current := t.store.aggregate.Versions[index].Status
		if !current.CanTransitionTo(value.Status) {
			return review.ErrInvalidProposalTransition
		}
		changedAt := value.ChangedAt
		t.store.aggregate.Versions[index].Status = value.Status
		t.store.aggregate.Versions[index].StatusReason = value.Reason
		t.store.aggregate.Versions[index].StatusChangedAt = &changedAt
		t.store.aggregate.Proposal.Status = value.Status
		t.store.aggregate.Proposal.CurrentVersion = proposalVersionPointer(value.Version)
		if value.ApplyOperationID != "" {
			t.store.aggregate.Proposal.ApplyingOperationID = operationIDPointer(value.ApplyOperationID)
		}
		return nil
	}
	return ErrReviewStoreNotFound
}
func (t *proposalTurnTx) UpdateProposalWorkspace(_ context.Context, value review.ProposalWorkspace) error {
	t.store.aggregate.Workspace = value
	return nil
}
func (t *proposalTurnTx) CreateProposalWorkspaceLifecycle(_ context.Context, value ProposalWorkspaceLifecycle) error {
	t.store.lifecycle = value
	return nil
}
func (t *proposalTurnTx) UpdateProposalWorkspaceLifecycle(_ context.Context, value ProposalWorkspaceLifecycle) error {
	t.store.lifecycle = value
	return nil
}

var _ ProviderConversationPort = (*proposalProviderFake)(nil)
var _ ProposalTurnWorkspace = (*proposalWorkspaceFake)(nil)
var _ ProposalTurnLease = (*proposalLeaseFake)(nil)

func proposalPromptIntent() review.ProposalIntent {
	capture := domain.CaptureID("capture-1")
	return review.ProposalIntent{
		ID:              domain.ProposalID("proposal-1"),
		ThreadID:        domain.ReviewThreadID("thread-1"),
		Summary:         "Handle the selected error",
		ExpectedPaths:   []repository.RepoPath{repository.RepoPath("main.go")},
		AnchorVersionID: 1,
		ConfirmedAgainst: review.GenerationProvenance{
			SessionID:  domain.ReviewSessionID("session-1"),
			Generation: 1,
			CaptureID:  &capture,
			Base:       repository.SnapshotRef{Kind: repository.SnapshotEmpty},
			Head:       repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: domain.WorktreeID("worktree-1"), Fingerprint: "fingerprint-1"},
		},
		ConfirmedAt: time.Unix(1, 0).UTC(),
	}
}
