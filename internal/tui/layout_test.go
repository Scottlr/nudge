package tui

import "testing"

func TestCalculateLayoutBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		dim    Dimensions
		mode   LayoutMode
		region Rect
	}{
		{name: "unknown", dim: Dimensions{}, mode: LayoutUnknown},
		{name: "too small", dim: Dimensions{Width: 39, Height: 12}, mode: LayoutTooSmall},
		{name: "narrow", dim: Dimensions{Width: 40, Height: 12}, mode: LayoutNarrow},
		{name: "medium width boundary", dim: Dimensions{Width: 80, Height: 24}, mode: LayoutMedium},
		{name: "wide height boundary", dim: Dimensions{Width: 120, Height: 30}, mode: LayoutWide},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			layout := CalculateLayout(test.dim)
			if layout.Mode != test.mode {
				t.Fatalf("mode = %q, want %q", layout.Mode, test.mode)
			}
			for name, region := range map[string]Rect{
				"repository": layout.Regions.Repository,
				"code":       layout.Regions.Code,
				"threads":    layout.Regions.Threads,
				"discussion": layout.Regions.Discussion,
				"lower":      layout.Regions.Lower,
				"main":       layout.Regions.Main,
				"tabs":       layout.Regions.Tabs,
				"status":     layout.Regions.Status,
			} {
				if region.X < 0 || region.Y < 0 || region.Width < 0 || region.Height < 0 {
					t.Fatalf("%s has negative geometry: %#v", name, region)
				}
			}
		})
	}
}

func TestLayoutPreservesResponsiveGeometry(t *testing.T) {
	t.Parallel()

	wide := CalculateLayout(Dimensions{Width: 120, Height: 30})
	if wide.Regions.Repository.Width+wide.Regions.Code.Width != 120 || wide.Regions.Repository.Height+wide.Regions.Threads.Height != 29 {
		t.Fatalf("wide rows do not cover the body: %#v", wide.Regions)
	}
	medium := CalculateLayout(Dimensions{Width: 80, Height: 24})
	if medium.Regions.Repository.Width+medium.Regions.Code.Width != 80 || medium.Regions.Repository.Height+medium.Regions.Lower.Height != 23 {
		t.Fatalf("medium rows do not cover the body: %#v", medium.Regions)
	}
	narrow := CalculateLayout(Dimensions{Width: 40, Height: 12})
	if narrow.Regions.Tabs.Height+narrow.Regions.Main.Height+narrow.Regions.Status.Height != 12 {
		t.Fatalf("narrow regions do not cover the terminal: %#v", narrow.Regions)
	}
}
