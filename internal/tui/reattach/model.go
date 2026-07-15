package reattach

import (
	"fmt"
	"strings"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
	"github.com/Scottlr/nudge/internal/theme"
)

// Projection is the immutable application-owned evidence shown by the
// reattachment modal. The component stores no repository or workflow truth.
type Projection struct {
	ThreadID          domain.ReviewThreadID
	CurrentGeneration repository.TargetGeneration
	Original          review.CodeAnchor
	State             review.AnchorState
	Reason            string
	Candidates        []review.AnchorCandidate
	CandidateOverflow bool
}

func (p Projection) Validate() error {
	if p.ThreadID == "" || p.CurrentGeneration == 0 || p.Original.Validate() != nil || p.Original.TargetGeneration != p.CurrentGeneration || (p.State != review.AnchorAmbiguous && p.State != review.AnchorOrphaned) || p.Reason == "" || len(p.Candidates) > review.MaxAnchorReconciliationCandidates {
		return fmt.Errorf("invalid reattachment projection")
	}
	for _, candidate := range p.Candidates {
		if candidate.Validate() != nil || candidate.Generation != p.CurrentGeneration || candidate.Side != p.Original.Side {
			return fmt.Errorf("invalid reattachment candidate")
		}
	}
	return nil
}

func (p Projection) clone() Projection {
	copyValue := p
	copyValue.Original = cloneAnchor(p.Original)
	copyValue.Candidates = make([]review.AnchorCandidate, len(p.Candidates))
	for index, candidate := range p.Candidates {
		copyValue.Candidates[index] = cloneCandidate(candidate)
	}
	return copyValue
}

// ReattachIntent is emitted after explicit confirmation and is translated by
// the root into app.ReattachAnchor.
type ReattachIntent struct {
	ThreadID             domain.ReviewThreadID
	CurrentGeneration    repository.TargetGeneration
	Candidate            review.AnchorCandidate
	CandidateFingerprint string
	Actor                string
}

// Intent is the inert child-to-root boundary.
type Intent struct {
	Reattach *ReattachIntent
	Cancel   bool
}

// SetProjectionMsg replaces the immutable evidence set after an authoritative
// reconciliation or candidate regeneration.
type SetProjectionMsg struct{ Projection Projection }

// MoveSelectionMsg moves the stable candidate selection by a bounded delta.
type MoveSelectionMsg struct{ Delta int }

// SelectCandidateMsg selects by candidate identity rather than row ordinal.
type SelectCandidateMsg struct{ Fingerprint string }

// BeginConfirmMsg opens the explicit confirmation state.
type BeginConfirmMsg struct{}

// ConfirmMsg confirms the currently selected candidate.
type ConfirmMsg struct{}

// CancelMsg exits confirmation or closes the child modal.
type CancelMsg struct{}

// SetFocusMsg supplies the root-owned focus state.
type SetFocusMsg struct{ Focused bool }

// SetSizeMsg supplies the bounded terminal-cell viewport.
type SetSizeMsg struct{ Width, Height int }

// SetErrorMsg returns stale-confirmation feedback to candidate selection.
type SetErrorMsg struct{ Message string }

// Model is the bounded keyboard-first manual reattachment projection.
type Model struct {
	projection Projection
	selected   int
	confirming bool
	focused    bool
	width      int
	height     int
	actor      string
	lastError  string
	theme      theme.Theme
}

// NewModel creates an empty reattachment projection.
func NewModel(actor string) *Model {
	if strings.TrimSpace(actor) == "" {
		actor = "reviewer"
	}
	return &Model{actor: actor, theme: theme.BuiltinTerminalDefault()}
}

func (m *Model) SetTheme(value theme.Theme) {
	if m != nil && value.Validate() == nil {
		m.theme = value
	}
}

func (m *Model) SetSize(width, height int) {
	if m != nil {
		m.width, m.height = max(width, 0), max(height, 0)
	}
}

func (m *Model) Projection() Projection {
	if m == nil {
		return Projection{}
	}
	return m.projection.clone()
}

func (m *Model) Selected() *review.AnchorCandidate {
	if m == nil || m.selected < 0 || m.selected >= len(m.projection.Candidates) {
		return nil
	}
	candidate := cloneCandidate(m.projection.Candidates[m.selected])
	return &candidate
}

func (m *Model) Confirming() bool { return m != nil && m.confirming }

func (m *Model) LastError() string {
	if m == nil {
		return ""
	}
	return m.lastError
}

func cloneAnchor(value review.CodeAnchor) review.CodeAnchor {
	value.Path = append([]byte(nil), value.Path...)
	value.PreviousPath = append([]byte(nil), value.PreviousPath...)
	if value.Relocation != nil {
		copyValue := *value.Relocation
		copyValue.PreviousPath = append([]byte(nil), value.Relocation.PreviousPath...)
		value.Relocation = &copyValue
	}
	return value
}

func cloneCandidate(value review.AnchorCandidate) review.AnchorCandidate {
	value.SourcePath = append([]byte(nil), value.SourcePath...)
	value.Path = append([]byte(nil), value.Path...)
	value.BeforeContext = append([]string(nil), value.BeforeContext...)
	value.AfterContext = append([]string(nil), value.AfterContext...)
	return value
}

func max(left, right int) int {
	if left > right {
		return left
	}
	return right
}
