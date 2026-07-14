package discussion

import (
	"github.com/Scottlr/nudge/internal/domain/review"
	"github.com/Scottlr/nudge/internal/tui/components/comment"
)

// Update applies one typed frontend message and emits inert root intents.
func (m *Model) Update(message any) []Intent {
	if m == nil {
		return nil
	}
	switch value := message.(type) {
	case SetThreadMsg:
		return m.setThread(value)
	case MessagePageResultMsg:
		return m.acceptPage(value.Result)
	case BodyRangeResultMsg:
		m.acceptBody(value.Result, value.Err)
	case MoveSelectionMsg:
		m.moveSelection(value.Delta)
	case PresentMessageMsg:
		return m.presentMessage(value.MessageID)
	case ToggleReplyMsg:
		if m.thread != nil {
			if m.draft == nil {
				m.draft = comment.NewModel(review.CodeAnchor{})
			}
			m.replyFocused = !m.replyFocused
			if m.replyFocused {
				return []Intent{}
			}
		}
	case UpdateDraftMsg:
		if m.replyFocused && m.draft != nil {
			intent, _ := m.draft.Update(value.Message)
			if intent.CreateThread != nil {
				return m.sendReply()
			}
			if intent.Cancelled {
				m.replyFocused = false
			}
		}
	case SetDraftMsg:
		if m.draft == nil {
			m.draft = comment.NewModel(review.CodeAnchor{})
		}
		m.draft.SetValue(value.Text)
	case ResolveMsg:
		if m.thread != nil {
			return []Intent{{Resolve: &ResolveIntent{ThreadID: m.thread.ID, Resolved: value.Resolved}}}
		}
	case LoadNextPageMsg:
		if request := m.pageRequest(); request != nil {
			return []Intent{{MessagePageRequest: request}}
		}
	case SetFocusMsg:
		m.focused = value.Focused
		if !m.focused {
			m.replyFocused = false
		}
	}
	return nil
}
