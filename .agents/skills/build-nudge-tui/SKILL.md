---
name: build-nudge-tui
description: "Implement and review Nudge's Bubble Tea v2 terminal projections, snapshot bridge, bounded repository/code/thread/discussion/proposal viewports, semantic themes, highlighting, responsive layouts, keymaps, focus, reduced motion, accessibility, and production rendered-review loop. Use for T012-T015, T022, T025, T039, T073, T084-T085, and T114-T115. Never move query or workflow truth into components."
---

# Build Nudge TUI

Build a keyboard-first review surface whose panes remain synchronized projections of one application snapshot.

Primary task family: T012-T015, T022, T025, T039, T073, T084-T085, and T114-T115. Each pane is bounded in its first implementation; there is no later virtualization retrofit.

## Workflow

1. Read sections 3-5, 12.2-12.5, 13, and 16.1/16.4 of `docs/Nudge_PRD_Technical_Design.md`.
2. Read only the references needed by the task:
   - [state-and-message-flow.md](references/state-and-message-flow.md) for root ownership, component intents, snapshots, focus, and responsive layout.
   - [diff-rendering.md](references/diff-rendering.md) for logical rows, source spans, style composition, selection, navigation, and caching.
   - [terminal-accessibility.md](references/terminal-accessibility.md) for semantic themes, cell width, sanitization, colorless/ASCII/reduced-motion modes, input, and performance.
   - [visual-identity-and-review.md](references/visual-identity-and-review.md) for T114-T115, Nudge's restrained no-blue/no-orange-family built-in identity, current reference-product posture, proportional production capture matrix, visual rubric, privacy rules, and computer-use feedback loop.
3. Use the pinned stable imports directly: `charm.land/bubbletea/v2`, `charm.land/bubbles/v2`, `charm.land/lipgloss/v2`, `github.com/alecthomas/chroma/v2`, and `github.com/charmbracelet/x/ansi` only where their owning task calls for them. Do not wrap them in a generic UI framework.
4. Identify the application snapshot fields and command intents the component needs. Extend frontend-neutral projections rather than importing an adapter.
5. Store only frontend state such as focus, dimensions, selection, filter text, scroll offsets, editor buffers, overlays, and animation frame in the TUI model.
6. Keep child components deterministic: accept projection plus UI message, return updated component plus intents. Let the root translate product intents into application commands.
7. Compose only visible logical rows from app-owned immutable content/row IDs into terminal strings and sanitize untrusted text before styling.
8. Preserve active thread ID, file, anchor, and selection across layout changes.
9. Treat hierarchy pages, snapshot-wide search results, and explicit large-file ranges as separate application-owned projections; never search only loaded nodes or assemble a giant line/file in the model.
10. Add model/update or focused snapshot tests only when they protect interaction semantics or a realistic regression.
11. For T114-T115 or any task that changes a visible surface, run the applicable production render-review loop from `visual-identity-and-review.md`; use honest reachable state, fix blocking/important findings at their owner, and recapture without creating a demo or screenshot harness.

## Hard guards

- Bubble Tea is the UI loop, not the domain architecture.
- Implement Bubble Tea v2's declarative root contract exactly: `Init() tea.Cmd`, `Update(tea.Msg) (tea.Model, tea.Cmd)`, and `View() tea.View` using `tea.NewView(content)`. Cursor and terminal modes belong on the returned view, not imperative renderer side channels.
- Components never call Git, provider, workspace, or store adapters.
- Provider availability, runtime approval, proposal status, repository pages, and action eligibility arrive as neutral application projections/intents. This skill owns their presentation and input routing only; the corresponding domain, Codex, Git, storage, and persistence skills own their meaning.
- Components hold stable IDs and projections, not authoritative domain entity copies.
- Proposal review renders the persisted immutable proposal artifact, never live workspace content; unsupported entries remain visible with action-gating reasons.
- Runtime approval and proposal approval use distinct commands, confirmations, state, labels, and styles.
- Use unified diff as the v1 default; do not introduce split diff or source editing.
- Measure terminal cells, not bytes or runes. Keep gutters and line numbers separate from source spans.
- Use `x/ansi` consistently after tab expansion for width, cut, wrap, and truncation; do not introduce a second width library or measure styled byte strings manually.
- Use semantic roles, never embedded literal colors in components.
- Nudge-authored built-in UI and bundled default syntax values are neutral-dominant and contain no blue, cyan, teal, orange, amber, or copper. Terminal-default may inherit host colors; user-authored themes are not subject to the shipped-palette restriction.
- Keep ordinary screens visually restrained: one muted mulberry/plum identity accent plus at most one necessary sage/forest, rose/red, or muted straw/citrine state accent should dominate; use structure rather than unrelated high-chroma combinations.
- Keep keyboard-only, monochrome, ASCII, and reduced-motion operation complete.
- T084 owns command metadata/keymap/help plus the run-scoped handler registry. Runtime and proposal approvals use distinct IDs/contexts/handlers, unavailable commands cannot dispatch, and components keep no raw-key branch.
- T084 stores physical bindings as Bubbles v2 `key.Binding` and matches `tea.KeyPressMsg`; do not collapse v2 key code/text/modifiers into a home-grown keystroke type. T020/T022 wrap Bubbles v2 `textarea` rather than building an editor.
- Focus and selection use shared typed intents and every workflow is keyboard-complete. Mouse support is deferred from v1; do not add dormant config, flags, reporting, or partial handlers.
- Use one application-level animation tick only while visible work is active.
- Never render repository, branch, path, provider, or stderr control sequences as trusted terminal escapes.
- Full-tree fuzzy search delegates to the bounded application query. Explicit over-threshold content remains immutable-range-bound and pathological lines render as labelled bounded continuation segments.
- T073 owns the shared scheduler/window/cache budget. T014/T081 repository/search, T015/T072 code, T022 thread/discussion, T039 proposal, and T013/T084/T085 layout/overlay work consume it directly; they do not create local timers, cache policy, cross-pane mega-projections, or a later "make it bounded" pass.
- Keep owner-local render benchmarks separate from T074 release evidence. Release workload claims require exact fixture, geometry, cold/warm state, timer boundary, deterministic row/cell/byte/allocation bounds, reference-runner percentile policy, and provenance identity.
- Visual acceptance uses labelled captures of the production TUI in a real terminal through computer use or equivalent screen capture. Reference Claude Code, GitHub Copilot CLI, and OpenCode for quality attributes only; never copy their branding, palette, or exact layout, and never replace the loop with aesthetic screenshot churn.
