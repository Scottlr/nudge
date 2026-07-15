# ADR-012: Proposal workspace lifecycle and recovery

## Status

Accepted for Nudge v1. ADR-009 remains reserved for stale-proposal and
no-auto-merge policy. This ADR governs ownership, lifecycle, cleanup, and
recovery only.

## Ownership model

Every proposal workspace records one immutable identity, policy version, and
the four canonical roots from ADR-003:

- baseline: Nudge-owned immutable input;
- admin: Nudge-owned private Git/object/index/manifest state;
- result: provider-writable turn root;
- destination: user edit worktree and index, never provider-readable.

T108 owns the root-ownership proof. T035 owns active baseline/result
installation, reset, advancement, and recovery. T109 alone owns automatic
retention and retirement. T110 freezes result truth, T111 derives the complete
patch/index artifact, and T038 publishes the immutable proposal version.

No provider event, provider commit, provider ref, provider index, elapsed time,
or storage pressure can authorize deletion or proposal publication.

## Lock and reservation order

Ordinary actor work already holds its session-writer lease. It then acquires:

1. global capacity-reservation lock;
2. repository capacity lock;
3. stable-ordered destination, workspace, and protected-log owner locks.

Heavy workspace/provider mutation occurs only after the capacity reservation is
admitted. Maintenance first acquires the repository gate and all affected
session locks in stable order, then follows the same global-to-repository
capacity order and owner-lock order. No path acquires the maintenance gate
while holding a capacity or owner lock.

The process/sandbox owner remains responsible for joining the contained
provider process tree. A filesystem lock does not prove descendant quiescence.

## Lifecycle

### Create and install

1. Verify the repository/worktree binding and accepted target generation.
2. Admit a bounded capacity plan, including temporary copies, WAL, final
   artifacts, and recovery reserve.
3. Create four roots in Nudge-owned storage with restrictive native defaults.
4. Create the independent private Git/admin domain with no `.git` pointer in
   result, no alternates/shared objects, and a private index before exposing
   any Git-derived result state.
5. Write and fsync an ownership marker and manifest before exposing the result
   root to a provider.
6. Materialize baseline bytes from the accepted capture or pinned objects and
   independently verify the manifest identity.
7. Record the active baseline identity and workspace lease durably.

If any phase is uncertain, leave the workspace non-ready and retain it for
explicit repair/recovery; never infer ownership from a path name alone.

### Mutating turn

The proposal turn receives only the result root and the agreed provider
permission profile. Network remains disabled. The platform adapter must prove
filesystem boundary, alias denial, descendant containment, and explicit
environment handling before the turn starts. Capacity is rechecked on both
configured bounds.

At turn end, Nudge cancels/joins the contained process tree and requires a
quiescence proof covering descendants, writable handles, and result-root
stability. A terminal protocol event alone is not sufficient.

### Snapshot and publication

After quiescence, T110 independently captures the complete result tree,
including bounded untracked additions. T111 computes the binary-capable full
patch and verified review index from the immutable baseline/result pair. T038
publishes one immutable proposal version. A zero-delta result records
`no_changes` and creates no empty approvable version.

## Reset, rejection, and restoration

Reset uses the same verified ownership marker, canonical roots, native no-follow
operations, and journaled bounded batches as creation. It never blindly removes
a computed recursive path. Rejection records history, restores the result to
the Nudge baseline, removes proposal-created untracked files, and preserves
the thread, messages, and provider conversation.

On restart, Nudge reads the durable workspace manifest and independently
revalidates every root identity, lease, ownership marker, baseline identity,
capacity reservation, and apply/workspace journal. Missing or replaced roots
become `proposal_workspace_missing`/`repair_required`; they are not silently
recreated over an ambiguous path. If safe recreation is possible, the current
target generation and accepted baseline are used, never later live bytes.

## Cleanup and retention

Unresolved workspaces survive ordinary exit. T109 may retire a workspace only
after the thread/proposal retention policy allows it, ownership is positive,
the process tree is quiescent, the workspace lease is released, and the
reservation/journal is closed. Accepted history and immutable proposal
artifacts are never deleted because a workspace is under pressure.

Cleanup verifies canonical roots, native identities, ownership marker,
workspace identity, and expected root set before removal. Ambiguous ownership,
mixed external changes, open handles, or a live/unknown descendant produce
`repair_required` and stop. There is no implicit `repair-all`.

Explicit doctor repair must carry an exact versioned plan and health revision,
revalidate ownership, acquire the owning locks, perform idempotent journaled
phases, and emit a redacted audit record. Repository cleanup and aggregate
artifact quarantine remain owned by the storage/repair tasks, not this
workspace lifecycle.

## Storage and crash recovery

V1 records ordinary volume evidence as monitored, with a checked peak
reservation, a 1 GiB result-delta ceiling, a 2 GiB minimum free-space reserve,
and a 256 MiB protected recovery reserve. A growth breach returns
`workspace_growth_limit`, cancels the contained process tree, and requires the
quiescence proof before snapshot or deletion. A platform-specific hard quota
may be used only with native evidence that covers the exact result root and
all descendants; free-space polling is never described as hard quota.

Crash recovery classifies incomplete creation, provider turn, snapshot, reset,
and cleanup journals separately. It never rolls back user files, deletes
accepted history, steals another process's lease, or assumes a detached child
has stopped. An unresolved process/handle or mixed filesystem state remains
non-ready until the owning repair flow proves closure.
