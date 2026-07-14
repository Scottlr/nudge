package tui

import (
	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/highlight"
	codepane "github.com/Scottlr/nudge/internal/tui/components/code"
	treepane "github.com/Scottlr/nudge/internal/tui/components/tree"
)

// syncLocalReviewPanes adopts the immutable pages already carried by the
// local-review snapshot into the bounded child projections. The root owns
// this bridge; the panes remain inert and never query adapters directly.
func (m *Model) syncLocalReviewPanes() {
	if m == nil || m.repositoryPane == nil || m.codePane == nil || m.localReview.Revision == 0 {
		return
	}

	treeIntents := m.repositoryPane.Update(treepane.SnapshotRevisionMsg{Revision: m.localReview.Revision})
	if m.localReview.TreePage.Validate() == nil {
		for _, intent := range treeIntents {
			if intent.PageRequest == nil {
				continue
			}
			m.repositoryPane.Update(treepane.PageResultMsg{Result: treepane.PageResult{
				Request: *intent.PageRequest,
				Page:    m.localReview.TreePage,
			}})
			break
		}
	}

	if m.localReview.Displayed == nil || m.localReview.Displayed.Validate() != nil {
		m.codePane.Update(codepane.SnapshotContentMsg{})
		return
	}
	codeIntents := m.codePane.Update(codepane.SnapshotContentMsg{Content: *m.localReview.Displayed})
	if m.localReview.DisplayedPage != nil && m.localReview.DisplayedPage.Validate() == nil {
		for _, intent := range codeIntents {
			if intent.PageRequest == nil {
				continue
			}
			m.codePane.Update(codepane.PageResultMsg{Result: codepane.PageResult{
				Request: *intent.PageRequest,
				Page:    *m.localReview.DisplayedPage,
			}})
			break
		}
		m.syncLocalHighlights()
	}
}

func (m *Model) syncLocalHighlights() {
	highlighted := m.localReview.Highlighted
	page := m.localReview.DisplayedPage
	if highlighted == nil || page == nil {
		return
	}
	for _, row := range page.Rows {
		if row.BaseLine != nil {
			m.codePane.Update(codepane.SetHighlightMsg{
				RowID: row.ID,
				Side:  app.SideBase,
				Spans: highlightedLine(highlighted, *row.BaseLine),
			})
		}
		if row.HeadLine != nil {
			m.codePane.Update(codepane.SetHighlightMsg{
				RowID: row.ID,
				Side:  app.SideHead,
				Spans: highlightedLine(highlighted, *row.HeadLine),
			})
		}
	}
}

func highlightedLine(file *highlight.HighlightedFile, line int) []highlight.StyledSpan {
	if file == nil || line <= 0 || line > len(file.Lines) {
		return nil
	}
	return file.Lines[line-1]
}

func (m *Model) resizeChildPanes() {
	if m == nil {
		return
	}
	setSize := func(rect Rect, set func(int, int)) {
		set(maxInt(rect.Width-2, 0), maxInt(rect.Height-3, 0))
	}
	if m.repositoryPane != nil {
		setSize(m.layout.Regions.Repository, m.repositoryPane.SetSize)
	}
	if m.codePane != nil {
		setSize(m.layout.Regions.Code, m.codePane.SetSize)
	}
	m.updateChildFocus()
}

func (m *Model) updateChildFocus() {
	if m == nil || m.codePane == nil {
		return
	}
	m.codePane.Update(codepane.SetFocusMsg{Focused: m.focus == PaneCode})
}
