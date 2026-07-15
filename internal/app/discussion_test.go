package app

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/provider"
)

func TestBuildFocusedDiscussionPrompt(t *testing.T) {
	context := DiscussionContext{
		Target:       "local working tree generation 7",
		Path:         repository.RepoPath("internal/app/example.go"),
		Side:         repository.DiffHead,
		Lines:        DiscussionLineRange{Start: 12, End: 14},
		SelectedText: "return value, nil",
		Hunk:         "@@ -12,3 +12,3 @@\n-return old\n+return value",
		UserConcern:  "Does this preserve the error boundary?",
	}
	prompt, err := BuildDiscussionPrompt(context)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Target: local working tree generation 7",
		"Path: internal/app/example.go",
		"Diff side: head",
		"Selected lines: 12-14",
		"return value, nil",
		"Does this preserve the error boundary?",
		"Read-only boundary",
		"Do not use network access",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt does not contain %q:\n%s", want, prompt)
		}
	}

	oversize := context
	oversize.Hunk = strings.Repeat("h", int(DefaultResourcePolicy().Provider.HunkContextBytes)+1)
	compacted, err := BuildDiscussionPrompt(oversize)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compacted, "optional context unit(s) omitted") || strings.Contains(compacted, oversize.Hunk) {
		t.Fatal("oversize optional hunk was not visibly omitted")
	}

	oversize.UserConcern = strings.Repeat("c", int(DefaultResourcePolicy().Provider.ConcernBytes)+1)
	if _, err := BuildDiscussionPrompt(oversize); !errors.Is(err, ErrProviderInputLimit) {
		t.Fatalf("oversize concern error = %v, want provider input limit", err)
	}
}

func TestDiscussionPermissionsReadOnly(t *testing.T) {
	policy := provider.TurnPermissionPolicy{
		Filesystem:       provider.FilesystemPromptOnly,
		Network:          provider.NetworkDisabled,
		RuntimeApprovals: provider.RuntimeApprovalsDisabled,
	}
	availability := ResolveDiscussionAvailability(DiscussionAvailabilityInput{
		Decision: CapabilityDecision{Review: true, MaterializeReviewSnapshot: false},
		ProviderEvidence: DiscussionEvidence{
			ProviderCompatibility: true,
			Account:               true,
			Disclosure:            true,
			TurnPermission:        true,
		},
		PromptContextReady:  true,
		EmptyRootPermission: true,
	})
	if !availability.Available || availability.Mode != DiscussionModePromptOnly || len(availability.RepositoryReasons) == 0 {
		t.Fatalf("availability = %+v, want prompt-only with repository reasons", availability)
	}
	if err := policy.Validate(provider.DefaultValidationLimits()); err != nil {
		t.Fatal(err)
	}
}

func TestDiscussionRejectsStaleGeneration(t *testing.T) {
	request := DiscussionDispatchRequest{
		ThreadID:           domain.ReviewThreadID("thread-1"),
		ConversationID:     domain.ProviderConversationID("conversation-1"),
		Context:            testDiscussionContext(),
		Availability:       testPromptOnlyAvailability(),
		Permissions:        provider.TurnPermissionPolicy{Filesystem: provider.FilesystemPromptOnly, Network: provider.NetworkDisabled, RuntimeApprovals: provider.RuntimeApprovalsDisabled},
		CapabilityPolicy:   CurrentCapabilityPolicyVersion,
		ResourcePolicy:     CurrentResourcePolicyVersion,
		Evidence:           CurrentCapabilityEvidenceVersion,
		PermissionVersion:  "provider-permissions-v1",
		ExpectedGeneration: 7,
		CurrentGeneration:  8,
	}
	if _, err := request.PrepareStartProviderTurn(); !errors.Is(err, ErrDiscussionStale) {
		t.Fatalf("stale generation error = %v, want ErrDiscussionStale", err)
	}
}

func TestDiscussionQueuesWhenOffline(t *testing.T) {
	availability := ResolveDiscussionAvailability(DiscussionAvailabilityInput{
		Decision: CapabilityDecision{Review: true},
		ProviderEvidence: DiscussionEvidence{
			Account:        true,
			Disclosure:     true,
			TurnPermission: true,
		},
		PromptContextReady:  true,
		EmptyRootPermission: true,
	})
	if availability.Available || availability.Mode != DiscussionModeDisabled {
		t.Fatalf("offline availability = %+v, want disabled pending dispatch", availability)
	}
	request := DiscussionDispatchRequest{Availability: availability}
	if _, err := request.PrepareStartProviderTurn(); !errors.Is(err, ErrDiscussionUnavailable) {
		t.Fatalf("offline dispatch error = %v, want ErrDiscussionUnavailable", err)
	}
}

func TestDiscussionBindsPinnedObjectSnapshotProvenance(t *testing.T) {
	root := filepath.Join(t.TempDir(), "review-snapshot")
	head := repository.ObjectID(strings.Repeat("a", 40))
	parent := repository.ObjectID(strings.Repeat("b", 40))
	lease := &ReviewSnapshotLease{
		ID: "lease-1", SnapshotID: "snapshot-1", TargetKind: repository.TargetCommit,
		HeadObjectID: head, BaseObjectID: parent, ParentLabel: "parent 1",
		SourceRef: "target:commit:head:" + string(head) + ":base:" + string(parent) + ":parent:parent 1",
		Root:      root, ManifestHash: strings.Repeat("c", 64), ProcessNonce: strings.Repeat("d", 64), AcquiredAt: time.Now().UTC(),
	}
	request := DiscussionDispatchRequest{
		ThreadID: "thread-1", ConversationID: "conversation-1", Context: DiscussionContext{
			Target: "commit HEAD generation 7", Path: repository.RepoPath("main.go"), Side: repository.DiffHead,
			Lines: DiscussionLineRange{Start: 1, End: 1}, SelectedText: "return nil", UserConcern: "Concern", WorkingDir: root,
		},
		Availability: DiscussionAvailability{Available: true, Mode: DiscussionModeFilesystem}, SnapshotLease: lease,
		Permissions: provider.TurnPermissionPolicy{
			Filesystem: provider.FilesystemReviewSnapshot, ReadableRoots: []provider.PermissionRoot{{Path: root}},
			Containment: provider.ContainmentEvidence{CanonicalRead: true, NoSymlinkEscape: true, NoJunctionEscape: true, NoMountEscape: true, NoHardLinkAlias: true, HandlesQuiescent: true},
			Network:     provider.NetworkDisabled, RuntimeApprovals: provider.RuntimeApprovalsDisabled,
		},
		CapabilityPolicy: CurrentCapabilityPolicyVersion, ResourcePolicy: CurrentResourcePolicyVersion, Evidence: CurrentCapabilityEvidenceVersion, PermissionVersion: "provider-permissions-v1",
	}
	turn, err := request.PrepareStartProviderTurn()
	if err != nil {
		t.Fatal(err)
	}
	if turn.Provenance.SourceCaptureID != "" || turn.Provenance.SourceSnapshotRef != lease.SourceRef || turn.Provenance.ReviewSnapshotID != lease.SnapshotID || turn.Provenance.ManifestHash != lease.ManifestHash {
		t.Fatalf("object snapshot provenance = %#v", turn.Provenance)
	}
}

func testDiscussionContext() DiscussionContext {
	return DiscussionContext{
		Target:       "local generation 7",
		Path:         repository.RepoPath("main.go"),
		Side:         repository.DiffHead,
		Lines:        DiscussionLineRange{Start: 1, End: 1},
		SelectedText: "return nil",
		UserConcern:  "Concern",
	}
}

func testPromptOnlyAvailability() DiscussionAvailability {
	return DiscussionAvailability{Available: true, Mode: DiscussionModePromptOnly}
}
