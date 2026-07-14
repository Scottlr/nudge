package comment

import (
	"fmt"
	"strings"

	"github.com/Scottlr/nudge/internal/domain/review"
	"github.com/Scottlr/nudge/internal/presentation"
)

// View renders the editor, bounded byte state, and explicit action hints.
func (m *Model) View() string {
	var lines []string
	if summary := anchorSummary(m.anchor); summary != "" {
		lines = append(lines, "Concern on "+summary)
	}
	lines = append(lines, m.textarea.View())
	if m.lastErr != nil {
		lines = append(lines, fmt.Sprintf("%s (%d/%d bytes)", m.lastErr, m.ByteCount(), MaxCommentBytes))
	} else {
		lines = append(lines, fmt.Sprintf("%d/%d bytes | ctrl+enter send | esc cancel", m.ByteCount(), MaxCommentBytes))
	}
	return strings.Join(lines, "\n")
}

func anchorSummary(anchor review.CodeAnchor) string {
	if len(anchor.Path) == 0 || anchor.StartLine <= 0 || anchor.EndLine < anchor.StartLine {
		return ""
	}
	path := presentation.ProjectTerminalText(string(anchor.Path.Bytes()), presentation.TerminalTextScalar)
	side := string(anchor.Side)
	return fmt.Sprintf("%s:%d-%d (%s)", path, anchor.StartLine, anchor.EndLine, side)
}
