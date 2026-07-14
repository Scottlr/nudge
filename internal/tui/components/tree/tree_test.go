package tree

import (
	"strings"
	"testing"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/tui/viewport"
)

func TestInitialPageRequestIsBoundedAndCoalesced(t *testing.T) {
	m := NewModel()
	intents := m.InitialPageRequest()
	if len(intents) != 1 || intents[0].PageRequest == nil {
		t.Fatalf("InitialPageRequest() = %#v, want one page request", intents)
	}
	request := *intents[0].PageRequest
	if request.Identity != (PageIdentity{Filter: FilterChanged, Limit: defaultPageLimit}) {
		t.Fatalf("request identity = %#v", request.Identity)
	}
	if request.Query.ParentPath != nil || request.Query.Filter != app.TreeFilterChanged || request.Query.Limit != defaultPageLimit {
		t.Fatalf("request query = %#v", request.Query)
	}
	if got := m.InitialPageRequest(); got != nil {
		t.Fatalf("repeated InitialPageRequest() = %#v, want nil", got)
	}
}

func TestExpansionRequestsMissingChildrenAndRejectsCollapsedResult(t *testing.T) {
	m := NewModel()
	rootIntent := m.InitialPageRequest()
	rootRequest := *rootIntent[0].PageRequest
	m.Update(PageResultMsg{Result: PageResult{
		Request: rootRequest,
		Page:    page(t, entry(t, "src", true), entry(t, "top.go", false)),
	}})

	intents := m.Update(ToggleExpandedMsg{Path: "src"})
	if len(intents) != 1 || intents[0].PageRequest == nil {
		t.Fatalf("expand intents = %#v, want one child request", intents)
	}
	childRequest := *intents[0].PageRequest
	if childRequest.Identity.Parent != "src" || childRequest.Identity.Cursor != "" {
		t.Fatalf("child request identity = %#v", childRequest.Identity)
	}
	m.SetSize(80, 4)
	if !strings.Contains(m.View(), "[loading]") {
		t.Fatalf("expanded view = %q, want loading row", m.View())
	}

	m.Update(ToggleExpandedMsg{Path: "src"})
	m.Update(PageResultMsg{Result: PageResult{
		Request: childRequest,
		Page:    page(t, entry(t, "src/main.go", false)),
	}})
	if got := m.PageCount(); got != 1 {
		t.Fatalf("PageCount after collapsed result = %d, want root only", got)
	}
}

func TestFilterClearsPagesAndPreservesSelectedRawPathCandidate(t *testing.T) {
	m := NewModel()
	rootRequest := *m.InitialPageRequest()[0].PageRequest
	selected := repository.RepoPath("unsafe\x01.go")
	m.Update(PageResultMsg{Result: PageResult{
		Request: rootRequest,
		Page:    page(t, entry(t, string(selected), false)),
	}})

	intents := m.Update(SelectRowMsg{Path: selected.Key()})
	if len(intents) != 1 || intents[0].SelectPath == nil || string(intents[0].SelectPath.Path) != string(selected) {
		t.Fatalf("selection intents = %#v, want exact raw path", intents)
	}

	intents = m.Update(SetFilterMsg{Filter: FilterAll})
	if len(intents) != 1 || intents[0].PageRequest == nil || intents[0].PageRequest.Identity.Filter != FilterAll {
		t.Fatalf("filter intents = %#v, want all-filter page request", intents)
	}
	if m.PageCount() != 0 || m.selected != selected.Key() {
		t.Fatalf("filter state pages=%d selected=%q, want pages=0 selected=%q", m.PageCount(), m.selected, selected.Key())
	}
}

func TestRevisionRejectsOldPageResult(t *testing.T) {
	m := NewModel()
	oldRequest := *m.InitialPageRequest()[0].PageRequest
	newIntents := m.Update(SnapshotRevisionMsg{Revision: 2})
	if len(newIntents) != 1 || newIntents[0].PageRequest == nil || newIntents[0].PageRequest.Identity.SnapshotRevision != 2 {
		t.Fatalf("revision intents = %#v", newIntents)
	}
	m.Update(PageResultMsg{Result: PageResult{
		Request: oldRequest,
		Page:    page(t, entry(t, "stale.go", false)),
	}})
	if got := m.PageCount(); got != 0 {
		t.Fatalf("PageCount after stale result = %d, want 0", got)
	}
}

func TestViewUsesWindowAndRenderBudget(t *testing.T) {
	m := NewModel()
	request := *m.InitialPageRequest()[0].PageRequest
	entries := make([]repository.TreeEntry, 0, 40)
	for index := 0; index < 40; index++ {
		entries = append(entries, entry(t, "file-"+strings.Repeat("0", 2)+string(rune('a'+index/26))+string(rune('a'+index%26)), false))
	}
	m.Update(PageResultMsg{Result: PageResult{Request: request, Page: page(t, entries...)}})
	m.SetSize(80, 3)
	m.SetBudget(viewport.RenderBudget{MaxRows: 2, MaxCells: 1000})
	lines := strings.Split(m.View(), "\n")
	if len(lines) > 2 {
		t.Fatalf("rendered %d lines, want at most 2", len(lines))
	}
}

func TestThreadBadgesProjectIntoRows(t *testing.T) {
	m := NewModel()
	request := *m.InitialPageRequest()[0].PageRequest
	path := repository.RepoPath("main.go")
	m.Update(PageResultMsg{Result: PageResult{Request: request, Page: page(t, entry(t, string(path), false))}})
	m.Update(SetThreadBadgesMsg{Badges: map[repository.RepoPathKey]ThreadBadge{
		path.Key(): {Count: 3, Status: "open"},
	}})
	if !strings.Contains(m.View(), "[threads:3,open]") {
		t.Fatalf("badged view = %q, want thread count", m.View())
	}
}

func TestRowBadgePreservesChangeAndConflictEvidence(t *testing.T) {
	if got := rowBadge(TreeRow{Change: repository.ChangeModified, Staged: true, Unstaged: true}); got != "[modified,staged+unstaged]" {
		t.Fatalf("change badge = %q", got)
	}
	if got := rowBadge(TreeRow{Conflict: true}); got != "[conflict]" {
		t.Fatalf("conflict badge = %q", got)
	}
}

func TestSearchUsesImmutableSnapshotAndReturnsToHierarchySelection(t *testing.T) {
	m := NewModel()
	rootRequest := *m.InitialPageRequest()[0].PageRequest
	m.Update(PageResultMsg{Result: PageResult{Request: rootRequest, Page: page(t, entry(t, "loaded.go", false))}})
	m.Update(SelectRowMsg{Path: repository.RepoPathKey("loaded.go")})
	m.Update(SetSearchSnapshotMsg{Snapshot: repository.SnapshotRef{Kind: repository.SnapshotEmpty}})
	intents := m.Update(SetSearchQueryMsg{Query: "unloaded"})
	if len(intents) != 1 || intents[0].Search == nil || intents[0].Search.Query.Snapshot.Kind != repository.SnapshotEmpty {
		t.Fatalf("search intent = %#v", intents)
	}
	request := *intents[0].Search
	resultPage := app.SearchTreePage{Snapshot: repository.SnapshotRef{Kind: repository.SnapshotEmpty}, Complete: true, ScannedEntries: 1, Matches: []app.TreeSearchMatch{
		{Entry: entry(t, "deep/unloaded.go", false), Rank: app.TreeSearchRank{Class: 2, PathBytes: 16}, MatchRanges: []repository.ByteRange{{Start: 5, End: 13}}},
	}}
	m.Update(SearchResultMsg{Result: SearchResult{Request: request, Page: resultPage}})
	if got := m.View(); !strings.Contains(got, "deep/unloaded.go") || strings.Contains(got, "> loaded.go ") {
		t.Fatalf("search view = %q", got)
	}
	m.Update(ExitSearchMsg{})
	if got := m.View(); !strings.Contains(got, "loaded.go") {
		t.Fatalf("hierarchy view after search = %q", got)
	}
}

func page(t *testing.T, entries ...repository.TreeEntry) app.TreePage {
	t.Helper()
	result := app.TreePage{Entries: entries, Snapshot: repository.SnapshotRef{Kind: repository.SnapshotEmpty}}
	if err := result.Validate(); err != nil {
		t.Fatalf("invalid test page: %v", err)
	}
	return result
}

func entry(t *testing.T, path string, directory bool) repository.TreeEntry {
	t.Helper()
	lastSlash := strings.LastIndexByte(path, '/')
	parentText, nameText := "", path
	if lastSlash >= 0 {
		parentText, nameText = path[:lastSlash], path[lastSlash+1:]
	}
	pathValue, err := repository.NewRepoPath([]byte(path))
	if err != nil {
		t.Fatalf("path %q: %v", path, err)
	}
	nameValue, err := repository.NewRepoPath([]byte(nameText))
	if err != nil {
		t.Fatalf("name %q: %v", nameText, err)
	}
	var parentValue repository.RepoPath
	if parentText != "" {
		parentValue, err = repository.NewRepoPath([]byte(parentText))
		if err != nil {
			t.Fatalf("parent %q: %v", parentText, err)
		}
	}
	kind, mode := repository.FileKindRegular, uint32(0o100644)
	if directory {
		kind, mode = repository.FileKindDirectory, 0o40000
	}
	result := repository.TreeEntry{Path: pathValue, Name: nameValue, Parent: parentValue, Kind: kind, Mode: mode, LazyChild: directory}
	if err := result.Validate(); err != nil {
		t.Fatalf("invalid test entry %q: %v", path, err)
	}
	return result
}
