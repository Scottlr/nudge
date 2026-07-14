package code

import (
	"strings"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/tui/viewport"
	"github.com/charmbracelet/x/ansi"
)

// View renders only the bounded T073 window and frame budget.
func (m *Model) View() string {
	if m == nil {
		return ""
	}
	work, err := m.budget.Begin()
	if err != nil {
		return ""
	}
	if m.large != nil && m.large.open != nil {
		value := m.large.view(m.renderWidth(), m.renderHeight())
		if value != "" {
			lines := strings.Split(value, "\n")
			admitted := make([]string, 0, len(lines))
			for _, line := range lines {
				if !work.Admit(ansi.StringWidth(line)) {
					break
				}
				admitted = append(admitted, line)
			}
			return strings.Join(admitted, "\n")
		}
	}
	if m.content.Validate() != nil {
		return m.renderPlaceholder(work, "no displayed content")
	}
	if message := statusText(m.content); message != "" {
		return m.renderPlaceholder(work, message)
	}
	rows := m.visibleRows()
	if len(rows) == 0 {
		if m.pending[""] != 0 {
			return m.renderPlaceholder(work, "loading content")
		}
		return m.renderPlaceholder(work, "no rows available")
	}
	window := viewport.Window(len(rows), m.selectedIndex(rows), m.top, m.renderHeight(), m.overscan)
	lineWidth := gutterWidth(rows[window.Start:window.End], m.side)
	width := m.renderWidth()
	lines := make([]string, 0, window.Count())
	for index := window.Start; index < window.End; index++ {
		row := rows[index]
		selected := row.Evidence.ID == m.selected
		_, matched := m.searchMatches[row.Evidence.ID]
		anchored := m.selectionFrom != nil && *m.selectionFrom == row.Evidence.ID
		line := composeRow(row, m.side, m.left, width, lineWidth, m.theme, selected, matched, m.focused, anchored)
		if !work.Admit(ansi.StringWidth(line)) {
			break
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (m *Model) renderPlaceholder(work viewport.FrameWork, text string) string {
	row := placeholderRow(m.content, text)
	result := composeRow(row, app.SideNone, 0, m.renderWidth(), 1, m.theme, false, false, m.focused, false)
	if !work.Admit(ansi.StringWidth(result)) {
		return ""
	}
	return result
}

func (m *Model) renderWidth() int {
	if m.width <= 0 {
		return 120
	}
	return m.width
}

func (m *Model) selectedIndex(rows []codeRow) int {
	for index, row := range rows {
		if row.Evidence.ID == m.selected {
			return index
		}
	}
	return 0
}
