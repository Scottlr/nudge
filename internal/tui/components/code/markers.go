package code

import (
	"fmt"
	"sort"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
	"github.com/Scottlr/nudge/internal/theme"
)

// ThreadMarker is the stable projection needed to place one review thread on
// a code row. The full independent status axes remain available to precedence
// and accessibility logic; the glyph is only a visual projection.
type ThreadMarker struct {
	ThreadID  domain.ReviewThreadID
	Path      repository.RepoPathKey
	Side      app.RowSide
	StartLine int
	EndLine   int
	Status    review.ThreadStatus
}

// SetThreadMarkersMsg replaces markers for one immutable displayed-content
// identity. Markers from another content revision are discarded.
type SetThreadMarkersMsg struct {
	ContentID app.DisplayedContentID
	Markers   []ThreadMarker
}

type markerKey struct {
	Path repository.RepoPathKey
	Side app.RowSide
	Line int
}

type markerGroup struct {
	Items []ThreadMarker
}

func (m *Model) setThreadMarkers(message SetThreadMarkersMsg) {
	if m == nil || message.ContentID != m.content.ID {
		return
	}
	markers := make(map[markerKey]markerGroup)
	for _, marker := range message.Markers {
		if marker.ThreadID == "" || marker.Path == "" || (marker.Side != app.SideBase && marker.Side != app.SideHead) || marker.StartLine <= 0 || marker.EndLine < marker.StartLine {
			continue
		}
		if marker.Status.Resolution.Validate() != nil || marker.Status.Conversation.Validate() != nil || marker.Status.Proposal.Validate() != nil || marker.Status.Anchor.Validate() != nil || marker.Status.Read.Validate() != nil {
			continue
		}
		for line := marker.StartLine; line <= marker.EndLine; line++ {
			key := markerKey{Path: marker.Path, Side: marker.Side, Line: line}
			group := markers[key]
			group.Items = append(group.Items, marker)
			markers[key] = group
		}
	}
	for key, group := range markers {
		sort.Slice(group.Items, func(i, j int) bool { return group.Items[i].ThreadID < group.Items[j].ThreadID })
		markers[key] = group
	}
	m.markers = markers
}

func (m *Model) markerForRow(row codeRow, side app.RowSide) (string, theme.Role) {
	if m == nil || (side != app.SideBase && side != app.SideHead) {
		return "", theme.RoleForeground
	}
	line := rowLine(row, side)
	if line <= 0 {
		return "", theme.RoleForeground
	}
	path := m.content.BasePath
	if side == app.SideHead {
		path = m.content.HeadPath
	}
	if path == nil {
		return "", theme.RoleForeground
	}
	group, ok := m.markers[markerKey{Path: path.Key(), Side: side, Line: line}]
	if !ok || len(group.Items) == 0 {
		return "", theme.RoleForeground
	}
	start := false
	for _, item := range group.Items {
		if item.StartLine == line {
			start = true
			break
		}
	}
	if !start {
		return "│", markerRole(group.Items)
	}
	glyph := markerGlyph(group.Items)
	if len(group.Items) > 1 {
		glyph = fmt.Sprintf("%s%d", glyph, len(group.Items))
	}
	return glyph, markerRole(group.Items)
}

func markerGlyph(items []ThreadMarker) string {
	for _, item := range items {
		switch {
		case item.Status.FailurePhase != "" || item.Status.ErrorCode != "" || item.Status.Conversation == review.ConversationFailed || item.Status.Proposal == review.ProposalFailed:
			return "!"
		case item.Status.Anchor == review.AnchorOrphaned || item.Status.Anchor == review.AnchorAmbiguous:
			return "?"
		case item.Status.Proposal == review.ProposalReady || item.Status.Proposal == review.ProposalStale || item.Status.Proposal == review.ProposalApplying:
			return "p"
		case item.Status.Conversation != review.ConversationIdle:
			return "~"
		}
	}
	for _, item := range items {
		if item.Status.Resolution == review.ResolutionResolved {
			return "x"
		}
	}
	return "o"
}

func markerRole(items []ThreadMarker) theme.Role {
	for _, item := range items {
		switch {
		case item.Status.FailurePhase != "" || item.Status.ErrorCode != "" || item.Status.Conversation == review.ConversationFailed || item.Status.Proposal == review.ProposalFailed:
			return theme.RoleThreadError
		case item.Status.Anchor == review.AnchorOrphaned || item.Status.Anchor == review.AnchorAmbiguous:
			return theme.RoleThreadOrphaned
		case item.Status.Proposal != review.ProposalNone:
			return theme.RoleThreadProposal
		case item.Status.Conversation != review.ConversationIdle:
			return theme.RoleThreadBusy
		}
	}
	for _, item := range items {
		if item.Status.Resolution == review.ResolutionResolved {
			return theme.RoleThreadResolved
		}
	}
	return theme.RoleThreadOpen
}
