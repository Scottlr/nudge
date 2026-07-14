# Export, cleanup, and focused storage repair

Use this reference for T059-T060, T095, T101, and T102. T058 authorizes exact repair plans but never owns these effects.

## Human-readable Markdown export

- Export one selected thread or proposal as deterministic human-readable UTF-8 Markdown from stable application projections, never raw SQL rows, workspace directories, logs, credentials, raw protocol, runtime arguments, or unrelated review data.
- Pin one short bounded `ExportSelection` containing IDs/status/order plus immutable terminal message and proposal body IDs, lengths, and hashes. Close the transaction before slow sink I/O.
- Stream terminal message bodies and safe textual hunks through identity/hash-bound readers and fixed buffers. Binary, invalid-text, special, or over-threshold entries remain visible as kind/size/mode/hash metadata; do not invent Base64 transport or an import/archive protocol.
- Keep sections/order deterministic and sanitize untrusted Markdown/terminal text. Stdout contains only Markdown—no ANSI or progress—and cancellation/broken pipe never produces a completed partial file.
- File output uses checked capacity plus a create-new owner-only same-directory spool and proven atomic no-replace publication. Never overwrite an existing user path.
- The export operation owns only its exact temporary handle/path. Never scan or guess-delete residue in a user-selected output directory.
- Export is read-only and cannot trigger cleanup, provider work, or repository mutation.

## Exact repository cleanup

T060 cleans one resolved repository's Nudge-owned local state only.

- Produce a read-only, revision-bound plan listing exact rows/artifacts, counts, exclusions, blockers, and irreversible effects. The shipped preview is product behavior; do not create a development dry-run utility.
- Confirmation binds plan ID, repository identity, revision, and resource summary. Re-enumerate immediately before mutation; drift requires a new plan/confirmation.
- Store the cleanup journal outside repository rows so cascades cannot remove the recovery record. Persist prepared, quiesced, filesystem-removed, database-removed, verified, and terminal phases.
- Hold the repository maintenance gate, stable-sorted session writers, capacity locks, and owner locks in ADR-012 order. A live process leaves the plan pending; timeout is not abandonment proof.
- Remove only individually enumerated resources with matching canonical root, owner/artifact ID, marker/nonce, expected kind, manifest, and current lifecycle state. Never use a generic recursive-delete helper on a computed path.
- Delete dependent SQLite rows only after journalled filesystem effects, retain a minimal redacted tombstone, and classify restart idempotently.
- Never mutate the checkout, index, refs, Git config, credentials, remote conversations, other repositories, global live logs, or user-selected export directories. Cleanup never performs an implicit export.

## Focused repair effects

Every effect begins with a current T079 finding and exact T058 plan/revision, then revalidates ownership, owner activity, locks, ledger/health revisions, and postcondition.

### T095 ledger entry or reservation

- Correct one missing/mismatched ledger entry only for a positively owned contained artifact whose marker/manifest still matches.
- Close one reservation only after owner journal and OS-lock evidence proves no active/nonterminal lifecycle can consume it. Age or PID is insufficient.
- Apply one fenced checked SQLite compare-and-swap to the exact row and repository/global totals. Never change artifact bytes.
- Reconcile only the affected IDs afterward. CAS/revision drift requires a fresh plan, not a heuristic retry.

### T101 derived-artifact rebuild

- Rebuild only an owner-declared derived class with a complete immutable authoritative source, deterministic/verifiable builder, output manifest, limits, and lifecycle/ledger postcondition.
- Never rebuild canonical messages, anchors, proposal versions/results, apply journals, provider transcripts, or user files; mutable current-worktree bytes and provider prose are not historical truth.
- Revalidate source, reserve every affected volume, stream through T066, verify independently, adopt same-volume/no-replace, and settle owner lifecycle plus T067 accounting exactly once.
- On interruption, preserve spool/adoption evidence and converge to zero or one verified charged artifact. Do not combine rebuild with ledger correction, quarantine, cleanup, or history deletion.

### T102 residue quarantine

- Move one exact positively owned, inactive, non-accepted residue only when marker/nonce, canonical/native identity, bounded manifest, owner state, revisions, source root, quarantine root, and same-volume identity still match.
- Acquire owner/quarantine locks, create a unique protected destination, and perform one atomic no-replace rename. Never copy-and-delete, recurse beyond the manifest, or create/adopt an ownership marker.
- Verify source absence and exact quarantined identity before terminalizing the record. Retry returns already-quarantined only for the same record and artifact identity.
- Quarantine retains evidence. It does not delete/purge data, reclaim space, correct ledger totals, close reservations, rebuild content, or implement cleanup policy.
- Unowned, ambiguous, active, accepted-history, cross-volume, collision, link/reparse, or drift cases remain untouched.

## Focused tests

Use behavior tests for Markdown selection/redaction/streaming/no-overwrite output, cleanup confirmation/ownership/lock/journal phases, checked ledger CAS and inactivity proof, rebuild source drift/reservation/spool/adoption replay, and quarantine containment/atomic rename/crash classification.
