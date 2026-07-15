package proposal

import (
	"sort"

	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

// SetProjectionMsg replaces the immutable proposal summary projection.
type SetProjectionMsg struct{ Projection Projection }

// EntryPageResultMsg carries one application-owned entry page.
type EntryPageResultMsg struct{ Page EntryPage }

// EntryPageErrorMsg retires one failed entry page request.
type EntryPageErrorMsg struct {
	Request EntryPageRequest
	Err     error
}

// HunkPageResultMsg carries one application-owned hunk page.
type HunkPageResultMsg struct{ Page HunkPage }

// HunkPageErrorMsg retires one failed hunk page request.
type HunkPageErrorMsg struct {
	Request HunkPageRequest
	Err     error
}

// PatchRangeResultMsg carries one verified immutable patch range.
type PatchRangeResultMsg struct{ Range PatchRange }

// PatchRangeErrorMsg retires one failed range request.
type PatchRangeErrorMsg struct {
	Request PatchRangeRequest
	Err     error
}

type MoveFileMsg struct{ Delta int }
type MoveHunkMsg struct{ Delta int }
type SelectEntryMsg struct{ EntryID string }
type SelectHunkMsg struct{ HunkID string }
type LoadNextEntryPageMsg struct{}
type LoadNextHunkPageMsg struct{}
type RequestInitialRangeMsg struct{}
type LoadNextRangeMsg struct{}
type AcknowledgeDisclosureMsg struct{}
type BeginApproveMsg struct{}
type ConfirmApproveMsg struct{}
type BeginRejectMsg struct{}
type ConfirmRejectMsg struct{}
type BeginRefreshMsg struct{}
type ConfirmRefreshMsg struct{}
type BeginDiscardMsg struct{}
type ConfirmDiscardMsg struct{}
type CancelConfirmationMsg struct{}
type SetFocusMsg struct{ Focused bool }
type SetModeMsg struct{ Mode Mode }
type ReturnToDiscussionMsg struct{}

func (m *Model) replaceProjection(next Projection) []Intent {
	if m == nil {
		return nil
	}
	if next.Validate() != nil {
		m.resetReview()
		m.projection = Projection{}
		m.mode = ModeDiscussion
		m.lastError = "invalid proposal review projection"
		return nil
	}
	if m.projection.Revision != 0 && next.Revision < m.projection.Revision {
		return nil
	}
	changed := m.identityChanged(next)
	if changed {
		m.resetReview()
	}
	if !changed && m.projection.Status != next.Status {
		m.confirmation = confirmationNone
	}
	m.projection = cloneProjection(next)
	m.lastError = ""
	if next.NoChanges {
		if next.FailedAttemptID == "" {
			m.mode = ModeDiscussion
		} else {
			m.mode = ModeReview
		}
		return nil
	}
	m.mode = ModeReview
	return m.initialEntryRequest()
}

func (m *Model) acceptEntryPage(page EntryPage) []Intent {
	if m == nil || m.entryPending == nil || !sameEntryRequest(*m.entryPending, page.Request) || !validateProjectionIdentity(page.Request.ProposalID, page.Request.Version, page.Request.PatchSHA256, page.Request.IndexHash, m.projection) {
		return nil
	}
	if page.Validate() != nil {
		m.entryPending = nil
		m.lastError = "proposal entry page could not be verified"
		return nil
	}
	m.entryPending = nil
	m.entries[page.Request.Cursor] = entryPageState{page: cloneEntryPage(page)}
	m.touchEntryPage(page.Request.Cursor)
	m.entryNext = page.NextCursor
	if page.Total > m.entryTotal {
		m.entryTotal = page.Total
	}
	for _, entry := range page.Entries {
		m.evidence.entries[entry.ID] = struct{}{}
	}
	if m.selectedEntry == "" && len(page.Entries) > 0 {
		m.selectedEntry = page.Entries[0].ID
	}
	m.reposition()
	return nil
}

func (m *Model) acceptHunkPage(page HunkPage) []Intent {
	if m == nil || m.hunkPending == nil || !sameHunkRequest(*m.hunkPending, page.Request) || !validateProjectionIdentity(page.Request.ProposalID, page.Request.Version, page.Request.PatchSHA256, page.Request.IndexHash, m.projection) || page.Request.EntryID != m.selectedEntry {
		return nil
	}
	if page.Validate() != nil {
		m.hunkPending = nil
		m.lastError = "proposal hunk page could not be verified"
		return nil
	}
	m.hunkPending = nil
	key := hunkPageKey(page.Request.EntryID, page.Request.Cursor)
	m.hunks[key] = hunkPageState{page: cloneHunkPage(page)}
	m.touchHunkPage(key)
	m.hunkNext = page.NextCursor
	for _, hunk := range page.Hunks {
		m.evidence.hunks[hunkEvidenceKey(page.Request.EntryID, hunk.ID)] = struct{}{}
		if m.selectedHunk == "" {
			m.selectedHunk = hunk.ID
		}
	}
	if page.NextCursor == "" {
		m.evidence.hunksComplete[page.Request.EntryID] = true
	}
	return nil
}

func (m *Model) acceptRange(value PatchRange) []Intent {
	if m == nil || m.rangePending == nil || !sameRangeRequest(*m.rangePending, value.RangeRequest()) || !validateProjectionIdentity(value.Request.ProposalID, value.Request.Version, value.Request.PatchSHA256, value.Request.IndexHash, m.projection) {
		return nil
	}
	if value.Validate() != nil {
		m.rangePending = nil
		m.lastError = "proposal patch range could not be verified"
		return nil
	}
	m.rangePending = nil
	m.currentRange = clonePatchRange(value)
	m.mergeCoverage(value.Request.Offset, value.Request.Offset+uint64(len(value.Bytes)))
	return nil
}

func (r PatchRange) RangeRequest() PatchRangeRequest { return r.Request }

func (m *Model) moveFile(delta int) []Intent {
	entries := m.residentEntries()
	if len(entries) == 0 {
		return m.initialEntryRequest()
	}
	index := 0
	for candidate, entry := range entries {
		if entry.ID == m.selectedEntry {
			index = candidate
			break
		}
	}
	next := index + delta
	if delta > 0 && next >= len(entries) && m.entryNext != "" {
		return m.requestEntryPage(m.entryNext)
	}
	if delta < 0 && next < 0 {
		return nil
	}
	next = clampInt(next, 0, len(entries)-1)
	if entries[next].ID != m.selectedEntry {
		m.selectedEntry = entries[next].ID
		m.clearHunks()
	}
	m.reposition()
	return nil
}

func (m *Model) residentHunks() []Hunk {
	if m == nil || m.selectedEntry == "" {
		return nil
	}
	result := make([]Hunk, 0)
	seen := make(map[string]struct{})
	for _, page := range m.hunks {
		if page.page.Request.EntryID != m.selectedEntry {
			continue
		}
		for _, hunk := range page.page.Hunks {
			if _, exists := seen[hunk.ID]; exists {
				continue
			}
			seen[hunk.ID] = struct{}{}
			result = append(result, hunk)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Ordinal < result[j].Ordinal })
	return result
}

func (m *Model) moveHunk(delta int) []Intent {
	entry, ok := m.selectedEntryValue()
	if !ok || entry.HunkCount == 0 {
		return nil
	}
	hunks := m.residentHunks()
	if len(hunks) == 0 {
		return m.InitialHunkRequest()
	}
	index := 0
	for candidate, hunk := range hunks {
		if hunk.ID == m.selectedHunk {
			index = candidate
			break
		}
	}
	next := index + delta
	if delta > 0 && next >= len(hunks) && m.hunkNext != "" {
		return m.requestHunkPage(m.hunkNext)
	}
	if delta < 0 && next < 0 {
		return nil
	}
	next = clampInt(next, 0, len(hunks)-1)
	m.selectedHunk = hunks[next].ID
	return nil
}

func (m *Model) handleApprove(confirm bool) []Intent {
	if m == nil || m.projection.NoChanges {
		return nil
	}
	if !confirm {
		if !m.CanApprove() {
			m.lastError = m.approvalReason()
			return nil
		}
		m.confirmation = confirmationApprove
		return nil
	}
	if m.confirmation != confirmationApprove || !m.CanApprove() {
		m.confirmation = confirmationNone
		m.lastError = m.approvalReason()
		return nil
	}
	m.confirmation = confirmationNone
	identity := m.actionIdentity()
	return []Intent{{Approve: &ApproveProposalIntent{Identity: identity, Destination: m.projection.Destination}}}
}

func (m *Model) handleReject(confirm bool) []Intent {
	if m == nil || m.projection.Validate() != nil || m.projection.NoChanges || m.projection.Status != review.ProposalVersionReady {
		return nil
	}
	if !confirm {
		m.confirmation = confirmationReject
		return nil
	}
	if m.confirmation != confirmationReject {
		return nil
	}
	m.confirmation = confirmationNone
	return []Intent{{Reject: &RejectProposalIntent{Identity: m.actionIdentity()}}}
}

func (m *Model) handleRefresh(confirm bool) []Intent {
	if m == nil || !m.CanRefresh() {
		return nil
	}
	if !confirm {
		m.confirmation = confirmationRefresh
		return nil
	}
	if m.confirmation != confirmationRefresh || !m.CanRefresh() {
		m.confirmation = confirmationNone
		m.lastError = "proposal refresh is unavailable"
		return nil
	}
	m.confirmation = confirmationNone
	return []Intent{{Refresh: &RefreshProposalIntent{Identity: m.actionIdentity()}}}
}

func (m *Model) handleDiscard(confirm bool) []Intent {
	if m == nil || !m.CanDiscardResult() {
		return nil
	}
	if !confirm {
		m.confirmation = confirmationDiscard
		return nil
	}
	if m.confirmation != confirmationDiscard || !m.CanDiscardResult() {
		m.confirmation = confirmationNone
		return nil
	}
	m.confirmation = confirmationNone
	return []Intent{{Discard: &DiscardProposalResultIntent{Identity: ResultDiscardIdentity{ProposalID: m.projection.ProposalID, AttemptID: m.projection.FailedAttemptID}, Reason: "failed proposal result discarded"}}}
}

func (m *Model) actionIdentity() ActionIdentity {
	return ActionIdentity{ProposalID: m.projection.ProposalID, Version: m.projection.Version, PatchSHA256: m.projection.PatchSHA256, IndexHash: m.projection.IndexHash}
}

func (m *Model) approvalReason() string {
	switch {
	case m == nil || m.projection.Validate() != nil:
		return "proposal review is unavailable"
	case m.projection.NoChanges:
		return "no proposed changes"
	case m.projection.Status != review.ProposalVersionReady:
		return "proposal is not ready"
	case m.projection.Applicability != ApplicabilityReady:
		return m.projection.ApplicabilityReason
	case !m.entryPagesComplete() || uint64(len(m.evidence.entries)) < m.projection.FileCount:
		return "complete file disclosure is required"
	case !m.evidenceComplete():
		return "complete hunk and patch disclosure is required"
	case !m.evidence.disclosureAccepted:
		return "acknowledge the complete proposal disclosure"
	default:
		return "proposal approval is unavailable"
	}
}

func (m *Model) touchEntryPage(cursor string) {
	m.entryOrder = touchKey(m.entryOrder, cursor)
	for len(m.entryOrder) > maxRetainedPages {
		oldest := m.entryOrder[0]
		m.entryOrder = m.entryOrder[1:]
		delete(m.entries, oldest)
	}
}

func (m *Model) touchHunkPage(key string) {
	m.hunkOrder = touchKey(m.hunkOrder, key)
	for len(m.hunkOrder) > maxRetainedHunks {
		oldest := m.hunkOrder[0]
		m.hunkOrder = m.hunkOrder[1:]
		delete(m.hunks, oldest)
	}
}

func touchKey(order []string, key string) []string {
	result := order[:0]
	for _, existing := range order {
		if existing != key {
			result = append(result, existing)
		}
	}
	return append(result, key)
}

func cloneEntryPage(value EntryPage) EntryPage {
	entries := append([]Entry(nil), value.Entries...)
	value.Entries = make([]Entry, len(entries))
	for index, entry := range entries {
		value.Entries[index] = entry
		value.Entries[index].Path = repository.RepoPath(entry.Path.Bytes())
		value.Entries[index].OldPath = clonePath(entry.OldPath)
		if entry.ModeTransition != nil {
			transition := *entry.ModeTransition
			value.Entries[index].ModeTransition = &transition
		}
	}
	return value
}

func cloneHunkPage(value HunkPage) HunkPage {
	value.Hunks = append([]Hunk(nil), value.Hunks...)
	return value
}

func clonePatchRange(value PatchRange) *PatchRange {
	value.Bytes = append([]byte(nil), value.Bytes...)
	return &value
}

func sameEntryRequest(left, right EntryPageRequest) bool {
	return left.ProposalID == right.ProposalID && left.Version == right.Version && left.PatchSHA256 == right.PatchSHA256 && left.IndexHash == right.IndexHash && left.Cursor == right.Cursor && left.Limit == right.Limit && left.Token == right.Token
}

func sameHunkRequest(left, right HunkPageRequest) bool {
	return left.ProposalID == right.ProposalID && left.Version == right.Version && left.PatchSHA256 == right.PatchSHA256 && left.IndexHash == right.IndexHash && left.EntryID == right.EntryID && left.Cursor == right.Cursor && left.Limit == right.Limit && left.Token == right.Token
}

func sameRangeRequest(left, right PatchRangeRequest) bool {
	return left.ProposalID == right.ProposalID && left.Version == right.Version && left.PatchSHA256 == right.PatchSHA256 && left.IndexHash == right.IndexHash && left.PatchBytes == right.PatchBytes && left.Offset == right.Offset && left.MaxBytes == right.MaxBytes && left.Token == right.Token
}

func hunkPageKey(entryID, cursor string) string     { return entryID + "\x00" + cursor }
func hunkEvidenceKey(entryID, hunkID string) string { return entryID + "\x00" + hunkID }

// Update applies one typed UI message and returns inert root intents.
func (m *Model) Update(message any) []Intent {
	if m == nil {
		return nil
	}
	switch value := message.(type) {
	case SetProjectionMsg:
		return m.replaceProjection(value.Projection)
	case EntryPageResultMsg:
		return m.acceptEntryPage(value.Page)
	case EntryPageErrorMsg:
		if m.entryPending != nil && sameEntryRequest(*m.entryPending, value.Request) {
			m.entryPending = nil
			m.lastError = "proposal entry page request failed"
		}
	case HunkPageResultMsg:
		return m.acceptHunkPage(value.Page)
	case HunkPageErrorMsg:
		if m.hunkPending != nil && sameHunkRequest(*m.hunkPending, value.Request) {
			m.hunkPending = nil
			m.lastError = "proposal hunk page request failed"
		}
	case PatchRangeResultMsg:
		return m.acceptRange(value.Range)
	case PatchRangeErrorMsg:
		if m.rangePending != nil && sameRangeRequest(*m.rangePending, value.Request) {
			m.rangePending = nil
			m.lastError = "proposal patch range request failed"
		}
	case MoveFileMsg:
		return m.moveFile(value.Delta)
	case MoveHunkMsg:
		return m.moveHunk(value.Delta)
	case SelectEntryMsg:
		for _, entry := range m.residentEntries() {
			if entry.ID == value.EntryID {
				m.selectedEntry = value.EntryID
				m.clearHunks()
				m.reposition()
				break
			}
		}
	case SelectHunkMsg:
		for _, hunk := range m.residentHunks() {
			if hunk.ID == value.HunkID {
				m.selectedHunk = value.HunkID
				break
			}
		}
	case LoadNextEntryPageMsg:
		if m.entryNext != "" {
			return m.requestEntryPage(m.entryNext)
		}
	case LoadNextHunkPageMsg:
		if m.hunkNext != "" {
			return m.requestHunkPage(m.hunkNext)
		}
	case RequestInitialRangeMsg:
		return m.InitialRangeRequest()
	case LoadNextRangeMsg:
		return m.NextRangeRequest()
	case AcknowledgeDisclosureMsg:
		if m.evidenceComplete() {
			m.evidence.disclosureAccepted = true
		} else {
			m.lastError = m.approvalReason()
		}
	case BeginApproveMsg:
		return m.handleApprove(false)
	case ConfirmApproveMsg:
		return m.handleApprove(true)
	case BeginRejectMsg:
		return m.handleReject(false)
	case ConfirmRejectMsg:
		return m.handleReject(true)
	case BeginRefreshMsg:
		return m.handleRefresh(false)
	case ConfirmRefreshMsg:
		return m.handleRefresh(true)
	case BeginDiscardMsg:
		return m.handleDiscard(false)
	case ConfirmDiscardMsg:
		return m.handleDiscard(true)
	case CancelConfirmationMsg:
		m.confirmation = confirmationNone
	case SetFocusMsg:
		m.focused = value.Focused
	case SetModeMsg:
		if value.Mode.Validate() == nil && (value.Mode == ModeDiscussion || !m.projection.NoChanges || m.projection.FailedAttemptID != "") {
			m.mode = value.Mode
		}
	case ReturnToDiscussionMsg:
		m.mode = ModeDiscussion
		identity := ActionIdentity{}
		if !m.projection.NoChanges && m.projection.Validate() == nil {
			identity = m.actionIdentity()
		}
		return []Intent{{Mode: &ModeIntent{Mode: ModeDiscussion, Identity: identity}}}
	}
	return nil
}
