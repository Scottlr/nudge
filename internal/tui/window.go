package tui

// WindowRange is a bounded logical-row window. It contains indices only; it
// never retains row content or calls a pane query.
type WindowRange struct {
	Total    int
	Cursor   int
	Top      int
	Start    int
	End      int
	Overscan int
}

// Count returns the number of logical rows in the render window.
func (w WindowRange) Count() int {
	if w.End <= w.Start {
		return 0
	}
	return w.End - w.Start
}

// Window clamps total/cursor/top and expands only the visible viewport by the
// requested overscan. The adjusted Top keeps the cursor visible whenever the
// viewport has height.
func Window(total, cursor, top, height, overscan int) WindowRange {
	total = maxInt(total, 0)
	height = maxInt(height, 0)
	overscan = maxInt(overscan, 0)
	if total == 0 || height == 0 {
		return WindowRange{Total: total, Overscan: overscan}
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= total {
		cursor = total - 1
	}
	visibleHeight := minInt(height, total)
	maxTop := maxInt(total-visibleHeight, 0)
	top = clampInt(top, 0, maxTop)
	if cursor < top {
		top = cursor
	}
	if cursor >= top+visibleHeight {
		top = cursor - visibleHeight + 1
	}
	start := maxInt(top-overscan, 0)
	end := minInt(top+visibleHeight+overscan, total)
	return WindowRange{Total: total, Cursor: cursor, Top: top, Start: start, End: end, Overscan: overscan}
}

func clampInt(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
