package tui

import "errors"

var errInvalidRenderBudget = errors.New("invalid render budget")

// RenderBudget bounds one root frame by logical rows and terminal cells. The
// defaults align the initial 200-row page contract with a bounded 120-cell
// shell row; callers may provide a smaller run-scoped budget.
type RenderBudget struct {
	MaxRows  int
	MaxCells int
}

// DefaultRenderBudget returns the v1 shared shell budget.
func DefaultRenderBudget() RenderBudget {
	return RenderBudget{MaxRows: 200, MaxCells: 200 * 120}
}

// Validate checks the two independent work ceilings.
func (b RenderBudget) Validate() error {
	if b.MaxRows <= 0 || b.MaxCells <= 0 {
		return errInvalidRenderBudget
	}
	return nil
}

// FrameWork tracks admitted work for one render frame.
type FrameWork struct {
	Budget RenderBudget
	Rows   int
	Cells  int
}

// Begin starts an empty frame allowance.
func (b RenderBudget) Begin() (FrameWork, error) {
	if err := b.Validate(); err != nil {
		return FrameWork{}, err
	}
	return FrameWork{Budget: b}, nil
}

// Admit reserves one logical row and its measured cell work. It rejects the
// whole row when either bound would be exceeded, allowing the caller to return
// a continuation without partially composing a row.
func (w *FrameWork) Admit(cells int) bool {
	if w == nil || cells < 0 || w.Rows >= w.Budget.MaxRows || cells > w.Budget.MaxCells-w.Cells {
		return false
	}
	w.Rows++
	w.Cells += cells
	return true
}

// Exhausted reports whether another row cannot be admitted under either
// bound.
func (w FrameWork) Exhausted() bool {
	return w.Rows >= w.Budget.MaxRows || w.Cells >= w.Budget.MaxCells
}
