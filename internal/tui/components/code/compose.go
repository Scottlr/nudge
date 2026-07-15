package code

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/highlight"
	"github.com/Scottlr/nudge/internal/presentation"
	"github.com/Scottlr/nudge/internal/theme"
	"github.com/charmbracelet/x/ansi"
)

const (
	tabWidth           = 4
	maxRenderedLineLen = 64 * 1024
)

func gutterWidth(rows []codeRow, side app.RowSide) int {
	width := 1
	for _, row := range rows {
		line := rowLine(row, side)
		if line > 0 {
			width = maxInt(width, len(fmt.Sprintf("%d", line)))
		}
	}
	return width
}

func rowLine(row codeRow, side app.RowSide) int {
	if side == app.SideBase && row.Evidence.BaseLine != nil {
		return *row.Evidence.BaseLine
	}
	if side == app.SideHead && row.Evidence.HeadLine != nil {
		return *row.Evidence.HeadLine
	}
	return 0
}

func rowText(row codeRow, side app.RowSide) string {
	switch row.Evidence.Kind {
	case app.DisplayedRowDiffHeader, app.DisplayedRowHunkHeader, app.DisplayedRowNoNewline, app.DisplayedRowPlaceholder:
		return row.Evidence.Text
	case app.DisplayedRowAdded, app.DisplayedRowSource:
		if row.Evidence.Kind == app.DisplayedRowSource && row.Evidence.Text != "" {
			return row.Evidence.Text
		}
		if row.Evidence.Kind == app.DisplayedRowSource && side == app.SideBase {
			return row.Evidence.BaseText
		}
		return row.Evidence.HeadText
	case app.DisplayedRowDeleted:
		return row.Evidence.BaseText
	case app.DisplayedRowContext:
		if row.Evidence.ContextCollapsed && row.Evidence.Text != "" {
			return row.Evidence.Text
		}
		if side == app.SideBase {
			return row.Evidence.BaseText
		}
		return row.Evidence.HeadText
	default:
		return ""
	}
}

func rowSpans(row codeRow, side app.RowSide) []highlight.StyledSpan {
	spans := row.spans(side)
	if len(spans) != 0 {
		return spans
	}
	return nil
}

func boundedDisplayText(value string) string {
	if len(value) <= maxRenderedLineLen {
		return presentation.ProjectTerminalText(value, presentation.TerminalTextScalar)
	}
	value = value[:maxRenderedLineLen]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return presentation.ProjectTerminalText(value, presentation.TerminalTextScalar) + " ... line truncated ..."
}

func expandTabs(value string) string {
	var builder strings.Builder
	column := 0
	for _, char := range value {
		if char == '\t' {
			spaces := tabWidth - column%tabWidth
			builder.WriteString(strings.Repeat(" ", spaces))
			column += spaces
			continue
		}
		text := string(char)
		builder.WriteString(text)
		column += ansi.StringWidth(text)
	}
	return builder.String()
}

func rowMarker(row codeRow) string {
	switch row.Evidence.Kind {
	case app.DisplayedRowAdded:
		return "+"
	case app.DisplayedRowDeleted:
		return "-"
	case app.DisplayedRowHunkHeader:
		return "@"
	case app.DisplayedRowPlaceholder:
		return "!"
	case app.DisplayedRowDiffHeader:
		return "#"
	case app.DisplayedRowNoNewline:
		return "\\"
	default:
		return " "
	}
}

func diffRole(row codeRow) theme.Role {
	switch row.Evidence.Kind {
	case app.DisplayedRowAdded:
		return theme.RoleDiffAdded
	case app.DisplayedRowDeleted:
		return theme.RoleDiffDeleted
	case app.DisplayedRowHunkHeader:
		return theme.RoleDiffHunk
	case app.DisplayedRowContext, app.DisplayedRowSource:
		return theme.RoleDiffContext
	default:
		return theme.RoleForeground
	}
}

func syntaxText(spans []highlight.StyledSpan, fallback string, styles theme.Theme) string {
	if len(spans) == 0 {
		return expandTabs(fallback)
	}
	var builder strings.Builder
	for _, span := range spans {
		value := expandTabs(presentation.ProjectTerminalText(span.Text, presentation.TerminalTextScalar))
		role := syntaxRole(span.Token)
		style, ok := styles.StyleFor(role)
		if ok {
			builder.WriteString(style.Lipgloss().Render(value))
		} else {
			builder.WriteString(value)
		}
	}
	return builder.String()
}

func syntaxRole(token string) theme.Role {
	value := strings.ToLower(token)
	switch {
	case strings.Contains(value, "comment"):
		return theme.RoleSyntaxComment
	case strings.Contains(value, "keyword"):
		return theme.RoleSyntaxKeyword
	case strings.Contains(value, "string"):
		return theme.RoleSyntaxString
	case strings.Contains(value, "number"):
		return theme.RoleSyntaxNumber
	case strings.Contains(value, "class"), strings.Contains(value, "type"):
		return theme.RoleSyntaxType
	case strings.Contains(value, "operator"):
		return theme.RoleSyntaxOperator
	case strings.Contains(value, "punctuation"):
		return theme.RoleSyntaxPunct
	default:
		return theme.RoleSyntax
	}
}

func composeRow(row codeRow, side app.RowSide, left, width, lineWidth int, styles theme.Theme, selected, matched, focused, anchored bool, threadMarker string, threadRole theme.Role) string {
	if width <= 0 {
		return ""
	}
	lineNumber := rowLine(row, side)
	gutter := strings.Repeat(" ", lineWidth)
	if lineNumber > 0 {
		gutter = fmt.Sprintf("%*d", lineWidth, lineNumber)
	}
	contentWidth := maxInt(width-lineWidth-5-ansi.StringWidth(threadMarker), 0)
	content := syntaxText(rowSpans(row, side), boundedDisplayText(rowText(row, side)), styles)
	content = ansi.Cut(content, left, left+contentWidth)
	marker := threadMarker
	if marker != "" {
		if style, ok := styles.StyleFor(threadRole); ok {
			marker = style.Lipgloss().Render(marker)
		}
	}
	line := gutter + " " + marker + " " + rowMarker(row) + " " + content
	base, ok := styles.StyleFor(diffRole(row))
	if !ok {
		base, _ = styles.StyleFor(theme.RoleForeground)
	}
	rendered := base.Lipgloss().Render(line)
	role := theme.RoleForeground
	switch {
	case selected:
		role = theme.RoleSelection
	case anchored:
		role = theme.RoleCursor
	case matched:
		role = theme.RoleSearch
	case focused:
		role = theme.RoleFocus
	}
	overlay, ok := styles.StyleFor(role)
	if ok && role != theme.RoleForeground {
		rendered = overlay.Lipgloss().Render(rendered)
	}
	return ansi.Truncate(rendered, width, "")
}

func placeholderRow(content app.DisplayedContent, text string) codeRow {
	return codeRow{Evidence: app.DisplayedRow{ID: app.CodeRowID{Content: content.ID, Ordinal: ^uint64(0)}, Kind: app.DisplayedRowPlaceholder, Side: app.SideNone, Text: text, Placeholder: app.PlaceholderError}}
}

func statusText(content app.DisplayedContent) string {
	if content.Status == app.ContentReady {
		return ""
	}
	reason := presentation.ProjectTerminalText(content.Reason, presentation.TerminalTextScalar)
	switch content.Status {
	case app.ContentBinary:
		return "binary content; source display unavailable"
	case app.ContentUnmerged:
		return "unmerged content; review-only"
	case app.ContentLoading:
		return "loading content"
	case app.ContentTooLarge:
		return "content exceeds the bounded display limit"
	case app.ContentError:
		if reason != "" {
			return "content unavailable: " + reason
		}
		return "content unavailable"
	default:
		return "content unavailable"
	}
}
