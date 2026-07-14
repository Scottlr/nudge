# Processes, paths, permissions, and locks

## Child processes

- Resolve a configured Git/Codex executable only when it is absolute, canonical, and a regular platform-executable file. Otherwise search a sanitized `PATH` with empty/relative entries removed and reject candidates beneath the current directory, repository/worktree, or any Nudge workspace/root. Record native identity and revalidate path, type, containment, and identity immediately before every spawn to close replacement races; never fall back to a repository shadow binary.
- Use a safe runner that receives an executable and explicit argument vector. Never route Git, Codex, or operational commands through a shell string.
- Keep the T006 lifecycles distinct: `Run` is a small bounded finite command, `RunStream` is a large finite streaming command, and `Start` is the managed-duplex app-server process. Bound stdout, stderr, frames, queues, cumulative content, and one-shot output independently.
- Supply a controlled environment, working directory, locale, pager/editor/interactive settings, timeout/cancellation, correlation ID, and OS-specific process-tree containment. On cancellation, terminate the contained tree and wait for handle/descendant quiescence before treating writable results as derivable.
- Preserve typed exit, timeout, cancellation, output-limit, spawn, and protocol errors. Redact before terminal/log projection.

## Native paths and protected creation

- Resolve config/state/cache/log roots once with XDG on Linux, Application Support/Caches on macOS, and roaming/local app-data separation on Windows. Pass resolved locations through constructors.
- Sensitive parents, files, markers, locks, logs, SQLite files, spools, snapshots, and workspaces must be private from creation using owner-only/no-follow primitives. Canonical containment plus a matching persisted marker/nonce proves ownership; a directory name or parent alone does not.
- Reject symlink, junction, reparse-point, mount, short-name, normalization, case-fold, and hard-link alias escapes according to the operation's capability policy. Recursive deletion accepts a stable owned ID, never an arbitrary user path.
- Keep raw repository paths out of generic platform path helpers; `RepoPath` semantics belong to the Git skill/domain.
- T088's `internal/paths` executor is the one exception: it consumes an already parsed raw identity plus a verified root and owns held-handle native qualification/effect. Generic config/data path helpers must not duplicate that repository leaf-operation seam.

## Global lock order

- Session creation/writer claim briefly holds repository maintenance gate, then the session OS lock; the actor keeps that session lock for its writable lifetime.
- Ordinary heavy work: already-held session writer -> global capacity-reservation lock -> repository capacity lock -> stable-ordered destination/workspace/log owner locks.
- Maintenance/repair/cleanup: repository maintenance gate -> all affected session locks in stable ID order -> global capacity -> repository capacity -> stable-ordered owner locks.
- Record the order in ADR-012. Never request the repository gate while holding a capacity/owner lock; never steal a lock from PID, age, or timeout.
- `$manage-nudge-owned-storage` owns reservation semantics. Platform locks merely implement its accepted global/repository lock primitives; a reservation is persisted before the first heavy temporary/provider workspace lease and released only through its owning operation/journal after completion or reconciled cancellation.
