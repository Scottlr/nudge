package code

import (
	"strings"
	"testing"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
	"github.com/charmbracelet/x/ansi"
)

func TestThreadMarkersGroupOverlapAndPreserveVisualPrecedence(t *testing.T) {
	contentID := app.DisplayedContentID(strings.Repeat("a", 64))
	path := repository.RepoPath("main.go")
	line := 1
	page := app.DisplayedContentPage{ContentID: contentID, Rows: []app.DisplayedRow{{ID: app.CodeRowID{Content: contentID}, Kind: app.DisplayedRowContext, HunkID: "hunk", BaseLine: &line, HeadLine: &line, BaseText: "value", HeadText: "value", Side: app.SideBoth, Selectable: true}}}
	content := app.DisplayedContent{ID: contentID, Mode: app.DisplayUnifiedDiff, Status: app.ContentReady, BasePath: &path, HeadPath: &path}
	model := NewModel()
	requests := model.Update(SnapshotContentMsg{Content: content})
	model.Update(PageResultMsg{Result: PageResult{Request: *requests[0].PageRequest, Page: page}})
	status := review.ThreadStatus{Resolution: review.ResolutionOpen, Conversation: review.ConversationIdle, Proposal: review.ProposalReady, Anchor: review.AnchorValid, Read: review.Unread}
	model.Update(SetThreadMarkersMsg{ContentID: contentID, Markers: []ThreadMarker{{ThreadID: domain.ReviewThreadID("thread-1"), Path: path.Key(), Side: app.SideHead, StartLine: 1, EndLine: 1, Status: status}, {ThreadID: domain.ReviewThreadID("thread-2"), Path: path.Key(), Side: app.SideHead, StartLine: 1, EndLine: 1, Status: status}}})
	plain := ansi.Strip(model.View())
	if !strings.Contains(plain, "p2") {
		t.Fatalf("overlap marker = %q, want proposal marker with count", plain)
	}
	model.Update(SetThreadMarkersMsg{ContentID: app.DisplayedContentID(strings.Repeat("b", 64)), Markers: []ThreadMarker{{ThreadID: "thread-3", Path: path.Key(), Side: app.SideHead, StartLine: 1, EndLine: 1, Status: status}}})
	if !strings.Contains(ansi.Strip(model.View()), "p2") {
		t.Fatal("stale content marker replaced current marker projection")
	}
}

func TestMarkerGlyphPrecedenceDoesNotCollapseAxes(t *testing.T) {
	status := review.ThreadStatus{Resolution: review.ResolutionResolved, Conversation: review.ConversationFailed, Proposal: review.ProposalReady, Anchor: review.AnchorOrphaned, Read: review.Unread, FailurePhase: review.FailurePhaseProvider, ErrorCode: review.ErrorCode("provider_failed")}
	if got := markerGlyph([]ThreadMarker{{ThreadID: "thread-1", Status: status}}); got != "!" {
		t.Fatalf("marker glyph = %q, want error precedence", got)
	}
	if status.Resolution != review.ResolutionResolved || status.Proposal != review.ProposalReady || status.Anchor != review.AnchorOrphaned || status.Read != review.Unread {
		t.Fatal("marker precedence mutated independent status axes")
	}
}
