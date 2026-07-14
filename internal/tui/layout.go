package tui

// LayoutMode is the responsive shell arrangement selected for the current
// terminal dimensions.
type LayoutMode string

const (
	LayoutUnknown  LayoutMode = "unknown"
	LayoutWide     LayoutMode = "wide"
	LayoutMedium   LayoutMode = "medium"
	LayoutNarrow   LayoutMode = "narrow"
	LayoutTooSmall LayoutMode = "too_small"
)

const (
	wideMinWidth    = 120
	mediumMinWidth  = 80
	narrowMinWidth  = 40
	wideMinHeight   = 30
	mediumMinHeight = 24
	narrowMinHeight = 12
)

// Dimensions are terminal cell dimensions supplied by Bubble Tea.
type Dimensions struct {
	Width  int
	Height int
}

// Rect is a bounded terminal-cell rectangle.
type Rect struct {
	X      int
	Y      int
	Width  int
	Height int
}

// Empty reports whether a rectangle has no renderable cells.
func (r Rect) Empty() bool {
	return r.Width <= 0 || r.Height <= 0
}

// LayoutRegions names the shell regions without assigning them pane data.
type LayoutRegions struct {
	Repository Rect
	Code       Rect
	Threads    Rect
	Discussion Rect
	Lower      Rect
	Main       Rect
	Tabs       Rect
	Status     Rect
}

// Layout is the pure responsive geometry result consumed by the root view.
type Layout struct {
	Mode       LayoutMode
	Dimensions Dimensions
	Regions    LayoutRegions
}

// LayoutFor is a convenience wrapper for callers that have separate width and
// height values.
func LayoutFor(width, height int) Layout {
	return CalculateLayout(Dimensions{Width: width, Height: height})
}

// CalculateLayout derives the shell geometry from cell dimensions only.
func CalculateLayout(dimensions Dimensions) Layout {
	dimensions.Width = maxInt(dimensions.Width, 0)
	dimensions.Height = maxInt(dimensions.Height, 0)
	layout := Layout{Mode: layoutMode(dimensions), Dimensions: dimensions}
	if dimensions.Width == 0 || dimensions.Height == 0 {
		return layout
	}

	statusHeight := 1
	status := Rect{X: 0, Y: dimensions.Height - statusHeight, Width: dimensions.Width, Height: statusHeight}
	layout.Regions.Status = status
	bodyHeight := maxInt(dimensions.Height-statusHeight, 0)

	switch layout.Mode {
	case LayoutWide:
		leftWidth := dimensions.Width / 3
		rightWidth := dimensions.Width - leftWidth
		topHeight := maxInt(bodyHeight/2, 1)
		bottomHeight := maxInt(bodyHeight-topHeight, 0)
		leftTop := Rect{X: 0, Y: 0, Width: leftWidth, Height: topHeight}
		rightTop := Rect{X: leftWidth, Y: 0, Width: rightWidth, Height: topHeight}
		leftBottom := Rect{X: 0, Y: topHeight, Width: leftWidth, Height: bottomHeight}
		rightBottom := Rect{X: leftWidth, Y: topHeight, Width: rightWidth, Height: bottomHeight}
		layout.Regions.Repository = leftTop
		layout.Regions.Code = rightTop
		layout.Regions.Threads = leftBottom
		layout.Regions.Discussion = rightBottom
	case LayoutMedium:
		leftWidth := dimensions.Width / 3
		rightWidth := dimensions.Width - leftWidth
		topHeight := maxInt(bodyHeight*2/3, 1)
		lowerHeight := maxInt(bodyHeight-topHeight, 0)
		layout.Regions.Repository = Rect{X: 0, Y: 0, Width: leftWidth, Height: topHeight}
		layout.Regions.Code = Rect{X: leftWidth, Y: 0, Width: rightWidth, Height: topHeight}
		layout.Regions.Lower = Rect{X: 0, Y: topHeight, Width: dimensions.Width, Height: lowerHeight}
	case LayoutNarrow:
		tabsHeight := 1
		layout.Regions.Tabs = Rect{X: 0, Y: 0, Width: dimensions.Width, Height: tabsHeight}
		layout.Regions.Main = Rect{X: 0, Y: tabsHeight, Width: dimensions.Width, Height: maxInt(dimensions.Height-statusHeight-tabsHeight, 0)}
	case LayoutTooSmall:
		layout.Regions.Main = Rect{X: 0, Y: 0, Width: dimensions.Width, Height: bodyHeight}
	}
	return layout
}

func layoutMode(dimensions Dimensions) LayoutMode {
	if dimensions.Width <= 0 || dimensions.Height <= 0 {
		return LayoutUnknown
	}
	if dimensions.Width < narrowMinWidth || dimensions.Height < narrowMinHeight {
		return LayoutTooSmall
	}
	if dimensions.Width < mediumMinWidth || dimensions.Height < mediumMinHeight {
		return LayoutNarrow
	}
	if dimensions.Width < wideMinWidth || dimensions.Height < wideMinHeight {
		return LayoutMedium
	}
	return LayoutWide
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
