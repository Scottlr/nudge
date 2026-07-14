package tui

import "github.com/Scottlr/nudge/internal/tui/viewport"

// RenderBudget is kept as a root alias for existing TUI consumers. The
// implementation lives in the dependency-free viewport package so panes can
// be composed by the root without an import cycle.
type RenderBudget = viewport.RenderBudget

// FrameWork is the per-frame admission tracker used by bounded projections.
type FrameWork = viewport.FrameWork

// DefaultRenderBudget returns the v1 shared render budget.
func DefaultRenderBudget() RenderBudget {
	return viewport.DefaultRenderBudget()
}
