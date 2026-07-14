package code

import (
	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/highlight"
)

type codeRow struct {
	Evidence  app.DisplayedRow
	BaseSpans []highlight.StyledSpan
	HeadSpans []highlight.StyledSpan
}

type pageState struct {
	Request    PageRequest
	Rows       []codeRow
	NextCursor string
}

func newCodeRow(row app.DisplayedRow) codeRow {
	return codeRow{Evidence: cloneDisplayedRow(row)}
}

func cloneDisplayedRow(row app.DisplayedRow) app.DisplayedRow {
	page := app.DisplayedContentPage{ContentID: row.ID.Content, Rows: []app.DisplayedRow{row}}
	return page.Clone().Rows[0]
}

func (row codeRow) spans(side app.RowSide) []highlight.StyledSpan {
	if side == app.SideBase {
		return row.BaseSpans
	}
	return row.HeadSpans
}

func cloneSpans(spans []highlight.StyledSpan) []highlight.StyledSpan {
	return append([]highlight.StyledSpan(nil), spans...)
}
