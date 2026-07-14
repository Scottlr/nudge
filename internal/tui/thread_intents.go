package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/Scottlr/nudge/internal/app"
	discussionpane "github.com/Scottlr/nudge/internal/tui/components/discussion"
	threadpane "github.com/Scottlr/nudge/internal/tui/components/threads"
)

func (m *Model) threadPaneMessage(message any) []tea.Cmd {
	if m == nil || m.threadPane == nil {
		return nil
	}
	return m.handleThreadIntents(m.threadPane.Update(message))
}

func (m *Model) discussionPaneMessage(message any) []tea.Cmd {
	if m == nil || m.discussionPane == nil {
		return nil
	}
	return m.handleDiscussionIntents(m.discussionPane.Update(message))
}

func (m *Model) handleThreadIntents(intents []threadpane.Intent) []tea.Cmd {
	commands := make([]tea.Cmd, 0, len(intents))
	for _, intent := range intents {
		if intent.Activate == nil || m.client == nil {
			continue
		}
		command := app.ActivateThread{SessionID: intent.Activate.SessionID, ThreadID: intent.Activate.ThreadID}
		commands = append(commands, dispatchCommand(m.ctx, m.client, command))
	}
	return commands
}

func (m *Model) handleDiscussionIntents(intents []discussionpane.Intent) []tea.Cmd {
	commands := make([]tea.Cmd, 0, len(intents))
	for _, intent := range intents {
		if m.client == nil {
			continue
		}
		var command app.Command
		switch {
		case intent.Reply != nil:
			if m.sessionGuard == nil {
				m.lastError = "thread write session unavailable"
				continue
			}
			command = app.ReplyToThread{Guard: *m.sessionGuard, ThreadID: intent.Reply.ThreadID, Text: intent.Reply.Text}
		case intent.Resolve != nil:
			if m.sessionGuard == nil {
				m.lastError = "thread write session unavailable"
				continue
			}
			command = app.ResolveThread{Guard: *m.sessionGuard, ThreadID: intent.Resolve.ThreadID, Resolved: intent.Resolve.Resolved}
		case intent.MarkRead != nil:
			if m.sessionGuard == nil {
				m.lastError = "thread write session unavailable"
				continue
			}
			command = app.MarkThreadRead{Guard: *m.sessionGuard, ThreadID: intent.MarkRead.ThreadID}
		default:
			continue
		}
		commands = append(commands, dispatchCommand(m.ctx, m.client, command))
	}
	return commands
}
