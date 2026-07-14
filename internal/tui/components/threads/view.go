package threads

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

// View renders only the visible bounded thread window.
func (m *Model) View() string {
	if m == nil {
		return ""
	}
	work, err := m.budget.Begin()
	if err != nil {
		return ""
	}
	if len(m.items) == 0 {
		if m.pending != nil {
			return "loading review threads"
		}
		return "no review threads"
	}
	window := viewport.Window(len(m.items), m.selectedIndex(), m.top, m.renderHeight(), m.overscan)
	lines := make([]string, 0, window.Count())
	for index := window.Start; index < window.End; index++ {
		line := m.renderItem(m.items[index])
		if !work.Admit(ansi.StringWidth(line)) {
			break
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (m *Model) renderItem(item app.ThreadSummary) string {
	selection := " "
	if item.ID == m.selected {
		selection = ">"
	}
	marker := summaryMarker(item)
	path := presentation.ProjectTerminalText(string(item.AnchorPath.Bytes()), presentation.TerminalTextScalar)
	location := path
	if item.AnchorStartLine > 0 {
		location = fmt.Sprintf("%s:%d", path, item.AnchorStartLine)
		if item.AnchorEndLine > item.AnchorStartLine {
			location = fmt.Sprintf("%s-%d", location, item.AnchorEndLine)
		}
	}
	title := presentation.ProjectTerminalText(item.Title, presentation.TerminalTextScalar)
	if title == "" {
		title = "untitled concern"
	}
	status := summaryStatus(item)
	line := fmt.Sprintf("%s %s %-18s %s", selection, marker, ansi.Truncate(location, 28, "…"), ansi.Truncate(title, 36, "…"))
	if item.Unread {
		line += " *"
	}
	if status != "" {
		line += " [" + status + "]"
	}
	style, ok := m.theme.StyleFor(summaryRole(item))
	if !ok {
		style, _ = m.theme.StyleFor(theme.RoleForeground)
	}
	if item.ID == m.selected && m.focused {
		focus, exists := m.theme.StyleFor(theme.RoleFocus)
		if exists {
			style = focus
		}
	}
	return style.Lipgloss().Render(ansi.Truncate(line, maxInt(m.width, 1), ""))
}

func summaryMarker(item app.ThreadSummary) string {
	switch {
	case item.FailurePhase != "" || item.ErrorCode != "" || item.Conversation == review.ConversationFailed || item.Proposal == review.ProposalFailed:
		return "!"
	case item.Anchor == review.AnchorOrphaned || item.Anchor == review.AnchorAmbiguous:
		return "?"
	case item.Proposal == review.ProposalReady || item.Proposal == review.ProposalStale || item.Proposal == review.ProposalApplying:
		return "p"
	case item.Conversation != review.ConversationIdle:
		return "~"
	case item.Resolution == review.ResolutionResolved:
		return "x"
	default:
		return "o"
	}
}

func summaryStatus(item app.ThreadSummary) string {
	parts := make([]string, 0, 3)
	if item.Resolution == review.ResolutionResolved {
		parts = append(parts, "resolved")
	} else {
		parts = append(parts, "open")
	}
	if item.Conversation != review.ConversationIdle {
		parts = append(parts, string(item.Conversation))
	}
	if item.Proposal != review.ProposalNone {
		parts = append(parts, string(item.Proposal))
	}
	if item.Anchor == review.AnchorOrphaned || item.Anchor == review.AnchorAmbiguous {
		parts = append(parts, string(item.Anchor))
	}
	return strings.Join(parts, ",")
}

func summaryRole(item app.ThreadSummary) theme.Role {
	switch {
	case item.FailurePhase != "" || item.ErrorCode != "" || item.Conversation == review.ConversationFailed || item.Proposal == review.ProposalFailed:
		return theme.RoleThreadError
	case item.Anchor == review.AnchorOrphaned || item.Anchor == review.AnchorAmbiguous:
		return theme.RoleThreadOrphaned
	case item.Proposal != review.ProposalNone:
		return theme.RoleThreadProposal
	case item.Conversation != review.ConversationIdle:
		return theme.RoleThreadBusy
	case item.Resolution == review.ResolutionResolved:
		return theme.RoleThreadResolved
	default:
		return theme.RoleThreadOpen
	}
}
