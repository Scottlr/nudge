package code

import "github.com/Scottlr/nudge/internal/app"

func selectableSide(row codeRow, requested, fallback app.RowSide) (app.RowSide, SelectionRejectedReason) {
	if !row.Evidence.Selectable {
		return app.SideNone, SelectionNotSelectable
	}
	if requested == app.SideNone {
		requested = fallback
		if requested == app.SideNone && row.Evidence.Side != app.SideBoth {
			requested = row.Evidence.Side
		}
	}
	switch row.Evidence.Side {
	case app.SideBase, app.SideHead:
		if requested != row.Evidence.Side {
			return app.SideNone, SelectionSideMismatch
		}
	case app.SideBoth:
		if requested != app.SideBase && requested != app.SideHead {
			return app.SideNone, SelectionSideRequired
		}
	default:
		return app.SideNone, SelectionNotSelectable
	}
	return requested, ""
}

func selectionForRows(content app.DisplayedContentID, start, end codeRow, side app.RowSide) (RangeSelection, SelectionRejectedReason) {
	if !start.Evidence.ID.Matches(content) || !end.Evidence.ID.Matches(content) {
		return RangeSelection{}, SelectionDifferentContent
	}
	if !start.Evidence.Selectable || !end.Evidence.Selectable {
		return RangeSelection{}, SelectionNotSelectable
	}
	if start.Evidence.Side == app.SideBoth && side != app.SideBase && side != app.SideHead || end.Evidence.Side == app.SideBoth && side != app.SideBase && side != app.SideHead {
		return RangeSelection{}, SelectionSideRequired
	}
	if start.Evidence.Side != app.SideBoth && start.Evidence.Side != side || end.Evidence.Side != app.SideBoth && end.Evidence.Side != side {
		return RangeSelection{}, SelectionSideMismatch
	}
	if start.Evidence.HunkID != end.Evidence.HunkID {
		return RangeSelection{}, SelectionDifferentHunk
	}
	result := RangeSelection{ContentID: content, Start: start.Evidence.ID, End: end.Evidence.ID, Side: side, HunkID: start.Evidence.HunkID}
	if result.Start.Ordinal > result.End.Ordinal {
		result.Start, result.End = result.End, result.Start
	}
	return result, ""
}
