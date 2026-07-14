# Terminal accessibility and input

Consult sections 4.3-4.7, 12.5, 13.5-13.7, and 16.1/16.4 of the technical design.

## Layout and width

- Wide: four synchronized panes.
- Medium: two upper columns plus one toggled lower panel.
- Narrow: one pane at a time with a persistent mode/tab bar.
- Measure terminal cell width with combining marks, wide characters, emoji, and tabs handled consistently.
- Expand tabs consistently, then use `github.com/charmbracelet/x/ansi` for width, cut, wrap, and truncation. Do not mix width libraries or measure styled bytes manually.
- Sanitize control characters and terminal escape sequences in every untrusted string.

## Accessibility

- Ship dark, light, and terminal-default semantic themes.
- Support TOML user themes, true/256/basic color degradation, `NO_COLOR`, monochrome, ASCII markers/borders, and reduced motion.
- Never encode meaning in color or animation alone.
- Keep complete keyboard operation and visible focus.
- Use one global animation tick at about 4-6 fps only while a visible thread is busy.
- V1 is keyboard-first and ships no mouse config or CLI flag. A future mouse task must reuse the same typed focus/selection/activation intents and preserve complete keyboard parity rather than branching product behavior.

## Input

- Preserve multiline text and paste safely.
- Wrap `charm.land/bubbles/v2/textarea` for multiline editing and add only Nudge's explicit-send, byte-limit, cancellation, and draft-retention policy; do not implement a second text editor.
- Do not send on accidental bare Enter when multiline editing is active.
- Show send/cancel hints and the active anchor above the editor.
- Retain unsent input across temporary modal interruptions.
- Require an unambiguous visible proposal and confirmation before application.
- `Codex runtime approval` and `Approve proposal` are separate commands, state, confirmation copy, key bindings, and semantic styles. Never reuse “approve” context so one looks like the other.
- Exact runtime command/network/tool scope may be shown transiently in the active approval overlay, but it must not be copied into durable transcript/history/default logs; durable UI reload uses redacted/hash fields only.
- Runtime grant-once/deny and proposal approve/reject use distinct T084 command IDs, contexts, registered handlers, and typed application intents even when mutually exclusive contexts share a physical key.

## Responsiveness

- Lazy-load trees, files, diffs, and transcript pages.
- Do not retokenize while scrolling.
- Coalesce provider deltas before redraw.
- Keep cancellation visible even when process shutdown lags.
- Avoid per-thread tickers and full projection rebuilds on every token.
- T073 owns the shared immutable window/cache/render scheduler; focused pane tasks consume it rather than adding local timers/caches.
- T074's release workload declares exact fixtures, terminal geometry, cold/warm state, timer boundaries, deterministic retained-work limits, sample/percentile policy, reference runner, and source/policy/tool identity. A fast local benchmark alone is not a support claim.
