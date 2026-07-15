package tui

import (
	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
	"github.com/Scottlr/nudge/internal/highlight"
	codepane "github.com/Scottlr/nudge/internal/tui/components/code"
	discussionpane "github.com/Scottlr/nudge/internal/tui/components/discussion"
	threadpane "github.com/Scottlr/nudge/internal/tui/components/threads"
	treepane "github.com/Scottlr/nudge/internal/tui/components/tree"
	reattachpane "github.com/Scottlr/nudge/internal/tui/reattach"
)

// syncLocalReviewPanes adopts the immutable pages already carried by the
// local-review snapshot into the bounded child projections. The root owns
// this bridge; the panes remain inert and never query adapters directly.
func (m *Model) syncLocalReviewPanes() {
	if m == nil || m.repositoryPane == nil || m.codePane == nil || m.localReview.Revision == 0 {
		return
	}

	treeIntents := m.repositoryPane.Update(treepane.SnapshotRevisionMsg{Revision: m.localReview.Revision})
	if m.localReview.Target != nil {
		m.repositoryPane.Update(treepane.SetSearchSnapshotMsg{Snapshot: m.localReview.Target.Head})
	}
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
		m.syncThreadMarkers()
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
	m.syncThreadMarkers()
}

func (m *Model) syncReviewSnapshot() {
	if m == nil {
		return
	}
	if m.threadPane != nil {
		m.threadPane.Update(threadpane.SetSnapshotMsg{Snapshot: m.snapshot})
	}
	if m.discussionPane != nil {
		var thread *app.ThreadSummary
		if m.snapshot.ActiveThread != nil {
			copyValue := m.snapshot.ActiveThread.Summary
			thread = &copyValue
		}
		m.discussionPane.Update(discussionpane.SetThreadMsg{Revision: m.snapshot.Revision, Thread: thread})
	}
	m.syncThreadMarkers()
}

func (m *Model) syncThreadMarkers() {
	if m == nil || m.codePane == nil {
		return
	}
	content := m.codePane.Content()
	if content.Validate() != nil {
		return
	}
	items := m.snapshot.ThreadWindow.Items
	if len(items) == 0 {
		items = m.snapshot.Threads
	}
	markers := make([]codepane.ThreadMarker, 0, len(items))
	for _, item := range items {
		side := app.SideHead
		if item.AnchorSide == repository.DiffBase {
			side = app.SideBase
		}
		markers = append(markers, codepane.ThreadMarker{
			ThreadID:  item.ID,
			Path:      item.AnchorPath.Key(),
			Side:      side,
			StartLine: item.AnchorStartLine,
			EndLine:   item.AnchorEndLine,
			Status: review.ThreadStatus{
				Resolution:   item.Resolution,
				Conversation: item.Conversation,
				Proposal:     item.Proposal,
				Anchor:       item.Anchor,
				Read:         item.Read,
				FailurePhase: item.FailurePhase,
				ErrorCode:    item.ErrorCode,
			},
		})
	}
	m.codePane.Update(codepane.SetThreadMarkersMsg{ContentID: content.ID, Markers: markers})
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
		m.repositoryPane.Update(treepane.SetFocusMsg{Focused: m.focus == PaneRepository})
	}
	if m.codePane != nil {
		setSize(m.layout.Regions.Code, m.codePane.SetSize)
	}
	if m.threadPane != nil {
		setSize(m.layout.Regions.Threads, m.threadPane.SetSize)
	}
	if m.discussionPane != nil {
		setSize(m.layout.Regions.Discussion, m.discussionPane.SetSize)
	}
	m.updateChildFocus()
}

func (m *Model) updateChildFocus() {
	if m == nil || m.codePane == nil {
		return
	}
	m.codePane.Update(codepane.SetFocusMsg{Focused: m.focus == PaneCode})
	if m.threadPane != nil {
		m.threadPane.Update(threadpane.SetFocusMsg{Focused: m.focus == PaneThreads})
	}
	if m.discussionPane != nil {
		m.discussionPane.Update(discussionpane.SetFocusMsg{Focused: m.focus == PaneDiscussion})
	}
	if m.reattachPane != nil {
		m.reattachPane.Update(reattachpane.SetSizeMsg{Width: maxInt(m.dimensions.Width-4, 0), Height: maxInt(m.dimensions.Height-4, 0)})
	}
}
