package threads

import (
	"testing"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/review"
)

func TestVisibleAnimatedWorkUsesRenderedWindowAndBusyStates(t *testing.T) {
	session := domain.ReviewSessionID("session-1")
	model := NewModel()
	model.SetSize(80, 12)
	model.Update(SetSnapshotMsg{Snapshot: app.AppSnapshot{
		Revision:  1,
		SessionID: &session,
		Threads: []app.ThreadSummary{
			{ID: "thread-1", SessionID: session, Resolution: review.ResolutionOpen, Conversation: review.ConversationStreaming, Proposal: review.ProposalNone, Anchor: review.AnchorValid, Read: review.Unread},
			{ID: "thread-2", SessionID: session, Resolution: review.ResolutionOpen, Conversation: review.ConversationAwaitingRuntimeApproval, Proposal: review.ProposalNone, Anchor: review.AnchorValid, Read: review.Unread},
			{ID: "thread-3", SessionID: session, Resolution: review.ResolutionOpen, Conversation: review.ConversationIdle, Proposal: review.ProposalGenerating, Anchor: review.AnchorValid, Read: review.Unread},
		}}})
	if got := model.VisibleAnimatedWork(); got != 2 {
		t.Fatalf("visible animated work = %d, want 2", got)
	}
}
