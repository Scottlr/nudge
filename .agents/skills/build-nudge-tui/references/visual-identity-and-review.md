# Visual Identity and Rendered Review

Use this reference for T114-T115 and whenever a task adds or materially changes a visible Nudge TUI surface.

## Visual identity contract

Nudge is calm, precise, and review-oriented. The interface should feel dense enough for serious code work without becoming a wall of boxes, badges, or competing accents.

Built-in themes use this palette structure:

- structure: terminal defaults or low-chroma graphite, charcoal, warm gray, and ivory;
- identity and active focus: muted mulberry or plum, never indigo or blue-violet;
- positive state: sage or forest, not teal;
- destructive or error state: rose or red;
- warning: muted straw or citrine yellow, never amber or orange; and
- informational emphasis: identity accent, underline, weight, label, or border treatment rather than conventional blue.

Nudge-authored built-in UI roles and bundled default syntax styles contain no blue, cyan, teal, orange, amber, or copper. Terminal-default may inherit colors selected by the user's terminal, which Nudge cannot control, and this restriction does not police user-authored themes. Exact color values belong to one named palette-token table in `internal/theme`; choose them only after checking dark, light, terminal-default, downsampled, and high-contrast behavior.

On a normal frame, one identity accent and at most one state accent should dominate. Additional state colors appear only when simultaneous state genuinely requires them. Prefer whitespace, alignment, stable placement, concise labels, border weight, and text contrast over colored containers.

Color never carries meaning alone. Focus, selection, diff kind, thread state, warnings, disabled actions, runtime approval, and proposal approval also need text, glyph, border, placement, or shape distinctions that survive monochrome and ASCII modes.

## Reference posture

Before a substantial review, inspect current official material rather than relying on a frozen recollection:

- [Claude Code terminal configuration and fullscreen behavior](https://code.claude.com/docs/en/terminal-config)
- [GitHub Copilot CLI interactive interface](https://docs.github.com/en/copilot/concepts/agents/copilot-cli/about-copilot-cli)
- [OpenCode TUI](https://opencode.ai/docs/tui/)
- [OpenCode themes](https://opencode.ai/docs/themes/)

Extract qualities, not designs:

- clear primary input or current action;
- readable hierarchy at terminal density;
- compact but legible status and mode information;
- progressive disclosure for secondary detail;
- strong focused/selected state; and
- predictable placement through streaming and resize.

Do not copy a reference product's branding, colors, mascot, splash treatment, exact pane layout, glyph language, or interaction model. Nudge's source of truth remains its product workflow and semantic theme system.

## Preconditions

- Use the production `nudge` executable and production composition.
- Use a disposable, nonsensitive real Git repository to reach honest clean, changed, conflict, long-path, loading, error, thread, approval, or proposal states as the owning features become available.
- Captures contain no credentials, personal paths, private source, raw provider payloads, or sensitive message/prompt content.
- Never add a demo binary, fake data provider, product screenshot mode, diagnostic endpoint, test-only production hook, recorder, or committed fixture application for visual staging.
- Prefer the environment's computer-use or real-screen capture capability. A terminal screenshot is evidence of what was actually displayed, not a renderer-specific synthetic approximation.

## Proportional capture matrix

Do not run a full Cartesian matrix for every small change. Select the rows affected by the task:

| Change | Required review views |
|---|---|
| First integrated shell or pane composition | One representative wide, medium, and narrow geometry; boundary widths implicated by a finding |
| Layout, focus, overlay, or help | Wide, medium, narrow, relevant threshold minus/at/plus one column, and the affected modal/focus state |
| Theme or semantic-role change | Dark, light, terminal-default, high-contrast where implemented, plus affected downsampled mode |
| Glyph, terminal capability, or motion change | Unicode, ASCII, no-color, and reduced-motion/static states that the product can honestly select |
| Thread/discussion change | Empty/loading, active thread, long content, unavailable/error, unread/busy, and relevant responsive layouts |
| Runtime or proposal approval change | Disabled, ready, confirmation, stale/error, and cancellation states; show both approval kinds are unmistakably different |
| Proposal review change | Text and non-text entries, warnings, ready, stale, no-changes, complete-disclosure, and confirmation states |

Each capture is labelled in the review message or handoff with:

- source revision or build identity;
- terminal application and cell geometry;
- theme, color capability, glyph mode, and motion mode;
- exact visible product state; and
- keyboard path used to reach it when that is not obvious.

Screenshots are ephemeral by default. Keep a concise finding/outcome list in the task or pull-request handoff; commit an image only when a separate user-documentation task genuinely needs it.

## Review rubric

Review the captures at actual size and answer these questions:

1. Is the current focus, selected code/thread, primary status, and next safe action immediately clear?
2. Does the layout read in the intended order without excessive borders, dead space, crowding, or decorative noise?
3. Are repository, code, thread, discussion, status, and overlay regions aligned and stable across resize and streaming?
4. Are long paths, long unbroken lines, messages, errors, badges, and help hints clipped or wrapped deliberately rather than accidentally?
5. Can diff kind, state, focus, selection, disabled actions, warnings, runtime approval, and proposal approval be understood without color?
6. Is the palette neutral-dominant, with no built-in blue/orange and no unrelated high-chroma combination competing for attention?
7. Are dark, light, terminal-default, downsampled, monochrome, ASCII, and reduced-motion variants coherent for the modes affected by this task?
8. Does the view preserve Nudge's own identity while meeting the reference products' bar for terminal-native clarity and polish?

Classify findings as:

- **blocking**: unsafe ambiguity, hidden confirmation, unreadable/clipped primary content, lost focus, misleading state, or unusable responsive mode;
- **important**: weak hierarchy, inconsistent rhythm, distracting palette, avoidable density, unclear action prominence, or visible roughness; or
- **optional**: a preference that does not materially harm structure, clarity, accessibility, or identity.

## Feedback loop

1. Launch production Nudge against the prepared disposable repository.
2. Use keyboard interaction and computer use to reach genuine review states and capture the proportional matrix.
3. Feed the labelled images and rubric to Codex or a human reviewer.
4. Map each blocking/important finding to the existing theme, root/layout, or pane owner; never create a parallel visual abstraction.
5. Patch production code, then recapture the affected views and ask the reviewer to confirm the finding is resolved.
6. Finish only when no blocking or important visual finding remains. Preserve optional observations in the handoff without silently expanding the task.

T115 applies the same loop once across the integrated candidate before T053/T074. Its minimum matrix adds repository-wide search, manual anchor reattachment, representative binary/rename/special or unsupported evidence, runtime approval, ready/stale proposal review and confirmation, and colorless ASCII/static operation to representative wide/dark, medium/light, and narrow/terminal-default views. It is an acceptance gate, not permission to introduce a late styling framework; defects return to the existing owner and the affected views are recaptured.

Visual review does not replace behavior tests. Add a focused model/theme/layout test only when a finding exposes a realistic structural regression; do not create aesthetic screenshot goldens or capture automation by default.
