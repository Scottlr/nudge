package reattach

import (
	"fmt"
	"strings"

	"github.com/Scottlr/nudge/internal/domain/review"
	"github.com/Scottlr/nudge/internal/presentation"
	"github.com/Scottlr/nudge/internal/theme"
	"github.com/charmbracelet/x/ansi"
)

// View renders the bounded original evidence and candidate window. It never
// reads repository content; all displayed text comes from the projection.
func (m *Model) View() string {
	if m == nil {
		return ""
	}
	if m.projection.Validate() != nil {
		return "manual anchor reattachment unavailable"
	}
	lines := []string{
		m.style(theme.RoleFocus, "Manual anchor reattachment"),
		m.style(theme.RoleMuted, fmt.Sprintf("thread %s  generation %d  outcome %s", safe(string(m.projection.ThreadID)), m.projection.CurrentGeneration, safe(string(m.projection.State)))),
		m.style(theme.RoleMuted, "original: "+anchorLocation(m.projection.Original)),
	}
	if m.projection.Original.SelectedText != "" {
		lines = append(lines, m.style(theme.RoleForeground, "  "+safeOneLine(m.projection.Original.SelectedText)))
	}
	lines = append(lines, m.style(theme.RoleWarning, "reason: "+safe(m.projection.Reason)))
	if len(m.projection.Candidates) == 0 {
		lines = append(lines, m.style(theme.RoleError, "No exact match is available in the scoped current file."))
	} else {
		lines = append(lines, m.style(theme.RoleFocus, "Candidates"))
		for index, candidate := range m.projection.Candidates {
			prefix := "  "
			role := theme.RoleForeground
			if index == m.selected {
				prefix = "> "
				role = theme.RoleFocus
			}
			lines = append(lines, m.style(role, fmt.Sprintf("%s%d %s %s", prefix, index+1, safe(string(candidate.Tier)), candidateLocation(candidate))))
			if candidate.SelectedText != "" {
				lines = append(lines, m.style(role, "    "+safeOneLine(candidate.SelectedText)))
			}
			for _, context := range candidate.BeforeContext {
				lines = append(lines, m.style(theme.RoleMuted, "    before: "+safeOneLine(context)))
			}
			for _, context := range candidate.AfterContext {
				lines = append(lines, m.style(theme.RoleMuted, "    after: "+safeOneLine(context)))
			}
		}
		if m.projection.CandidateOverflow {
			lines = append(lines, m.style(theme.RoleWarning, "more than 20 candidates matched; refine the evidence and regenerate"))
		}
	}
	if m.confirming {
		lines = append(lines, m.style(theme.RoleWarning, "CONFIRM: attach this candidate? Enter confirms; Esc cancels"))
	} else {
		lines = append(lines, m.style(theme.RoleHelp, "j/k or arrows select  |  Enter review/confirm  |  Esc cancel"))
	}
	if m.lastError != "" {
		lines = append(lines, m.style(theme.RoleError, "error: "+safe(m.lastError)))
	}
	return m.fit(lines)
}

func (m *Model) style(role theme.Role, value string) string {
	style, ok := m.theme.StyleFor(role)
	if !ok {
		return value
	}
	return style.Lipgloss().Render(value)
}

func (m *Model) fit(lines []string) string {
	if m.width <= 0 {
		return strings.Join(lines, "\n")
	}
	limit := m.height
	if limit <= 0 || limit > len(lines) {
		limit = len(lines)
	}
	for index := range lines[:limit] {
		lines[index] = ansi.Truncate(lines[index], m.width, "")
	}
	return strings.Join(lines[:limit], "\n")
}

func anchorLocation(anchor review.CodeAnchor) string {
	return fmt.Sprintf("%s:%d-%d (%s)", safe(string(anchor.Path)), anchor.StartLine, anchor.EndLine, safe(string(anchor.Side)))
}

func candidateLocation(candidate review.AnchorCandidate) string {
	return fmt.Sprintf("%s:%d-%d (%s)", safe(string(candidate.Path)), candidate.StartLine, candidate.EndLine, shortFingerprint(review.AnchorCandidateFingerprint(candidate)))
}

func safe(value string) string {
	return presentation.ProjectTerminalText(value, presentation.TerminalTextScalar)
}

func safeOneLine(value string) string {
	return presentation.ProjectTerminalText(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " "), presentation.TerminalTextScalar)
}

func shortFingerprint(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}
