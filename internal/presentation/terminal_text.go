// Package presentation contains frontend-neutral inert terminal projections.
package presentation

import "strings"

// TerminalTextMode controls whether line boundaries are displayed as content
// or preserved as normalized structure.
type TerminalTextMode uint8

const (
	// TerminalTextScalar displays structural newlines as visible text.
	TerminalTextScalar TerminalTextMode = iota
	// TerminalTextMultiline preserves normalized newline boundaries.
	TerminalTextMultiline
)

// ProjectTerminalText converts untrusted text into inert terminal content.
// Invalid UTF-8, C0/C1 controls, escape introducers, bidi controls, and tabs
// cannot remain executable or invisible in the returned projection. The input
// string remains unchanged for callers that retain canonical data separately.
func ProjectTerminalText(input string, mode TerminalTextMode) string {
	valid := strings.ToValidUTF8(input, "\uFFFD")
	if mode != TerminalTextMultiline {
		return sanitizeLine(valid, true)
	}

	valid = strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(valid)
	lines := strings.Split(valid, "\n")
	for i, line := range lines {
		lines[i] = sanitizeLine(line, false)
	}
	return strings.Join(lines, "\n")
}

func sanitizeLine(input string, showNewlines bool) string {
	var output strings.Builder
	output.Grow(len(input))
	for _, r := range input {
		switch {
		case r == '\t':
			output.WriteString(`\t`)
		case showNewlines && r == '\n':
			output.WriteString(`\n`)
		case showNewlines && r == '\r':
			output.WriteString(`\r`)
		case r < 0x20 || r == 0x7f || r >= 0x80 && r <= 0x9f:
			output.WriteRune('\uFFFD')
		case isBidiControl(r) || r == '\u2028' || r == '\u2029':
			output.WriteRune('\uFFFD')
		default:
			output.WriteRune(r)
		}
	}
	return output.String()
}

func isBidiControl(r rune) bool {
	switch r {
	case '\u061C', '\u200E', '\u200F', '\u202A', '\u202B', '\u202C', '\u202D', '\u202E', '\u2066', '\u2067', '\u2068', '\u2069':
		return true
	default:
		return false
	}
}
