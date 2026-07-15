package provider

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalidProviderValue  = errors.New("invalid provider value")
	ErrInvalidPermission     = errors.New("invalid provider permission policy")
	ErrInvalidApproval       = errors.New("invalid runtime approval")
	ErrApprovalExpired       = errors.New("runtime approval expired")
	ErrApprovalDuplicate     = errors.New("runtime approval already resolved")
	ErrApprovalCancelled     = errors.New("runtime approval cancelled")
	ErrApprovalStale         = errors.New("runtime approval is stale")
	ErrApprovalScopeMismatch = errors.New("runtime approval scope mismatch")
	ErrInvalidEvent          = errors.New("invalid provider event")
	ErrEventOutOfOrder       = errors.New("provider event is out of order")
)

// ValidationLimits mirrors the provider cells in app.ResourcePolicy without
// making the provider-neutral package depend on the application package.
type ValidationLimits struct {
	OpaqueRefBytes     uint64
	MethodBytes        uint64
	DisplayBytes       uint64
	HumanErrorBytes    uint64
	PathBytes          uint64
	PromptBytes        uint64
	TurnContentBytes   uint64
	CoalescingKeyBytes uint64
	MaxRoots           uint64
}

// DefaultValidationLimits returns the v1 provider admission limits. The app
// port may supply a lower policy-derived copy when runtime tuning is active.
func DefaultValidationLimits() ValidationLimits {
	return ValidationLimits{
		OpaqueRefBytes:     4 * 1024,
		MethodBytes:        256,
		DisplayBytes:       1024,
		HumanErrorBytes:    64 * 1024,
		PathBytes:          32 * 1024,
		PromptBytes:        2 * 1024 * 1024,
		TurnContentBytes:   32 * 1024 * 1024,
		CoalescingKeyBytes: 256,
		MaxRoots:           64,
	}
}

func (l ValidationLimits) validate() error {
	if l.OpaqueRefBytes == 0 || l.MethodBytes == 0 || l.DisplayBytes == 0 || l.HumanErrorBytes == 0 || l.PathBytes == 0 || l.PromptBytes == 0 || l.TurnContentBytes == 0 || l.CoalescingKeyBytes == 0 || l.MaxRoots == 0 {
		return ErrInvalidProviderValue
	}
	return nil
}

func validateText(value, label string, maximum uint64, allowEmpty bool) error {
	if !allowEmpty && value == "" {
		return fmt.Errorf("%s: %w", label, ErrInvalidProviderValue)
	}
	if !utf8.ValidString(value) || uint64(len([]byte(value))) > maximum || strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("%s: %w", label, ErrInvalidProviderValue)
	}
	return nil
}

func validateOpaque(value, label string, limits ValidationLimits) error {
	return validateText(value, label, limits.OpaqueRefBytes, false)
}

func validateLocalID(value string, label string, limits ValidationLimits) error {
	return validateText(value, label, limits.OpaqueRefBytes, false)
}

func validateMode(mode TurnMode) error {
	if mode != TurnDiscuss && mode != TurnPropose {
		return ErrInvalidProviderValue
	}
	return nil
}

func (c CorrelationID) Validate() error {
	return validateOpaque(string(c), "correlation id", DefaultValidationLimits())
}

func (r ProviderConversationRef) Validate() error {
	return validateOpaque(string(r), "conversation ref", DefaultValidationLimits())
}

func (r ProviderTurnRef) Validate() error {
	return validateOpaque(string(r), "turn ref", DefaultValidationLimits())
}

func (r ProviderRequestID) Validate() error {
	return validateOpaque(string(r), "request id", DefaultValidationLimits())
}

func (r PermissionRoot) Validate(limits ValidationLimits) error {
	if err := limits.validate(); err != nil {
		return err
	}
	if err := validateText(r.Path, "permission root", limits.PathBytes, false); err != nil || !filepath.IsAbs(r.Path) {
		return ErrInvalidPermission
	}
	if filepath.Clean(r.Path) == "." {
		return ErrInvalidPermission
	}
	return nil
}

func (e ContainmentEvidence) validForRead() bool {
	return e.CanonicalRead && e.NoSymlinkEscape && e.NoJunctionEscape && e.NoMountEscape && e.NoHardLinkAlias && e.HandlesQuiescent
}

func (e ContainmentEvidence) validForWrite() bool {
	return e.validForRead() && e.CanonicalWrite
}

func validRootSet(roots []PermissionRoot, limits ValidationLimits) error {
	if uint64(len(roots)) > limits.MaxRoots {
		return ErrInvalidPermission
	}
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		if err := root.Validate(limits); err != nil {
			return err
		}
		key := filepath.Clean(root.Path)
		if _, ok := seen[key]; ok {
			return ErrInvalidPermission
		}
		seen[key] = struct{}{}
	}
	return nil
}

func pathWithin(root, child string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(child))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func pathWithinAny(roots []PermissionRoot, child string) bool {
	for _, root := range roots {
		if pathWithin(root.Path, child) {
			return true
		}
	}
	return false
}

// Validate checks that the policy cannot grant ambient reads, out-of-scope
// writes, or enabled network access.
func (p TurnPermissionPolicy) Validate(limits ValidationLimits) error {
	if err := limits.validate(); err != nil {
		return err
	}
	switch p.Filesystem {
	case FilesystemPromptOnly, FilesystemReviewSnapshot, FilesystemProposalResult:
	default:
		return ErrInvalidPermission
	}
	if p.Network != NetworkDisabled || (p.RuntimeApprovals != RuntimeApprovalsDisabled && p.RuntimeApprovals != RuntimeApprovalsExplicit) {
		return ErrInvalidPermission
	}
	if err := validRootSet(p.ReadableRoots, limits); err != nil {
		return err
	}
	if err := validRootSet(p.WritableRoots, limits); err != nil {
		return err
	}
	if err := validRootSet(p.RuntimeRoots, limits); err != nil {
		return err
	}

	switch p.Filesystem {
	case FilesystemPromptOnly:
		if len(p.ReadableRoots) != 0 || len(p.WritableRoots) != 0 || p.ProposalResultRoot.Path != "" || p.Containment != (ContainmentEvidence{}) {
			return ErrInvalidPermission
		}
	case FilesystemReviewSnapshot:
		if len(p.ReadableRoots) == 0 || len(p.WritableRoots) != 0 || len(p.RuntimeRoots) != 0 || !p.Containment.validForRead() || p.ProposalResultRoot.Path != "" {
			return ErrInvalidPermission
		}
	case FilesystemProposalResult:
		if len(p.WritableRoots) == 0 || p.ProposalResultRoot.Path == "" || !p.Containment.validForWrite() {
			return ErrInvalidPermission
		}
		if err := p.ProposalResultRoot.Validate(limits); err != nil {
			return err
		}
		for _, root := range p.WritableRoots {
			if !pathWithin(p.ProposalResultRoot.Path, root.Path) {
				return ErrInvalidPermission
			}
		}
	}
	return nil
}

func validateModePermissions(mode TurnMode, permissions TurnPermissionPolicy, limits ValidationLimits) error {
	if err := validateMode(mode); err != nil {
		return err
	}
	if err := permissions.Validate(limits); err != nil {
		return err
	}
	if mode == TurnDiscuss && permissions.Filesystem == FilesystemProposalResult {
		return ErrInvalidPermission
	}
	if mode == TurnPropose && permissions.Filesystem != FilesystemProposalResult {
		return ErrInvalidPermission
	}
	return nil
}

func validateWorkingDir(workingDir string, permissions TurnPermissionPolicy, limits ValidationLimits) error {
	if workingDir == "" {
		if permissions.Filesystem != FilesystemPromptOnly {
			return ErrInvalidPermission
		}
		return nil
	}
	if validateText(workingDir, "working directory", limits.PathBytes, false) != nil || !filepath.IsAbs(workingDir) {
		return ErrInvalidPermission
	}
	switch permissions.Filesystem {
	case FilesystemPromptOnly:
		if !pathWithinAny(permissions.RuntimeRoots, workingDir) {
			return ErrInvalidPermission
		}
	case FilesystemReviewSnapshot:
		if !pathWithinAny(permissions.ReadableRoots, workingDir) {
			return ErrInvalidPermission
		}
	case FilesystemProposalResult:
		if !pathWithin(permissions.ProposalResultRoot.Path, workingDir) || !pathWithinAny(permissions.WritableRoots, workingDir) {
			return ErrInvalidPermission
		}
	default:
		return ErrInvalidPermission
	}
	return nil
}

// Validate checks all local identity and policy fields before an adapter sends
// a conversation-start request to an external provider.
func (r StartConversationRequest) Validate() error {
	limits := DefaultValidationLimits()
	if validateLocalID(string(r.ThreadID), "thread id", limits) != nil || validateLocalID(string(r.OperationID), "operation id", limits) != nil || r.CorrelationID.Validate() != nil || validateModePermissions(r.Mode, r.Permissions, limits) != nil || validateWorkingDir(r.WorkingDir, r.Permissions, limits) != nil {
		return ErrInvalidProviderValue
	}
	return nil
}

// Validate checks prompt, working-directory, identity, and permission
// relationships before an adapter starts a provider turn.
func (r TurnRequest) Validate() error {
	limits := DefaultValidationLimits()
	if validateLocalID(string(r.ThreadID), "thread id", limits) != nil || validateLocalID(string(r.OperationID), "operation id", limits) != nil || r.CorrelationID.Validate() != nil || validateModePermissions(r.Mode, r.Permissions, limits) != nil || validateText(r.Prompt, "turn prompt", limits.PromptBytes, false) != nil {
		return ErrInvalidProviderValue
	}
	return validateWorkingDir(r.WorkingDir, r.Permissions, limits)
}

// ValidateSteeringInput applies the bounded provider-turn input limit to a
// steering message without exposing a second generic command API.
func ValidateSteeringInput(input string) error {
	return validateText(input, "steering input", DefaultValidationLimits().PromptBytes, false)
}

func (s RuntimeApprovalScope) Validate(limits ValidationLimits) error {
	if err := limits.validate(); err != nil {
		return err
	}
	switch s.Kind {
	case RuntimeApprovalCommand:
		if err := validateText(s.Executable, "approval executable", limits.PathBytes, false); err != nil || !filepath.IsAbs(s.Executable) {
			return ErrInvalidApproval
		}
		if err := validateOpaque(s.ArgumentsDigest, "approval arguments digest", limits); err != nil {
			return ErrInvalidApproval
		}
		if s.Path.Path != "" || s.Tool != "" {
			return ErrInvalidApproval
		}
	case RuntimeApprovalFile:
		if err := s.Path.Validate(limits); err != nil || s.Executable != "" || s.ArgumentsDigest != "" || s.Tool != "" {
			return ErrInvalidApproval
		}
	case RuntimeApprovalTool:
		if err := validateText(s.Tool, "approval tool", limits.MethodBytes, false); err != nil || s.Executable != "" || s.ArgumentsDigest != "" || s.Path.Path != "" {
			return ErrInvalidApproval
		}
	case RuntimeApprovalNetwork:
		return ErrInvalidApproval
	default:
		return ErrInvalidApproval
	}
	return nil
}

func (r RuntimeApprovalRequest) ValidateAt(now time.Time) error {
	limits := DefaultValidationLimits()
	if err := limits.validate(); err != nil || validateOpaque(string(r.RequestID), "request id", limits) != nil || validateLocalID(string(r.ThreadID), "thread id", limits) != nil || validateLocalID(string(r.OperationID), "operation id", limits) != nil || r.CorrelationID.Validate() != nil || r.TurnRef.Validate() != nil || r.Scope.Validate(limits) != nil || r.ExpiresAt.IsZero() || !r.ExpiresAt.After(now) {
		return ErrInvalidApproval
	}
	return nil
}

func (r RuntimeApprovalResponse) ValidateAgainst(request RuntimeApprovalRequest, now time.Time) error {
	limits := DefaultValidationLimits()
	if err := validateOpaque(string(r.RequestID), "request id", limits); err != nil {
		return ErrInvalidApproval
	}
	if err := validateLocalID(string(r.ThreadID), "thread id", limits); err != nil {
		return ErrInvalidApproval
	}
	if err := validateLocalID(string(r.OperationID), "operation id", limits); err != nil {
		return ErrInvalidApproval
	}
	if r.CorrelationID.Validate() != nil || r.TurnRef.Validate() != nil || r.Scope.Validate(limits) != nil || (r.Decision != ApprovalAllowOnce && r.Decision != ApprovalDeny) {
		return ErrInvalidApproval
	}
	if !request.ExpiresAt.After(now) {
		return ErrApprovalExpired
	}
	if r.RequestID != request.RequestID || r.ThreadID != request.ThreadID || r.OperationID != request.OperationID || r.CorrelationID != request.CorrelationID || r.TurnRef != request.TurnRef {
		return ErrApprovalStale
	}
	if r.Scope != request.Scope {
		return ErrApprovalScopeMismatch
	}
	return nil
}

// ApprovalState describes the one-shot lifecycle of a runtime approval.
type ApprovalState string

const (
	ApprovalPending  ApprovalState = "pending"
	ApprovalResolved ApprovalState = "resolved"
	ApprovalCanceled ApprovalState = "canceled"
)

// RuntimeApproval tracks one pending approval without changing proposal state.
type RuntimeApproval struct {
	Request  RuntimeApprovalRequest
	State    ApprovalState
	Decision ApprovalDecision
}

// NewRuntimeApproval admits a new, future-expiring runtime request.
func NewRuntimeApproval(request RuntimeApprovalRequest, now time.Time) (RuntimeApproval, error) {
	if err := request.ValidateAt(now); err != nil {
		return RuntimeApproval{}, err
	}
	return RuntimeApproval{Request: request, State: ApprovalPending}, nil
}

// Respond resolves a pending request exactly once and rejects stale, expired,
// cancelled, or scope-mismatched responses.
func (a *RuntimeApproval) Respond(response RuntimeApprovalResponse, now time.Time) error {
	if a == nil || a.State == "" {
		return ErrInvalidApproval
	}
	if a.State == ApprovalCanceled {
		return ErrApprovalCancelled
	}
	if a.State == ApprovalResolved {
		return ErrApprovalDuplicate
	}
	if err := a.ResponseValidation(response, now); err != nil {
		return err
	}
	a.State = ApprovalResolved
	a.Decision = response.Decision
	return nil
}

// ResponseValidation validates a response against this approval without
// mutating its one-shot state.
func (a *RuntimeApproval) ResponseValidation(response RuntimeApprovalResponse, now time.Time) error {
	if a == nil || a.State == "" {
		return ErrInvalidApproval
	}
	return response.ValidateAgainst(a.Request, now)
}

// Cancel invalidates a still-pending approval.
func (a *RuntimeApproval) Cancel() error {
	if a == nil || a.State == "" {
		return ErrInvalidApproval
	}
	if a.State == ApprovalResolved {
		return ErrApprovalDuplicate
	}
	if a.State == ApprovalCanceled {
		return ErrApprovalCancelled
	}
	a.State = ApprovalCanceled
	return nil
}
