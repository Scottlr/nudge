package proposal

import (
	"fmt"
	"strings"

	"github.com/Scottlr/nudge/internal/domain/review"
	"github.com/Scottlr/nudge/internal/presentation"
	"github.com/Scottlr/nudge/internal/theme"
	"github.com/Scottlr/nudge/internal/tui/viewport"
	"github.com/charmbracelet/x/ansi"
)

// View renders only the bounded proposal-review window and never rereads a
// live result or destination workspace.
func (m *Model) View() string {
	if m == nil {
		return ""
	}
	work, err := m.budget.Begin()
	if err != nil {
		return ""
	}
	if m.projection.Validate() != nil {
		return m.renderLines(work, []string{"no proposal selected"})
	}
	if m.projection.NoChanges {
		lines := []string{"No proposed changes", "return to discussion or request a new change"}
		if m.projection.FailedAttemptID != "" {
			lines = []string{
				m.styled(theme.RoleError, "proposal result is not ready"),
				m.styled(theme.RoleMuted, "attempt: "+safeText(string(m.projection.FailedAttemptID))),
				m.styled(theme.RoleWarning, "reason: "+safeText(m.projection.FailedAttemptReason)),
				"the isolated result can be discarded after exact confirmation; source, destination, history, and conversation remain",
			}
			if m.confirmation == confirmationDiscard {
				lines = append(lines, m.styled(theme.RoleWarning, "CONFIRM DISCARD FAILED RESULT"), m.styled(theme.RoleHelp, "confirm to reset the isolated result to the trusted baseline; cancel to return"))
			} else if m.CanDiscardResult() {
				lines = append(lines, m.styled(theme.RoleProposalFailed, "Discard failed result is available after exact confirmation"))
			}
		}
		return m.renderLines(work, lines)
	}

	lines := []string{
		m.styled(theme.RoleProposal, fmt.Sprintf("Proposed change v%d  %s", m.projection.Version, shortHash(m.projection.PatchSHA256))),
		m.styled(m.statusRole(), fmt.Sprintf("status: %s  applicability: %s", statusLabel(m.projection.Status), m.projection.Applicability)),
		m.styled(theme.RoleMuted, fmt.Sprintf("files: %d  hunks: %d  rows: %d  patch: %d bytes  destination: %s", m.projection.FileCount, m.projection.HunkCount, m.projection.RowCount, m.projection.PatchBytes, safeText(m.projection.Destination))),
	}
	if m.projection.Scope == "broader" {
		lines = append(lines, m.styled(theme.RoleWarning, "scope: broader than the confirmed request — "+safeText(m.projection.ScopeReason)))
	}
	for _, warning := range m.projection.Warnings {
		lines = append(lines, m.styled(theme.RoleWarning, "warning: "+safeText(warning)))
	}
	if m.projection.Applicability != ApplicabilityReady {
		lines = append(lines, m.styled(theme.RoleError, "not approvable: "+safeText(m.projection.ApplicabilityReason)))
	}
	if m.projection.StatusReason != "" {
		lines = append(lines, m.styled(theme.RoleMuted, "reason: "+safeText(m.projection.StatusReason)))
	}

	entries := m.residentEntries()
	if len(entries) == 0 {
		if m.entryPending != nil {
			lines = append(lines, m.styled(theme.RoleMuted, "loading proposal entries"))
		} else {
			lines = append(lines, m.styled(theme.RoleError, "no verifiable proposal entries"))
		}
	} else {
		lines = append(lines, m.styled(theme.RoleFocus, "Files"))
		window := viewport.Window(len(entries), m.selectedEntryIndex(entries), m.top, maxInt(m.renderHeight()-len(lines)-7, 1), m.overscan)
		for index := window.Start; index < window.End; index++ {
			line := m.renderEntry(entries[index])
			if !work.Admit(ansi.StringWidth(line)) {
				break
			}
			lines = append(lines, line)
		}
	}

	if entry, ok := m.selectedEntryValue(); ok {
		lines = append(lines, m.styled(theme.RoleFocus, "Selected: "+m.entryTitle(entry)))
		if entry.HunkCount > 0 {
			hunks := m.residentHunks()
			if len(hunks) == 0 {
				lines = append(lines, m.styled(theme.RoleMuted, "hunks: load to review all hunk evidence"))
			} else {
				parts := make([]string, 0, len(hunks))
				for _, hunk := range hunks {
					marker := " "
					if hunk.ID == m.selectedHunk {
						marker = ">"
					}
					parts = append(parts, fmt.Sprintf("%s hunk %d (%d rows)", marker, hunk.Ordinal+1, hunk.Rows))
				}
				lines = append(lines, m.styled(theme.RoleDiffHunk, strings.Join(parts, "  ")))
			}
		}
		lines = append(lines, m.renderRange(entry))
	}

	entriesSeen, totalEntries, bytesSeen, totalBytes := m.DisclosureSummary()
	lines = append(lines, m.styled(theme.RoleMuted, fmt.Sprintf("disclosure: files %d/%d  patch bytes %d/%d", entriesSeen, totalEntries, bytesSeen, totalBytes)))
	if m.confirmation != confirmationNone {
		lines = append(lines, m.confirmationLines()...)
	} else if m.CanApprove() {
		lines = append(lines, m.styled(theme.RoleProposalReady, "Approve proposal is available after exact confirmation; Reject proposal remains separate"))
	} else {
		lines = append(lines, m.styled(theme.RoleDisabled, "Approve proposal disabled: "+m.approvalReason()))
	}
	if m.lastError != "" {
		lines = append(lines, m.styled(theme.RoleError, "error: "+safeText(m.lastError)))
	}
	return m.renderLines(work, lines)
}

func (m *Model) renderEntry(entry Entry) string {
	marker := " "
	role := theme.RoleMuted
	if entry.ID == m.selectedEntry {
		marker = ">"
		if m.focused {
			role = theme.RoleFocus
		} else {
			role = theme.RoleSelection
		}
	}
	if entry.Unsupported {
		role = theme.RoleWarning
	}
	label := m.entryTitle(entry)
	if entry.Binary {
		label += " [binary]"
	}
	if entry.Unsupported {
		label += " [unsupported]"
	}
	return m.styled(role, fmt.Sprintf("%s %3d  %-10s %s", marker, entry.Ordinal+1, entryKind(entry), ansi.Truncate(label, maxInt(m.width-24, 16), m.theme.Glyph(theme.GlyphEllipsis))))
}

func (m *Model) renderRange(entry Entry) string {
	if entry.Binary {
		return m.styled(theme.RoleMuted, fmt.Sprintf("binary patch: %d bytes, sha256 %s", entry.Length, shortHash(entry.SHA256)))
	}
	if m.currentRange == nil {
		return m.styled(theme.RoleMuted, "patch window: request the selected immutable range")
	}
	text := presentation.ProjectTerminalText(string(m.currentRange.Bytes), presentation.TerminalTextMultiline)
	parts := strings.Split(text, "\n")
	if len(parts) > 8 {
		parts = parts[:8]
		parts = append(parts, m.theme.Glyph(theme.GlyphEllipsis)+" patch window continues")
	}
	result := make([]string, 0, len(parts)+1)
	result = append(result, m.styled(theme.RoleDiffHunk, fmt.Sprintf("patch window @%d (%d bytes)", m.currentRange.Request.Offset, len(m.currentRange.Bytes))))
	for _, part := range parts {
		result = append(result, m.styled(theme.RoleDiffContext, "│ "+part))
	}
	return strings.Join(result, "\n")
}

func (m *Model) confirmationLines() []string {
	identity := m.actionIdentity()
	if m.confirmation == confirmationApprove {
		return []string{
			m.styled(theme.RoleWarning, "CONFIRM APPROVE PROPOSAL"),
			m.styled(theme.RoleOverlayTitle, fmt.Sprintf("version %d / sha256 %s / %d complete files", identity.Version, identity.PatchSHA256, m.projection.FileCount)),
			m.styled(theme.RoleOverlay, "the complete displayed patch will be applied to "+safeText(m.projection.Destination)),
			m.styled(theme.RoleHelp, "confirm to dispatch Approve proposal; cancel to return"),
		}
	}
	if m.confirmation == confirmationDiscard {
		return []string{
			m.styled(theme.RoleWarning, "CONFIRM DISCARD FAILED RESULT"),
			m.styled(theme.RoleOverlayTitle, "attempt "+safeText(string(m.projection.FailedAttemptID))),
			m.styled(theme.RoleOverlay, "reset only the isolated result to the trusted baseline; source, destination, history, and conversation remain"),
			m.styled(theme.RoleHelp, "confirm to dispatch Discard failed result; cancel to return"),
		}
	}
	return []string{
		m.styled(theme.RoleWarning, "CONFIRM REJECT PROPOSAL"),
		m.styled(theme.RoleOverlayTitle, fmt.Sprintf("version %d / sha256 %s", identity.Version, identity.PatchSHA256)),
		m.styled(theme.RoleOverlay, "the immutable proposal version will be rejected"),
		m.styled(theme.RoleHelp, "confirm to dispatch Reject proposal; cancel to return"),
	}
}

func (m *Model) renderLines(work viewport.FrameWork, lines []string) string {
	result := make([]string, 0, len(lines))
	width := m.width
	if width <= 0 {
		width = 120
	}
	for _, line := range lines {
		line = ansi.Truncate(line, width, m.theme.Glyph(theme.GlyphEllipsis))
		if !work.Admit(ansi.StringWidth(line)) {
			break
		}
		result = append(result, line)
	}
	return strings.Join(result, "\n")
}

func (m *Model) styled(role theme.Role, value string) string {
	style, ok := m.theme.StyleFor(role)
	if !ok {
		style, _ = m.theme.StyleFor(theme.RoleForeground)
	}
	return style.Lipgloss().Render(safeText(value))
}

func (m *Model) statusRole() theme.Role {
	switch m.projection.Status {
	case "ready":
		return theme.RoleProposalReady
	case "stale":
		return theme.RoleProposalStale
	case "applying":
		return theme.RoleProposalApplying
	case "applied":
		return theme.RoleProposalApplied
	case "rejected":
		return theme.RoleProposalRejected
	case "failed":
		return theme.RoleProposalFailed
	default:
		return theme.RoleProposal
	}
}

func statusLabel(status review.ProposalStatus) string {
	return string(status)
}

func (m *Model) selectedEntryIndex(entries []Entry) int {
	for index, entry := range entries {
		if entry.ID == m.selectedEntry {
			return index
		}
	}
	return 0
}

func (m *Model) entryTitle(entry Entry) string {
	path := presentation.ProjectTerminalText(string(entry.Path.Bytes()), presentation.TerminalTextScalar)
	if entry.OldPath != nil && string(entry.OldPath.Bytes()) != string(entry.Path.Bytes()) {
		oldPath := presentation.ProjectTerminalText(string(entry.OldPath.Bytes()), presentation.TerminalTextScalar)
		return oldPath + " → " + path
	}
	return path
}

func entryKind(entry Entry) string {
	if entry.Kind == "" {
		return "unknown"
	}
	return string(entry.Kind)
}

func shortHash(value string) string {
	if len(value) <= 16 {
		return value
	}
	return value[:12] + "…"
}

func safeText(value string) string {
	return presentation.ProjectTerminalText(value, presentation.TerminalTextScalar)
}
