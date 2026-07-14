# CLI, configuration, safe output, and health

## Composition and configuration

- `cmd/nudge` contains only the thin executable entrypoint and injected build/version variables. `internal/cli` owns Cobra parsing/help, configuration overlays, dependency composition, typed outcome rendering, and exit-code mapping.
- Preserve exact target exclusivity and shipped flag names. Parse usage errors before application startup and do not initialize adapters a command does not need.
- Configuration precedence is defaults < protected config file < environment < CLI. Retain per-field source metadata, apply T070 input limits, reject invalid overlays atomically, and never store Codex credentials.
- Mouse support is deferred from v1. Do not reserve a dormant `UI.Mouse` setting or `--no-mouse` flag; add configuration only with the future behavior task that consumes it.
- Keep platform paths centralized and typed. Resolve owner-only config/data/cache/runtime roots without trusting the current directory, repository content, environment-relative segments, or link/reparse traversal.

## Terminal-safe CLI output

- Route repository/provider/path/ref/error text and Git/Codex stderr summaries through the frontend-neutral inert-display projection before terminal output.
- Escape C0/C1 controls, ESC/CSI/OSC, bidi overrides, invalid UTF-8, and unsafe newline/width content while retaining raw identity separately. JSON/export use their own encoders.
- Use product language from `AGENTS.md`; do not let Cobra or error strings reintroduce provider-centric actions.

## Doctor contract

- Plain `nudge doctor` opens available durable state read-only/query-only and performs no migration, permission fix, artifact rewrite, repair execution, provider connection, login, or mutating capability probe.
- Report stable typed findings, bounded evidence, current health revision, and redacted remediation. Provider/account state is `not_checked` unless last-known timestamped evidence exists.
- `doctor --live-codex` is an explicit provider action owned by `$integrate-nudge-codex`; `doctor --repair` is an explicit T058 framework action and never changes the semantics of plain doctor.
- Default commands never stage, commit, push, fetch, create branches/PRs, upload state, emit telemetry, or mutate merely to inspect configuration/health.
