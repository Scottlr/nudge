# State and message flow

Consult sections 9.4, 13.1-13.2, and Appendix D of the technical design.

## Root model

The root owns the latest immutable `AppSnapshot`, `Pane` focus, `LayoutMode`, dimensions, child models, overlay stack, one animation frame, keymap, and theme. It implements Bubble Tea v2's declarative `View() tea.View` contract and puts cursor/terminal modes on that returned view. It does not own a second copy of review sessions, threads, messages, or proposals.

## Flow

1. Bubble Tea delivers input, resize, tick, or application-event messages.
2. The focused component updates UI-only state and emits zero or more typed UI intents.
3. The root handles local focus/layout/overlay concerns and translates product intents into application commands.
4. Async work occurs behind the application client.
5. A new snapshot revision or paged response returns to the root.
6. Components derive views and indexes from that revision.

One T084 command metadata registry owns keyboard IDs/bindings/help. A separate run-scoped handler registry makes only implemented feature handlers available; T032 registers distinct runtime grant/deny handlers and T039 registers proposal review/approve/reject handlers. Unavailable metadata never dispatches, and components never retain raw-key escape paths.

Target-bearing focus/selection uses shared typed intents keyed by `FocusTargetID`, `RepoPathKey`, `DisplayRowID`, or `ThreadID`. Keyboard translation owns the v1 input path; do not add a string target payload, dormant mouse branch, or input-specific application command.

Keep high-frequency message deltas from rebuilding unrelated tree and diff indexes.

Repository hierarchy pages and repository-wide search pages have distinct cursor identities but share one immutable snapshot revision. Search input emits a cancellable application intent over the whole tree; it never scans only expanded/loaded component rows. Explicit large-content views likewise consume immutable identity-bound range/segment pages and retain the last valid page while a newer request loads or cancels.

## Synchronization

- Store `ThreadID`, file path, side, and anchor position as synchronization keys.
- Selecting a thread activates its discussion, file, and anchor through one root intent.
- Selecting a marker activates the same thread object as the thread-list row.
- Preserve active selection across wide, medium, and narrow layouts.
- Define explicit marker/badge precedence when resolved, orphaned, unread, failed, and proposal states coexist; do not collapse the underlying state axes.
- Derive a v1 title from the first nonblank comment line when no explicit title editing flow exists.
