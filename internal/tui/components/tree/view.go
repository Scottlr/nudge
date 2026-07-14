package tree

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/presentation"
	"github.com/Scottlr/nudge/internal/theme"
	nudgetui "github.com/Scottlr/nudge/internal/tui"
	"github.com/charmbracelet/x/ansi"
)

// View renders only the T073 window and admitted frame work. It never renders
// rows outside the bounded visible window.
func (m *Model) View() string {
	if m == nil {
		return ""
	}
	rows := m.flattenRows()
	window := nudgetui.Window(len(rows), m.selectedIndex(rows), m.top, m.renderHeight(), m.overscan)
	work, err := m.budget.Begin()
	if err != nil {
		return ""
	}
	lines := make([]string, 0, window.Count())
	for index := window.Start; index < window.End; index++ {
		row := rows[index]
		line := m.renderRow(row)
		if !work.Admit(ansi.StringWidth(line)) {
			break
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (m *Model) renderRow(row TreeRow) string {
	if row.Loading {
		return strings.Repeat("  ", row.Depth) + "[loading]"
	}
	selection := " "
	if row.Path.Key() == m.selected {
		selection = ">"
	}
	marker := " "
	if row.Kind == repository.FileKindDirectory {
		marker = "[+]"
		if m.expanded[row.Path.Key()] {
			marker = "[-]"
		}
	}
	label := presentation.ProjectTerminalText(string(row.Name.Bytes()), presentation.TerminalTextScalar)
	line := fmt.Sprintf("%s %s%s %s", selection, strings.Repeat("  ", row.Depth), marker, label)
	if badge := rowBadge(row); badge != "" {
		line += " " + badge
	}
	style := m.rowStyle(row)
	return style.Render(line)
}

func rowBadge(row TreeRow) string {
	if row.Conflict {
		return "[conflict]"
	}
	if row.Change != "" {
		stage := ""
		switch {
		case row.Staged && row.Unstaged:
			stage = ",staged+unstaged"
		case row.Staged:
			stage = ",staged"
		case row.Unstaged:
			stage = ",unstaged"
		}
		return "[" + string(row.Change) + stage + "]"
	}
	if row.ThreadCount > 0 {
		status := presentation.ProjectTerminalText(row.ThreadStatus, presentation.TerminalTextScalar)
		if status != "" {
			return fmt.Sprintf("[threads:%d,%s]", row.ThreadCount, status)
		}
		return fmt.Sprintf("[threads:%d]", row.ThreadCount)
	}
	return ""
}

func (m *Model) rowStyle(row TreeRow) lipgloss.Style {
	role := theme.RoleForeground
	if row.Path.Key() == m.selected {
		role = theme.RoleFocus
	} else if row.Conflict {
		role = theme.RoleThreadError
	} else {
		switch row.Change {
		case repository.ChangeAdded, repository.ChangeUntracked:
			role = theme.RoleDiffAdded
		case repository.ChangeDeleted:
			role = theme.RoleDiffDeleted
		case repository.ChangeModified, repository.ChangeTypeChanged, repository.ChangeRenamed, repository.ChangeCopied:
			role = theme.RoleDiffModified
		}
	}
	style, ok := m.theme.StyleFor(role)
	if !ok {
		style, _ = m.theme.StyleFor(theme.RoleForeground)
	}
	return style.Lipgloss()
}
