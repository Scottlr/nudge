package reattach

import (
	"github.com/Scottlr/nudge/internal/domain/review"
)

// Update applies one typed UI message and returns inert root intents.
func (m *Model) Update(message any) []Intent {
	if m == nil {
		return nil
	}
	switch value := message.(type) {
	case SetProjectionMsg:
		m.setProjection(value.Projection)
	case MoveSelectionMsg:
		m.move(value.Delta)
	case SelectCandidateMsg:
		m.selectFingerprint(value.Fingerprint)
	case BeginConfirmMsg:
		m.beginConfirm()
	case ConfirmMsg:
		return m.confirm()
	case CancelMsg:
		if m.confirming {
			m.confirming = false
			return nil
		}
		return []Intent{{Cancel: true}}
	case SetFocusMsg:
		m.focused = value.Focused
	case SetSizeMsg:
		m.SetSize(value.Width, value.Height)
	case SetErrorMsg:
		m.confirming = false
		m.lastError = value.Message
	}
	return nil
}

func (m *Model) setProjection(value Projection) {
	if value.Validate() != nil {
		m.lastError = "reattachment evidence unavailable"
		m.confirming = false
		return
	}
	previous := ""
	if selected := m.Selected(); selected != nil {
		previous = review.AnchorCandidateFingerprint(*selected)
	}
	m.projection = value.clone()
	m.confirming = false
	m.lastError = ""
	m.selected = 0
	if previous != "" {
		m.selectFingerprint(previous)
	}
}

func (m *Model) move(delta int) {
	if len(m.projection.Candidates) == 0 || delta == 0 {
		return
	}
	m.confirming = false
	m.lastError = ""
	m.selected += delta
	if m.selected < 0 {
		m.selected = 0
	}
	if m.selected >= len(m.projection.Candidates) {
		m.selected = len(m.projection.Candidates) - 1
	}
}

func (m *Model) selectFingerprint(fingerprint string) {
	if fingerprint == "" {
		return
	}
	for index, candidate := range m.projection.Candidates {
		if review.AnchorCandidateFingerprint(candidate) == fingerprint {
			m.selected = index
			m.confirming = false
			m.lastError = ""
			return
		}
	}
}

func (m *Model) beginConfirm() {
	if m.Selected() == nil {
		m.lastError = "no candidate is available to confirm"
		return
	}
	m.confirming = true
	m.lastError = ""
}

func (m *Model) confirm() []Intent {
	if !m.confirming {
		m.beginConfirm()
		return nil
	}
	candidate := m.Selected()
	if candidate == nil || m.projection.Validate() != nil {
		m.confirming = false
		m.lastError = "candidate evidence is no longer available"
		return nil
	}
	m.confirming = false
	return []Intent{{Reattach: &ReattachIntent{
		ThreadID:             m.projection.ThreadID,
		CurrentGeneration:    m.projection.CurrentGeneration,
		Candidate:            *candidate,
		CandidateFingerprint: review.AnchorCandidateFingerprint(*candidate),
		Actor:                m.actor,
	}}}
}
