package theme

// The palette table is the only source of colors for Nudge-authored built-in
// styles. The names deliberately describe the approved product palette rather
// than terminal capabilities or component-specific usage.
var paletteTokens = map[string]string{
	"graphite": "#27232b",
	"charcoal": "#17151a",
	"ivory":    "#f3eee4",
	"ash":      "#a79aa6",
	"mulberry": "#8e5a78",
	"plum":     "#6b405e",
	"sage":     "#7e9f8b",
	"forest":   "#35604a",
	"rose":     "#b56b75",
	"red":      "#a43e4d",
	"straw":    "#c2aa68",
	"citrine":  "#a89a3a",
}

// PaletteTokens returns a copy of the approved built-in palette token table.
func PaletteTokens() map[string]string {
	copyTokens := make(map[string]string, len(paletteTokens))
	for name, value := range paletteTokens {
		copyTokens[name] = value
	}
	return copyTokens
}

func palette(name string) string {
	return paletteTokens[name]
}

func glyphs() (map[GlyphPurpose]string, map[GlyphPurpose]string) {
	unicode := map[GlyphPurpose]string{
		GlyphThreadOpen:     "o",
		GlyphThreadBusy:     "~",
		GlyphThreadProposal: "p",
		GlyphThreadResolved: "x",
		GlyphThreadError:    "!",
		GlyphThreadOrphaned: "?",
		GlyphMarkerRail:     "│",
		GlyphEllipsis:       "…",
		GlyphLineBreak:      "↵",
		GlyphContinuation:   "›",
	}
	ascii := map[GlyphPurpose]string{
		GlyphThreadOpen:     "o",
		GlyphThreadBusy:     "~",
		GlyphThreadProposal: "p",
		GlyphThreadResolved: "x",
		GlyphThreadError:    "!",
		GlyphThreadOrphaned: "?",
		GlyphMarkerRail:     "|",
		GlyphEllipsis:       "...",
		GlyphLineBreak:      "\\n",
		GlyphContinuation:   ">",
	}
	return unicode, ascii
}

func roleSet(foreground, background, muted, focus, selection, added, deleted, modified, warning, error string) map[Role]Style {
	roles := make(map[Role]Style, len(requiredRoles))
	for _, role := range requiredRoles {
		roles[role] = Style{Foreground: foreground}
	}
	roles[RoleForeground] = Style{Foreground: foreground}
	roles[RoleBackground] = Style{Background: background}
	roles[RoleBorder] = Style{Foreground: muted, Border: muted}
	roles[RoleBorderInactive] = Style{Foreground: muted, Border: muted}
	roles[RoleBorderFocus] = Style{Foreground: focus, Border: focus, Bold: true}
	roles[RoleMuted] = Style{Foreground: muted}
	roles[RoleFocus] = Style{Foreground: focus, Bold: true}
	roles[RoleSelection] = Style{Foreground: foreground, Background: selection, Underline: true}
	roles[RoleDisabled] = Style{Foreground: muted, Italic: true}
	roles[RoleHelp] = Style{Foreground: muted}
	roles[RoleWarning] = Style{Foreground: warning, Bold: true}
	roles[RoleError] = Style{Foreground: error, Bold: true}
	roles[RoleOverlay] = Style{Foreground: foreground, Background: background}
	roles[RoleOverlayTitle] = Style{Foreground: focus, Background: background, Bold: true}
	roles[RoleCursor] = Style{Foreground: background, Background: foreground, Underline: true}
	roles[RoleSearch] = Style{Foreground: background, Background: warning, Underline: true}
	roles[RoleSearchMatch] = Style{Foreground: background, Background: warning}
	roles[RoleBadge] = Style{Foreground: muted}
	roles[RoleBadgeAdded] = Style{Foreground: added, Bold: true}
	roles[RoleBadgeDeleted] = Style{Foreground: deleted, Bold: true}
	roles[RoleBadgeModified] = Style{Foreground: modified, Bold: true}
	roles[RoleBadgeConflict] = Style{Foreground: error, Bold: true}
	roles[RoleMarker] = Style{Foreground: focus, Bold: true}
	roles[RoleDiffAdded] = Style{Foreground: added}
	roles[RoleDiffDeleted] = Style{Foreground: deleted}
	roles[RoleDiffModified] = Style{Foreground: modified}
	roles[RoleDiffHunk] = Style{Foreground: focus, Bold: true}
	roles[RoleDiffContext] = Style{Foreground: muted}
	roles[RoleDiffIndicator] = Style{Foreground: focus, Bold: true}
	roles[RoleThreadOpen] = Style{Foreground: foreground}
	roles[RoleThreadBusy] = Style{Foreground: focus, Bold: true}
	roles[RoleThreadProposal] = Style{Foreground: warning, Bold: true}
	roles[RoleThreadResolved] = Style{Foreground: added}
	roles[RoleThreadError] = Style{Foreground: error, Bold: true}
	roles[RoleThreadOrphaned] = Style{Foreground: deleted, Italic: true}
	roles[RoleStatus] = Style{Foreground: muted}
	roles[RoleStatusConnected] = Style{Foreground: added}
	roles[RoleStatusDisconnected] = Style{Foreground: muted}
	roles[RoleStatusBusy] = Style{Foreground: focus, Bold: true}
	roles[RoleStatusWarning] = Style{Foreground: warning, Bold: true}
	roles[RoleStatusError] = Style{Foreground: error, Bold: true}
	roles[RoleProposal] = Style{Foreground: focus}
	roles[RoleProposalGenerating] = Style{Foreground: focus, Bold: true}
	roles[RoleProposalReady] = Style{Foreground: warning, Bold: true}
	roles[RoleProposalStale] = Style{Foreground: deleted, Bold: true}
	roles[RoleProposalApplying] = Style{Foreground: focus, Bold: true}
	roles[RoleProposalApplied] = Style{Foreground: added}
	roles[RoleProposalRejected] = Style{Foreground: muted}
	roles[RoleProposalFailed] = Style{Foreground: error, Bold: true}
	roles[RoleSyntax] = Style{Foreground: foreground}
	roles[RoleSyntaxComment] = Style{Foreground: muted, Italic: true}
	roles[RoleSyntaxKeyword] = Style{Foreground: focus, Bold: true}
	roles[RoleSyntaxString] = Style{Foreground: added}
	roles[RoleSyntaxNumber] = Style{Foreground: modified}
	roles[RoleSyntaxType] = Style{Foreground: focus}
	roles[RoleSyntaxOperator] = Style{Foreground: foreground}
	roles[RoleSyntaxPunct] = Style{Foreground: muted}
	return roles
}

func finishTheme(name, syntax string, roles map[Role]Style) Theme {
	unicode, ascii := glyphs()
	return Theme{
		Name:        name,
		Roles:       roles,
		SyntaxStyle: syntax,
		Glyphs:      unicode,
		ASCIIGlyphs: ascii,
		Policy:      DefaultRenderPolicy(),
	}
}

func darkRoles() map[Role]Style {
	return roleSet(
		palette("ivory"), palette("charcoal"), palette("ash"), palette("mulberry"),
		palette("plum"), palette("sage"), palette("rose"), palette("straw"),
		palette("straw"), palette("red"),
	)
}

func lightRoles() map[Role]Style {
	return roleSet(
		palette("charcoal"), palette("ivory"), palette("graphite"), palette("plum"),
		palette("plum"), palette("forest"), palette("red"), palette("plum"),
		palette("citrine"), palette("red"),
	)
}

func highContrastRoles() map[Role]Style {
	roles := roleSet(
		palette("ivory"), palette("charcoal"), palette("ivory"), palette("mulberry"),
		palette("ivory"), palette("sage"), palette("rose"), palette("straw"),
		palette("straw"), palette("rose"),
	)
	roles[RoleSelection] = Style{Foreground: palette("charcoal"), Background: palette("ivory"), Bold: true}
	roles[RoleCursor] = Style{Foreground: palette("charcoal"), Background: palette("straw"), Bold: true}
	return roles
}

func terminalRoles() map[Role]Style {
	roles := make(map[Role]Style, len(requiredRoles))
	for _, role := range requiredRoles {
		roles[role] = Style{Foreground: inherit, Background: inherit, Border: inherit}
	}
	roles[RoleFocus] = Style{Foreground: inherit, Background: inherit, Border: inherit, Bold: true}
	roles[RoleBorderFocus] = Style{Foreground: inherit, Background: inherit, Border: inherit, Bold: true}
	roles[RoleSelection] = Style{Foreground: inherit, Background: inherit, Border: inherit, Underline: true}
	roles[RoleThreadBusy] = Style{Foreground: inherit, Background: inherit, Border: inherit, Bold: true}
	roles[RoleThreadProposal] = Style{Foreground: inherit, Background: inherit, Border: inherit, Bold: true}
	roles[RoleThreadError] = Style{Foreground: inherit, Background: inherit, Border: inherit, Bold: true}
	roles[RoleThreadOrphaned] = Style{Foreground: inherit, Background: inherit, Border: inherit, Italic: true}
	roles[RoleCursor] = Style{Foreground: inherit, Background: inherit, Border: inherit, Underline: true}
	roles[RoleSearch] = Style{Foreground: inherit, Background: inherit, Border: inherit, Underline: true}
	roles[RoleWarning] = Style{Foreground: inherit, Background: inherit, Border: inherit, Bold: true}
	roles[RoleError] = Style{Foreground: inherit, Background: inherit, Border: inherit, Bold: true}
	roles[RoleOverlayTitle] = Style{Foreground: inherit, Background: inherit, Border: inherit, Bold: true}
	return roles
}

// BuiltinDark returns the restrained dark built-in theme.
func BuiltinDark() Theme {
	return finishTheme("dark", "nudge-dark", darkRoles())
}

// BuiltinLight returns the restrained light built-in theme.
func BuiltinLight() Theme {
	return finishTheme("light", "nudge-light", lightRoles())
}

// BuiltinHighContrast returns a high-contrast built-in theme for low-vision
// and downsampled terminal environments.
func BuiltinHighContrast() Theme {
	return finishTheme("high-contrast", "nudge-high-contrast", highContrastRoles())
}

// BuiltinTerminalDefault returns a theme that inherits terminal colors while
// retaining semantic emphasis through text treatment.
func BuiltinTerminalDefault() Theme {
	return finishTheme("terminal", "terminal-default", terminalRoles())
}

// Builtin returns a complete built-in theme by stable identity.
func Builtin(name string) (Theme, bool) {
	value, ok := Builtins()[name]
	return value, ok
}

// Builtins returns independent copies of all shipped themes keyed by name.
func Builtins() map[string]Theme {
	dark := BuiltinDark()
	light := BuiltinLight()
	highContrast := BuiltinHighContrast()
	terminal := BuiltinTerminalDefault()
	return map[string]Theme{
		dark.Name:         cloneTheme(dark),
		light.Name:        cloneTheme(light),
		highContrast.Name: cloneTheme(highContrast),
		terminal.Name:     cloneTheme(terminal),
	}
}

func cloneTheme(value Theme) Theme {
	value.Roles = cloneRoles(value.Roles)
	value.Glyphs = cloneGlyphs(value.Glyphs)
	value.ASCIIGlyphs = cloneGlyphs(value.ASCIIGlyphs)
	return value
}
