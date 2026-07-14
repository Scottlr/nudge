---
name: persist-nudge-state
description: "Implement and review Nudge durable-state persistence with database/sql and a CGo-free SQLite driver, including embedded checksummed migrations, WAL and bounded keyset/range access, writable-session OS locks and writer-epoch fencing, saved repository base preferences, and exact migration or stale-lease repair. Use for T018-T019, T083, T099-T100, internal/store/sqlite, or persistence-facing application ports. Do not redefine domain aggregates or own artifact-ledger accounting, Git/workspace truth, generic repair, TUI, or release behavior."
---

# Persist Nudge State

Implement durable behavior behind application-owned contracts without making SQLite the product state machine.

## Workflow

1. Read `docs/Nudge_PRD_Technical_Design.md`, `AGENTS.md`, and any accepted ADR governing persistence, lock order, or repair. They remain authoritative.
2. Read only the references needed for the task:
   - [sqlite-store.md](references/sqlite-store.md) for T018, T083, connection policy, transactions, bounded pages/ranges, WAL, and saved base preferences.
   - [session-leases.md](references/session-leases.md) for T019/T100 writable-session ownership, restore/no-persist behavior, OS locks, writer epochs, and stale-lease fencing.
   - [migration-and-repair.md](references/migration-and-repair.md) for T018/T099 migration ownership, checksum rules, recognized interruption recovery, and repair-handler boundaries.
3. Identify the application consumer and domain invariant before changing a schema or query. Keep the interface with that consumer; let `internal/store/sqlite` implement it.
4. Name transaction, revision, fencing, item/byte, cancellation, and WAL consequences before writing SQL.
5. Keep filesystem, process, Git, provider, and SQLite effects in separate phases. Use the owning application journal when an operation crosses those boundaries.
6. Add only focused behavior tests that protect transactions, restoration, fencing, migration compatibility, paging/range integrity, or a realistic repair regression.

## Primary task ownership

- T018: core review-store migrations, transactions, keyset pages, and writer policy.
- T019: durable local-session restore plus explicit process-scoped `--no-persist` behavior.
- T083: replacement-safe saved base-ref preference persistence and optimistic revision.
- T099: exact recovery for registered interrupted migrations.
- T100: exact repair of an OS-lock-proven stale session lease and its fencing state.

T018 owns the shared connection/WAL/keyset/range policy. T021/T031/T034/T038/T047 add aggregate-specific bounded queries, bodies, publications, and journals with the behavior that consumes them.

## Hard guards

- Use `database/sql` with the selected CGo-free SQLite driver. Enable foreign keys on every connection and use the documented bounded pool, effective-writer, busy, WAL, and checkpoint policies.
- The application reducer owns canonical workflow truth. Store rows, triggers, and adapter callbacks do not synthesize domain transitions or bypass application commands/events.
- Keep migrations embedded, additive, checksummed, immutable after application, and ordered by aggregate owner. Unknown or changed applied migrations fail closed.
- Bind pages to stable keyset cursors and revisions, and bind immutable body/patch reads to exact ID, length, and hash. Never put complete large history or BLOBs into canonical snapshots.
- SQLite serialization is not cross-process ownership. A writable durable session requires the designated OS lock plus the current durable lease, writer epoch/fencing token, and expected revision.
- PID, hostname, timestamps, lock-file existence, or process-list observations never prove a writer is dead and never authorize lease repair.
- `--no-persist` creates no review/provider/message/snapshot/preference rows and acquires no durable session lease.
- T067 owns the artifact/reservation ledger and storage totals. Do not add artifact accounting, capacity reconciliation, or an `internal/capacity` database adapter here.
- T058 owns repair authorization and audit. This skill owns only the T099 migration and T100 session-lease effects; it never accepts arbitrary SQL, paths, lease rows, or a repair-all request.
- Do not add smoke tests, diagnostic scripts, dry runs, temporary validators, demo databases, or broad test scaffolding. Use focused package or cross-process behavior tests only when they protect a realistic contract.
