// Package theme defines semantic terminal styles independent of layout or
// workflow state.
package theme

import (
	"errors"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

const inherit = "inherit"

var (
	// ErrInvalidTheme reports a missing or malformed semantic theme.
	ErrInvalidTheme = errors.New("invalid theme")
)

// Style is the resolved value for one semantic role. Color fields contain a
// named built-in palette value, a hex color supplied by a later user-theme
// loader, or inherit for terminal-default styles.
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

// Lipgloss converts the semantic style into the renderer style used by later
// TUI composition. Empty and inherited colors intentionally remain unset.
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
// whole-file highlighter.
type Theme struct {
	Name        string
	Roles       map[Role]Style
	SyntaxStyle string
}

// Validate checks that a theme has a complete semantic contract. Additional
// roles are allowed so user themes can evolve without breaking old consumers.
func (t Theme) Validate() error {
	if strings.TrimSpace(t.Name) == "" || strings.TrimSpace(t.SyntaxStyle) == "" || t.Roles == nil {
		return fmt.Errorf("%w: missing identity or roles", ErrInvalidTheme)
	}
	for _, role := range requiredRoles {
		if _, ok := t.Roles[role]; !ok {
			return fmt.Errorf("%w: missing role %q", ErrInvalidTheme, role)
		}
	}
	return nil
}

// StyleFor resolves a role without exposing the mutable map to callers.
func (t Theme) StyleFor(role Role) (Style, bool) {
	style, ok := t.Roles[role]
	return style, ok
}

func cloneRoles(roles map[Role]Style) map[Role]Style {
	copyRoles := make(map[Role]Style, len(roles))
	for role, style := range roles {
		copyRoles[role] = style
	}
	return copyRoles
}
