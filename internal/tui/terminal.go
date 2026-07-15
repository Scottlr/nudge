package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/Scottlr/nudge/internal/terminal"
	"github.com/Scottlr/nudge/internal/theme"
	"github.com/charmbracelet/colorprofile"
)

// TerminalPolicy is the TUI bridge between neutral terminal policy and the
// semantic theme plus root scheduler.
type TerminalPolicy struct {
	Capability terminal.Policy
	Theme      theme.RenderPolicy
}

// WithTerminalPreferences installs disable-only preferences and starts the
// model conservatively until Bubble Tea delivers its profile message.
func WithTerminalPreferences(input terminal.Input) ModelOption {
	return func(model *Model) {
		model.terminalInput = input
		_ = model.applyTerminalProfile(input.Profile)
	}
}

// TerminalPolicy returns the current presentation policy without workflow
// state or raw terminal evidence.
func (m *Model) TerminalPolicy() TerminalPolicy {
	if m == nil {
		return TerminalPolicy{}
	}
	return TerminalPolicy{Capability: m.terminalPolicy.Clone(), Theme: m.theme.Policy}
}

func (m *Model) applyTerminalProfile(profile colorprofile.Profile) TickPlan {
	if m == nil {
		return TickPlan{}
	}
	m.terminalInput.Profile = profile
	capability := terminal.Resolve(m.terminalInput)
	m.terminalPolicy = capability
	m.applyTheme(m.theme.WithPolicy(theme.RenderPolicy{
		Color:    capability.Color != terminal.ColorNone,
		ASCII:    capability.ASCII,
		Explicit: true,
	}))
	if m.scheduler != nil {
		return m.scheduler.SetReducedMotion(capability.Motion != terminal.MotionAnimated)
	}
	return TickPlan{}
}

func (m *Model) applyColorProfile(message tea.ColorProfileMsg) TickPlan {
	return m.applyTerminalProfile(message.Profile)
}

func (m *Model) syncVisibleAnimatedWork() TickPlan {
	if m == nil || m.scheduler == nil {
		return TickPlan{}
	}
	count := 0
	if m.threadPaneVisible() && m.threadPane != nil {
		count = m.threadPane.VisibleAnimatedWork()
	}
	return m.scheduler.SetVisibleAnimatedWork(count)
}

func (m *Model) threadPaneVisible() bool {
	if m == nil {
		return false
	}
	switch m.layout.Mode {
	case LayoutWide:
		return true
	case LayoutMedium:
		return m.lowerPane == PaneThreads
	case LayoutNarrow:
		return m.narrowPane == PaneThreads
	default:
		return false
	}
}

func mergeTickPlans(first, second TickPlan) TickPlan {
	if first.Command != nil {
		return first
	}
	return second
}
