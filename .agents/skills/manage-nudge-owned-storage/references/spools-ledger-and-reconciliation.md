# Spools, ledger, and reconciliation

Use this reference for T066-T067, T079, and every artifact owner that streams or retains data.

## Streaming spool and adoption

- Require a live T065 reservation before creating a spool.
- Resolve protected roots from owner IDs; never accept an arbitrary absolute destination.
- Create parents/files create-new, owner-only, no-follow, canonically contained, and marker/nonce bound at the first observable instant.
- Stream through fixed buffers while checking byte, entry, depth, and manifest limits plus cancellation at bounded intervals. Compute hashes and checked counts incrementally.
- `CloseAndVerify` must fsync where supported, close all handles, reopen no-follow, and independently verify type, length, hash, and complete owner manifest. Writer success is not verification.
- Publish only a closed verified candidate through a proven same-volume atomic no-replace primitive. Revalidate expected destination absence/identity and fsync the parent where supported.
- Directory spools require no-follow descendant enumeration, complete bounded manifests, and proof that writers/handles are quiescent. Never trust a provider-writable tree merely because it is under an owned root.
- Generic replacement is forbidden. An owner that genuinely replaces state needs its own journal and preconditions.
- Abort/recover only the exact marker-bound spool after acquiring the owner lock and checking nonce, containment, reservation, phase, and active handles. Ambiguity preserves evidence.

## Durable ledger

T067 owns the SQLite source of truth for active reservations, accepted artifacts, conservative repository/global totals, pressure, and uncertainty. Keep persistence under `internal/store/sqlite`; `internal/capacity` never opens the database.

Ledger rows bind stable owner, artifact, operation, reservation, repository, class, volume, manifest hash, lifecycle state, logical bytes, observed bytes, accounting version, and policy version. Labels never derive from content.

Convert a matching reservation into one or more independently verified accepted-artifact entries and update totals in one fenced transaction. Idempotency keys prevent duplicate publication, double counting, or double release. Unknown compression/dedupe/reflink behavior does not reduce conservative charging.

Use checked projected totals for repository/global soft and hard budgets. Soft pressure blocks optional retained growth. Hard crossing or accounting uncertainty blocks publication while preserving existing review, export, query-only doctor, exact repair, and confirmed cleanup. Pressure never evicts accepted history.

Expose bounded revisioned ledger snapshots without filesystem I/O. Pre-ledger/legacy identities begin `accounting_uncertain`; do not infer rows from names or directories.

## Owner integration and conformance

Every heavy owner—capture, review snapshot, workspace/baseline/result, proposal patch/BLOB/WAL, cache, log, export, cleanup, and retained history—must map its first implementation to:

- one T065 peak plan/reservation phase;
- one T066 spool/verification/adoption path where files are built;
- one T067 artifact class and finalization transition;
- exact cancellation/restart journal phases; and
- pressure/health behavior.

Fix duplicate free-space, temp, deletion, or accounting logic at the owning package. Do not create an integration manager or test-owned production truth. T074 may exercise these production paths as part of its bounded release workload.

## Bounded reconciliation and health

T079 compares the ledger with owner evidence; it does not replace either source of truth.

- Owner inspectors accept stable ledger artifact IDs and return only containment, marker/nonce/manifest identity, observed bytes, lifecycle/lease state, and typed uncertainty.
- A bounded filesystem-only candidate pass may enumerate only configured Nudge-owned roots and read a small fixed-format marker. Unknown entries remain untouched; filenames and familiar directory shapes prove nothing.
- Persist reconciliation epochs/cursors/discrepancy summaries keyed by scope, ledger revision, policy version, and stable last owner/artifact ID. Advance in T070 count/byte batches and restart idempotently.
- Query-only health reads one consistent ledger snapshot plus bounded evidence and never advances a cursor, changes totals, releases a reservation, moves a file, or creates a repair.
- Classify missing artifact, missing ledger entry, manifest/size mismatch, stale reservation, owned temporary residue, and ownership uncertainty explicitly. Exact matches may refresh existing observed evidence; discrepancies are not silently rewritten.
- Emit a T058 plan candidate only when exact current evidence selects T095 ledger/reservation correction, T101 derived-artifact rebuild, or T102 same-volume residue quarantine. T079 executes none of those effects.
- Bind plans to health/ledger revisions, reconciliation epoch, stable IDs, marker/manifest evidence, locks, effect, and postcondition. Concurrent drift makes the plan stale.

## Focused tests

Protect bounded streaming/cancellation, independent verification, no-replace races, lifecycle failpoints, ledger idempotency/fencing/totals, owner conformance, epoch/cursor restart, query-only immutability, and exact plan classification.
