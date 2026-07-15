package app

import (
	"errors"
	"testing"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

func TestCapabilityPolicyV1IsExhaustiveAndPermanentFalseCellsStayFalse(t *testing.T) {
	t.Parallel()

	policy := NewCapabilityPolicyV1()
	if err := policy.Validate(); err != nil {
		t.Fatalf("policy invalid: %v", err)
	}
	regularReview, err := policy.Desired(CapabilityCell{FileKind: repository.FileKindRegular, ChangeKind: repository.ChangeModified, PathClass: PathClassNormal, Axis: CapabilityReview})
	if err != nil || !regularReview.Enabled {
		t.Fatalf("regular review cell = %+v, %v", regularReview, err)
	}
	gitlinkApply, err := policy.Desired(CapabilityCell{FileKind: repository.FileKindGitlink, ChangeKind: repository.ChangeModified, PathClass: PathClassNormal, Axis: CapabilityApply})
	if err != nil || gitlinkApply.Enabled || gitlinkApply.Reason != ReasonPermanentFalse {
		t.Fatalf("gitlink apply cell = %+v, %v", gitlinkApply, err)
	}
	traversalProposal, err := policy.Desired(CapabilityCell{FileKind: repository.FileKindRegular, ChangeKind: repository.ChangeModified, PathClass: PathClassTraversal, Axis: CapabilityPropose})
	if err != nil || traversalProposal.Enabled || traversalProposal.Reason != ReasonPermanentFalse {
		t.Fatalf("traversal proposal cell = %+v, %v", traversalProposal, err)
	}
	symlinkRename, err := policy.Desired(CapabilityCell{FileKind: repository.FileKindSymlink, ChangeKind: repository.ChangeRenamed, PathClass: PathClassNormal, Axis: CapabilityApply})
	if err != nil || symlinkRename.Enabled || symlinkRename.Reason != ReasonPermanentFalse {
		t.Fatalf("symlink rename cell = %+v, %v", symlinkRename, err)
	}
}

func TestCapabilityPolicyDisableOnly(t *testing.T) {
	t.Parallel()

	policy := NewCapabilityPolicyV1()
	cell := CapabilityCell{FileKind: repository.FileKindRegular, ChangeKind: repository.ChangeModified, PathClass: PathClassNormal, Axis: CapabilityApply}
	disabled, err := policy.DisableOnly([]CapabilityCell{cell})
	if err != nil {
		t.Fatalf("DisableOnly() error = %v", err)
	}
	desired, err := disabled.Desired(cell)
	if err != nil || desired.Enabled || desired.Reason != ReasonPolicyDisabled {
		t.Fatalf("disabled cell = %+v, %v", desired, err)
	}
	permanent := CapabilityCell{FileKind: repository.FileKindGitlink, ChangeKind: repository.ChangeModified, PathClass: PathClassNormal, Axis: CapabilityApply}
	unchanged, err := disabled.DisableOnly([]CapabilityCell{permanent})
	if err != nil {
		t.Fatalf("permanent DisableOnly() error = %v", err)
	}
	desired, _ = unchanged.Desired(permanent)
	if desired.Reason != ReasonPermanentFalse {
		t.Fatalf("permanent cell changed = %+v", desired)
	}
	unknown := cell
	unknown.FileKind = repository.FileKind("future")
	if _, err := policy.DisableOnly([]CapabilityCell{unknown}); !errors.Is(err, ErrUnknownCapabilityCell) {
		t.Fatalf("unknown cell error = %v", err)
	}
}

func TestResolveCapabilityRequiresExactEvidenceAndCurrentQualification(t *testing.T) {
	t.Parallel()

	policy := NewCapabilityPolicyV1()
	request := qualifiedRequest(policy, repository.FileKindRegular, repository.ChangeModified, PathClassNormal, IndexClean)
	decision, err := ResolveCapability(policy, request)
	if err != nil {
		t.Fatalf("ResolveCapability() error = %v", err)
	}
	if !decision.Review || !decision.Anchor || !decision.MaterializeReviewSnapshot || !decision.Propose || !decision.Apply {
		t.Fatalf("qualified decision = %+v", decision)
	}
	if err := decision.Validate(policy); err != nil {
		t.Fatalf("decision invalid: %v", err)
	}

	request.ImplementationEvidence = request.ImplementationEvidence[:len(request.ImplementationEvidence)-1]
	missing, err := ResolveCapability(policy, request)
	if err != nil || missing.Apply || missing.ReasonsByAxis[CapabilityApply][0].Code != ReasonCapabilityNotImplemented {
		t.Fatalf("missing evidence decision = %+v, %v", missing, err)
	}
	request = qualifiedRequest(policy, repository.FileKindRegular, repository.ChangeModified, PathClassNormal, IndexClean)
	request.ImplementationEvidence[0].ExpiresWithPolicy++
	stale, err := ResolveCapability(policy, request)
	if err != nil || stale.Anchor || stale.ReasonsByAxis[CapabilityAnchor][0].Code != ReasonEvidenceStale {
		t.Fatalf("stale evidence decision = %+v, %v", stale, err)
	}

	raw := qualifiedRequest(policy, repository.FileKindRegular, repository.ChangeModified, PathClassRaw, IndexClean)
	raw.Current.Platform.NativeAction = false
	raw.Current.Platform.HeldHandles = false
	rawDecision, err := ResolveCapability(policy, raw)
	if err != nil || !rawDecision.Anchor || !rawDecision.MaterializeReviewSnapshot || rawDecision.Apply {
		t.Fatalf("raw-path decision = %+v, %v", rawDecision, err)
	}

	unmerged := qualifiedRequest(policy, repository.FileKindRegular, repository.ChangeModified, PathClassNormal, IndexUnmerged)
	unmergedDecision, err := ResolveCapability(policy, unmerged)
	if err != nil || !unmergedDecision.Review || unmergedDecision.Anchor || unmergedDecision.Propose || unmergedDecision.Apply || unmergedDecision.ReasonsByAxis[CapabilityApply][0].Code != ReasonIndexUnsafe {
		t.Fatalf("unmerged decision = %+v, %v", unmergedDecision, err)
	}
}

func TestResolveCapabilityGatesModeTransitionsByCurrentEvidence(t *testing.T) {
	t.Parallel()

	policy := NewCapabilityPolicyV1()
	request := qualifiedRequest(policy, repository.FileKindRegular, repository.ChangeModified, PathClassNormal, IndexClean)
	transition, err := repository.NewModeTransition(0o100644, 0o100755)
	if err != nil {
		t.Fatalf("NewModeTransition() error = %v", err)
	}
	request.ModeTransition = &transition

	blocked, err := ResolveCapability(policy, request)
	if err != nil {
		t.Fatalf("ResolveCapability() error = %v", err)
	}
	if blocked.Propose || blocked.Apply || blocked.ReasonsByAxis[CapabilityApply][0].Code != ReasonExecutableModeUnrepresentable {
		t.Fatalf("mode transition without evidence = %+v", blocked)
	}

	request.Current.Mode = &ModeCapabilityEvidence{
		Version:              ModeCapabilityEvidenceVersion,
		Transition:           transition,
		GenericTransition:    true,
		OldEndpointSupported: true,
		NewEndpointSupported: true,
		PlatformSupported:    true,
		CoreFilemode:         true,
		ModeRepresentable:    true,
		ModeReadbackVerified: true,
		ContentUnchanged:     true,
		IndexUnchanged:       true,
	}
	qualified, err := ResolveCapability(policy, request)
	if err != nil {
		t.Fatalf("ResolveCapability() with mode evidence error = %v", err)
	}
	if !qualified.Propose || !qualified.Apply {
		t.Fatalf("qualified mode transition = %+v", qualified)
	}
}

func TestCaptureEvaluationAndDiscussionAvailability(t *testing.T) {
	t.Parallel()

	policy := NewCapabilityPolicyV1()
	request := qualifiedRequest(policy, repository.FileKindRegular, repository.ChangeModified, PathClassNormal, IndexClean)
	decision, err := ResolveCapability(policy, request)
	if err != nil {
		t.Fatalf("ResolveCapability() error = %v", err)
	}
	evaluation := CapturePolicyEvaluation{
		CaptureID:             request.Key.CaptureID,
		CaptureFormatVersion:  1,
		PolicyVersion:         policy.Version,
		ResourcePolicyVersion: policy.ResourcePolicyVersion,
		EvidenceVersion:       policy.EvidenceVersion,
		Decisions:             []CapabilityDecision{decision},
		ManifestHash:          "manifest-hash",
	}
	if err := evaluation.Validate(policy); err != nil {
		t.Fatalf("evaluation invalid: %v", err)
	}
	evaluation.Decisions = append(evaluation.Decisions, decision)
	if err := evaluation.Validate(policy); !errors.Is(err, ErrInvalidCapabilityPolicy) {
		t.Fatalf("duplicate evaluation error = %v", err)
	}

	available := ComposeDiscussionAvailability(DiscussionModeReadOnly, decision, request.Current.Session, DiscussionEvidence{ProviderCompatibility: true, Account: true, Disclosure: true, TurnPermission: true})
	if !available.Available {
		t.Fatalf("available discussion = %+v", available)
	}
	providerBlocked := ComposeDiscussionAvailability(DiscussionModeReadOnly, decision, request.Current.Session, DiscussionEvidence{Account: true, Disclosure: true, TurnPermission: true})
	if providerBlocked.Available || decision.Apply == false {
		t.Fatalf("provider-blocked discussion = %+v; repository decision = %+v", providerBlocked, decision)
	}
}

func qualifiedRequest(policy CapabilityPolicyV1, fileKind repository.FileKind, changeKind repository.ChangeKind, pathClass PathClass, index IndexState) CapabilityRequest {
	path, _ := repository.NewRepoPath([]byte("src/main.go"))
	request := CapabilityRequest{
		Key: CapabilityKey{
			Path:                  path.Key(),
			CaptureID:             domain.CaptureID("capture-1"),
			PolicyVersion:         policy.Version,
			ResourcePolicyVersion: policy.ResourcePolicyVersion,
			EvidenceVersion:       policy.EvidenceVersion,
		},
		FileKind:   fileKind,
		ChangeKind: changeKind,
		PathClass:  pathClass,
		Index:      index,
		Current: CurrentCapabilityEvidence{
			GitDeterministic:      true,
			LimitOutcome:          LimitAccepted,
			ResourcePolicyVersion: policy.ResourcePolicyVersion,
			EvidenceVersion:       policy.EvidenceVersion,
			Platform:              PlatformEvidence{CanonicalPath: true, NativeAction: true, NoFollow: true, HeldHandles: true},
			Session:               SessionEvidence{SnapshotLease: true, ReadContainment: true, ProposalWorkspace: true, EditDestination: true, ApplyLease: true, Permissions: true},
		},
	}
	for _, axis := range []CapabilityAxis{CapabilityAnchor, CapabilityMaterializeReviewSnapshot, CapabilityPropose, CapabilityApply} {
		request.ImplementationEvidence = append(request.ImplementationEvidence, ImplementationEvidence{
			Cell:              request.cell(axis),
			OwnerVersion:      "owner/v1",
			ConformanceSet:    "conformance-1",
			ExpiresWithPolicy: policy.ResourcePolicyVersion,
			EvidenceVersion:   policy.EvidenceVersion,
			Supported:         true,
		})
	}
	return request
}
