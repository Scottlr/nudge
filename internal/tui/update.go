package tui

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/presentation"
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
	case tea.WindowSizeMsg:
		m.setDimensions(Dimensions{Width: message.Width, Height: message.Height})
	case SnapshotMsg:
		if message.Snapshot.Revision >= m.snapshot.Revision {
			m.snapshot = message.Snapshot.Clone()
		}
		if !m.snapshotClosed && m.snapshots != nil {
			return m, receiveSnapshot(m.snapshots)
		}
	case SnapshotStreamClosedMsg:
		m.snapshotClosed = true
	case EventMsg:
		if !m.eventsClosed && m.events != nil {
			return m, receiveEvent(m.events)
		}
	case EventStreamClosedMsg:
		m.eventsClosed = true
	case FocusNextMsg:
		m.moveFocus(1)
	case FocusPreviousMsg:
		m.moveFocus(-1)
	case SetFocusMsg:
		m.setFocus(message.Pane)
	case SetNarrowPaneMsg:
		m.setNarrowPane(message.Pane)
	case StartVisibleAnimationMsg:
		return m, m.schedulerPlan(m.scheduler.StartVisibleWork())
	case StopVisibleAnimationMsg:
		m.scheduler.StopVisibleWork()
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
		m.overlays.Push(message.Overlay)
	case DismissOverlayMsg:
		m.overlays.Pop()
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
		return
	}
	if m.layout.Mode == LayoutMedium && (m.focus == PaneThreads || m.focus == PaneDiscussion) {
		m.lowerPane = m.focus
	}
	if !isPane(m.focus) {
		m.focus = PaneRepository
	}
}

func (m *Model) moveFocus(step int) {
	panes := []Pane{PaneRepository, PaneCode, PaneThreads, PaneDiscussion}
	current := 0
	for i, pane := range panes {
		if pane == m.focus {
			current = i
			break
		}
	}
	if step < 0 {
		current = (current + len(panes) - 1) % len(panes)
	} else {
		current = (current + 1) % len(panes)
	}
	m.setFocus(panes[current])
}

func (m *Model) setFocus(pane Pane) {
	if !isPane(pane) {
		return
	}
	m.focus = pane
	if pane == PaneThreads || pane == PaneDiscussion {
		m.lowerPane = pane
	}
	if m.layout.Mode == LayoutNarrow {
		m.narrowPane = pane
	}
}

func (m *Model) setNarrowPane(pane Pane) {
	if !isPane(pane) {
		return
	}
	m.narrowPane = pane
	if m.layout.Mode == LayoutNarrow {
		m.focus = pane
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
