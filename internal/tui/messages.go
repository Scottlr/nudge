package tui

import (
	"errors"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	reattachpane "github.com/Scottlr/nudge/internal/tui/reattach"
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

// LocalReviewMsg carries one immutable local-review composition update.
type LocalReviewMsg struct {
	Snapshot app.LocalReviewSnapshot
}

// LocalReviewStreamClosedMsg reports that the local-review worker exited.
type LocalReviewStreamClosedMsg struct{}

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

// StartVisibleAnimationMsg and StopVisibleAnimationMsg let the owning root
// consumer declare whether an animated marker is visible.
type StartVisibleAnimationMsg struct{}
type StopVisibleAnimationMsg struct{}

// SetVisibleAnimatedWorkMsg declares the current count of visible animated
// work items. The root scheduler owns the resulting timer chain.
type SetVisibleAnimatedWorkMsg struct {
	Count int
}

// SetReducedMotionMsg supplies the run-scoped presentation decision without
// making the scheduler responsible for terminal capability policy.
type SetReducedMotionMsg struct {
	Reduced bool
}

// InvalidateRenderMsg requests one coalesced redraw/tick opportunity.
type InvalidateRenderMsg struct{}

// ApplicationIntentMsg carries an application-owned command for asynchronous
// dispatch by the root.
type ApplicationIntentMsg struct {
	Command app.Command
}

// ShowAnchorReattachmentMsg opens the manual anchor projection with one
// immutable candidate set.
type ShowAnchorReattachmentMsg struct{ Projection reattachpane.Projection }

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

// RuntimeApprovalMsg presents one ephemeral provider approval request. The
// message is never persisted and the exact scope is discarded on resolution.
type RuntimeApprovalMsg struct {
	Approval app.RuntimeApproval
}

// ClearRuntimeApprovalMsg removes the current runtime approval overlay after
// provider resolution or reconnect.
type ClearRuntimeApprovalMsg struct{}

// QuitIntentMsg is emitted by the input owner after its explicit quit policy
// has been satisfied.
type QuitIntentMsg struct{}

var errNilApplicationCommand = errors.New("nil application command")
