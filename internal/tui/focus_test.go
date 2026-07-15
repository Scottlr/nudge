package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestFocusRingMatchesResponsiveLayoutsAndWraps(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mode LayoutMode
		want int
	}{
		{name: "wide", mode: LayoutWide, want: 4},
		{name: "medium", mode: LayoutMedium, want: 3},
		{name: "narrow", mode: LayoutNarrow, want: 4},
		{name: "too small", mode: LayoutTooSmall, want: 0},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			layout := CalculateLayout(Dimensions{Width: 120, Height: 30})
			switch test.mode {
			case LayoutMedium:
				layout = CalculateLayout(Dimensions{Width: 80, Height: 24})
			case LayoutNarrow:
				layout = CalculateLayout(Dimensions{Width: 40, Height: 12})
			case LayoutTooSmall:
				layout = CalculateLayout(Dimensions{Width: 20, Height: 10})
			}
			ring := BuildFocusRing(layout, PaneThreads, PaneCode)
			if len(ring) != test.want {
				t.Fatalf("ring length = %d, want %d: %#v", len(ring), test.want, ring)
			}
		})
	}

	ring := FocusRing{
		{ID: "disabled", Pane: PaneRepository, Visible: true, Enabled: false},
		{ID: "first", Pane: PaneCode, Visible: true, Enabled: true},
		{ID: "second", Pane: PaneThreads, Visible: true, Enabled: true},
	}
	next, ok := ring.Next("second", 1)
	if !ok || next.ID != "first" {
		t.Fatalf("wraparound target = %#v, ok=%v", next, ok)
	}
}

func TestModalFocusRestoresOrFallsBackAfterResize(t *testing.T) {
	t.Parallel()

	model := NewModel(nil, WithDimensions(120, 30))
	updated, _ := model.Update(SetFocusMsg{Pane: PaneCode})
	model = updated.(*Model)
	if model.FocusTarget() != FocusTargetCode {
		t.Fatalf("focus target = %q, want %q", model.FocusTarget(), FocusTargetCode)
	}
	updated, _ = model.Update(ShowOverlayMsg{Overlay: Overlay{ID: "help", Dismissible: true}})
	model = updated.(*Model)
	if model.FocusTarget() != FocusTargetID("overlay:help") {
		t.Fatalf("overlay focus target = %q", model.FocusTarget())
	}
	updated, _ = model.Update(DismissOverlayMsg{})
	model = updated.(*Model)
	if model.FocusTarget() != FocusTargetCode {
		t.Fatalf("restored focus target = %q, want %q", model.FocusTarget(), FocusTargetCode)
	}
	updated, _ = model.Update(ShowOverlayMsg{Overlay: Overlay{ID: "resize", Dismissible: true}})
	model = updated.(*Model)
	updated, _ = model.Update(tea.WindowSizeMsg{Width: 20, Height: 10})
	model = updated.(*Model)
	updated, _ = model.Update(DismissOverlayMsg{})
	model = updated.(*Model)
	if model.FocusTarget() != "" {
		t.Fatalf("too-small focus target = %q, want no target", model.FocusTarget())
	}
}
