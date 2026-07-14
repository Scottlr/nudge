---
name: manage-nudge-owned-storage
description: "Implement or review Nudge owned-storage capacity planning, cross-process reservations, bounded artifact spooling and adoption, durable ledger/accounting, reconciliation and capacity health, human-readable export, repository cleanup, and focused storage repair, rebuild, or quarantine. Use for work under internal/capacity, owned-storage ports in internal/app, storage adapters in internal/store/sqlite, artifact-owner lifecycle integration, or T059-T060, T065-T067, T079, T095, T101, and T102 behavior. Do not use to redefine Git, proposal, workspace, or release truth."
---

# Manage Nudge Owned Storage

Keep every heavy artifact attributable, reserved, bounded, independently verified, transactionally accounted, and recoverable without guessing ownership.

## Workflow

1. Read the relevant sections of `docs/Nudge_PRD_Technical_Design.md`, accepted ADRs, and the exact owning task. Treat T059-T060, T065-T067, T079, T095, T101, and T102 as the primary decomposition.
2. Read only the reference needed for the change:
   - [capacity-and-reservations.md](references/capacity-and-reservations.md) for checked per-volume peak plans, reserves, reservation markers, lock order, and pressure behavior.
   - [spools-ledger-and-reconciliation.md](references/spools-ledger-and-reconciliation.md) for bounded spools, no-replace adoption, durable ledger truth, owner integration, reconciliation epochs, and query-only health.
   - [export-cleanup-and-storage-repair.md](references/export-cleanup-and-storage-repair.md) for human-readable Markdown export, exact repository cleanup, ledger/reservation correction, derived-artifact rebuild, and evidence-preserving quarantine.
3. Name the application-owned port, artifact owner, authoritative source identity, owner marker/nonce, artifact class, lifecycle journal, volume set, and ledger transition before editing an adapter.
4. Classify the object explicitly as canonical retained truth, owner-declared derived data, active temporary/spool state, or positively owned residue. Never infer the class from a path or filename.
5. For heavy work, define the checked per-volume peak, acquire the T065 reservation in ADR-012 order, stream through T066, verify independently, adopt no-replace, and settle T067 accounting exactly once.
6. For SQLite/filesystem mutations, persist intent before the external effect, define restart classification for every phase, and keep slow streaming outside SQLite transactions.
7. Let T079 compare ledger and owner evidence in bounded epochs. Execute a discrepancy only through the exact T058-authorized owner handler: T095, T101, or T102.
8. Add only focused behavior tests for arithmetic, locking, lifecycle phases, ownership, atomicity, accounting, or realistic races.

## Hard Guards

- Keep interfaces with application consumers. Keep pure checked arithmetic in `internal/capacity`, SQLite in `internal/store/sqlite`, and artifact-specific truth with its owner.
- Follow ADR-012 lock order exactly. Never acquire the repository maintenance gate while holding capacity or owner locks, and never treat PID or elapsed time as ownership proof.
- Count input, temporary, verification, final, copy-on-write worst case, database/WAL, atomic output, concurrent reservations, and protected reserve on every affected volume.
- Create owner-only/no-follow state at the first observable instant. Stream with bounded buffers and checked counters; publish only after close and independent verification through a proven same-volume no-replace primitive.
- Treat the T067 ledger as durable accounting truth and T079 as bounded evidence reconciliation. Neither filenames nor an unbounded filesystem walk may create ownership truth.
- Never scan, adopt, move, quarantine, or delete an arbitrary path. Require stable owner/artifact IDs, canonical containment, marker/nonce, manifest/native evidence, current revisions, the owning lock, and an exact postcondition.
- Storage pressure blocks optional growth. It never authorizes automatic deletion of accepted history or weakens review, export, query-only doctor, exact repair, or confirmed cleanup access.
- Do not reinterpret repository captures, immutable snapshots, proposal baselines/results, apply journals, or provider events. Consume their owner-declared identities and stop when authoritative evidence is incomplete.
- Never mutate the user repository, worktree, index, refs, Git config, credentials, remote provider state, or user-selected export directories outside the exact output operation.
- T058 owns authorization, registry, confirmation, and audit only. Do not add repair-all, arbitrary SQL/path mutation, or an owner-neutral repair handler.
- Do not change support matrices, performance/security gates, packaging, publication, or other release policy. Each heavy owner integrates T065-T067 in its first implementation; T074 may exercise those production paths without creating a second storage-conformance subsystem.
- Do not add or run smoke tests, diagnostic scripts, dry runs, temporary validators, demo builders, scanners, profilers, load generators, or broad test churn. A shipped read-only cleanup preview is product behavior, not development validation tooling.
