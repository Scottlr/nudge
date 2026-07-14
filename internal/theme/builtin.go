package theme

// The palette table is the only source of colors for Nudge-authored built-in
// styles. The names deliberately describe the approved product palette rather
// than terminal capabilities or component-specific usage.
var paletteTokens = map[string]string{
	"graphite": "#27232b",
	"charcoal": "#17151a",
	"ivory":    "#f3eee4",
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

func darkRoles() map[Role]Style {
	return map[Role]Style{
		RoleForeground:     {Foreground: palette("ivory")},
		RoleBackground:     {Background: palette("charcoal")},
		RoleBorder:         {Foreground: palette("graphite")},
		RoleMuted:          {Foreground: palette("graphite")},
		RoleFocus:          {Foreground: palette("mulberry"), Bold: true},
		RoleSelection:      {Foreground: palette("ivory"), Background: palette("plum")},
		RoleDiffAdded:      {Foreground: palette("sage")},
		RoleDiffDeleted:    {Foreground: palette("rose")},
		RoleDiffModified:   {Foreground: palette("straw")},
		RoleDiffHunk:       {Foreground: palette("mulberry"), Bold: true},
		RoleDiffContext:    {Foreground: palette("graphite")},
		RoleThreadOpen:     {Foreground: palette("ivory")},
		RoleThreadBusy:     {Foreground: palette("mulberry"), Bold: true},
		RoleThreadProposal: {Foreground: palette("straw"), Bold: true},
		RoleThreadResolved: {Foreground: palette("forest")},
		RoleThreadError:    {Foreground: palette("red"), Bold: true},
		RoleThreadOrphaned: {Foreground: palette("rose"), Italic: true},
		RoleCursor:         {Foreground: palette("charcoal"), Background: palette("ivory")},
		RoleSearch:         {Foreground: palette("charcoal"), Background: palette("citrine")},
	}
}

func lightRoles() map[Role]Style {
	return map[Role]Style{
		RoleForeground:     {Foreground: palette("charcoal")},
		RoleBackground:     {Background: palette("ivory")},
		RoleBorder:         {Foreground: palette("graphite")},
		RoleMuted:          {Foreground: palette("graphite")},
		RoleFocus:          {Foreground: palette("plum"), Bold: true},
		RoleSelection:      {Foreground: palette("ivory"), Background: palette("plum")},
		RoleDiffAdded:      {Foreground: palette("forest")},
		RoleDiffDeleted:    {Foreground: palette("red")},
		RoleDiffModified:   {Foreground: palette("citrine")},
		RoleDiffHunk:       {Foreground: palette("plum"), Bold: true},
		RoleDiffContext:    {Foreground: palette("graphite")},
		RoleThreadOpen:     {Foreground: palette("charcoal")},
		RoleThreadBusy:     {Foreground: palette("plum"), Bold: true},
		RoleThreadProposal: {Foreground: palette("citrine"), Bold: true},
		RoleThreadResolved: {Foreground: palette("forest")},
		RoleThreadError:    {Foreground: palette("red"), Bold: true},
		RoleThreadOrphaned: {Foreground: palette("red"), Italic: true},
		RoleCursor:         {Foreground: palette("ivory"), Background: palette("charcoal")},
		RoleSearch:         {Foreground: palette("ivory"), Background: palette("forest")},
	}
}

func terminalRoles() map[Role]Style {
	roles := make(map[Role]Style, len(requiredRoles))
	for _, role := range requiredRoles {
		roles[role] = Style{Foreground: inherit, Background: inherit, Border: inherit}
	}
	roles[RoleFocus] = Style{Foreground: inherit, Background: inherit, Border: inherit, Bold: true}
	roles[RoleSelection] = Style{Foreground: inherit, Background: inherit, Border: inherit, Underline: true}
	roles[RoleThreadBusy] = Style{Foreground: inherit, Background: inherit, Border: inherit, Bold: true}
	roles[RoleThreadProposal] = Style{Foreground: inherit, Background: inherit, Border: inherit, Bold: true}
	roles[RoleThreadError] = Style{Foreground: inherit, Background: inherit, Border: inherit, Bold: true}
	roles[RoleThreadOrphaned] = Style{Foreground: inherit, Background: inherit, Border: inherit, Italic: true}
	roles[RoleCursor] = Style{Foreground: inherit, Background: inherit, Border: inherit, Underline: true}
	roles[RoleSearch] = Style{Foreground: inherit, Background: inherit, Border: inherit, Underline: true}
	return roles
}

// BuiltinDark returns the restrained dark built-in theme.
func BuiltinDark() Theme {
	return Theme{Name: "dark", Roles: darkRoles(), SyntaxStyle: "nudge-dark"}
}

// BuiltinLight returns the restrained light built-in theme.
func BuiltinLight() Theme {
	return Theme{Name: "light", Roles: lightRoles(), SyntaxStyle: "nudge-light"}
}

// BuiltinTerminalDefault returns a theme that inherits terminal colors while
// retaining semantic emphasis through text treatment.
func BuiltinTerminalDefault() Theme {
	return Theme{Name: "terminal", Roles: terminalRoles(), SyntaxStyle: "terminal-default"}
}

// Builtins returns independent copies of all shipped themes keyed by name.
func Builtins() map[string]Theme {
	dark := BuiltinDark()
	light := BuiltinLight()
	terminal := BuiltinTerminalDefault()
	return map[string]Theme{
		dark.Name:     {Name: dark.Name, Roles: cloneRoles(dark.Roles), SyntaxStyle: dark.SyntaxStyle},
		light.Name:    {Name: light.Name, Roles: cloneRoles(light.Roles), SyntaxStyle: light.SyntaxStyle},
		terminal.Name: {Name: terminal.Name, Roles: cloneRoles(terminal.Roles), SyntaxStyle: terminal.SyntaxStyle},
	}
}
