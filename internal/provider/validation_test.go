package provider

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
)

func TestTurnPermissionPolicyRejectsAmbientOrOutOfScopeAccess(t *testing.T) {
	limits := DefaultValidationLimits()
	if err := (TurnPermissionPolicy{Filesystem: FilesystemPromptOnly, Network: NetworkDisabled, RuntimeApprovals: RuntimeApprovalsDisabled}).Validate(limits); err != nil {
		t.Fatalf("prompt-only policy: %v", err)
	}
	if err := (TurnPermissionPolicy{Filesystem: FilesystemPromptOnly, ReadableRoots: []PermissionRoot{{Path: filepath.VolumeName(filepath.Dir(t.TempDir())) + string(filepath.Separator)}}, Network: NetworkDisabled, RuntimeApprovals: RuntimeApprovalsDisabled}).Validate(limits); !errors.Is(err, ErrInvalidPermission) {
		t.Fatalf("ambient read accepted: %v", err)
	}

	result := t.TempDir()
	proposal := TurnPermissionPolicy{
		Filesystem:         FilesystemProposalResult,
		WritableRoots:      []PermissionRoot{{Path: filepath.Join(result, "nested")}},
		ProposalResultRoot: PermissionRoot{Path: result},
		Containment: ContainmentEvidence{
			CanonicalRead: true, CanonicalWrite: true, NoSymlinkEscape: true,
			NoJunctionEscape: true, NoMountEscape: true, NoHardLinkAlias: true,
			HandlesQuiescent: true,
		},
		Network:          NetworkDisabled,
		RuntimeApprovals: RuntimeApprovalsExplicit,
	}
	if err := proposal.Validate(limits); err != nil {
		t.Fatalf("proposal policy: %v", err)
	}
	proposal.WritableRoots[0].Path = filepath.Join(result, "..", "outside")
	if err := proposal.Validate(limits); !errors.Is(err, ErrInvalidPermission) {
		t.Fatalf("out-of-scope write accepted: %v", err)
	}
}

func TestRuntimeApprovalIsOneShotAndScopeBound(t *testing.T) {
	now := time.Unix(100, 0)
	request := RuntimeApprovalRequest{
		RequestID:     ProviderRequestID("request-1"),
		ThreadID:      domain.ReviewThreadID("thread-1"),
		OperationID:   domain.OperationID("operation-1"),
		CorrelationID: CorrelationID("correlation-1"),
		TurnRef:       ProviderTurnRef("turn-ref-1"),
		Scope: RuntimeApprovalScope{
			Kind:            RuntimeApprovalCommand,
			Executable:      filepath.Join(t.TempDir(), "git"),
			ArgumentsDigest: "sha256:arguments",
		},
		ExpiresAt: now.Add(time.Minute),
	}
	approval, err := NewRuntimeApproval(request, now)
	if err != nil {
		t.Fatalf("new approval: %v", err)
	}
	response := RuntimeApprovalResponse{
		RequestID:     request.RequestID,
		ThreadID:      request.ThreadID,
		OperationID:   request.OperationID,
		CorrelationID: request.CorrelationID,
		TurnRef:       request.TurnRef,
		Scope:         request.Scope,
		Decision:      ApprovalAllowOnce,
	}
	if err := approval.Respond(response, now.Add(time.Second)); err != nil {
		t.Fatalf("resolve approval: %v", err)
	}
	if err := approval.Respond(response, now.Add(2*time.Second)); !errors.Is(err, ErrApprovalDuplicate) {
		t.Fatalf("duplicate response accepted: %v", err)
	}

	request.ExpiresAt = now.Add(time.Hour)
	pending, err := NewRuntimeApproval(request, now)
	if err != nil {
		t.Fatalf("new pending approval: %v", err)
	}
	response.Scope.ArgumentsDigest = "sha256:other"
	if err := pending.Respond(response, now.Add(time.Second)); !errors.Is(err, ErrApprovalScopeMismatch) {
		t.Fatalf("scope mismatch accepted: %v", err)
	}
	if err := pending.Cancel(); err != nil {
		t.Fatalf("cancel approval: %v", err)
	}
	if err := pending.Respond(RuntimeApprovalResponse{RequestID: request.RequestID}, now.Add(2*time.Second)); !errors.Is(err, ErrApprovalCancelled) {
		t.Fatalf("cancelled response accepted: %v", err)
	}
}

func TestTurnRequestBindsWorkingDirectoryToPermissionMode(t *testing.T) {
	root := t.TempDir()
	request := TurnRequest{
		ThreadID:    domain.ReviewThreadID("thread-1"),
		OperationID: domain.OperationID("operation-1"),
		Mode:        TurnDiscuss,
		Prompt:      "Explain this review concern.",
		WorkingDir:  "",
		Permissions: TurnPermissionPolicy{
			Filesystem:       FilesystemPromptOnly,
			Network:          NetworkDisabled,
			RuntimeApprovals: RuntimeApprovalsDisabled,
		},
		CorrelationID: CorrelationID("correlation-1"),
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("prompt-only turn: %v", err)
	}
	request.WorkingDir = root
	if err := request.Validate(); !errors.Is(err, ErrInvalidPermission) {
		t.Fatalf("prompt-only working directory accepted: %v", err)
	}
}
