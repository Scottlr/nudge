// Package theme defines semantic terminal styles independent of layout or
// workflow state.
package theme

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
)

const inherit = "inherit"

var (
	// ErrInvalidTheme reports a missing or malformed semantic theme.
	ErrInvalidTheme = errors.New("invalid theme")
)

// RenderPolicy controls presentation capabilities selected by the caller.
// Theme resolution never probes the terminal or changes application state.
type RenderPolicy struct {
	Color    bool
	ASCII    bool
	Explicit bool
}

// DefaultRenderPolicy returns the normal Unicode, color-capable presentation.
func DefaultRenderPolicy() RenderPolicy {
	return RenderPolicy{Color: true, Explicit: true}
}

// MonochromeRenderPolicy returns a policy that disables all SGR styling while
// preserving structural labels and markers emitted by the panes.
func MonochromeRenderPolicy() RenderPolicy {
	return RenderPolicy{Explicit: true}
}

// ASCIIRenderPolicy returns a color-capable policy that replaces Unicode
// glyphs and borders.
func ASCIIRenderPolicy() RenderPolicy {
	return RenderPolicy{Color: true, ASCII: true, Explicit: true}
}

// GlyphPurpose identifies a visual marker whose meaning must survive theme
// and terminal capability changes.
type GlyphPurpose string

const (
	GlyphThreadOpen     GlyphPurpose = "thread_open"
	GlyphThreadBusy     GlyphPurpose = "thread_busy"
	GlyphThreadProposal GlyphPurpose = "thread_proposal"
	GlyphThreadResolved GlyphPurpose = "thread_resolved"
	GlyphThreadError    GlyphPurpose = "thread_error"
	GlyphThreadOrphaned GlyphPurpose = "thread_orphaned"
	GlyphMarkerRail     GlyphPurpose = "marker_rail"
	GlyphEllipsis       GlyphPurpose = "ellipsis"
	GlyphLineBreak      GlyphPurpose = "line_break"
	GlyphContinuation   GlyphPurpose = "continuation"
)

var requiredGlyphs = [...]GlyphPurpose{
	GlyphThreadOpen,
	GlyphThreadBusy,
	GlyphThreadProposal,
	GlyphThreadResolved,
	GlyphThreadError,
	GlyphThreadOrphaned,
	GlyphMarkerRail,
	GlyphEllipsis,
	GlyphLineBreak,
	GlyphContinuation,
}

// Style is the resolved value for one semantic role. Color fields contain a
// built-in palette value, a validated user color, or inherit for terminal
// defaults. StyleFor masks all fields when color is disabled.
type Style struct {
	Foreground string
	Background string
	Border     string
	Bold       bool
	Italic     bool
	Underline  bool
}

// UsesInheritedColors reports whether the style leaves its colors to the
// terminal rather than selecting an application palette value.
func (s Style) UsesInheritedColors() bool {
	return s.Foreground == inherit || s.Background == inherit || s.Border == inherit
}

// Lipgloss converts the semantic style into the renderer style used by TUI
// composition. Empty and inherited colors intentionally remain unset.
func (s Style) Lipgloss() lipgloss.Style {
	result := lipgloss.NewStyle().Bold(s.Bold).Italic(s.Italic).Underline(s.Underline)
	if s.Foreground != "" && s.Foreground != inherit {
		result = result.Foreground(lipgloss.Color(s.Foreground))
	}
	if s.Background != "" && s.Background != inherit {
		result = result.Background(lipgloss.Color(s.Background))
	}
	return result
}

// Theme is a semantic role map plus the syntax style identifier used by the
// whole-file highlighter and safe glyph mappings for the active policy.
type Theme struct {
	Name        string
	Roles       map[Role]Style
	SyntaxStyle string
	Glyphs      map[GlyphPurpose]string
	ASCIIGlyphs map[GlyphPurpose]string
	Policy      RenderPolicy
}

// Validate checks that a theme has a complete semantic contract and safe
// values. Additional roles are allowed so callers can carry forward future
// role values without breaking old consumers.
func (t Theme) Validate() error {
	if !validIdentifier(t.Name) || !validSafeText(t.SyntaxStyle, 128) || t.Roles == nil {
		return fmt.Errorf("%w: missing identity or roles", ErrInvalidTheme)
	}
	for _, role := range requiredRoles {
		style, ok := t.Roles[role]
		if !ok {
			return fmt.Errorf("%w: missing role %q", ErrInvalidTheme, role)
		}
		if !validColor(style.Foreground) || !validColor(style.Background) || !validColor(style.Border) {
			return fmt.Errorf("%w: invalid color in role %q", ErrInvalidTheme, role)
		}
	}
	for _, glyph := range requiredGlyphs {
		value, ok := t.Glyphs[glyph]
		if !ok || !validGlyph(value, false) {
			return fmt.Errorf("%w: missing or unsafe glyph %q", ErrInvalidTheme, glyph)
		}
		ascii, ok := t.ASCIIGlyphs[glyph]
		if !ok || !validGlyph(ascii, true) {
			return fmt.Errorf("%w: missing or unsafe ASCII glyph %q", ErrInvalidTheme, glyph)
		}
	}
	return nil
}

// StyleFor resolves a role under the active render policy. Monochrome output
// intentionally emits no SGR attributes; panes retain meaning through their
// labels, borders, markers, and layout.
func (t Theme) StyleFor(role Role) (Style, bool) {
	style, ok := t.Roles[role]
	if !ok {
		return Style{}, false
	}
	if t.Policy.Explicit && !t.Policy.Color {
		style.Foreground = ""
		style.Background = ""
		style.Border = ""
		style.Bold = false
		style.Italic = false
		style.Underline = false
	}
	return style, true
}

// WithPolicy returns an independent theme carrying the selected presentation
// capabilities. It does not modify the original theme or workflow state.
func (t Theme) WithPolicy(policy RenderPolicy) Theme {
	t.Roles = cloneRoles(t.Roles)
	t.Glyphs = cloneGlyphs(t.Glyphs)
	t.ASCIIGlyphs = cloneGlyphs(t.ASCIIGlyphs)
	t.Policy = policy
	return t
}

// Border returns a stable Unicode or ASCII border for the active policy.
func (t Theme) Border() lipgloss.Border {
	if t.Policy.ASCII {
		return lipgloss.ASCIIBorder()
	}
	return lipgloss.NormalBorder()
}

// Glyph returns a safe semantic marker for the active policy.
func (t Theme) Glyph(purpose GlyphPurpose) string {
	if t.Policy.ASCII {
		return t.ASCIIGlyphs[purpose]
	}
	return t.Glyphs[purpose]
}

func cloneRoles(roles map[Role]Style) map[Role]Style {
	copyRoles := make(map[Role]Style, len(roles))
	for role, style := range roles {
		copyRoles[role] = style
	}
	return copyRoles
}

func cloneGlyphs(glyphs map[GlyphPurpose]string) map[GlyphPurpose]string {
	copyGlyphs := make(map[GlyphPurpose]string, len(glyphs))
	for purpose, value := range glyphs {
		copyGlyphs[purpose] = value
	}
	return copyGlyphs
}

func validColor(value string) bool {
	if value == "" || value == inherit {
		return true
	}
	if len(value) != 4 && len(value) != 7 && len(value) != 9 || value[0] != '#' {
		return false
	}
	for _, char := range value[1:] {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}

func validOverrideColor(value string) bool {
	return value != "" && validColor(value)
}

func validIdentifier(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for index, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || (index > 0 && (char == '-' || char == '_')) {
			continue
		}
		return false
	}
	return true
}

// ValidName reports whether a configured theme identity can safely select a
// built-in or one protected themes/<name>.toml file.
func ValidName(value string) bool {
	return validIdentifier(value)
}

func validSafeText(value string, limit int) bool {
	if value == "" || len(value) > limit || strings.ContainsRune(value, '\uFFFD') {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) || unicode.Is(unicode.Bidi_Control, char) {
			return false
		}
	}
	return true
}

func validGlyph(value string, ascii bool) bool {
	if value == "" || len(value) > 32 || !validSafeText(value, 32) {
		return false
	}
	if ascii {
		for _, char := range value {
			if char > 0x7f {
				return false
			}
		}
	}
	return true
}
