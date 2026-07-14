package code

import (
	"strings"
	"testing"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/highlight"
	nudgetui "github.com/Scottlr/nudge/internal/tui"
)

func TestContentPageRequestsAreIdentityBoundAndLazy(t *testing.T) {
	m := NewModel()
	content := testContent(t, "patch-1")
	intents := m.Update(SnapshotContentMsg{Content: content})
	if len(intents) != 1 || intents[0].PageRequest == nil {
		t.Fatalf("content intents = %#v, want one page request", intents)
	}
	request := *intents[0].PageRequest
	if request.ContentID != content.ID || request.Cursor != "" || request.Limit != defaultPageLimit {
		t.Fatalf("page request = %#v", request)
	}
	if got := m.InitialPageRequest(); got != nil {
		t.Fatalf("repeated initial request = %#v", got)
	}
	m.Update(PageResultMsg{Result: PageResult{Request: request, Page: page(t, content.ID, sourceRow(content.ID, 0, "hello"))}})
	m.SetSize(60, 4)
	if !strings.Contains(m.View(), "hello") {
		t.Fatalf("view = %q, want loaded row", m.View())
	}

	other := testContent(t, "patch-2")
	m.Update(SnapshotContentMsg{Content: other})
	m.Update(PageResultMsg{Result: PageResult{Request: request, Page: page(t, content.ID, sourceRow(content.ID, 0, "stale"))}})
	if strings.Contains(m.View(), "stale") {
		t.Fatalf("stale page was rendered: %q", m.View())
	}
}

func TestSelectionRejectsCrossSideAndAcceptsSameHunkSide(t *testing.T) {
	m := NewModel()
	content := testContent(t, "patch-1")
	request := setReadyContent(t, m, content)
	m.Update(PageResultMsg{Result: PageResult{Request: request, Page: page(t, content.ID,
		contextRow(content.ID, 0, "hunk-1", "same"),
		addedRow(content.ID, 1, "hunk-1", "added"),
	)}})
	first := app.CodeRowID{Content: content.ID, Ordinal: 0}
	second := app.CodeRowID{Content: content.ID, Ordinal: 1}
	m.Update(SelectRowMsg{RowID: first, Side: app.SideBase})
	m.Update(BeginSelectionMsg{})
	if intents := m.Update(ExtendSelectionMsg{RowID: second}); intents != nil || m.LastSelectionRejection().Reason != SelectionSideMismatch {
		t.Fatalf("cross-side selection intents=%#v rejection=%#v", intents, m.LastSelectionRejection())
	}
	m.Update(SelectRowMsg{RowID: first, Side: app.SideHead})
	m.Update(BeginSelectionMsg{})
	intents := m.Update(ExtendSelectionMsg{RowID: second})
	if len(intents) != 1 || intents[0].Selection == nil {
		t.Fatalf("same-side selection intents = %#v", intents)
	}
	selection := intents[0].Selection.Selection
	if selection.Side != app.SideHead || selection.HunkID != "hunk-1" || selection.Start.Ordinal != 0 || selection.End.Ordinal != 1 {
		t.Fatalf("selection = %#v", selection)
	}
}

func TestCollapsedContextAndSearchRemainBoundedProjections(t *testing.T) {
	m := NewModel()
	content := testContent(t, "patch-1")
	request := setReadyContent(t, m, content)
	m.Update(PageResultMsg{Result: PageResult{Request: request, Page: page(t, content.ID,
		contextRow(content.ID, 0, "hunk-1", "first"),
		contextRow(content.ID, 1, "hunk-1", "second"),
	)}})
	m.SetSize(80, 4)
	m.Update(ToggleContextMsg{GroupID: "context-1"})
	if !strings.Contains(m.View(), "context collapsed; expand to view") || strings.Contains(m.View(), "second") {
		t.Fatalf("collapsed view = %q", m.View())
	}
	m.Update(ToggleContextMsg{GroupID: "context-1"})
	if !strings.Contains(m.View(), "second") {
		t.Fatalf("expanded view = %q", m.View())
	}

	intents := m.Update(SetSearchQueryMsg{Query: "second"})
	if len(intents) != 1 || intents[0].Search == nil || intents[0].Search.ContentID != content.ID {
		t.Fatalf("search intents = %#v", intents)
	}
	search := *intents[0].Search
	m.Update(SearchResultMsg{Result: SearchResult{Request: search, Matches: []app.CodeRowID{{Content: content.ID, Ordinal: 1}}}})
	if selected, _ := m.Selected(); selected.Ordinal != 1 {
		t.Fatalf("selected row after search = %#v", selected)
	}
}

func TestViewUsesCellWindowBudgetAndHighlightSpans(t *testing.T) {
	m := NewModel()
	content := testContent(t, "patch-1")
	request := setReadyContent(t, m, content)
	rows := make([]app.DisplayedRow, 0, 40)
	for index := 0; index < 40; index++ {
		rows = append(rows, sourceRow(content.ID, uint64(index), "line-"+strings.Repeat("x", 20)))
	}
	m.Update(PageResultMsg{Result: PageResult{Request: request, Page: page(t, content.ID, rows...)}})
	m.Update(SetHighlightMsg{RowID: rows[0].ID, Side: app.SideHead, Spans: []highlight.StyledSpan{{Text: "\tΩ", Token: "Comment"}}})
	m.SetSize(24, 3)
	m.SetBudget(nudgetui.RenderBudget{MaxRows: 2, MaxCells: 1000})
	lines := strings.Split(m.View(), "\n")
	if len(lines) > 2 || strings.Contains(m.View(), "\x1b") {
		t.Fatalf("bounded/safe view lines=%d content=%q", len(lines), m.View())
	}
}

func TestExplicitPlaceholderStatesStayVisible(t *testing.T) {
	m := NewModel()
	content := testContent(t, "patch-1")
	content.Status = app.ContentBinary
	content.Reason = "binary"
	if intents := m.Update(SnapshotContentMsg{Content: content}); intents != nil {
		t.Fatalf("binary content intents = %#v, want none", intents)
	}
	m.SetSize(80, 2)
	if !strings.Contains(m.View(), "binary content") {
		t.Fatalf("binary placeholder view = %q", m.View())
	}
}

func setReadyContent(t *testing.T, m *Model, content app.DisplayedContent) PageRequest {
	t.Helper()
	intents := m.Update(SnapshotContentMsg{Content: content})
	if len(intents) != 1 || intents[0].PageRequest == nil {
		t.Fatalf("ready content intents = %#v", intents)
	}
	return *intents[0].PageRequest
}

func testContent(t *testing.T, diff string) app.DisplayedContent {
	t.Helper()
	identity := app.DisplayedContentIdentity{
		TargetIdentity:         "target-1",
		CaptureIdentity:        "capture-1",
		Base:                   repository.SnapshotRef{Kind: repository.SnapshotEmpty},
		Head:                   repository.SnapshotRef{Kind: repository.SnapshotEmpty},
		DiffIdentity:           diff,
		RowConstructionVersion: 1,
	}
	id, err := app.NewDisplayedContentID(identity)
	if err != nil {
		t.Fatal(err)
	}
	content := app.DisplayedContent{ID: id, Mode: app.DisplayUnifiedDiff, Status: app.ContentReady}
	if err := content.Validate(); err != nil {
		t.Fatal(err)
	}
	return content
}

func page(t *testing.T, content app.DisplayedContentID, rows ...app.DisplayedRow) app.DisplayedContentPage {
	t.Helper()
	result := app.DisplayedContentPage{ContentID: content, Rows: rows}
	if err := result.Validate(); err != nil {
		t.Fatalf("invalid test page: %v", err)
	}
	return result
}

func sourceRow(content app.DisplayedContentID, ordinal uint64, text string) app.DisplayedRow {
	line := int(ordinal + 1)
	return app.DisplayedRow{ID: app.CodeRowID{Content: content, Ordinal: ordinal}, Kind: app.DisplayedRowSource, Side: app.SideHead, Selectable: true, HeadLine: &line, Text: text}
}

func contextRow(content app.DisplayedContentID, ordinal uint64, hunk, text string) app.DisplayedRow {
	line := int(ordinal + 1)
	return app.DisplayedRow{ID: app.CodeRowID{Content: content, Ordinal: ordinal}, Kind: app.DisplayedRowContext, HunkID: hunk, Side: app.SideBoth, Selectable: true, BaseLine: &line, HeadLine: &line, BaseText: text, HeadText: text, ContextGroup: "context-1"}
}

func addedRow(content app.DisplayedContentID, ordinal uint64, hunk, text string) app.DisplayedRow {
	line := int(ordinal + 1)
	return app.DisplayedRow{ID: app.CodeRowID{Content: content, Ordinal: ordinal}, Kind: app.DisplayedRowAdded, HunkID: hunk, Side: app.SideHead, Selectable: true, HeadLine: &line, HeadText: text}
}
