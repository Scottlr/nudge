package discussion

import (
	"fmt"
	"strings"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain/review"
	"github.com/Scottlr/nudge/internal/presentation"
	"github.com/Scottlr/nudge/internal/theme"
	"github.com/Scottlr/nudge/internal/tui/viewport"
	"github.com/charmbracelet/x/ansi"
)

// View renders only the visible bounded message window and retained draft.
func (m *Model) View() string {
	if m == nil || m.thread == nil {
		return "No review thread selected"
	}
	work, err := m.budget.Begin()
	if err != nil {
		return ""
	}
	lines := []string{m.threadHeader()}
	if len(m.messages) == 0 {
		if m.pendingPage != nil {
			lines = append(lines, "loading discussion messages")
		} else {
			lines = append(lines, "no discussion messages")
		}
	} else {
		window := viewport.Window(len(m.messages), m.selectedIndex(), m.top, maxInt(m.renderHeight()-1, 1), m.overscan)
		for index := window.Start; index < window.End; index++ {
			line := m.renderMessage(m.messages[index])
			if !work.Admit(ansi.StringWidth(line)) {
				break
			}
			lines = append(lines, line)
		}
	}
	if m.replyFocused && m.draft != nil {
		lines = append(lines, m.draft.View())
	} else {
		lines = append(lines, "ctrl+enter reply | R resolve/reopen | mark read after presentation")
	}
	return strings.Join(lines, "\n")
}

func (m *Model) threadHeader() string {
	path := presentation.ProjectTerminalText(string(m.thread.AnchorPath.Bytes()), presentation.TerminalTextScalar)
	title := presentation.ProjectTerminalText(m.thread.Title, presentation.TerminalTextScalar)
	if title == "" {
		title = "untitled concern"
	}
	location := path
	if m.thread.AnchorStartLine > 0 {
		location = fmt.Sprintf("%s:%d", path, m.thread.AnchorStartLine)
	}
	ellipsis := m.theme.Glyph(theme.GlyphEllipsis)
	return fmt.Sprintf("%s - %s", ansi.Truncate(title, 40, ellipsis), ansi.Truncate(location, 40, ellipsis))
}

func (m *Model) renderMessage(message app.MessageSummary) string {
	selection := " "
	if message.ID == m.selected {
		selection = ">"
	}
	role := string(message.Role)
	state, ok := m.bodies[message.ID]
	body := ""
	if message.ByteLength == 0 {
		body = "[empty message]"
	} else if !ok {
		body = "[message body loading]"
	} else if state.err != "" {
		body = "[message body unavailable]"
	} else if !state.ready {
		body = "[message body loading; range incomplete]"
	} else {
		body = strings.ReplaceAll(string(state.chunk.Bytes), "\n", " "+m.theme.Glyph(theme.GlyphLineBreak)+" ")
		body = presentation.ProjectTerminalText(body, presentation.TerminalTextScalar)
	}
	line := fmt.Sprintf("%s %s: %s", selection, presentation.ProjectTerminalText(role, presentation.TerminalTextScalar), body)
	styleRole := theme.RoleForeground
	if message.Role == review.RoleAssistant {
		styleRole = theme.RoleMuted
	}
	if message.ID == m.selected && m.focused {
		styleRole = theme.RoleFocus
	}
	style, exists := m.theme.StyleFor(styleRole)
	if !exists {
		style, _ = m.theme.StyleFor(theme.RoleForeground)
	}
	return style.Lipgloss().Render(ansi.Truncate(line, maxInt(m.width, 1), ""))
}
