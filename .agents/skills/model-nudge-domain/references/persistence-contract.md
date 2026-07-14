# Application persistence contract

Use this reference to define durable semantics and application-owned store ports. `$persist-nudge-state` owns SQLite schemas, migrations, queries, WAL, and lease implementation; `$manage-nudge-owned-storage` owns artifact/reservation accounting.

`ProposalIntent` becomes immutable only after pre-persistence validation: summary at most 64 KiB UTF-8; expected raw paths deduplicated by `RepoPathKey`, raw-key sorted, at most 5,000 entries/16 KiB each/512 KiB total. Over-limit drafts create no lineage, workspace, or provider side effect and are never truncated or display-normalized into persisted identity.

## Durable ownership

- Keep repository, worktree, review session, target spec/generation/capture, thread, anchor version, normalized message, Nudge-local provider conversation/turn, opaque provider refs, immutable review snapshot, proposal intent/workspace/version, destination preconditions, and apply operation as distinct identities.
- Treat normalized Nudge messages as restoration truth; provider history is supplementary. Persist accepted streaming deltas as ordered immutable chunks and freeze terminal length/hash/status before a body is exportable or canonical.
- Keep `ProviderConversationID`/`ProviderTurnID` strong and separate from nullable opaque `ProviderConversationRef`/`ProviderTurnRef`.
- Persist a confirmed `ProposalIntent` before the external proposal turn. Confirmation-generation provenance stays immutable across refresh; each proposal version records its own source generation and separate destination applicability.
- Ready proposal versions and their complete binary-capable patch identities are immutable. Do not store display strings as raw path/content identity.

## Consumer-owned store ports

- Define ports around application use cases and aggregate transactions, not generic table/CRUD access. Inputs/outputs use strong IDs, enums, revisions, typed cursors, and immutable body references.
- Require compare-and-swap expected revisions for canonical mutations and writer lease/epoch fencing for durable session writes. A stale writer returns a typed conflict with no partial state.
- Define revision-bound keyset pages capped by whole-item count and encoded bytes. Large bodies use exact ID/length/hash-bound sequential or range reads; unfinished bodies never appear complete.
- Name idempotency/correlation keys for every externally retryable operation. Duplicate commands converge on the same domain result rather than creating duplicate conversations, versions, journals, or events.
- Keep aggregate ownership additive: each later provider/snapshot/proposal/apply/storage/repair task adds only the persistence contract its aggregate requires. T018 does not speculate future tables.

## Cross-boundary journals

For an operation that crosses durable state and filesystem/process effects, the application owns a journal contract with exact intent, preconditions, effect identity, verification, terminal outcome, and repair-required evidence:

1. persist intent before the external side effect;
2. revalidate immediately before mutation;
3. perform one exact authorized effect under the owning lock/lease;
4. verify the postcondition independently;
5. persist the terminal domain event/revision;
6. on restart, classify unapplied, applied, or indeterminate without blind retry/rollback.

The adapter persists this contract but does not invent transitions from filesystem resemblance or SQL row shape.

## Persistence modes

`--no-persist` uses a fresh process-unique session identity, writes no review/provider/message/snapshot/preference state, and acquires no durable session lease. It supports review and process-lifetime read-only discussion through in-memory IDs and an owned immutable snapshot, but no provider-writable proposal workspace, proposal review/apply, transcript restoration, or claimed remote-create recovery.

## Boundaries

- Load `$persist-nudge-state` for `database/sql`, SQLite driver/connection policy, migrations, keyset/range implementation, WAL, saved preferences, durable session leases, and migration/lease repair.
- Load `$manage-nudge-owned-storage` for capacity reservations, spools, artifact ledger/accounting, reconciliation, export, cleanup, and storage repair.
- Load `$manage-nudge-git-proposals` for capture/snapshot/workspace/apply filesystem truth. Store ports retain its identities but never recompute them.
