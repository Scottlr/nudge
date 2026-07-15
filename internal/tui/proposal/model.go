// Package proposal owns the bounded proposal-review projection. It never
// reads a workspace or treats provider output as patch truth.
package proposal

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
	"github.com/Scottlr/nudge/internal/theme"
	"github.com/Scottlr/nudge/internal/tui/viewport"
)

const (
	defaultEntryLimit = 32
	defaultHunkLimit  = 64
	maxRetainedPages  = 6
	maxRetainedHunks  = 4
	maxCoverageSpans  = 4096
	maxRangeBytes     = 256 * 1024
	defaultHeight     = 24
)

var (
	ErrInvalidProjection = errors.New("invalid proposal review projection")
	ErrInvalidPage       = errors.New("invalid proposal review page")
	ErrInvalidRange      = errors.New("invalid proposal review range")
)

// Mode is the local review surface displayed by the component.
type Mode string

const (
	ModeDiscussion Mode = "discussion"
	ModeReview     Mode = "proposal_review"
)

func (m Mode) Validate() error {
	if m != ModeDiscussion && m != ModeReview {
		return ErrInvalidProjection
	}
	return nil
}

// Applicability is the independent gate that explains why a ready-looking
// proposal may still be non-approvable.
type Applicability string

const (
	ApplicabilityReady        Applicability = "ready"
	ApplicabilityStale        Applicability = "stale"
	ApplicabilityUnsupported  Applicability = "unsupported"
	ApplicabilityIncomplete   Applicability = "incomplete"
	ApplicabilityRepairNeeded Applicability = "repair_required"
)

func (a Applicability) Validate() error {
	switch a {
	case ApplicabilityReady, ApplicabilityStale, ApplicabilityUnsupported, ApplicabilityIncomplete, ApplicabilityRepairNeeded:
		return nil
	default:
		return ErrInvalidProjection
	}
}

// Projection is the bounded, display-oriented view of one immutable proposal
// version. It carries identities and summary evidence, not patch bytes.
type Projection struct {
	Revision            uint64
	ProposalID          domain.ProposalID
	Version             review.ProposalVersionNumber
	PatchSHA256         string
	IndexHash           string
	ArtifactID          string
	PatchBytes          uint64
	FileCount           uint64
	HunkCount           uint64
	RowCount            uint64
	Scope               review.ProposalScope
	ScopeReason         string
	Status              review.ProposalStatus
	StatusReason        string
	Applicability       Applicability
	ApplicabilityReason string
	Destination         string
	Warnings            []string
	NoChanges           bool
	FailedAttemptID     domain.OperationID
	FailedAttemptReason string
	ResultDisposition   review.ProposalResultDisposition
}

func (p Projection) Validate() error {
	if p.Revision == 0 || p.ProposalID == "" || !utf8.ValidString(string(p.ProposalID)) {
		return ErrInvalidProjection
	}
	if p.NoChanges {
		if p.Version != 0 || p.PatchSHA256 != "" || p.IndexHash != "" || p.ArtifactID != "" || p.PatchBytes != 0 || p.FileCount != 0 || p.HunkCount != 0 || p.RowCount != 0 || p.Status != "" {
			return ErrInvalidProjection
		}
		if p.FailedAttemptID != "" && (p.ResultDisposition != review.ProposalResultPresent && p.ResultDisposition != review.ProposalResultDiscarding || p.FailedAttemptReason == "" || !utf8.ValidString(string(p.FailedAttemptID))) {
			return ErrInvalidProjection
		}
		if p.FailedAttemptID == "" && p.ResultDisposition != "" && p.ResultDisposition != review.ProposalResultNone {
			return ErrInvalidProjection
		}
		return validateProjectionText(p.ScopeReason, p.StatusReason, p.ApplicabilityReason, p.Destination, p.Warnings)
	}
	if p.Version == 0 || !validSHA256(p.PatchSHA256) || !validSHA256(p.IndexHash) || p.ArtifactID == "" || p.PatchBytes == 0 || p.FileCount == 0 || p.Scope.Validate() != nil || p.Status.Validate() != nil || p.Applicability.Validate() != nil || p.Destination == "" {
		return ErrInvalidProjection
	}
	if p.StatusReason == "" || p.ApplicabilityReason == "" {
		return ErrInvalidProjection
	}
	return validateProjectionText(p.ScopeReason, p.StatusReason, p.ApplicabilityReason, p.Destination, p.Warnings)
}

func validateProjectionText(values ...any) error {
	for _, value := range values {
		switch current := value.(type) {
		case string:
			if !utf8.ValidString(current) || len(current) > 1024 {
				return ErrInvalidProjection
			}
		case []string:
			if len(current) > 32 {
				return ErrInvalidProjection
			}
			for _, item := range current {
				if !utf8.ValidString(item) || len(item) > 512 {
					return ErrInvalidProjection
				}
			}
		}
	}
	return nil
}

// Entry is one complete file-level index item. Paths remain raw repository
// identities until the view projects them through terminal sanitization.
type Entry struct {
	ID             string
	Ordinal        uint64
	Path           repository.RepoPath
	OldPath        *repository.RepoPath
	Kind           repository.ChangeKind
	OldKind        repository.FileKind
	NewKind        repository.FileKind
	OldMode        uint32
	NewMode        uint32
	ModeTransition *repository.ModeTransition
	Binary         bool
	Unsupported    bool
	Reason         string
	Offset         int64
	Length         int64
	HunkCount      uint64
	Bytes          uint64
	SHA256         string
}

func (e Entry) Validate() error {
	if e.ID == "" || len(e.ID) > 128 || !utf8.ValidString(e.ID) || e.Path.Validate() != nil || e.Offset < 0 || e.Length <= 0 || !validSHA256(e.SHA256) || !utf8.ValidString(e.Reason) || len(e.Reason) > 512 {
		return ErrInvalidPage
	}
	if e.OldPath != nil && e.OldPath.Validate() != nil {
		return ErrInvalidPage
	}
	newPath := clonePath(&e.Path)
	if e.Kind == repository.ChangeDeleted {
		newPath = nil
	}
	var transition *repository.ModeTransition
	if e.ModeTransition != nil {
		value := *e.ModeTransition
		transition = &value
	}
	change := repository.ChangedFile{OldPath: clonePath(e.OldPath), NewPath: newPath, Kind: e.Kind, OldFileKind: e.OldKind, NewFileKind: e.NewKind, OldMode: e.OldMode, NewMode: e.NewMode, ModeTransition: transition, Binary: e.Binary}
	if change.Validate() != nil {
		return ErrInvalidPage
	}
	return nil
}

// Hunk is bounded metadata for one immutable patch hunk.
type Hunk struct {
	ID        string
	Ordinal   uint64
	Offset    int64
	Length    int64
	BaseStart int
	BaseCount int
	HeadStart int
	HeadCount int
	Rows      int
	SHA256    string
}

func (h Hunk) Validate() error {
	if h.ID == "" || len(h.ID) > 128 || !utf8.ValidString(h.ID) || h.Offset < 0 || h.Length <= 0 || h.BaseStart < 0 || h.BaseCount < 0 || h.HeadStart < 0 || h.HeadCount < 0 || h.Rows < 0 || !validSHA256(h.SHA256) {
		return ErrInvalidPage
	}
	return nil
}

// EntryPageRequest is a stable identity-bound keyset page request.
type EntryPageRequest struct {
	ProposalID  domain.ProposalID
	Version     review.ProposalVersionNumber
	PatchSHA256 string
	IndexHash   string
	Cursor      string
	Limit       int
	Token       uint64
}

func (r EntryPageRequest) Validate() error {
	if r.ProposalID == "" || r.Version == 0 || !validSHA256(r.PatchSHA256) || !validSHA256(r.IndexHash) || !validCursor(r.Cursor) || r.Limit <= 0 || r.Limit > defaultEntryLimit || r.Token == 0 {
		return ErrInvalidPage
	}
	return nil
}

// EntryPage is a complete bounded response for one entry request.
type EntryPage struct {
	Request    EntryPageRequest
	Entries    []Entry
	NextCursor string
	Total      uint64
}

func (p EntryPage) Validate() error {
	if p.Request.Validate() != nil || len(p.Entries) > p.Request.Limit || !validCursor(p.NextCursor) || p.Total < uint64(len(p.Entries)) {
		return ErrInvalidPage
	}
	previous := uint64(0)
	seen := make(map[string]struct{}, len(p.Entries))
	for index, entry := range p.Entries {
		if entry.Validate() != nil || index > 0 && entry.Ordinal <= previous {
			return ErrInvalidPage
		}
		previous = entry.Ordinal
		if _, exists := seen[entry.ID]; exists {
			return ErrInvalidPage
		}
		seen[entry.ID] = struct{}{}
	}
	return nil
}

// HunkPageRequest identifies one entry's bounded hunk page.
type HunkPageRequest struct {
	ProposalID  domain.ProposalID
	Version     review.ProposalVersionNumber
	PatchSHA256 string
	IndexHash   string
	EntryID     string
	Cursor      string
	Limit       int
	Token       uint64
}

func (r HunkPageRequest) Validate() error {
	if r.ProposalID == "" || r.Version == 0 || !validSHA256(r.PatchSHA256) || !validSHA256(r.IndexHash) || r.EntryID == "" || !utf8.ValidString(r.EntryID) || !validCursor(r.Cursor) || r.Limit <= 0 || r.Limit > defaultHunkLimit || r.Token == 0 {
		return ErrInvalidPage
	}
	return nil
}

// HunkPage is a complete bounded response for one hunk request.
type HunkPage struct {
	Request    HunkPageRequest
	Hunks      []Hunk
	NextCursor string
	Total      uint64
}

func (p HunkPage) Validate() error {
	if p.Request.Validate() != nil || len(p.Hunks) > p.Request.Limit || !validCursor(p.NextCursor) || p.Total < uint64(len(p.Hunks)) {
		return ErrInvalidPage
	}
	previous := uint64(0)
	seen := make(map[string]struct{}, len(p.Hunks))
	for index, hunk := range p.Hunks {
		if hunk.Validate() != nil || index > 0 && hunk.Ordinal <= previous {
			return ErrInvalidPage
		}
		previous = hunk.Ordinal
		if _, exists := seen[hunk.ID]; exists {
			return ErrInvalidPage
		}
		seen[hunk.ID] = struct{}{}
	}
	return nil
}

// PatchRangeRequest identifies one bounded range in the immutable patch.
type PatchRangeRequest struct {
	ProposalID  domain.ProposalID
	Version     review.ProposalVersionNumber
	PatchSHA256 string
	IndexHash   string
	PatchBytes  uint64
	Offset      uint64
	MaxBytes    uint64
	Token       uint64
}

func (r PatchRangeRequest) Validate() error {
	if r.ProposalID == "" || r.Version == 0 || !validSHA256(r.PatchSHA256) || !validSHA256(r.IndexHash) || r.PatchBytes == 0 || r.Offset > r.PatchBytes || r.MaxBytes == 0 || r.MaxBytes > maxRangeBytes || r.Token == 0 {
		return ErrInvalidRange
	}
	return nil
}

// PatchRange is a verified immutable response. Bytes are retained only for
// the current visible window; coverage stores identity and interval metadata.
type PatchRange struct {
	Request  PatchRangeRequest
	Bytes    []byte
	SHA256   string
	Complete bool
}

func (r PatchRange) Validate() error {
	if r.Request.Validate() != nil || !r.Complete || uint64(len(r.Bytes)) == 0 || uint64(len(r.Bytes)) > r.Request.MaxBytes || uint64(len(r.Bytes)) > r.Request.PatchBytes || r.Request.Offset > r.Request.PatchBytes-uint64(len(r.Bytes)) || !validSHA256(r.SHA256) {
		return ErrInvalidRange
	}
	digest := sha256.Sum256(r.Bytes)
	if hex.EncodeToString(digest[:]) != r.SHA256 {
		return ErrInvalidRange
	}
	return nil
}

// ActionIdentity is the exact identity carried to proposal command handlers.
type ActionIdentity struct {
	ProposalID  domain.ProposalID
	Version     review.ProposalVersionNumber
	PatchSHA256 string
	IndexHash   string
}

func (i ActionIdentity) Validate(p Projection) error {
	if p.Validate() != nil || p.NoChanges || i.ProposalID != p.ProposalID || i.Version != p.Version || i.PatchSHA256 != p.PatchSHA256 || i.IndexHash != p.IndexHash {
		return ErrInvalidProjection
	}
	return nil
}

// ApproveProposalIntent and RejectProposalIntent are deliberately separate
// from runtime-approval intents and carry the exact reviewed identity.
type ApproveProposalIntent struct {
	Identity    ActionIdentity
	Destination string
}

type RejectProposalIntent struct{ Identity ActionIdentity }

// ResultDiscardIdentity binds failed-result cleanup to the exact terminal
// attempt currently visible in proposal mode.
type ResultDiscardIdentity struct {
	ProposalID domain.ProposalID
	AttemptID  domain.OperationID
}

func (i ResultDiscardIdentity) Validate(p Projection) error {
	if p.Validate() != nil || !p.NoChanges || p.FailedAttemptID == "" || i.ProposalID != p.ProposalID || i.AttemptID != p.FailedAttemptID {
		return ErrInvalidProjection
	}
	return nil
}

// DiscardProposalResultIntent is deliberately distinct from RejectProposalIntent.
type DiscardProposalResultIntent struct {
	Identity ResultDiscardIdentity
	Reason   string
}

// ModeIntent returns the root to discussion after a terminal review outcome.
type ModeIntent struct {
	Mode     Mode
	Identity ActionIdentity
}

// Intent is the child-to-root boundary. The component never invokes a store,
// Git, provider, or application command directly.
type Intent struct {
	EntryPage *EntryPageRequest
	HunkPage  *HunkPageRequest
	Range     *PatchRangeRequest
	Approve   *ApproveProposalIntent
	Reject    *RejectProposalIntent
	Discard   *DiscardProposalResultIntent
	Mode      *ModeIntent
}

type entryPageState struct{ page EntryPage }
type hunkPageState struct{ page HunkPage }
type coverageSpan struct{ start, end uint64 }

type confirmationKind string

const (
	confirmationNone    confirmationKind = ""
	confirmationApprove confirmationKind = "approve"
	confirmationReject  confirmationKind = "reject"
	confirmationDiscard confirmationKind = "discard"
)

type reviewEvidence struct {
	entries            map[string]struct{}
	hunks              map[string]struct{}
	hunksComplete      map[string]bool
	coverage           []coverageSpan
	disclosureAccepted bool
}

func newReviewEvidence() reviewEvidence {
	return reviewEvidence{entries: make(map[string]struct{}), hunks: make(map[string]struct{}), hunksComplete: make(map[string]bool)}
}

// Model is a bounded, disposable proposal-review projection.
type Model struct {
	projection    Projection
	mode          Mode
	entries       map[string]entryPageState
	entryOrder    []string
	entryNext     string
	entryTotal    uint64
	entryPending  *EntryPageRequest
	selectedEntry string
	hunks         map[string]hunkPageState
	hunkOrder     []string
	hunkNext      string
	hunkPending   *HunkPageRequest
	selectedHunk  string
	currentRange  *PatchRange
	rangePending  *PatchRangeRequest
	evidence      reviewEvidence
	confirmation  confirmationKind
	nextToken     uint64
	lastError     string
	focused       bool
	width, height int
	top           int
	overscan      int
	budget        viewport.RenderBudget
	theme         theme.Theme
}

// NewModel creates an empty bounded proposal review projection.
func NewModel() *Model {
	return &Model{mode: ModeDiscussion, entries: make(map[string]entryPageState), hunks: make(map[string]hunkPageState), evidence: newReviewEvidence(), overscan: 2, budget: viewport.DefaultRenderBudget(), theme: theme.BuiltinTerminalDefault()}
}

func (m *Model) SetSize(width, height int) {
	if m == nil {
		return
	}
	m.width, m.height = maxInt(width, 0), maxInt(height, 0)
	m.reposition()
}

func (m *Model) SetTheme(value theme.Theme) {
	if m != nil && value.Validate() == nil {
		m.theme = value
	}
}

func (m *Model) SetBudget(value viewport.RenderBudget) {
	if m != nil && value.Validate() == nil {
		m.budget = value
	}
}

func (m *Model) SetFocus(focused bool) {
	if m != nil {
		m.focused = focused
	}
}

func (m *Model) Projection() Projection {
	if m == nil {
		return Projection{}
	}
	return cloneProjection(m.projection)
}

func (m *Model) Mode() Mode {
	if m == nil {
		return ModeDiscussion
	}
	return m.mode
}

func (m *Model) SelectedEntry() string {
	if m == nil {
		return ""
	}
	return m.selectedEntry
}

func (m *Model) SelectedHunk() string {
	if m == nil {
		return ""
	}
	return m.selectedHunk
}

func (m *Model) LastError() string {
	if m == nil {
		return ""
	}
	return m.lastError
}

// Confirmation reports the pending exact whole-proposal confirmation.
func (m *Model) Confirmation() string {
	if m == nil {
		return ""
	}
	return string(m.confirmation)
}

// CanApprove reports whether complete, identity-bound disclosure permits the
// separate Approve proposal command to be emitted.
func (m *Model) CanApprove() bool {
	if m == nil || m.projection.Validate() != nil || m.projection.NoChanges || m.projection.Status != review.ProposalVersionReady || m.projection.Applicability != ApplicabilityReady || !m.evidenceComplete() || !m.evidence.disclosureAccepted {
		return false
	}
	return true
}

// CanDiscardResult reports whether an exact terminal failed-result reset may
// be confirmed. A ready version never satisfies this gate.
func (m *Model) CanDiscardResult() bool {
	return m != nil && m.projection.Validate() == nil && m.projection.NoChanges && m.projection.FailedAttemptID != "" && (m.projection.ResultDisposition == review.ProposalResultPresent || m.projection.ResultDisposition == review.ProposalResultDiscarding)
}

// DisclosureSummary returns bounded evidence counts for status bars and tests.
func (m *Model) DisclosureSummary() (entries, totalEntries, ranges, totalRanges uint64) {
	if m == nil || m.projection.Validate() != nil || m.projection.NoChanges {
		return 0, 0, 0, 0
	}
	entries = uint64(len(m.evidence.entries))
	totalEntries = m.projection.FileCount
	for _, span := range m.evidence.coverage {
		ranges += span.end - span.start
	}
	totalRanges = m.projection.PatchBytes
	return entries, totalEntries, ranges, totalRanges
}

func (m *Model) resetReview() {
	m.entries = make(map[string]entryPageState)
	m.entryOrder = nil
	m.entryNext = ""
	m.entryTotal = 0
	m.entryPending = nil
	m.selectedEntry = ""
	m.hunks = make(map[string]hunkPageState)
	m.hunkOrder = nil
	m.hunkNext = ""
	m.hunkPending = nil
	m.selectedHunk = ""
	m.currentRange = nil
	m.rangePending = nil
	m.evidence = newReviewEvidence()
	m.confirmation = confirmationNone
	m.top = 0
}

func (m *Model) identityChanged(next Projection) bool {
	return m.projection.ProposalID != next.ProposalID || m.projection.Version != next.Version || m.projection.PatchSHA256 != next.PatchSHA256 || m.projection.IndexHash != next.IndexHash
}

func (m *Model) initialEntryRequest() []Intent {
	if m == nil || m.projection.Validate() != nil || m.projection.NoChanges || m.entryPending != nil || len(m.entries) != 0 {
		return nil
	}
	return m.requestEntryPage("")
}

func (m *Model) requestEntryPage(cursor string) []Intent {
	if m == nil || m.projection.Validate() != nil || m.projection.NoChanges || m.entryPending != nil {
		return nil
	}
	m.nextToken++
	request := EntryPageRequest{ProposalID: m.projection.ProposalID, Version: m.projection.Version, PatchSHA256: m.projection.PatchSHA256, IndexHash: m.projection.IndexHash, Cursor: cursor, Limit: defaultEntryLimit, Token: m.nextToken}
	m.entryPending = &request
	return []Intent{{EntryPage: &request}}
}

func (m *Model) requestHunkPage(cursor string) []Intent {
	entry, ok := m.selectedEntryValue()
	if m == nil || !ok || m.hunkPending != nil || m.projection.Validate() != nil || m.projection.NoChanges || entry.HunkCount == 0 {
		return nil
	}
	m.nextToken++
	request := HunkPageRequest{ProposalID: m.projection.ProposalID, Version: m.projection.Version, PatchSHA256: m.projection.PatchSHA256, IndexHash: m.projection.IndexHash, EntryID: entry.ID, Cursor: cursor, Limit: defaultHunkLimit, Token: m.nextToken}
	m.hunkPending = &request
	return []Intent{{HunkPage: &request}}
}

func (m *Model) requestRange(offset, maxBytes uint64) []Intent {
	if m == nil || m.projection.Validate() != nil || m.projection.NoChanges || m.rangePending != nil || offset >= m.projection.PatchBytes {
		return nil
	}
	if maxBytes == 0 || maxBytes > maxRangeBytes {
		maxBytes = maxRangeBytes
	}
	if remaining := m.projection.PatchBytes - offset; maxBytes > remaining {
		maxBytes = remaining
	}
	m.nextToken++
	request := PatchRangeRequest{ProposalID: m.projection.ProposalID, Version: m.projection.Version, PatchSHA256: m.projection.PatchSHA256, IndexHash: m.projection.IndexHash, PatchBytes: m.projection.PatchBytes, Offset: offset, MaxBytes: maxBytes, Token: m.nextToken}
	m.rangePending = &request
	return []Intent{{Range: &request}}
}

// InitialEntryRequest starts the first bounded entry page.
func (m *Model) InitialEntryRequest() []Intent { return m.initialEntryRequest() }

// InitialHunkRequest starts the selected entry's first hunk page.
func (m *Model) InitialHunkRequest() []Intent {
	if m == nil || m.selectedEntry == "" || len(m.hunks) != 0 {
		return nil
	}
	return m.requestHunkPage("")
}

// InitialRangeRequest requests the selected entry's visible patch window.
func (m *Model) InitialRangeRequest() []Intent {
	entry, ok := m.selectedEntryValue()
	if !ok {
		return nil
	}
	return m.requestRange(uint64(entry.Offset), uint64(entry.Length))
}

// NextRangeRequest requests the first uncovered patch interval.
func (m *Model) NextRangeRequest() []Intent {
	if m == nil || m.projection.Validate() != nil || m.projection.NoChanges {
		return nil
	}
	offset := uint64(0)
	for _, span := range m.evidence.coverage {
		if span.start > offset {
			break
		}
		if span.end > offset {
			offset = span.end
		}
	}
	return m.requestRange(offset, maxRangeBytes)
}

func (m *Model) selectedEntryValue() (Entry, bool) {
	if m == nil || m.selectedEntry == "" {
		return Entry{}, false
	}
	for _, page := range m.entries {
		for _, entry := range page.page.Entries {
			if entry.ID == m.selectedEntry {
				return entry, true
			}
		}
	}
	return Entry{}, false
}

func (m *Model) residentEntries() []Entry {
	if m == nil {
		return nil
	}
	result := make([]Entry, 0)
	seen := make(map[string]struct{})
	for _, page := range m.entries {
		for _, entry := range page.page.Entries {
			if _, exists := seen[entry.ID]; exists {
				continue
			}
			seen[entry.ID] = struct{}{}
			result = append(result, entry)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Ordinal < result[j].Ordinal })
	return result
}

func (m *Model) reposition() {
	entries := m.residentEntries()
	index := 0
	for candidate, entry := range entries {
		if entry.ID == m.selectedEntry {
			index = candidate
			break
		}
	}
	m.top = viewport.Window(len(entries), index, m.top, m.renderHeight(), m.overscan).Top
}

func (m *Model) renderHeight() int {
	if m.height <= 0 {
		return defaultHeight
	}
	return m.height
}

func (m *Model) evidenceComplete() bool {
	if m == nil || m.projection.Validate() != nil || m.projection.NoChanges || !m.entryPagesComplete() || uint64(len(m.evidence.entries)) < m.projection.FileCount {
		return false
	}
	for _, entry := range m.residentEntries() {
		if entry.HunkCount > 0 && (!m.evidence.hunksComplete[entry.ID] || uint64(m.hunksSeen(entry.ID)) < entry.HunkCount) {
			return false
		}
	}
	return m.coverageComplete()
}

func (m *Model) entryPagesComplete() bool {
	return m.entryPending == nil && m.entryTotal >= m.projection.FileCount && m.entryNext == ""
}

func (m *Model) hunksSeen(entryID string) int {
	count := 0
	prefix := entryID + "\x00"
	for key := range m.evidence.hunks {
		if strings.HasPrefix(key, prefix) {
			count++
		}
	}
	return count
}

func (m *Model) coverageComplete() bool {
	return len(m.evidence.coverage) == 1 && m.evidence.coverage[0].start == 0 && m.evidence.coverage[0].end == m.projection.PatchBytes
}

func (m *Model) mergeCoverage(start, end uint64) {
	if start >= end {
		return
	}
	spans := append(append([]coverageSpan(nil), m.evidence.coverage...), coverageSpan{start: start, end: end})
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	merged := make([]coverageSpan, 0, len(spans))
	for _, span := range spans {
		if len(merged) == 0 || span.start > merged[len(merged)-1].end {
			merged = append(merged, span)
			continue
		}
		if span.end > merged[len(merged)-1].end {
			merged[len(merged)-1].end = span.end
		}
	}
	if len(merged) <= maxCoverageSpans {
		m.evidence.coverage = merged
	}
}

func (m *Model) clearHunks() {
	m.hunks = make(map[string]hunkPageState)
	m.hunkOrder = nil
	m.hunkNext = ""
	m.hunkPending = nil
	m.selectedHunk = ""
	m.currentRange = nil
	m.rangePending = nil
}

func cloneProjection(value Projection) Projection {
	value.Warnings = append([]string(nil), value.Warnings...)
	return value
}

func clonePath(value *repository.RepoPath) *repository.RepoPath {
	if value == nil {
		return nil
	}
	copyValue := repository.RepoPath(value.Bytes())
	return &copyValue
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || !utf8.ValidString(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validCursor(value string) bool {
	if len(value) > 1024 || !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

func validateProjectionIdentity(requestProposalID domain.ProposalID, requestVersion review.ProposalVersionNumber, requestPatchHash, requestIndexHash string, projection Projection) bool {
	return projection.Validate() == nil && !projection.NoChanges && requestProposalID == projection.ProposalID && requestVersion == projection.Version && requestPatchHash == projection.PatchSHA256 && requestIndexHash == projection.IndexHash
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func clampInt(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
