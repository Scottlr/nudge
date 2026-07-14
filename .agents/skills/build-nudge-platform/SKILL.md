---
name: build-nudge-platform
description: "Build Nudge's Go/CLI platform foundation: module composition, layered config, protected native paths, bounded child processes, shared OS locks, trusted executable resolution, query-only doctor, explicit repair authorization/audit, protected-path permission repair, and privacy-safe operational logging. Use for T001, T005-T006, T049, T058, T071, T080, and T092. Route SQLite, owned storage, and release work to their focused skills."
---

# Build Nudge Platform

Implement the shared operational substrate without letting CLI or OS adapters become product, persistence, storage, or release authority.

Primary task family: T001, T005-T006, T049, T058, T071, T080, and T092.

## Workflow

1. Read the applicable sections of `docs/Nudge_PRD_Technical_Design.md`, especially configuration, CLI, security, health/recovery, package layout, and ADRs.
2. Read only the references needed by the task:
   - [process-paths-locks.md](references/process-paths-locks.md) for explicit-argv processes, trusted executables, protected paths, native permissions, cancellation, and shared lock ordering.
   - [cli-config-health.md](references/cli-config-health.md) for composition, layered configuration, safe output, and plain doctor behavior.
   - [logging-and-repair-framework.md](references/logging-and-repair-framework.md) for typed safe logs, T058 plan authorization/audit, and T092 permission repair.
3. Identify the application-owned command or port consuming the platform capability. Keep Cobra, process, path, permission, and file-lock mechanics in adapters/composition roots.
4. Target Go 1.25.0, prefer the standard library, and use the focused dependencies selected by the task directly. Pin tools with Go's `tool` directive rather than installer scripts or wrapper frameworks.
5. Make platform differences explicit through small build-tagged adapters with registered evidence; do not scatter `runtime.GOOS` decisions through domain code.
6. Bound every config input, process frame/stream, log sink, health page, plan payload, and wait/cancellation path using T070 policy.
7. Add only focused tests that protect CLI contracts, executable/path/permission safety, lock behavior, query-only health, repair authorization, or log privacy against a realistic regression.

## Hard guards

- Keep `cmd/nudge` and Cobra handlers thin: parse/validate, compose dependencies, invoke application commands, render inert output, and map typed exit codes.
- Start platform directories from `os.UserConfigDir`/`os.UserCacheDir` plus only the required state/log split. Decode configuration with `github.com/pelletier/go-toml/v2` and `DisallowUnknownFields`; do not add a broad config framework or silently accept misspelled/retired fields.
- Never execute a shell command string. Start one revalidated executable with explicit arguments, controlled environment, bounded streams, cancellation, and process-tree termination.
- Resolve Git/Codex only from an explicit absolute configured path or sanitized `PATH` excluding empty/relative entries and the current directory, repository/worktree, and Nudge workspaces. Retain canonical regular-executable identity and revalidate immediately before spawn.
- Create sensitive files/directories owner-only at the first observable instant with create-new/no-follow and canonical-containment checks; never create permissively and chmod later.
- Follow ADR-012 lock order. PID, time, lock-file existence, or process-list guesses never prove ownership.
- Plain `nudge doctor` is query-only and never migrates, repairs, logs in, starts Codex, or mutates merely to probe. `$integrate-nudge-codex` owns explicit `--live-codex` behavior.
- T058 owns only the immutable plan registry, exact confirmation, revision/precondition revalidation, idempotency contract, and redacted audit. Owner effects remain in T092-T095 and T099-T103; no arbitrary path/SQL mutation or `repair-all` exists.
- T092 may repair only exact supported ownership/permission drift beneath a positively identified protected Nudge root. It never broadens ACLs, changes user-repository paths, or guesses identity.
- Admit default log fields only through the typed T071 safe vocabulary. Source, prompt, patch, provider body, credentials, raw arguments, environment, URLs, and runtime scope never enter default logs.
- Use `log/slog` records, levels, JSON encoding, and handler semantics behind that closed safe-field API. Implement only the narrow protected rotation/writer policy Nudge needs; do not build a general logging framework or test `slog` itself.
- Explicit debug uses a separate owner-only, bounded, time-limited sink and does not relax forbidden data classes. Capacity/sink failure stops that sink and exposes a non-recursive redacted counter.
- `$persist-nudge-state` owns SQLite/WAL/migrations/lease persistence. `$manage-nudge-owned-storage` owns capacity, spools, ledger, export, cleanup, and storage repair. `$qualify-and-release-nudge` owns evidence gates, support, docs, packaging, and publication.
- Do not create or run smoke tests, diagnostic scripts, dry runs, temporary validators, or demo utilities. Add focused behavior tests only when they protect a realistic regression.
