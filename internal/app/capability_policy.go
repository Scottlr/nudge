package app

import (
	"errors"
	"sort"
	"strings"
	"unicode"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

var (
	// ErrInvalidCapabilityPolicy reports an incomplete or contradictory matrix.
	ErrInvalidCapabilityPolicy = errors.New("invalid capability policy")
	// ErrUnknownCapabilityCell reports a cell absent from the versioned matrix.
	ErrUnknownCapabilityCell = errors.New("unknown capability cell")
	// ErrInvalidCapabilityEvidence reports evidence that cannot enable a cell.
	ErrInvalidCapabilityEvidence = errors.New("invalid capability evidence")
)

// CapabilityPolicyVersion identifies the desired repository-action matrix.
type CapabilityPolicyVersion uint32

const CurrentCapabilityPolicyVersion CapabilityPolicyVersion = 1

// EvidenceVersion identifies the registered owner-evidence contract.
type EvidenceVersion uint32

const CurrentCapabilityEvidenceVersion EvidenceVersion = 1

// CapabilityAxis is an independent repository capability decision.
type CapabilityAxis string

const (
	CapabilityReview                    CapabilityAxis = "review"
	CapabilityAnchor                    CapabilityAxis = "anchor"
	CapabilityMaterializeReviewSnapshot CapabilityAxis = "materialize_review_snapshot"
	CapabilityPropose                   CapabilityAxis = "propose"
	CapabilityApply                     CapabilityAxis = "apply"
)

var capabilityAxes = [...]CapabilityAxis{
	CapabilityReview,
	CapabilityAnchor,
	CapabilityMaterializeReviewSnapshot,
	CapabilityPropose,
	CapabilityApply,
}

// PathClass describes path identity evidence without converting raw bytes to
// native or display text.
type PathClass string

const (
	PathClassNormal        PathClass = "normal"
	PathClassRaw           PathClass = "raw"
	PathClassNativeInvalid PathClass = "native_invalid"
	PathClassAbsolute      PathClass = "absolute"
	PathClassTraversal     PathClass = "traversal"
	PathClassGitAdmin      PathClass = "git_admin"
	PathClassCollision     PathClass = "collision"
	PathClassSpecial       PathClass = "special"
)

var pathClasses = [...]PathClass{
	PathClassNormal, PathClassRaw, PathClassNativeInvalid, PathClassAbsolute,
	PathClassTraversal, PathClassGitAdmin, PathClassCollision, PathClassSpecial,
}

// IndexState describes index/path evidence that can independently disable
// mutation while leaving safe review visible.
type IndexState string

const (
	IndexClean             IndexState = "clean"
	IndexStaged            IndexState = "staged"
	IndexUnstaged          IndexState = "unstaged"
	IndexStagedAndUnstaged IndexState = "staged_and_unstaged"
	IndexUnmerged          IndexState = "unmerged"
	IndexSparseUnknown     IndexState = "sparse_unknown"
	IndexFilterUnknown     IndexState = "filter_unknown"
	IndexUnsafeHardLink    IndexState = "unsafe_hard_link"
	IndexUnknown           IndexState = "unknown"
)

// CapabilityCell is one versioned desired-policy coordinate.
type CapabilityCell struct {
	FileKind   repository.FileKind
	ChangeKind repository.ChangeKind
	PathClass  PathClass
	Axis       CapabilityAxis
}

// Validate checks that a cell belongs to the closed v1 coordinate set.
func (c CapabilityCell) Validate() error {
	if !validFileKind(c.FileKind) || !validChangeKind(c.ChangeKind) || !validPathClass(c.PathClass) || !validCapabilityAxis(c.Axis) {
		return ErrUnknownCapabilityCell
	}
	return nil
}

// DesiredCell is a policy-supported or policy-disabled cell. Disabled cells
// carry a stable reason and cannot be enabled by runtime configuration.
type DesiredCell struct {
	Cell    CapabilityCell
	Enabled bool
	Reason  CapabilityReasonCode
}

// CapabilityReasonCode is a stable safe reason suitable for UI, doctor, and
// support projections.
type CapabilityReasonCode string

const (
	ReasonCapabilityNotImplemented CapabilityReasonCode = "capability_not_implemented"
	ReasonEvidenceStale            CapabilityReasonCode = "evidence_stale"
	ReasonEvidenceWrongCell        CapabilityReasonCode = "evidence_wrong_cell"
	ReasonPolicyDisabled           CapabilityReasonCode = "policy_disabled"
	ReasonPermanentFalse           CapabilityReasonCode = "permanent_false"
	ReasonPolicyMismatch           CapabilityReasonCode = "policy_mismatch"
	ReasonResourceLimit            CapabilityReasonCode = "resource_limit"
	ReasonPlatformUnqualified      CapabilityReasonCode = "platform_unqualified"
	ReasonSessionUnavailable       CapabilityReasonCode = "session_unavailable"
	ReasonIndexUnsafe              CapabilityReasonCode = "index_unsafe"
	ReasonProviderUnavailable      CapabilityReasonCode = "provider_unavailable"
	ReasonAccountUnavailable       CapabilityReasonCode = "account_unavailable"
	ReasonDisclosureUnavailable    CapabilityReasonCode = "disclosure_unavailable"
	ReasonPermissionUnavailable    CapabilityReasonCode = "permission_unavailable"
)

// CapabilityReason is intentionally payload-free. Raw paths, prompts, and
// provider text never enter repository capability truth.
type CapabilityReason struct {
	Code CapabilityReasonCode
}

// CapabilityPolicyV1 is the exhaustive desired repository capability matrix.
type CapabilityPolicyV1 struct {
	Version               CapabilityPolicyVersion
	ResourcePolicyVersion ResourcePolicyVersion
	EvidenceVersion       EvidenceVersion
	cells                 map[CapabilityCell]DesiredCell
}

// NewCapabilityPolicyV1 builds every file/change/path/axis coordinate.
func NewCapabilityPolicyV1() CapabilityPolicyV1 {
	policy := CapabilityPolicyV1{
		Version:               CurrentCapabilityPolicyVersion,
		ResourcePolicyVersion: CurrentResourcePolicyVersion,
		EvidenceVersion:       CurrentCapabilityEvidenceVersion,
		cells:                 make(map[CapabilityCell]DesiredCell),
	}
	for _, fileKind := range allFileKinds() {
		for _, changeKind := range allChangeKinds() {
			for _, pathClass := range pathClasses {
				for _, axis := range capabilityAxes {
					cell := CapabilityCell{FileKind: fileKind, ChangeKind: changeKind, PathClass: pathClass, Axis: axis}
					enabled, reason := desiredCell(cell)
					policy.cells[cell] = DesiredCell{Cell: cell, Enabled: enabled, Reason: reason}
				}
			}
		}
	}
	return policy
}

// Validate proves that the matrix remains exhaustive and versioned.
func (p CapabilityPolicyV1) Validate() error {
	if p.Version != CurrentCapabilityPolicyVersion || p.ResourcePolicyVersion != CurrentResourcePolicyVersion || p.EvidenceVersion != CurrentCapabilityEvidenceVersion || len(p.cells) != len(allFileKinds())*len(allChangeKinds())*len(pathClasses)*len(capabilityAxes) {
		return ErrInvalidCapabilityPolicy
	}
	for _, fileKind := range allFileKinds() {
		for _, changeKind := range allChangeKinds() {
			for _, pathClass := range pathClasses {
				for _, axis := range capabilityAxes {
					cell := CapabilityCell{FileKind: fileKind, ChangeKind: changeKind, PathClass: pathClass, Axis: axis}
					if _, ok := p.cells[cell]; !ok {
						return ErrInvalidCapabilityPolicy
					}
				}
			}
		}
	}
	return nil
}

// Desired returns the exact policy cell, failing closed for unknown cells.
func (p CapabilityPolicyV1) Desired(cell CapabilityCell) (DesiredCell, error) {
	if err := cell.Validate(); err != nil {
		return DesiredCell{}, err
	}
	desired, ok := p.cells[cell]
	if !ok {
		return DesiredCell{}, ErrUnknownCapabilityCell
	}
	return desired, nil
}

// DisableOnly applies configuration that can turn a desired cell off but can
// never enable a permanent-false or unknown cell.
func (p CapabilityPolicyV1) DisableOnly(cells []CapabilityCell) (CapabilityPolicyV1, error) {
	if err := p.Validate(); err != nil {
		return CapabilityPolicyV1{}, err
	}
	result := p
	result.cells = make(map[CapabilityCell]DesiredCell, len(p.cells))
	for cell, desired := range p.cells {
		result.cells[cell] = desired
	}
	for _, cell := range cells {
		desired, err := result.Desired(cell)
		if err != nil {
			return CapabilityPolicyV1{}, err
		}
		if desired.Enabled {
			desired.Enabled = false
			desired.Reason = ReasonPolicyDisabled
			result.cells[cell] = desired
		}
	}
	return result, nil
}

func desiredCell(cell CapabilityCell) (bool, CapabilityReasonCode) {
	if cell.Axis == CapabilityReview {
		return true, ""
	}
	if cell.PathClass == PathClassAbsolute || cell.PathClass == PathClassTraversal || cell.PathClass == PathClassGitAdmin || cell.PathClass == PathClassCollision || cell.PathClass == PathClassSpecial {
		return false, ReasonPermanentFalse
	}
	if cell.FileKind == repository.FileKindGitlink || cell.FileKind == repository.FileKindDirectory || cell.FileKind == repository.FileKindUnknown {
		return false, ReasonPermanentFalse
	}
	if cell.FileKind == repository.FileKindSymlink && cell.ChangeKind != repository.ChangeAdded && cell.ChangeKind != repository.ChangeModified && cell.ChangeKind != repository.ChangeDeleted {
		return false, ReasonPermanentFalse
	}
	return true, ""
}

// ImplementationEvidence is registered owner/conformance evidence for one
// exact desired cell.
type ImplementationEvidence struct {
	Cell              CapabilityCell
	OwnerVersion      string
	ConformanceSet    string
	ExpiresWithPolicy ResourcePolicyVersion
	EvidenceVersion   EvidenceVersion
	Supported         bool
}

// Validate checks evidence identity and version freshness.
func (e ImplementationEvidence) Validate(policy CapabilityPolicyV1) error {
	if e.Cell.Validate() != nil || !stableText(e.OwnerVersion) || !stableText(e.ConformanceSet) || e.ExpiresWithPolicy != policy.ResourcePolicyVersion || e.EvidenceVersion != policy.EvidenceVersion || !e.Supported {
		return ErrInvalidCapabilityEvidence
	}
	return nil
}

// CapabilityKey binds a decision to one exact path/capture/policy identity.
type CapabilityKey struct {
	Path                  repository.RepoPathKey
	CaptureID             domain.CaptureID
	PolicyVersion         CapabilityPolicyVersion
	ResourcePolicyVersion ResourcePolicyVersion
	EvidenceVersion       EvidenceVersion
}

func (k CapabilityKey) Validate(policy CapabilityPolicyV1) error {
	if k.Path == "" || k.CaptureID == "" || k.PolicyVersion != policy.Version || k.ResourcePolicyVersion != policy.ResourcePolicyVersion || k.EvidenceVersion != policy.EvidenceVersion {
		return ErrInvalidCapabilityPolicy
	}
	return nil
}

// PlatformEvidence records current primitive qualification without making a
// platform label itself sufficient proof.
type PlatformEvidence struct {
	CanonicalPath bool
	NativeAction  bool
	NoFollow      bool
	HeldHandles   bool
}

// SessionEvidence records current session/destination qualification.
type SessionEvidence struct {
	SnapshotLease     bool
	ReadContainment   bool
	ProposalWorkspace bool
	EditDestination   bool
	ApplyLease        bool
	Permissions       bool
}

// CurrentCapabilityEvidence is the non-persisted evidence intersection used
// to evaluate one request.
type CurrentCapabilityEvidence struct {
	GitDeterministic      bool
	LimitOutcome          LimitOutcome
	ResourcePolicyVersion ResourcePolicyVersion
	EvidenceVersion       EvidenceVersion
	Platform              PlatformEvidence
	Session               SessionEvidence
}

// CapabilityRequest is an exact entry evaluation input.
type CapabilityRequest struct {
	Key                    CapabilityKey
	FileKind               repository.FileKind
	ChangeKind             repository.ChangeKind
	PathClass              PathClass
	Index                  IndexState
	ImplementationEvidence []ImplementationEvidence
	Current                CurrentCapabilityEvidence
}

func (r CapabilityRequest) cell(axis CapabilityAxis) CapabilityCell {
	return CapabilityCell{FileKind: r.FileKind, ChangeKind: r.ChangeKind, PathClass: r.PathClass, Axis: axis}
}

// CapabilityDecision is the effective provider-neutral repository decision.
// Provider/account/disclosure state is intentionally absent.
type CapabilityDecision struct {
	Key                       CapabilityKey
	Review                    bool
	Anchor                    bool
	MaterializeReviewSnapshot bool
	Propose                   bool
	Apply                     bool
	ReasonsByAxis             map[CapabilityAxis][]CapabilityReason
	PlatformRequirements      []PlatformRequirement
	PolicyVersion             CapabilityPolicyVersion
	ResourcePolicyVersion     ResourcePolicyVersion
	EvidenceVersion           EvidenceVersion
}

// Validate checks the decision shape and version identity before persistence
// or consumer dispatch.
func (d CapabilityDecision) Validate(policy CapabilityPolicyV1) error {
	if d.Key.Validate(policy) != nil || d.PolicyVersion != policy.Version || d.ResourcePolicyVersion != policy.ResourcePolicyVersion || d.EvidenceVersion != policy.EvidenceVersion {
		return ErrInvalidCapabilityPolicy
	}
	if len(d.ReasonsByAxis) != len(capabilityAxes) {
		return ErrInvalidCapabilityPolicy
	}
	for _, axis := range capabilityAxes {
		reasons, ok := d.ReasonsByAxis[axis]
		if !ok {
			return ErrInvalidCapabilityPolicy
		}
		for _, reason := range reasons {
			if !validReasonCode(reason.Code) {
				return ErrInvalidCapabilityPolicy
			}
		}
	}
	return nil
}

// PlatformRequirement identifies a safe primitive required by an axis.
type PlatformRequirement string

const (
	RequirementCanonicalPath PlatformRequirement = "canonical_path"
	RequirementNoFollow      PlatformRequirement = "no_follow"
	RequirementNativeAction  PlatformRequirement = "native_action"
	RequirementHeldHandles   PlatformRequirement = "held_handles"
)

// ResolveCapability intersects desired policy, exact owner evidence, current
// Git/limit/platform/session evidence, and index safety. Review remains safe
// and visible when mutation axes are disabled.
func ResolveCapability(policy CapabilityPolicyV1, request CapabilityRequest) (CapabilityDecision, error) {
	if err := policy.Validate(); err != nil || request.Key.Validate(policy) != nil || !validFileKind(request.FileKind) || !validChangeKind(request.ChangeKind) || !validPathClass(request.PathClass) || !validIndexState(request.Index) {
		return CapabilityDecision{}, ErrInvalidCapabilityPolicy
	}
	decision := CapabilityDecision{
		Key:                   request.Key,
		ReasonsByAxis:         make(map[CapabilityAxis][]CapabilityReason, len(capabilityAxes)),
		PolicyVersion:         policy.Version,
		ResourcePolicyVersion: policy.ResourcePolicyVersion,
		EvidenceVersion:       policy.EvidenceVersion,
		PlatformRequirements:  requirementsFor(request),
	}
	for _, axis := range capabilityAxes {
		desired, err := policy.Desired(request.cell(axis))
		if err != nil {
			return CapabilityDecision{}, err
		}
		enabled, reasons := resolveAxis(axis, desired, request, policy)
		decision.ReasonsByAxis[axis] = reasons
		switch axis {
		case CapabilityReview:
			decision.Review = enabled
		case CapabilityAnchor:
			decision.Anchor = enabled
		case CapabilityMaterializeReviewSnapshot:
			decision.MaterializeReviewSnapshot = enabled
		case CapabilityPropose:
			decision.Propose = enabled
		case CapabilityApply:
			decision.Apply = enabled
		}
	}
	return decision, nil
}

func resolveAxis(axis CapabilityAxis, desired DesiredCell, request CapabilityRequest, policy CapabilityPolicyV1) (bool, []CapabilityReason) {
	if !desired.Enabled {
		return false, []CapabilityReason{{Code: desired.Reason}}
	}
	if axis == CapabilityReview {
		return true, nil
	}
	if !request.Current.GitDeterministic {
		return false, []CapabilityReason{{Code: ReasonCapabilityNotImplemented}}
	}
	if request.Current.ResourcePolicyVersion != policy.ResourcePolicyVersion || request.Current.EvidenceVersion != policy.EvidenceVersion {
		return false, []CapabilityReason{{Code: ReasonPolicyMismatch}}
	}
	if request.Current.LimitOutcome != "" && request.Current.LimitOutcome != LimitAccepted {
		return false, []CapabilityReason{{Code: ReasonResourceLimit}}
	}
	if unsafeIndexState(request.Index) {
		return false, []CapabilityReason{{Code: ReasonIndexUnsafe}}
	}
	cell := request.cell(axis)
	evidence, found, stale := matchingEvidence(cell, request.ImplementationEvidence, policy)
	if !found {
		if stale {
			return false, []CapabilityReason{{Code: ReasonEvidenceStale}}
		}
		return false, []CapabilityReason{{Code: ReasonCapabilityNotImplemented}}
	}
	if evidence.Validate(policy) != nil {
		return false, []CapabilityReason{{Code: ReasonEvidenceStale}}
	}
	if !platformAllows(axis, request) {
		return false, []CapabilityReason{{Code: ReasonPlatformUnqualified}}
	}
	if !sessionAllows(axis, request.Current.Session) {
		return false, []CapabilityReason{{Code: ReasonSessionUnavailable}}
	}
	return true, nil
}

func matchingEvidence(cell CapabilityCell, all []ImplementationEvidence, policy CapabilityPolicyV1) (ImplementationEvidence, bool, bool) {
	stale := false
	for _, evidence := range all {
		if evidence.Cell != cell {
			continue
		}
		if evidence.ExpiresWithPolicy != policy.ResourcePolicyVersion || evidence.EvidenceVersion != policy.EvidenceVersion {
			stale = true
			continue
		}
		if evidence.Supported {
			return evidence, true, stale
		}
	}
	return ImplementationEvidence{}, false, stale
}

func platformAllows(axis CapabilityAxis, request CapabilityRequest) bool {
	requirements := requirementsFor(request)
	for _, requirement := range requirements {
		switch requirement {
		case RequirementCanonicalPath:
			if !request.Current.Platform.CanonicalPath {
				return false
			}
		case RequirementNoFollow:
			if !request.Current.Platform.NoFollow {
				return false
			}
		case RequirementNativeAction:
			if axis == CapabilityApply && !request.Current.Platform.NativeAction {
				return false
			}
		case RequirementHeldHandles:
			if axis == CapabilityApply && !request.Current.Platform.HeldHandles {
				return false
			}
		}
	}
	return true
}

func sessionAllows(axis CapabilityAxis, session SessionEvidence) bool {
	switch axis {
	case CapabilityAnchor:
		return session.SnapshotLease && session.ReadContainment
	case CapabilityMaterializeReviewSnapshot:
		return session.SnapshotLease && session.ReadContainment
	case CapabilityPropose:
		return session.SnapshotLease && session.ReadContainment && session.ProposalWorkspace && session.Permissions
	case CapabilityApply:
		return session.EditDestination && session.ApplyLease && session.Permissions
	default:
		return true
	}
}

func requirementsFor(request CapabilityRequest) []PlatformRequirement {
	requirements := make([]PlatformRequirement, 0, 3)
	if request.PathClass == PathClassRaw || request.PathClass == PathClassNativeInvalid {
		requirements = append(requirements, RequirementCanonicalPath)
	}
	if request.FileKind == repository.FileKindSymlink {
		requirements = append(requirements, RequirementNoFollow)
	}
	if request.PathClass == PathClassRaw || request.PathClass == PathClassNativeInvalid || request.FileKind == repository.FileKindSymlink {
		requirements = append(requirements, RequirementNativeAction)
	}
	if request.FileKind == repository.FileKindSymlink || request.PathClass == PathClassRaw || request.PathClass == PathClassNativeInvalid {
		requirements = append(requirements, RequirementHeldHandles)
	}
	sort.Slice(requirements, func(i, j int) bool { return requirements[i] < requirements[j] })
	return requirements
}

// CapturePolicyEvaluation is immutable policy/evidence identity recorded with
// an accepted capture evaluation.
type CapturePolicyEvaluation struct {
	CaptureID             domain.CaptureID
	CaptureFormatVersion  uint32
	PolicyVersion         CapabilityPolicyVersion
	ResourcePolicyVersion ResourcePolicyVersion
	EvidenceVersion       EvidenceVersion
	Decisions             []CapabilityDecision
	ManifestHash          string
}

// Validate checks that every decision belongs to this exact capture and
// policy identity and that no duplicate path decision is present.
func (e CapturePolicyEvaluation) Validate(policy CapabilityPolicyV1) error {
	if e.CaptureID == "" || e.CaptureFormatVersion == 0 || e.PolicyVersion != policy.Version || e.ResourcePolicyVersion != policy.ResourcePolicyVersion || e.EvidenceVersion != policy.EvidenceVersion || !stableText(e.ManifestHash) || e.ManifestHash == "" {
		return ErrInvalidCapabilityPolicy
	}
	seen := make(map[CapabilityKey]struct{}, len(e.Decisions))
	for _, decision := range e.Decisions {
		if decision.Key.CaptureID != e.CaptureID || decision.PolicyVersion != e.PolicyVersion || decision.ResourcePolicyVersion != e.ResourcePolicyVersion || decision.EvidenceVersion != e.EvidenceVersion || decision.Validate(policy) != nil {
			return ErrInvalidCapabilityPolicy
		}
		if _, duplicate := seen[decision.Key]; duplicate {
			return ErrInvalidCapabilityPolicy
		}
		seen[decision.Key] = struct{}{}
	}
	return nil
}

// DiscussionMode separates read-only discussion from proposal authorization.
type DiscussionMode string

const (
	DiscussionModeReadOnly DiscussionMode = "read_only"
	DiscussionModeProposal DiscussionMode = "proposal"
)

// DiscussionEvidence contains application/provider/account/disclosure state
// that must not rewrite repository capability truth.
type DiscussionEvidence struct {
	ProviderCompatibility bool
	Account               bool
	Disclosure            bool
	TurnPermission        bool
}

// DiscussionAvailability is the application-level composition of repository
// capability and provider/account/disclosure/permission evidence.
type DiscussionAvailability struct {
	Available             bool
	Mode                  DiscussionMode
	Materialization       bool
	SnapshotLease         bool
	ReadContainment       bool
	ProviderCompatibility bool
	Account               bool
	Disclosure            bool
	TurnPermission        bool
	Reasons               []CapabilityReason
}

// ComposeDiscussionAvailability keeps provider state outside stored repository
// capability decisions.
func ComposeDiscussionAvailability(mode DiscussionMode, decision CapabilityDecision, repositoryEvidence SessionEvidence, providerEvidence DiscussionEvidence) DiscussionAvailability {
	availability := DiscussionAvailability{
		Mode:                  mode,
		Materialization:       decision.MaterializeReviewSnapshot,
		SnapshotLease:         repositoryEvidence.SnapshotLease,
		ReadContainment:       repositoryEvidence.ReadContainment,
		ProviderCompatibility: providerEvidence.ProviderCompatibility,
		Account:               providerEvidence.Account,
		Disclosure:            providerEvidence.Disclosure,
		TurnPermission:        providerEvidence.TurnPermission,
	}
	if !availability.Materialization {
		availability.Reasons = append(availability.Reasons, CapabilityReason{Code: ReasonCapabilityNotImplemented})
	}
	if !availability.SnapshotLease {
		availability.Reasons = append(availability.Reasons, CapabilityReason{Code: ReasonSessionUnavailable})
	}
	if !availability.ReadContainment {
		availability.Reasons = append(availability.Reasons, CapabilityReason{Code: ReasonPlatformUnqualified})
	}
	if !availability.ProviderCompatibility {
		availability.Reasons = append(availability.Reasons, CapabilityReason{Code: ReasonProviderUnavailable})
	}
	if !availability.Account {
		availability.Reasons = append(availability.Reasons, CapabilityReason{Code: ReasonAccountUnavailable})
	}
	if !availability.Disclosure {
		availability.Reasons = append(availability.Reasons, CapabilityReason{Code: ReasonDisclosureUnavailable})
	}
	if !availability.TurnPermission {
		availability.Reasons = append(availability.Reasons, CapabilityReason{Code: ReasonPermissionUnavailable})
	}
	if mode == DiscussionModeProposal && !decision.Propose {
		availability.Reasons = append(availability.Reasons, CapabilityReason{Code: ReasonCapabilityNotImplemented})
	}
	availability.Available = len(availability.Reasons) == 0
	return availability
}

func unsafeIndexState(state IndexState) bool {
	switch state {
	case IndexUnmerged, IndexSparseUnknown, IndexFilterUnknown, IndexUnsafeHardLink, IndexUnknown:
		return true
	default:
		return false
	}
}

func validFileKind(kind repository.FileKind) bool {
	switch kind {
	case repository.FileKindRegular, repository.FileKindSymlink, repository.FileKindGitlink, repository.FileKindDirectory, repository.FileKindUnknown:
		return true
	default:
		return false
	}
}

func allFileKinds() []repository.FileKind {
	return []repository.FileKind{repository.FileKindRegular, repository.FileKindSymlink, repository.FileKindGitlink, repository.FileKindDirectory, repository.FileKindUnknown}
}

func validChangeKind(kind repository.ChangeKind) bool {
	switch kind {
	case repository.ChangeAdded, repository.ChangeModified, repository.ChangeDeleted, repository.ChangeRenamed, repository.ChangeCopied, repository.ChangeTypeChanged, repository.ChangeUntracked:
		return true
	default:
		return false
	}
}

func allChangeKinds() []repository.ChangeKind {
	return []repository.ChangeKind{repository.ChangeAdded, repository.ChangeModified, repository.ChangeDeleted, repository.ChangeRenamed, repository.ChangeCopied, repository.ChangeTypeChanged, repository.ChangeUntracked}
}

func validPathClass(class PathClass) bool {
	for _, candidate := range pathClasses {
		if candidate == class {
			return true
		}
	}
	return false
}

func validCapabilityAxis(axis CapabilityAxis) bool {
	for _, candidate := range capabilityAxes {
		if candidate == axis {
			return true
		}
	}
	return false
}

func validIndexState(state IndexState) bool {
	switch state {
	case IndexClean, IndexStaged, IndexUnstaged, IndexStagedAndUnstaged, IndexUnmerged, IndexSparseUnknown, IndexFilterUnknown, IndexUnsafeHardLink, IndexUnknown:
		return true
	default:
		return false
	}
}

func validReasonCode(code CapabilityReasonCode) bool {
	switch code {
	case ReasonCapabilityNotImplemented, ReasonEvidenceStale, ReasonEvidenceWrongCell, ReasonPolicyDisabled, ReasonPermanentFalse, ReasonPolicyMismatch, ReasonResourceLimit, ReasonPlatformUnqualified, ReasonSessionUnavailable, ReasonIndexUnsafe, ReasonProviderUnavailable, ReasonAccountUnavailable, ReasonDisclosureUnavailable, ReasonPermissionUnavailable:
		return true
	default:
		return false
	}
}

func stableText(value string) bool {
	if value == "" || strings.ContainsRune(value, '\uFFFD') {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return false
		}
	}
	return true
}
