package tui

import "strings"

// FocusTargetID is a stable identity for a disposable keyboard focus target.
type FocusTargetID string

const (
	FocusTargetRepository FocusTargetID = "pane:repository"
	FocusTargetCode       FocusTargetID = "pane:code"
	FocusTargetThreads    FocusTargetID = "pane:threads"
	FocusTargetDiscussion FocusTargetID = "pane:discussion"
)

// FocusTarget carries visibility and enablement evidence alongside identity.
type FocusTarget struct {
	ID      FocusTargetID
	Pane    Pane
	Visible bool
	Enabled bool
}

// FocusRing is an ordered, layout-specific set of navigable targets.
type FocusRing []FocusTarget

// BuildFocusRing derives the keyboard ring from the current responsive shell.
// Narrow mode exposes the persistent tab targets; only one pane body is
// visible, but every tab is an enabled visible navigation target.
func BuildFocusRing(layout Layout, lowerPane, narrowPane Pane) FocusRing {
	add := func(result FocusRing, pane Pane, visible bool) FocusRing {
		return append(result, FocusTarget{ID: focusTargetID(pane), Pane: pane, Visible: visible, Enabled: isPane(pane)})
	}
	var result FocusRing
	switch layout.Mode {
	case LayoutWide:
		for _, pane := range []Pane{PaneRepository, PaneCode, PaneThreads, PaneDiscussion} {
			result = add(result, pane, !focusRect(layout, pane).Empty())
		}
	case LayoutMedium:
		result = add(result, PaneRepository, !layout.Regions.Repository.Empty())
		result = add(result, PaneCode, !layout.Regions.Code.Empty())
		if lowerPane != PaneThreads && lowerPane != PaneDiscussion {
			lowerPane = PaneThreads
		}
		result = add(result, lowerPane, !layout.Regions.Lower.Empty())
	case LayoutNarrow:
		for _, pane := range []Pane{PaneRepository, PaneCode, PaneThreads, PaneDiscussion} {
			result = add(result, pane, !layout.Regions.Tabs.Empty())
		}
		if narrowPane != PaneRepository && narrowPane != PaneCode && narrowPane != PaneThreads && narrowPane != PaneDiscussion {
			return result
		}
	case LayoutTooSmall, LayoutUnknown:
		return nil
	}
	return result
}

// Next returns the next enabled visible target with deterministic wraparound.
func (r FocusRing) Next(current FocusTargetID, direction int) (FocusTarget, bool) {
	if direction == 0 {
		return FocusTarget{}, false
	}
	step := 1
	if direction < 0 {
		step = -1
	}
	start := -1
	for index, target := range r {
		if target.ID == current {
			start = index
			break
		}
	}
	if start < 0 {
		if step < 0 {
			start = 0
		} else {
			start = len(r) - 1
		}
	}
	for count := 0; count < len(r); count++ {
		index := (start + step*(count+1)) % len(r)
		if index < 0 {
			index += len(r)
		}
		target := r[index]
		if target.Visible && target.Enabled {
			return target, true
		}
	}
	return FocusTarget{}, false
}

// ForPane returns the visible enabled target for a pane.
func (r FocusRing) ForPane(pane Pane) (FocusTarget, bool) {
	for _, target := range r {
		if target.Pane == pane && target.Visible && target.Enabled {
			return target, true
		}
	}
	return FocusTarget{}, false
}

func focusTargetID(pane Pane) FocusTargetID {
	return FocusTargetID("pane:" + string(pane))
}

func focusRect(layout Layout, pane Pane) Rect {
	switch pane {
	case PaneRepository:
		return layout.Regions.Repository
	case PaneCode:
		return layout.Regions.Code
	case PaneThreads:
		return layout.Regions.Threads
	case PaneDiscussion:
		return layout.Regions.Discussion
	default:
		return Rect{}
	}
}

func overlayTargetID(id string) FocusTargetID {
	return FocusTargetID("overlay:" + strings.TrimSpace(id))
}

func (m *Model) focusRing() FocusRing {
	if m == nil {
		return nil
	}
	return BuildFocusRing(m.layout, m.lowerPane, m.narrowPane)
}

func (m *Model) currentFocusTarget() FocusTargetID {
	if m == nil {
		return ""
	}
	if m.overlays.Len() > 0 {
		if overlay, ok := m.overlays.Top(); ok {
			return overlayTargetID(overlay.ID)
		}
	}
	if m.focusTarget != "" {
		return m.focusTarget
	}
	return focusTargetID(m.focus)
}

func (m *Model) normalizeFocus() {
	if m == nil || m.layout.Mode == LayoutUnknown || m.layout.Mode == LayoutTooSmall {
		if m != nil {
			m.focusTarget = ""
		}
		return
	}
	ring := m.focusRing()
	if target, ok := ring.ForPane(m.focus); ok {
		m.focusTarget = target.ID
		return
	}
	if target, ok := ring.Next("", 1); ok {
		m.focus = target.Pane
		m.focusTarget = target.ID
	}
}

func (m *Model) showOverlay(overlay Overlay) {
	if m == nil {
		return
	}
	if m.overlays.Len() == 0 {
		previous := m.currentFocusTarget()
		m.focusRestore = &previous
	}
	if !m.overlays.Push(overlay) {
		return
	}
	m.focusTarget = overlayTargetID(overlay.ID)
}

func (m *Model) dismissOverlay() {
	if m == nil {
		return
	}
	if _, ok := m.overlays.Pop(); !ok {
		return
	}
	if m.overlays.Len() > 0 {
		if overlay, ok := m.overlays.Top(); ok {
			m.focusTarget = overlayTargetID(overlay.ID)
		}
		return
	}
	var previous FocusTargetID
	if m.focusRestore != nil {
		previous = *m.focusRestore
	}
	m.focusRestore = nil
	for _, target := range m.focusRing() {
		if target.ID == previous && target.Visible && target.Enabled {
			m.focus = target.Pane
			m.focusTarget = target.ID
			m.updateChildFocus()
			return
		}
	}
	m.normalizeFocus()
	m.updateChildFocus()
}
