package tui

import "testing"

func TestWindowClampsCursorTopAndOverscan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		total, cursor int
		top, height   int
		overscan      int
		want          WindowRange
	}{
		{name: "empty", total: 0, cursor: 4, top: 2, height: 5, overscan: 2, want: WindowRange{Overscan: 2}},
		{name: "cursor above top", total: 100, cursor: 2, top: 20, height: 10, overscan: 2, want: WindowRange{Total: 100, Cursor: 2, Top: 2, Start: 0, End: 14, Overscan: 2}},
		{name: "cursor below viewport", total: 100, cursor: 40, top: 2, height: 10, overscan: 2, want: WindowRange{Total: 100, Cursor: 40, Top: 31, Start: 29, End: 43, Overscan: 2}},
		{name: "end clamp", total: 20, cursor: 19, top: 19, height: 5, overscan: 4, want: WindowRange{Total: 20, Cursor: 19, Top: 15, Start: 11, End: 20, Overscan: 4}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			if got := Window(test.total, test.cursor, test.top, test.height, test.overscan); got != test.want {
				t.Fatalf("window = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestFrameBudgetStopsBeforePartialRows(t *testing.T) {
	t.Parallel()

	budget := RenderBudget{MaxRows: 2, MaxCells: 10}
	work, err := budget.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if !work.Admit(6) || work.Admit(5) || !work.Admit(4) || work.Admit(1) {
		t.Fatalf("unexpected frame admissions: %#v", work)
	}
	if work.Rows != 2 || work.Cells != 10 || !work.Exhausted() {
		t.Fatalf("frame budget accounting = %#v", work)
	}
}
