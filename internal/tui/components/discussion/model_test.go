package discussion

import (
	"strings"
	"testing"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/review"
)

func TestDiscussionRejectsStaleBodyAndMarksReadOnlyAfterPresentation(t *testing.T) {
	thread := app.ThreadSummary{ID: "thread-1", SessionID: "session-1", Title: "Concern", Resolution: review.ResolutionOpen, Conversation: review.ConversationIdle, Proposal: review.ProposalNone, Anchor: review.AnchorValid, Read: review.Unread}
	model := NewModel()
	model.SetSize(120, 8)
	pageIntents := model.Update(SetThreadMsg{Revision: 3, Thread: &thread})
	if len(pageIntents) != 1 || pageIntents[0].MessagePageRequest == nil {
		t.Fatalf("message page request = %#v", pageIntents)
	}
	pageRequest := *pageIntents[0].MessagePageRequest
	messageID := domain.MessageID("message-1")
	digest := strings.Repeat("a", 64)
	page := MessagePageResult{Request: pageRequest, Page: app.MessagePageResult{Revision: 3, Items: []app.MessageSummary{{ID: messageID, ThreadID: thread.ID, Role: review.RoleUser, Status: review.MessagePending, Ordinal: 1, ByteLength: 5, SHA256: digest}}}}
	bodyIntents := model.Update(MessagePageResultMsg{Result: page})
	if len(bodyIntents) != 1 || bodyIntents[0].BodyRangeRequest == nil {
		t.Fatalf("body range request = %#v", bodyIntents)
	}
	bodyRequest := *bodyIntents[0].BodyRangeRequest
	model.Update(BodyRangeResultMsg{Result: BodyRangeResult{Request: BodyRangeRequest{ThreadID: bodyRequest.ThreadID, Revision: bodyRequest.Revision, Range: bodyRequest.Range, Token: bodyRequest.Token + 1}, Chunk: app.MessageBodyChunk{MessageID: messageID, TotalLength: 5, SHA256: digest, Complete: true, Bytes: []byte("hello")}}})
	if !strings.Contains(model.View(), "message body loading") {
		t.Fatal("stale body range was presented as complete")
	}
	model.Update(BodyRangeResultMsg{Result: BodyRangeResult{Request: bodyRequest, Chunk: app.MessageBodyChunk{MessageID: messageID, Offset: 0, TotalLength: 5, SHA256: digest, Complete: true, Bytes: []byte("hello")}}})
	intents := model.Update(PresentMessageMsg{MessageID: messageID})
	if len(intents) != 1 || intents[0].MarkRead == nil || intents[0].MarkRead.ThreadID != thread.ID {
		t.Fatalf("mark-read intent = %#v", intents)
	}
}

func TestDiscussionRetainsDraftAcrossPageRefresh(t *testing.T) {
	thread := app.ThreadSummary{ID: "thread-1", SessionID: "session-1", Resolution: review.ResolutionOpen, Conversation: review.ConversationIdle, Proposal: review.ProposalNone, Anchor: review.AnchorValid, Read: review.Read}
	model := NewModel()
	model.Update(SetThreadMsg{Revision: 3, Thread: &thread})
	model.Update(ToggleReplyMsg{})
	model.Update(SetDraftMsg{Text: "keep this draft"})
	model.Update(SetThreadMsg{Revision: 3, Thread: &thread})
	if model.draft == nil {
		t.Fatal("draft was discarded")
	}
	if model.draft.Value() != "keep this draft" {
		t.Fatalf("draft = %q, want retained draft", model.draft.Value())
	}
}
