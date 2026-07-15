package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/review"
	"github.com/Scottlr/nudge/internal/terminal"
	"github.com/charmbracelet/colorprofile"
)

func TestTerminalProfileStartsConservativeAndUpdatesOnce(t *testing.T) {
	t.Parallel()
	model := NewModel(nil)
	initial := model.TerminalPolicy()
	if initial.Capability.Color != terminal.ColorNone || !initial.Capability.ASCII || initial.Capability.Motion != terminal.MotionStatic {
		t.Fatalf("initial terminal policy = %#v", initial)
	}
	if _, command := model.Update(StartVisibleAnimationMsg{}); command != nil {
		t.Fatal("unknown terminal policy scheduled animation")
	}

	updated, profileCommand := model.Update(tea.ColorProfileMsg{Profile: colorprofile.TrueColor})
	model = updated.(*Model)
	active := model.TerminalPolicy()
	if active.Capability.Color != terminal.ColorTrue || active.Capability.ASCII || active.Capability.Motion != terminal.MotionAnimated {
		t.Fatalf("active terminal policy = %#v", active)
	}
	if profileCommand == nil {
		t.Fatal("profile update did not resume visible animation")
	}
	updated, command := model.Update(SetVisibleAnimatedWorkMsg{Count: 2})
	model = updated.(*Model)
	if command == nil || model.scheduler.VisibleAnimatedWork() != 2 {
		t.Fatalf("visible animated work was not admitted: command=%v count=%d", command != nil, model.scheduler.VisibleAnimatedWork())
	}
}

func TestVisibleBusyThreadFeedsRootScheduler(t *testing.T) {
	t.Parallel()
	session := domain.ReviewSessionID("session-1")
	model := NewModel(nil,
		WithDimensions(140, 40),
		WithTerminalPreferences(terminal.Input{Preferences: terminal.Preferences{Unicode: true}}),
	)
	updated, command := model.Update(SnapshotMsg{Snapshot: app.AppSnapshot{
		Revision:  1,
		SessionID: &session,
		ThreadWindow: app.ThreadWindow{Items: []app.ThreadSummary{{
			ID: "thread-1", SessionID: session, Resolution: review.ResolutionOpen,
			Conversation: review.ConversationStreaming, Proposal: review.ProposalNone,
			Anchor: review.AnchorValid, Read: review.Unread,
		}}},
	}})
	model = updated.(*Model)
	if command != nil || model.scheduler.VisibleAnimatedWork() != 1 {
		t.Fatalf("unknown profile visible work = %d command=%v", model.scheduler.VisibleAnimatedWork(), command != nil)
	}
	updated, command = model.Update(tea.ColorProfileMsg{Profile: colorprofile.TrueColor})
	model = updated.(*Model)
	if command == nil || model.scheduler.VisibleAnimatedWork() != 1 {
		t.Fatalf("profile did not admit visible busy work: count=%d command=%v", model.scheduler.VisibleAnimatedWork(), command != nil)
	}
}

func TestTerminalPreferencesCannotBeOverriddenByDetection(t *testing.T) {
	t.Parallel()
	model := NewModel(nil, WithTerminalPreferences(terminal.Input{
		Environment: terminal.Environment{NoColor: true},
		Preferences: terminal.Preferences{Unicode: false, ReducedMotion: true},
	}))
	updated, _ := model.Update(tea.ColorProfileMsg{Profile: colorprofile.TrueColor})
	model = updated.(*Model)
	policy := model.TerminalPolicy().Capability
	if policy.Color != terminal.ColorNone || !policy.ASCII || policy.Motion != terminal.MotionStatic {
		t.Fatalf("disable-only preferences were overridden: %#v", policy)
	}
}
