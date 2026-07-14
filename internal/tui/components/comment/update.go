package comment

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Update handles explicit send/cancel keys before delegating all ordinary
// editing, including bare Enter, to the Bubbles textarea.
func (m *Model) Update(msg tea.Msg) (Intent, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		if isCancelKey(key) {
			return Intent{Cancelled: true}, nil
		}
		if isSendKey(key) {
			if err := m.validateDraft(); err != nil {
				return Intent{}, nil
			}
			return Intent{CreateThread: &CreateThreadIntent{Anchor: m.anchor, Comment: trimBlankLines(m.Value())}}, nil
		}
	}
	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	m.validateDraft()
	return Intent{}, cmd
}

func isSendKey(key tea.KeyPressMsg) bool {
	return key.Code == tea.KeyEnter && key.Mod&tea.ModCtrl != 0
}

func isCancelKey(key tea.KeyPressMsg) bool {
	return key.Code == tea.KeyEscape || key.Code == tea.KeyEsc
}

// trimBlankLines removes only blank lines at the outside of a submission.
// Internal indentation, spaces, and newlines are retained byte-for-byte.
func trimBlankLines(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	lines := strings.Split(value, "\n")
	first, last := 0, len(lines)-1
	for first <= last && strings.TrimSpace(lines[first]) == "" {
		first++
	}
	for last >= first && strings.TrimSpace(lines[last]) == "" {
		last--
	}
	if first > last {
		return ""
	}
	return strings.Join(lines[first:last+1], "\n")
}
