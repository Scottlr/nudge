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
	"github.com/charmbracelet/x/ansi"
)

// View renders the responsive shell and the bounded child projections owned by
// the repository and code panes. Workflow truth remains in application
// snapshots and the child models emit no adapter calls.
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
				m.panel(regions.Repository, PaneRepository, "Repository", m.repositoryBody()),
				m.panel(regions.Code, PaneCode, "Code / Diff", m.codeBody()),
			),
			lipgloss.JoinHorizontal(lipgloss.Top,
				m.panel(regions.Threads, PaneThreads, "Review threads", m.threadBody()),
				m.panel(regions.Discussion, PaneDiscussion, "Discussion", m.discussionBody()),
			),
		)
	case LayoutMedium:
		lowerPane, lowerTitle, lowerBody := m.lowerPanel()
		body = lipgloss.JoinVertical(lipgloss.Top,
			lipgloss.JoinHorizontal(lipgloss.Top,
				m.panel(regions.Repository, PaneRepository, "Repository", m.repositoryBody()),
				m.panel(regions.Code, PaneCode, "Code / Diff", m.codeBody()),
			),
			m.panel(regions.Lower, lowerPane, lowerTitle, lowerBody),
		)
	case LayoutNarrow:
		body = lipgloss.JoinVertical(lipgloss.Top,
			m.tabs(regions.Tabs),
			m.panel(regions.Main, m.narrowPane, m.narrowTitle(), m.narrowBody()),
		)
	}
	return lipgloss.JoinVertical(lipgloss.Top, body, m.statusBar(regions.Status))
}

func (m *Model) panel(rect Rect, pane Pane, title, body string) string {
	if rect.Empty() {
		return ""
	}
	style, _ := m.theme.StyleFor(theme.RoleBorder)
	panelStyle := lipgloss.NewStyle().Border(lipgloss.NormalBorder())
	if style.Border != "" && style.Border != "inherit" {
		panelStyle = panelStyle.BorderForeground(lipgloss.Color(style.Border))
	}
	focused := pane == m.focus
	if focused {
		focusStyle, ok := m.theme.StyleFor(theme.RoleFocus)
		if ok && focusStyle.Foreground != "" && focusStyle.Foreground != "inherit" {
			panelStyle = panelStyle.BorderForeground(lipgloss.Color(focusStyle.Foreground))
		}
	}
	innerWidth := maxInt(rect.Width-2, 0)
	innerHeight := maxInt(rect.Height-2, 0)
	panelStyle = panelStyle.Width(innerWidth).Height(innerHeight).MaxWidth(innerWidth).MaxHeight(innerHeight)
	lines := strings.Split(m.safeLines(body, maxInt(innerHeight-1, 0)), "\n")
	if innerHeight > 0 {
		lines = append([]string{m.panelHeading(title, focused)}, lines...)
		if len(lines) > innerHeight {
			lines = lines[:innerHeight]
		}
	}
	for index := range lines {
		lines[index] = ansi.Truncate(lines[index], innerWidth, "")
	}
	content := strings.Join(lines, "\n")
	return panelStyle.Render(content)
}

func (m *Model) panelHeading(title string, focused bool) string {
	label := "  " + title
	role := theme.RoleMuted
	if focused {
		label = "> " + title
		role = theme.RoleFocus
	}
	style, ok := m.theme.StyleFor(role)
	if !ok {
		return label
	}
	return style.Lipgloss().Render(label)
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
	return style.Lipgloss().Width(rect.Width).Height(rect.Height).MaxWidth(rect.Width).MaxHeight(rect.Height).Render(ansi.Truncate(strings.Join(labels, " "), rect.Width, ""))
}

func (m *Model) statusBar(rect Rect) string {
	if rect.Empty() {
		return ""
	}
	repositoryName := safeText(m.snapshot.Repository.DisplayName)
	if m.localReview.Repository != nil {
		repositoryName = safeText(m.localReview.Repository.Repository.DisplayName)
	}
	if repositoryName == "" {
		repositoryName = "no repository"
	}
	branch := safeText(m.snapshot.Repository.BranchName)
	if m.localReview.Repository != nil && m.localReview.Repository.Worktree != nil {
		worktree := m.localReview.Repository.Worktree
		branch = safeText(worktree.BranchName)
		if worktree.Detached {
			branch = "detached HEAD"
		}
	}
	if branch == "" {
		branch = "no branch"
	}
	phase := "idle"
	if m.localReview.Phase != "" {
		phase = localPhaseLabel(m.localReview.Phase)
	}
	target := "no target"
	if m.localReview.Target != nil || m.snapshot.Target.Present {
		target = "HEAD -> working tree"
	}
	changed := len(m.localReview.ChangedFiles)
	provider := string(m.snapshot.Provider.Connection)
	if provider == "" {
		provider = "not connected"
	}
	status := fmt.Sprintf("%s | %s | %s | %s | changed %d | focus %s | Codex %s | q quit%s", repositoryName, branch, target, phase, changed, m.focus, provider, m.statusError())
	style, _ := m.theme.StyleFor(theme.RoleMuted)
	return style.Lipgloss().Width(rect.Width).Height(rect.Height).MaxWidth(rect.Width).MaxHeight(rect.Height).Render(ansi.Truncate(safeText(status), rect.Width, ""))
}

func (m *Model) repositoryBody() string {
	if m.localReview.Phase == app.LocalReviewFailed {
		if m.localReview.Error != nil {
			return safeText(m.localReview.Error.Error())
		}
		return "Local review failed"
	}
	if m.localReview.Repository != nil {
		if m.repositoryPane != nil {
			if rendered := m.repositoryPane.View(); rendered != "" {
				return rendered
			}
		}
		entries := len(m.localReview.TreePage.Entries)
		name := safeText(m.localReview.Repository.Repository.DisplayName)
		focus := ""
		if m.localReview.Repository.Worktree != nil {
			focus = safeText(m.localReview.Repository.Worktree.LaunchFocus)
		}
		if focus == "" {
			focus = "."
		}
		lines := []string{name, "focus: " + focus, "state: " + localPhaseLabel(m.localReview.Phase)}
		for index, entry := range m.localReview.TreePage.Entries {
			if index >= 6 {
				break
			}
			lines = append(lines, treeEntryLabel(entry))
		}
		if entries == 0 {
			lines = append(lines, "no changed files")
		}
		return strings.Join(lines, "\n")
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
	if m.codePane != nil {
		if content := m.codePane.Content(); content.Validate() == nil {
			if rendered := m.codePane.View(); rendered != "" {
				return rendered
			}
		}
	}
	if m.localReview.ActiveFile != nil {
		path := changePathForView(*m.localReview.ActiveFile)
		lines := []string{safeText(path), "state: " + localPhaseLabel(m.localReview.Phase)}
		if m.localReview.Displayed != nil && m.localReview.Displayed.Status != app.ContentReady {
			lines[1] = "state: " + displayedContentStatusLabel(*m.localReview.Displayed)
			if reason := safeText(m.localReview.Displayed.Reason); reason != "" {
				lines = append(lines, "reason: "+reason)
			}
			return strings.Join(lines, "\n")
		}
		if m.localReview.FileDiff != nil {
			lines[1] = fmt.Sprintf("%d hunks", len(m.localReview.FileDiff.Hunks))
			for index, hunk := range m.localReview.FileDiff.Hunks {
				if index >= 4 {
					break
				}
				lines = append(lines, safeText(hunk.Header))
			}
			return strings.Join(lines, "\n")
		}
		return strings.Join(lines, "\n")
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

func (m *Model) lowerPanel() (Pane, string, string) {
	if m.lowerPane == PaneDiscussion {
		return PaneDiscussion, "Discussion", m.discussionBody()
	}
	return PaneThreads, "Review threads", m.threadBody()
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
	width := maxInt(m.dimensions.Width, 0)
	return lipgloss.NewStyle().Width(width).MaxWidth(width).MaxHeight(1).Render(ansi.Truncate(safeText(message), width, ""))
}

func localPhaseLabel(phase app.LocalReviewPhase) string {
	switch phase {
	case app.LocalReviewResolvingRepository:
		return "resolving repository"
	case app.LocalReviewCapturing:
		return "capturing local change"
	case app.LocalReviewLoadingTree:
		return "loading changed files"
	case app.LocalReviewLoadingFile:
		return "loading selected diff"
	case app.LocalReviewClean:
		return "clean"
	case app.LocalReviewReady:
		return "ready"
	case app.LocalReviewCancelled:
		return "cancelled"
	case app.LocalReviewFailed:
		return "error"
	default:
		return "starting"
	}
}

func displayedContentStatusLabel(content app.DisplayedContent) string {
	switch content.Status {
	case app.ContentBinary:
		return "binary content"
	case app.ContentUnmerged:
		return "unmerged content"
	case app.ContentLoading:
		return "loading content"
	case app.ContentTooLarge:
		return "content exceeds display limit"
	case app.ContentError:
		return "content unavailable"
	default:
		return "content unavailable"
	}
}

func treeEntryLabel(entry repository.TreeEntry) string {
	path := safeText(string(entry.Path.Bytes()))
	if entry.ChangedSummary == nil {
		return "  " + path
	}
	change := string(entry.ChangedSummary.Kind)
	if entry.ChangedSummary.Conflict != nil {
		change = "conflict"
	}
	return fmt.Sprintf("  %s [%s]", path, safeText(change))
}

func (m *Model) safeLines(value string, maxLines int) string {
	if maxLines <= 0 {
		return ""
	}
	lines := strings.Split(value, "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	return strings.Join(lines, "\n")
}

func safeText(value string) string {
	return presentation.ProjectTerminalText(value, presentation.TerminalTextScalar)
}
