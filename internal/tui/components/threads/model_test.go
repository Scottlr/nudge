package threads

import (
	"testing"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/review"
)

func TestThreadListRejectsStalePagesAndActivatesStableID(t *testing.T) {
	session := domain.ReviewSessionID("session-1")
	model := NewModel()
	requests := model.Update(SetSnapshotMsg{Snapshot: app.AppSnapshot{Revision: 7, SessionID: &session}})
	if len(requests) != 1 || requests[0].PageRequest == nil {
		t.Fatalf("initial request = %#v", requests)
	}
	request := *requests[0].PageRequest
	item := app.ThreadSummary{ID: "thread-1", SessionID: session, Resolution: review.ResolutionOpen, Conversation: review.ConversationIdle, Proposal: review.ProposalNone, Anchor: review.AnchorValid, Read: review.Unread}
	model.Update(PageResultMsg{Result: PageResult{Request: request, Page: app.ThreadPageResult{Revision: 6, Items: []app.ThreadSummary{item}}}})
	if model.Selected() != "" {
		t.Fatal("stale page changed selection")
	}
	model.Update(PageResultMsg{Result: PageResult{Request: request, Page: app.ThreadPageResult{Revision: 7, Items: []app.ThreadSummary{item}}}})
	if model.Selected() != item.ID {
		t.Fatalf("selected = %q, want %q", model.Selected(), item.ID)
	}
	intents := model.Update(ActivateSelectionMsg{})
	if len(intents) != 1 || intents[0].Activate == nil || intents[0].Activate.ThreadID != item.ID {
		t.Fatalf("activation intent = %#v", intents)
	}
}
