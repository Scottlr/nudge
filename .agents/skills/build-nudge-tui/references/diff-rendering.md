# Diff and source rendering

Consult sections 4.2, 4.4, 12.4, and 13.3-13.4 of the technical design.

## Logical rows

Represent file headers, hunk headers, context, additions, deletions, collapsed context, binary summaries, and large-file notices as typed rows. Each code row carries an app-owned immutable content/row ID, raw base/head `RepoPath` identity, old/new line identity, explicit side, hunk ID, source spans, and attached thread IDs.

- Compose only the viewport's visible rows.
- Keep line numbers, gutter markers, diff indicators, and source spans as separate cells.
- Horizontal scrolling applies to measured source cells without corrupting styling.
- Full-file comments are allowed only where a row maps to a valid anchorable target-side location; do not invent anchors for unrelated unchanged lines without a design change.
- A selected range stays on one diff side and cannot cross a hunk header.

## Highlighting

- Select Chroma lexer by filename, then content fallback.
- Tokenize the whole file so multiline constructs remain correct.
- Map tokens to per-line `StyledSpan` values.
- Cache by content hash, lexer, and syntax theme.
- Fall back to plain text above the configured threshold.

## Style order

Compose syntax foreground/emphasis, diff styling, selected range, active anchor, search match, then cursor/focus. Semantic theme roles decide actual terminal colors and attributes.

## Proposal view

Use a dedicated proposal review mode that reuses structured diff rows but reads only the persisted immutable proposal artifact (patch bytes, manifests, summaries, preconditions, capability evidence), never a live result workspace. Show every affected entry/hunk, proposal version/hash/state, scope warnings, capability reason, and stale reason before enabling approval. Provider messages are not a substitute for this view.

- Keep gitlinks, unknown/unsupported kinds, binary summaries, and raw-byte paths visible with inert escaped evidence even when proposal/apply is disabled.
- Keep unmerged stage evidence visibly distinct and inert. Do not flatten it into staged/unstaged or expose anchor/proposal/apply actions.
- T054's effective decision controls actions, not visibility. A disabled/limit entry can never disappear or be presented as a complete approvable patch.
- T039 establishes complete proposal-review semantics and bounded paging/range rendering in the first implementation, consuming T073 windowing and application/store-owned immutable queries. Over-budget versions remain non-approvable until every entry is reachable; "complete review" never means a near-limit patch must be resident.
- Render a terminal `no_changes` attempt as `No proposed changes` with discussion/new-request actions only—no version, reject, approval, or apply control.
