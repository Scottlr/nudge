package tui

import (
	"errors"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
)

// SnapshotMsg carries one immutable application projection to the root.
type SnapshotMsg struct {
	Snapshot app.AppSnapshot
}

// SnapshotStreamClosedMsg reports that the application snapshot stream ended.
type SnapshotStreamClosedMsg struct{}

// EventMsg wakes the root for one normalized application event. SnapshotMsg
// remains the source of rendered workflow truth.
type EventMsg struct {
	Event app.Event
}

// EventStreamClosedMsg reports that the application event stream ended.
type EventStreamClosedMsg struct{}

// FocusNextMsg and FocusPreviousMsg are typed focus intents from the input
// owner. They avoid embedding raw key handling in the root.
type FocusNextMsg struct{}
type FocusPreviousMsg struct{}

// SetFocusMsg asks the root to focus a semantic pane.
type SetFocusMsg struct {
	Pane Pane
}

// SetNarrowPaneMsg changes the visible tab in a narrow layout.
type SetNarrowPaneMsg struct {
	Pane Pane
}

// ApplicationIntentMsg carries an application-owned command for asynchronous
// dispatch by the root.
type ApplicationIntentMsg struct {
	Command app.Command
}

// DispatchResultMsg reports the bounded result of one application command.
type DispatchResultMsg struct {
	OperationID domain.OperationID
	Err         error
}

// ShowOverlayMsg and DismissOverlayMsg control the bounded root overlay stack.
type ShowOverlayMsg struct {
	Overlay Overlay
}

type DismissOverlayMsg struct{}

// QuitIntentMsg is emitted by the input owner after its explicit quit policy
// has been satisfied.
type QuitIntentMsg struct{}

var errNilApplicationCommand = errors.New("nil application command")
