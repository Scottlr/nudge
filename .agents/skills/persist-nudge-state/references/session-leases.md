# Durable sessions, OS locks, and writer fencing

Use this reference for T019 and T100. The OS-backed session lock proves cooperative process ownership; the durable writer epoch/fencing token proves whether a command may mutate persisted session state. Both are required.

## Session identity and restore

- Derive the durable session key from the stable repository/worktree/target identity defined by the application contract. Do not key restoration by display path, current directory, branch label, or provider conversation.
- Restore only when repository binding, target intent/generation compatibility, schema/policy versions, and persistence mode match. Surface replacement, ambiguity, or incompatible state instead of attaching old history to new repository truth.
- Persist normalized session state independently of provider history. Restoring a session does not automatically connect Codex, start a turn, migrate a store, or refresh a Git target.
- `--no-persist` uses a fresh process-unique session identity and in-memory IDs. It writes no durable review/provider/message/snapshot/preference state, claims no durable restore, and acquires no durable session lease.

## Writable-session lease contract

- Use the designated protected OS lock key and platform lock primitive from the accepted lock-order contract. Lock-file presence, PID, hostname, start time, or heartbeat age is not ownership proof.
- Never wait for an OS lock while holding a SQLite transaction. Acquire repository/session gates and the OS lock in the established global order, then use a short store transaction.
- Bind the durable lease to repository ID, worktree ID, session ID, lock identity, lease revision, opaque lease ID, writer epoch/fencing token, expected session revision, and lifecycle state.
- Every session-scoped mutation compares the current lease ID, writer epoch/token, and expected session revision in the same transaction that writes state and advances the revision. A stale writer fails without side effects.
- Renewal and release use compare-and-swap. Process exit releases the OS lock by handle lifecycle, but durable state remains explicit and auditable.
- Do not treat SQLite's single writer or busy handling as a replacement for the OS lease. They serialize database access but do not establish which Nudge process owns the session workflow.
- Delayed provider/process results carry their original operation and fencing identity. A later writer epoch rejects them even when their payload otherwise appears valid.

## Restore and failure behavior

- On normal open, acquire the exact OS lock before claiming writable ownership, then create or advance the durable writer epoch under compare-and-swap.
- A busy or unprovable lock yields a read-only/busy typed result according to the product contract. Do not steal, delete, or rename a lock file.
- A database, repository binding, lease, or session revision change between observation and acquisition invalidates the candidate and requires a fresh open.
- Crash recovery must converge so that a previous fencing token cannot authorize new writes. Do not erase the only evidence needed to reject delayed work.

## Stale-lease repair

- T049 may expose a T100 repair candidate only when durable evidence is complete enough to attempt the authoritative OS-lock check. Age or a missing process observation is only diagnostic context.
- T058 supplies an exact plan and confirmation. The plan binds repository/worktree/session identities, lock key/identity, health revision, lease revision, writer epoch/token, expected session revision, and exact terminal/fencing postcondition.
- Execute by acquiring the exact designated OS lock non-blockingly, then re-reading the lease through the normal lease manager. Any held/unsupported/ambiguous lock, renewal, replacement, or revision/epoch/token drift refuses mutation.
- Terminalize the prior lease and advance or retain the fence through one compare-and-swap lease-manager operation. Do not simply delete the row when that would erase fencing or audit evidence.
- Hold the OS lock until the durable transition commits and the old token is proven unable to authorize a mutation. Release it through normal structured cleanup.
- Duplicate execution is idempotent only for the same terminalized epoch/token and evidence. It must never affect a newer legitimate lease owner or auto-open a replacement writer.

## Focused tests

Use focused cross-process/platform lock contention, acquire/renew/release, restore compatibility, revision CAS, delayed old-writer rejection, crash/retry, no-persist, and stale-lease repair races. Do not add PID scanners, process killers, lease repair scripts, dry runs, smoke commands, or direct-SQL mutation utilities.
