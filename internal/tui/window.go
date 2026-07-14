package tui

import "github.com/Scottlr/nudge/internal/tui/viewport"

// WindowRange is a root alias retained for existing TUI callers.
type WindowRange = viewport.WindowRange

// Window returns a bounded visible logical-row window.
func Window(total, cursor, top, height, overscan int) WindowRange {
	return viewport.Window(total, cursor, top, height, overscan)
}
