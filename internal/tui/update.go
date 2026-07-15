package tui

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/presentation"
	discussionpane "github.com/Scottlr/nudge/internal/tui/components/discussion"
	threadpane "github.com/Scottlr/nudge/internal/tui/components/threads"
)

// Update applies frontend messages and returns commands for asynchronous
// stream reads or application dispatch. It never calls an adapter directly.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m == nil {
		return m, nil
	}
	if m.scheduler == nil {
		m.scheduler = DefaultRenderScheduler()
	}
	switch message := msg.(type) {
	case tea.ColorProfileMsg:
		return m, m.schedulerPlan(mergeTickPlans(m.applyColorProfile(message), m.syncVisibleAnimatedWork()))
	case tea.WindowSizeMsg:
		m.setDimensions(Dimensions{Width: message.Width, Height: message.Height})
		return m, m.schedulerPlan(m.syncVisibleAnimatedWork())
	case SnapshotMsg:
		var renderPlan TickPlan
		if message.Snapshot.Revision >= m.snapshot.Revision {
			m.snapshot = message.Snapshot.Clone()
			m.syncReviewSnapshot()
			renderPlan = m.syncVisibleAnimatedWork()
		}
		if !m.snapshotClosed && m.snapshots != nil {
			return m, tea.Batch(m.schedulerPlan(renderPlan), receiveSnapshot(m.snapshots))
		}
		return m, m.schedulerPlan(renderPlan)
	case SnapshotStreamClosedMsg:
		m.snapshotClosed = true
	case EventMsg:
		if !m.eventsClosed && m.events != nil {
			return m, receiveEvent(m.events)
		}
	case EventStreamClosedMsg:
		m.eventsClosed = true
	case LocalReviewMsg:
		if message.Snapshot.Revision >= m.localReview.Revision {
			m.localReview = message.Snapshot.Clone()
			m.syncLocalReviewPanes()
		}
		if m.local != nil {
			return m, receiveLocalReview(m.local)
		}
	case LocalReviewStreamClosedMsg:
		m.local = nil
	case threadpane.PageResultMsg, threadpane.PageErrorMsg, threadpane.MoveSelectionMsg, threadpane.ActivateSelectionMsg, threadpane.LoadNextPageMsg:
		return m, tea.Batch(m.threadPaneMessage(message)...)
	case discussionpane.MessagePageResultMsg, discussionpane.BodyRangeResultMsg, discussionpane.MoveSelectionMsg, discussionpane.PresentMessageMsg, discussionpane.ToggleReplyMsg, discussionpane.UpdateDraftMsg, discussionpane.SetDraftMsg, discussionpane.ResolveMsg, discussionpane.LoadNextPageMsg:
		return m, tea.Batch(m.discussionPaneMessage(message)...)
	case tea.KeyPressMsg:
		return m, m.handleKeyPress(message)
	case FocusNextMsg:
		m.moveFocus(1)
	case FocusPreviousMsg:
		m.moveFocus(-1)
	case SetFocusMsg:
		m.setFocus(message.Pane)
		return m, m.schedulerPlan(m.syncVisibleAnimatedWork())
	case SetNarrowPaneMsg:
		m.setNarrowPane(message.Pane)
		return m, m.schedulerPlan(m.syncVisibleAnimatedWork())
	case StartVisibleAnimationMsg:
		return m, m.schedulerPlan(m.scheduler.SetVisibleAnimatedWork(1))
	case StopVisibleAnimationMsg:
		m.scheduler.SetVisibleAnimatedWork(0)
	case SetVisibleAnimatedWorkMsg:
		return m, m.schedulerPlan(m.scheduler.SetVisibleAnimatedWork(message.Count))
	case SetReducedMotionMsg:
		return m, m.schedulerPlan(m.scheduler.SetReducedMotion(message.Reduced))
	case InvalidateRenderMsg:
		return m, m.schedulerPlan(m.scheduler.Invalidate())
	case RenderTickMsg:
		accepted, next := m.scheduler.AcceptTick(message)
		if accepted {
			m.animationFrame++
			return m, m.schedulerPlan(next)
		}
	case ApplicationIntentMsg:
		if message.Command == nil {
			m.lastError = presentation.ProjectTerminalText(errNilApplicationCommand.Error(), presentation.TerminalTextScalar)
			return m, nil
		}
		if m.client == nil {
			m.lastError = presentation.ProjectTerminalText("application client unavailable", presentation.TerminalTextScalar)
			return m, nil
		}
		return m, dispatchCommand(m.ctx, m.client, message.Command)
	case DispatchResultMsg:
		if message.Err != nil {
			m.lastError = presentation.ProjectTerminalText(message.Err.Error(), presentation.TerminalTextScalar)
		} else {
			m.lastError = ""
		}
	case ShowOverlayMsg:
		m.showOverlay(message.Overlay)
	case DismissOverlayMsg:
		m.dismissOverlay()
	case RuntimeApprovalMsg:
		m.showRuntimeApproval(message.Approval)
	case ClearRuntimeApprovalMsg:
		m.clearRuntimeApproval()
	case QuitIntentMsg:
		return m, func() tea.Msg { return tea.Quit() }
	}
	return m, nil
}

func (m *Model) schedulerPlan(plan TickPlan) tea.Cmd {
	if plan.Command == nil {
		return nil
	}
	return plan.Command
}

func receiveSnapshot(stream <-chan app.AppSnapshot) tea.Cmd {
	return func() tea.Msg {
		snapshot, ok := <-stream
		if !ok {
			return SnapshotStreamClosedMsg{}
		}
		return SnapshotMsg{Snapshot: snapshot}
	}
}

func receiveEvent(stream <-chan app.Event) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-stream
		if !ok {
			return EventStreamClosedMsg{}
		}
		return EventMsg{Event: event}
	}
}

func receiveLocalReview(stream <-chan app.LocalReviewSnapshot) tea.Cmd {
	return func() tea.Msg {
		snapshot, ok := <-stream
		if !ok {
			return LocalReviewStreamClosedMsg{}
		}
		return LocalReviewMsg{Snapshot: snapshot}
	}
}

func dispatchCommand(ctx context.Context, client app.ApplicationClient, command app.Command) tea.Cmd {
	return func() tea.Msg {
		if ctx == nil {
			ctx = context.Background()
		}
		operationID, err := client.Dispatch(ctx, command)
		return DispatchResultMsg{OperationID: operationID, Err: err}
	}
}

func (m *Model) setDimensions(dimensions Dimensions) {
	previousFocus := m.focus
	m.dimensions = dimensions
	m.layout = CalculateLayout(dimensions)
	if m.layout.Mode == LayoutNarrow {
		if isPane(previousFocus) {
			m.narrowPane = previousFocus
		}
		m.focus = m.narrowPane
	} else if m.layout.Mode == LayoutMedium && (m.focus == PaneThreads || m.focus == PaneDiscussion) {
		m.lowerPane = m.focus
	}
	if !isPane(m.focus) {
		m.focus = PaneRepository
	}
	m.normalizeFocus()
	m.resizeChildPanes()
}

func (m *Model) moveFocus(step int) {
	if target, ok := m.focusRing().Next(m.currentFocusTarget(), step); ok {
		m.setFocus(target.Pane)
	}
}

func (m *Model) setFocus(pane Pane) {
	if !isPane(pane) {
		return
	}
	if m.layout.Mode == LayoutMedium && (pane == PaneThreads || pane == PaneDiscussion) {
		m.lowerPane = pane
	}
	if m.layout.Mode == LayoutNarrow {
		m.narrowPane = pane
	}
	target, ok := m.focusRing().ForPane(pane)
	if !ok {
		if m.layout.Mode == LayoutUnknown || m.layout.Mode == LayoutTooSmall {
			m.focus = pane
			m.focusTarget = focusTargetID(pane)
		}
		return
	}
	m.focus = target.Pane
	m.focusTarget = target.ID
	if pane == PaneThreads || pane == PaneDiscussion {
		m.lowerPane = pane
	}
	m.updateChildFocus()
}

func (m *Model) setNarrowPane(pane Pane) {
	if !isPane(pane) {
		return
	}
	m.narrowPane = pane
	if m.layout.Mode == LayoutNarrow {
		m.setFocus(pane)
	}
}

func isPane(pane Pane) bool {
	switch pane {
	case PaneRepository, PaneCode, PaneThreads, PaneDiscussion:
		return true
	default:
		return false
	}
}

func (m *Model) statusError() string {
	if m.lastError == "" {
		return ""
	}
	return fmt.Sprintf(" | %s", m.lastError)
}
