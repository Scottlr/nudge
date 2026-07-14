# SQLite store and bounded history

Use this reference for T018, T083, and SQLite-facing portions of other owner tasks. Domain entities and transitions remain owned by `internal/domain` and `internal/app`; bounded paging/ranges, WAL, and cancellation land with the first store behavior that needs them rather than a later hardening pass.

## Adapter boundary

- Keep persistence interfaces with the application consumer. `internal/store/sqlite` translates strong IDs, enums, timestamps, revisions, and typed failures without exposing SQL rows as domain authority.
- Use the platform-provided protected database path and identity. Do not discover alternate databases from the working directory, repository, environment fallbacks, or user-supplied repair paths.
- Use `database/sql` and the selected CGo-free driver. Keep driver-specific setup and errors inside the adapter.
- Store timestamps in UTC and use the canonical ID/enum encodings fixed by the owning schema. Preserve opaque provider references as separate nullable values rather than collapsing them into Nudge-local IDs.

## Connection and transaction policy

- Enable foreign keys for every connection, not just the migration connection.
- Use a bounded connection pool with one effective writer, context-aware busy handling, bounded operation-class deadlines, WAL, and an explicit passive/restart checkpoint policy.
- Keep write transactions short. Never hold a SQLite transaction while waiting for an OS lock, Git/provider process, filesystem copy/hash, user confirmation, or long range stream.
- Use compare-and-swap revisions for session and preference mutations. Return typed conflict/stale results instead of retrying a changed semantic operation invisibly.
- Persist intent before a filesystem or provider side effect when the owning application workflow requires a journal. SQLite commit alone cannot make a cross-resource operation atomic.

## Core store ownership

- T018 owns the core repository, worktree, review session, target-generation reference, review thread, anchor-version, normalized message metadata, read-state, and required operation/journal schema specified by that task.
- Later aggregate owners add their own additive migrations. Do not preload provider, snapshot, proposal, apply, capacity, reconciliation, repair, cleanup, or release tables into the core migration.
- Enforce foreign keys and uniqueness around stable IDs and lifecycle identities, but do not encode the whole domain reducer as triggers or a cross-product status column.
- Normalized Nudge messages are restoration truth. Provider history is supplementary; provider DTOs and raw frames do not enter the review store.

## Bounded queries and immutable ranges

- Use revision-bound keyset pages with deterministic ordering, whole-item count limits, encoded-byte limits, and cursor identity that includes the query/sort/filter contract. Do not use deep offset pagination as the product contract.
- Return complete items only. When the next whole item would exceed the byte cap, stop the page and return a continuation cursor; never truncate an entity into a misleading valid row.
- Keep large immutable message bodies and proposal patches out of ordinary snapshots. Read them sequentially or by bounded range using exact artifact/message/version ID, terminal length, content hash, range, and revision.
- Validate range bounds and immutable identity before returning bytes. A missing or mismatched hash, length, index, or terminal status fails closed rather than returning a partial object as complete.
- Publish an immutable body or proposal version atomically with its verified length/hash/completeness and range-index identity. Incremental BLOB writes must not make an unfinished entity visible as ready.
- Bound journal retention, result counts, decoded bytes, transaction time, cancellation latency, and WAL growth under T070. T065 may reserve projected database/WAL growth; T067 remains the retained accounting owner.
- Checkpoint policy must preserve active readers and avoid blocking the single application writer. WAL pressure becomes typed health/backpressure, not an unbounded checkpoint loop or silent history deletion.

## Saved repository base preference

- Persist one bounded raw base-ref expression keyed by the replacement-safe `RepositoryID`, with optimistic revision and update time. Canonical paths are lookup evidence, never the preference key.
- Save or clear only after an explicit user action. Discovery, selection, or successful resolution alone never persists a preference.
- Preserve accepted expression bytes exactly. Validation and precedence belong to the application/Git owners: explicit flag, current-session choice, saved preference, then discovery.
- A replacement repository receives a different binding and cannot inherit the prior row. Ambiguous rebind and `--no-persist` disable preference load/save/clear.
- Store the reusable raw expression, not a resolved object ID. Each target generation separately records its frozen ref/object evidence.

## Focused tests

Protect migration round trips, foreign keys, Nudge aggregate atomicity, CAS conflicts, deterministic keyset cursors, item/byte caps, immutable range identity, cancellation, WAL/checkpoint behavior, repository-replacement preference isolation, and no-persist non-writing behavior.
