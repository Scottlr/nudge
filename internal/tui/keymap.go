package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain/review"
	"github.com/Scottlr/nudge/internal/tui/components/code"
	"github.com/Scottlr/nudge/internal/tui/components/comment"
	"github.com/Scottlr/nudge/internal/tui/components/discussion"
	threadpane "github.com/Scottlr/nudge/internal/tui/components/threads"
	"github.com/Scottlr/nudge/internal/tui/components/tree"
	reattachpane "github.com/Scottlr/nudge/internal/tui/reattach"
)

func newDefaultCommandRegistry() (*CommandRegistry, error) {
	registry := NewCommandRegistry()
	registrations := []CommandRegistration{
		commandRegistration(CommandApproveRuntimeOnce, ContextRuntimeApproval, "allow once", "Allow this exact Codex command once", []string{"y"}, runtimeApprovalAllowAvailable, false, func(m *Model) tea.Cmd { return m.resolveRuntimeApproval(true) }),
		commandRegistration(CommandDenyRuntime, ContextRuntimeApproval, "deny runtime request", "Deny this Codex runtime request", []string{"n"}, runtimeApprovalAvailable, true, func(m *Model) tea.Cmd { return m.resolveRuntimeApproval(false) }),
		commandRegistration(CommandCloseOverlay, ContextOverlay, "close", "Close the current modal", []string{"q", "esc", "ctrl+c"}, overlayDismissibleAvailable, false, func(m *Model) tea.Cmd {
			if m.AnchorReattachmentOpen() {
				return m.handleReattachMessage(reattachpane.CancelMsg{})
			}
			m.dismissOverlay()
			return nil
		}),
		commandRegistration(CommandID("reattach_move_up"), ContextOverlay, "move up", "Move through anchor candidates", []string{"k", "up"}, func(m *Model) bool { return m.AnchorReattachmentOpen() }, false, func(m *Model) tea.Cmd { return m.handleReattachMessage(reattachpane.MoveSelectionMsg{Delta: -1}) }),
		commandRegistration(CommandID("reattach_move_down"), ContextOverlay, "move down", "Move through anchor candidates", []string{"j", "down"}, func(m *Model) bool { return m.AnchorReattachmentOpen() }, false, func(m *Model) tea.Cmd { return m.handleReattachMessage(reattachpane.MoveSelectionMsg{Delta: 1}) }),
		commandRegistration(CommandID("reattach_confirm"), ContextOverlay, "confirm anchor", "Review or confirm the selected anchor candidate", []string{"enter"}, func(m *Model) bool { return m.AnchorReattachmentOpen() }, false, func(m *Model) tea.Cmd {
			if m.reattachPane.Confirming() {
				return m.handleReattachMessage(reattachpane.ConfirmMsg{})
			}
			return m.handleReattachMessage(reattachpane.BeginConfirmMsg{})
		}),
		commandRegistration(CommandEditorSubmit, ContextEditor, "send reply", "Submit the active multiline reply", []string{"ctrl+enter"}, editorAvailable, false, func(m *Model) tea.Cmd {
			return m.handleDiscussionMessage(discussion.UpdateDraftMsg{Message: comment.SubmitMsg{}})
		}),
		commandRegistration(CommandEditorCancel, ContextEditor, "cancel editor", "Close the editor and retain the draft", []string{"esc", "ctrl+c"}, editorAvailable, false, func(m *Model) tea.Cmd {
			return m.handleDiscussionMessage(discussion.UpdateDraftMsg{Message: comment.CancelMsg{}})
		}),
		commandRegistration(CommandFocusNext, ContextGlobal, "focus next", "Move focus to the next visible pane", []string{"tab"}, nil, false, func(m *Model) tea.Cmd { m.moveFocus(1); return nil }),
		commandRegistration(CommandFocusPrevious, ContextGlobal, "focus previous", "Move focus to the previous visible pane", []string{"shift+tab"}, nil, false, func(m *Model) tea.Cmd { m.moveFocus(-1); return nil }),
		commandRegistration(CommandQuit, ContextGlobal, "quit", "Exit Nudge", []string{"q", "ctrl+c"}, nil, true, func(*Model) tea.Cmd { return func() tea.Msg { return tea.Quit() } }),
		commandRegistration(CommandMoveUp, ContextPane, "move up", "Move through the focused list or code", []string{"k", "up"}, nil, false, func(m *Model) tea.Cmd { return m.handlePaneMessage(movePaneMessage{delta: -1}) }),
		commandRegistration(CommandMoveDown, ContextPane, "move down", "Move through the focused list or code", []string{"j", "down"}, nil, false, func(m *Model) tea.Cmd { return m.handlePaneMessage(movePaneMessage{delta: 1}) }),
		commandRegistration(CommandActivate, ContextPane, "open/select", "Open the selected item", []string{"enter"}, func(m *Model) bool { return m != nil && (m.focus == PaneRepository || m.focus == PaneThreads) }, false, func(m *Model) tea.Cmd { return m.handlePaneMessage(activatePaneMessage{pane: m.focus}) }),
		commandRegistration(CommandToggleSelection, ContextPane, "select range", "Begin or end a code range selection", []string{"v"}, func(m *Model) bool {
			return m != nil && m.focus == PaneCode && m.codePane != nil && m.codePane.Content().Validate() == nil
		}, false, func(m *Model) tea.Cmd { return m.handlePaneMessage(code.ToggleSelectionMsg{}) }),
		commandRegistration(CommandToggleFileFilter, ContextPane, "toggle file filter", "Switch between changed and all files", []string{"f"}, func(m *Model) bool { return m != nil && m.focus == PaneRepository && m.repositoryPane != nil }, false, func(m *Model) tea.Cmd {
			filter := tree.FilterAll
			if m.repositoryPane.Filter() == tree.FilterAll {
				filter = tree.FilterChanged
			}
			return m.handlePaneMessage(tree.SetFilterMsg{Filter: filter})
		}),
		commandRegistration(CommandReply, ContextPane, "reply", "Open the active thread reply editor", []string{"r"}, func(m *Model) bool {
			return m != nil && m.focus == PaneDiscussion && m.discussionPane != nil && m.discussionPane.Thread() != nil
		}, false, func(m *Model) tea.Cmd { return m.handleDiscussionMessage(discussion.ToggleReplyMsg{}) }),
		commandRegistration(CommandResolve, ContextPane, "resolve/reopen", "Resolve or reopen the active thread", []string{"R"}, func(m *Model) bool {
			return m != nil && m.focus == PaneDiscussion && m.discussionPane != nil && m.discussionPane.Thread() != nil
		}, false, func(m *Model) tea.Cmd {
			thread := m.discussionPane.Thread()
			return m.handleDiscussionMessage(discussion.ResolveMsg{Resolved: thread.Resolution != review.ResolutionResolved})
		}),
		commandRegistration(CommandHelp, ContextGlobal, "help", "Open context-sensitive keyboard help", []string{"?"}, nil, false, func(m *Model) tea.Cmd { m.openHelp(); return nil }),
	}
	for _, registration := range registrations {
		if err := registry.Register(registration); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func commandRegistration(id CommandID, context CommandContext, label, description string, keys []string, available func(*Model) bool, destructive bool, handler CommandHandler) CommandRegistration {
	return CommandRegistration{
		Spec:      CommandSpec{ID: id, Context: context, Label: label, Description: description, Required: true, Destructive: destructive, Binding: key.NewBinding(key.WithKeys(keys...), key.WithHelp(strings.Join(keys, "/"), description))},
		Available: available,
		Handler:   handler,
	}
}

func editorAvailable(m *Model) bool {
	return m != nil && m.discussionPane != nil && m.discussionPane.ReplyFocused()
}

func overlayDismissibleAvailable(m *Model) bool {
	if m == nil {
		return false
	}
	if m.AnchorReattachmentOpen() {
		return true
	}
	overlay, ok := m.overlays.Top()
	return ok && overlay.Dismissible
}

func (m *Model) handleReattachMessage(message any) tea.Cmd {
	if m == nil || m.reattachPane == nil {
		return nil
	}
	intents := m.reattachPane.Update(message)
	for _, intent := range intents {
		if intent.Cancel {
			m.reattachPane = reattachpane.NewModel("reviewer")
			m.reattachPending = false
			return nil
		}
		if intent.Reattach == nil {
			continue
		}
		if m.client == nil || m.sessionGuard == nil {
			m.reattachPane.Update(reattachpane.SetErrorMsg{Message: "session writer unavailable"})
			continue
		}
		m.reattachPending = true
		candidate := intent.Reattach
		return dispatchCommand(m.ctx, m.client, app.ReattachAnchor{Guard: *m.sessionGuard, ThreadID: candidate.ThreadID, CurrentGeneration: candidate.CurrentGeneration, Candidate: candidate.Candidate, CandidateFingerprint: candidate.CandidateFingerprint, Actor: candidate.Actor})
	}
	return nil
}

func runtimeApprovalAvailable(m *Model) bool {
	return m != nil && m.runtimeApproval != nil
}

func runtimeApprovalAllowAvailable(m *Model) bool {
	return runtimeApprovalAvailable(m) && app.CanApproveRuntimeApproval(*m.runtimeApproval)
}

type movePaneMessage struct{ delta int }
type activatePaneMessage struct{ pane Pane }

func (m *Model) handlePaneMessage(message any) tea.Cmd {
	if m == nil {
		return nil
	}
	if value, ok := message.(movePaneMessage); ok {
		switch m.focus {
		case PaneRepository:
			return tea.Batch(m.handleTreeIntents(m.repositoryPane.Update(tree.MoveSelectionMsg{Delta: value.delta}))...)
		case PaneCode:
			return m.handleCodeIntents(m.codePane.Update(code.MoveVerticalMsg{Delta: value.delta}))
		case PaneThreads:
			return tea.Batch(m.handleThreadIntents(m.threadPane.Update(threadpane.MoveSelectionMsg{Delta: value.delta}))...)
		case PaneDiscussion:
			return tea.Batch(m.handleDiscussionIntents(m.discussionPane.Update(discussion.MoveSelectionMsg{Delta: value.delta}))...)
		}
	}
	if value, ok := message.(activatePaneMessage); ok {
		switch value.pane {
		case PaneRepository:
			return tea.Batch(m.handleTreeIntents(m.repositoryPane.Update(tree.ActivateSelectionMsg{}))...)
		case PaneThreads:
			return tea.Batch(m.handleThreadIntents(m.threadPane.Update(threadpane.ActivateSelectionMsg{}))...)
		}
	}
	switch m.focus {
	case PaneRepository:
		return tea.Batch(m.handleTreeIntents(m.repositoryPane.Update(message))...)
	case PaneCode:
		return m.handleCodeIntents(m.codePane.Update(message))
	case PaneThreads:
		return tea.Batch(m.handleThreadIntents(m.threadPane.Update(message))...)
	case PaneDiscussion:
		return tea.Batch(m.handleDiscussionIntents(m.discussionPane.Update(message))...)
	default:
		return nil
	}
}

func (m *Model) handleTreeIntents(intents []tree.Intent) []tea.Cmd {
	commands := make([]tea.Cmd, 0, len(intents))
	for _, intent := range intents {
		if intent.SelectPath == nil || m.client == nil {
			continue
		}
		path, err := intent.SelectPath.Path.Path()
		if err != nil {
			m.lastError = "selected path unavailable"
			continue
		}
		commands = append(commands, dispatchCommand(m.ctx, m.client, app.SelectFile{Path: path}))
	}
	return commands
}

func (m *Model) handleCodeIntents(_ []code.Intent) tea.Cmd { return nil }

func (m *Model) handleDiscussionMessage(message any) tea.Cmd {
	if m == nil || m.discussionPane == nil {
		return nil
	}
	return tea.Batch(m.handleDiscussionIntents(m.discussionPane.Update(message))...)
}

func (m *Model) activeContexts() []CommandContext {
	if m == nil {
		return nil
	}
	if m.runtimeApproval != nil {
		return []CommandContext{ContextRuntimeApproval}
	}
	if m.AnchorReattachmentOpen() {
		return []CommandContext{ContextOverlay}
	}
	if m.overlays.Len() > 0 {
		return []CommandContext{ContextOverlay}
	}
	if editorAvailable(m) {
		return []CommandContext{ContextEditor}
	}
	return []CommandContext{ContextPane, ContextGlobal}
}

func (m *Model) handleKeyPress(message tea.KeyPressMsg) tea.Cmd {
	if m == nil || m.commands == nil {
		return nil
	}
	registration, matched, err := m.commands.Resolve(m, message, m.activeContexts())
	if err != nil {
		m.lastError = "keyboard dispatch unavailable"
		return nil
	}
	if matched {
		return registration.Handler(m)
	}
	if editorAvailable(m) {
		return m.handleDiscussionMessage(discussion.UpdateDraftMsg{Message: message})
	}
	return nil
}
