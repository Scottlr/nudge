package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/presentation"
	"github.com/Scottlr/nudge/internal/theme"
)

// View renders only the shell and truthful snapshot summaries. Concrete pane
// projections are added by their owning tasks.
func (m *Model) View() tea.View {
	if m == nil {
		return tea.NewView("")
	}
	view := tea.NewView(m.render())
	view.AltScreen = m.altScreen
	view.MouseMode = tea.MouseModeNone
	view.ReportFocus = m.reportFocus
	view.WindowTitle = "Nudge"
	return view
}

func (m *Model) render() string {
	if m.layout.Mode == LayoutUnknown {
		return "Waiting for terminal size"
	}
	if m.layout.Mode == LayoutTooSmall {
		return m.renderTooSmall()
	}
	regions := m.layout.Regions
	var body string
	switch m.layout.Mode {
	case LayoutWide:
		body = lipgloss.JoinVertical(lipgloss.Top,
			lipgloss.JoinHorizontal(lipgloss.Top,
				m.panel(regions.Repository, "Repository", m.repositoryBody()),
				m.panel(regions.Code, "Code / Diff", m.codeBody()),
			),
			lipgloss.JoinHorizontal(lipgloss.Top,
				m.panel(regions.Threads, "Review threads", m.threadBody()),
				m.panel(regions.Discussion, "Discussion", m.discussionBody()),
			),
		)
	case LayoutMedium:
		lowerTitle, lowerBody := m.lowerPanel()
		body = lipgloss.JoinVertical(lipgloss.Top,
			lipgloss.JoinHorizontal(lipgloss.Top,
				m.panel(regions.Repository, "Repository", m.repositoryBody()),
				m.panel(regions.Code, "Code / Diff", m.codeBody()),
			),
			m.panel(regions.Lower, lowerTitle, lowerBody),
		)
	case LayoutNarrow:
		body = lipgloss.JoinVertical(lipgloss.Top,
			m.tabs(regions.Tabs),
			m.panel(regions.Main, m.narrowTitle(), m.narrowBody()),
		)
	}
	return lipgloss.JoinVertical(lipgloss.Top, body, m.statusBar(regions.Status))
}

func (m *Model) panel(rect Rect, title, body string) string {
	if rect.Empty() {
		return ""
	}
	style, _ := m.theme.StyleFor(theme.RoleBorder)
	panelStyle := style.Lipgloss().Border(lipgloss.NormalBorder())
	if style.Border != "" && style.Border != "inherit" {
		panelStyle = panelStyle.BorderForeground(lipgloss.Color(style.Border))
	}
	innerWidth := maxInt(rect.Width-2, 0)
	innerHeight := maxInt(rect.Height-2, 0)
	panelStyle = panelStyle.Width(innerWidth).Height(innerHeight).MaxWidth(innerWidth).MaxHeight(innerHeight)
	content := m.safeLines(title+"\n"+body, innerHeight)
	return panelStyle.Render(content)
}

func (m *Model) tabs(rect Rect) string {
	if rect.Empty() {
		return ""
	}
	labels := []string{"Files", "Diff", "Threads", "Discussion"}
	active := map[Pane]string{PaneRepository: "Files", PaneCode: "Diff", PaneThreads: "Threads", PaneDiscussion: "Discussion"}[m.narrowPane]
	for i, label := range labels {
		if label == active {
			labels[i] = "[" + label + "]"
		}
	}
	style, _ := m.theme.StyleFor(theme.RoleFocus)
	return style.Lipgloss().Width(rect.Width).Height(rect.Height).MaxWidth(rect.Width).MaxHeight(rect.Height).Render(strings.Join(labels, " "))
}

func (m *Model) statusBar(rect Rect) string {
	if rect.Empty() {
		return ""
	}
	branch := safeText(m.snapshot.Repository.BranchName)
	if branch == "" && m.localReview.Repository != nil && m.localReview.Repository.Worktree != nil {
		branch = safeText(m.localReview.Repository.Worktree.BranchName)
	}
	if branch == "" {
		branch = "no repository"
	}
	phase := string(m.localReview.Phase)
	if phase == "" {
		phase = "idle"
	}
	changed := len(m.localReview.ChangedFiles)
	status := fmt.Sprintf("%s | %s | changed %d | focus %s | q quit%s", branch, phase, changed, m.focus, m.statusError())
	style, _ := m.theme.StyleFor(theme.RoleMuted)
	return style.Lipgloss().Width(rect.Width).Height(rect.Height).MaxWidth(rect.Width).MaxHeight(rect.Height).Render(safeText(status))
}

func (m *Model) repositoryBody() string {
	if m.localReview.Phase == app.LocalReviewFailed {
		if m.localReview.Error != nil {
			return safeText(m.localReview.Error.Error())
		}
		return "Local review failed"
	}
	if m.localReview.Repository != nil {
		entries := len(m.localReview.TreePage.Entries)
		name := safeText(m.localReview.Repository.Repository.DisplayName)
		focus := safeText(m.localReview.Repository.Worktree.LaunchFocus)
		if focus == "" {
			focus = "."
		}
		return fmt.Sprintf("%s\nworktree focus: %s\n%d changed-tree entries", name, focus, entries)
	}
	if m.snapshot.Repository.ID == "" {
		return "No repository selected"
	}
	if len(m.snapshot.Tree.Entries) == 0 {
		return "Repository loaded; no tree page is present"
	}
	return fmt.Sprintf("%d tree entries in the current projection", len(m.snapshot.Tree.Entries))
}

func (m *Model) codeBody() string {
	if m.localReview.ActiveFile != nil {
		path := changePathForView(*m.localReview.ActiveFile)
		if m.localReview.FileDiff != nil {
			return fmt.Sprintf("%s\n%d hunks | %s", safeText(path), len(m.localReview.FileDiff.Hunks), string(m.localReview.Phase))
		}
		return safeText(path) + "\nloading diff and content"
	}
	if m.snapshot.ActiveFile == nil {
		return "No file selected"
	}
	return "Selected file: " + safeText(string(m.snapshot.ActiveFile.Path.Bytes()))
}

func changePathForView(file repository.ChangedFile) string {
	if file.NewPath != nil {
		return string(file.NewPath.Bytes())
	}
	if file.OldPath != nil {
		return string(file.OldPath.Bytes())
	}
	return "unknown path"
}

func (m *Model) threadBody() string {
	if len(m.snapshot.Threads) == 0 {
		return "No review threads in the current snapshot"
	}
	return fmt.Sprintf("%d review threads in the current projection", len(m.snapshot.Threads))
}

func (m *Model) discussionBody() string {
	if m.snapshot.ActiveThread == nil {
		return "No review thread selected"
	}
	return fmt.Sprintf("Active review thread has %d messages", m.snapshot.ActiveThread.MessageCount)
}

func (m *Model) lowerPanel() (string, string) {
	if m.lowerPane == PaneDiscussion {
		return "Discussion", m.discussionBody()
	}
	return "Review threads", m.threadBody()
}

func (m *Model) narrowTitle() string {
	switch m.narrowPane {
	case PaneCode:
		return "Code / Diff"
	case PaneThreads:
		return "Review threads"
	case PaneDiscussion:
		return "Discussion"
	default:
		return "Repository"
	}
}

func (m *Model) narrowBody() string {
	switch m.narrowPane {
	case PaneCode:
		return m.codeBody()
	case PaneThreads:
		return m.threadBody()
	case PaneDiscussion:
		return m.discussionBody()
	default:
		return m.repositoryBody()
	}
}

func (m *Model) renderTooSmall() string {
	message := fmt.Sprintf("Terminal too small for Nudge (%d x %d); resize to at least %d x %d", m.dimensions.Width, m.dimensions.Height, narrowMinWidth, narrowMinHeight)
	return lipgloss.NewStyle().Width(maxInt(m.dimensions.Width, 0)).MaxWidth(maxInt(m.dimensions.Width, 0)).MaxHeight(1).Render(safeText(message))
}

func (m *Model) safeLines(value string, maxLines int) string {
	if maxLines <= 0 {
		return ""
	}
	value = presentation.ProjectTerminalText(value, presentation.TerminalTextMultiline)
	lines := strings.Split(value, "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	return strings.Join(lines, "\n")
}

func safeText(value string) string {
	return presentation.ProjectTerminalText(value, presentation.TerminalTextScalar)
}
