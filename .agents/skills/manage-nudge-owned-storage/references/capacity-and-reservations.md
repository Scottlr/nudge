# Capacity plans and cross-process reservations

Use this reference for T065 and every heavy artifact owner. Capacity integration lands with the owner’s first implementation rather than a later cross-owner retrofit.

## Ownership

- Let `internal/app` own the capacity request/reservation port.
- Keep only checked arithmetic and value policy in `internal/capacity`; it performs no filesystem or SQLite I/O.
- Put volume identity, free-space evidence, protected marker creation, and platform quota/preallocation evidence behind small platform adapters.
- Give every heavy operation one stable operation ID, optional repository ID, policy version, affected artifact classes, and owning lifecycle journal.
- Do not permit owner-local free-space formulas or process-local reservations.

## Checked per-volume peak

Build a `CapacityPlan` before the first heavy temporary, provider lease, output file, or other filesystem mutation. For each physical volume, include:

- concurrently retained inputs;
- build/copy temporaries and interrupted residue;
- independent verification reads/copies;
- final artifact bytes;
- conservative full copy-on-write, reflink, sparse, compression, or deduplication exposure unless a stronger primitive is proven;
- database BLOB/index cost and WAL growth;
- atomic-output overlap;
- active reservations from other Nudge processes;
- repository/global retained delta; and
- the protected post-peak reserve.

Use checked unsigned addition and multiplication. Overflow, unknown volume identity, unknown charge, or incomplete plan fails before mutation and can only increase conservatism.

T070 currently requires at least 2 GiB observed free after the projected peak, including the protected 256 MiB emergency recovery file. Runtime configuration may lower work maxima or increase reserves, never raise maxima or lower the safety reserve without a new policy version.

Map canonical owner-controlled source, spool, database, and destination paths to physical volumes independently. Cross-volume work receives one peak and reservation amount per volume. Never call a reservation hard unless an enforceable OS quota/preallocation primitive and its platform evidence actually guarantee it; otherwise report monitored-only behavior and recheck.

## Lock order

Ordinary work enters with its session writer when applicable, then acquires:

1. global capacity-reservation lock;
2. repository capacity lock; and
3. stable owner locks such as worktree, apply, workspace, spool, export, or log locks.

Repository maintenance or cleanup acquires:

1. repository maintenance gate;
2. all relevant session-writer locks in stable ID order;
3. global capacity-reservation lock;
4. repository capacity lock; and
5. remaining owner locks in their documented stable order.

Never request the maintenance gate while holding capacity or owner locks. SQLite writer serialization does not replace these OS locks. A timeout, PID, or timestamp never proves an owner dead.

## Durable reservation lifecycle

- While holding capacity locks, compare observed free bytes, T067 projected retained totals, protected active markers, requested peaks, and reserves.
- Create-new and fsync a private owner-only/no-follow reservation marker before releasing the locks or starting mutation.
- Store IDs, nonce, volume byte classes, policy version, phase, and timestamps only. Never store paths, source/provider bytes, prompts, credentials, or raw arguments.
- Bind the marker to the owning operation journal and OS-lock evidence. Unknown/corrupt marker versions remain charged and block conflicting work.
- Recheck long work at bounded byte/time checkpoints. Low reserve or ENOSPC enters the owner's journalled cancellation/emergency-recovery path and never publishes ready state.
- Release only the matching reservation after the owner has finalized accepted artifacts or cleaned/reconciled its temporaries. Crash recovery first classifies the owner journal and lock; it never steals by age.

## Focused tests

Protect checked exact/overflow arithmetic, multiple volumes, concurrent process exclusion, lock order, crash-marker classification, low-space rechecks, hard-versus-monitored evidence, and redacted markers. Use reduced meaningful limits inside focused tests. Do not create a disk scanner, stress/load program, smoke test, or diagnostic command.
