# ADR-003: Proposal isolation boundary

## Status

Accepted for Nudge v1. This decision blocks proposal-mode implementation until
the platform capability evidence described here is available.

## Decision

Nudge uses four distinct canonical roots for every proposal:

1. **Baseline root** — immutable Nudge-owned bytes reconstructed from the
   accepted capture or pinned Git objects.
2. **Admin root** — private Nudge-owned Git/object/index/manifest state. It is
   never the provider working directory and is never passed as a provider
   readable or writable root.
3. **Result root** — the only repository root exposed to a proposal provider
   turn. Nudge snapshots it independently after the turn is quiescent.
4. **Destination root** — the user's edit worktree and index. It is never
   exposed to the provider and is changed only by the later Nudge apply path.

The result root is a materialized filesystem tree, not an ordinary linked Git
worktree. When Nudge needs Git semantics, it invokes Git with an independent
private admin domain (`--git-dir=<admin>`, `--work-tree=<result>`, and a private
`GIT_INDEX_FILE` under admin), with no `.git` pointer in result, no alternates,
no remotes, no shared objects, no hardlinks/reflinks, and no provider-controlled
refs. Nudge owns Git commands, the private admin state, baseline/result
comparison, and cleanup. Provider prose, provider file events, provider-created
refs, and provider-mutated index state are not patch truth.

Proposal mode is admitted only when a registered native capability proves all
of the following for the complete provider process tree:

- filesystem access is limited to the result root (plus the minimum executable
  and runtime reads required by the platform);
- network access is disabled;
- descendants inherit the boundary and cannot detach outside it;
- the provider environment cannot inject Git directories, indexes, alternates,
  hooks, helpers, editors, pagers, remotes, or credential backchannels;
- symlink, junction/reparse-point, mount/bind, hard-link, and shared-clone
  aliases cannot create a writable path to another root; and
- the process tree and writable handles can be proven quiescent before Nudge
  freezes the result.

Codex permission prompts are not this proof. In the pinned app-server schema,
filesystem permission profiles are request data and `grantRoot` is marked
unstable. They can describe a requested turn policy, but a provider can never
make itself safe by asking for a narrower prompt. Nudge fails closed with the
typed `proposal_isolation_unavailable` result when the native proof is absent.

## Alternatives considered

### Ordinary linked worktree — rejected

`git worktree add` leaves `.git` indirection and shared repository metadata.
The provider could target the common directory, worktree administration,
refs, index, hooks, alternates, config, object replacement, or another linked
worktree. A different visible directory is not a security boundary.

### Independent Git directory and index — necessary but insufficient

Copying objects, refs, and index state into a Nudge-owned admin root removes the
provider's need to access the user's Git metadata. It does not stop a provider
from opening the user's worktree or destination by absolute path, following an
alias, or using an inherited handle. This is part of the chosen construction,
not the complete isolation proof.

### Separate clone/private object database — necessary but insufficient

A separate clone with copied objects avoids shared object/refs mutation and
is safer for Git operations than a linked worktree. Clone/reflink/alternates
optimizations are not accepted unless native evidence proves that later writes
cannot modify the source inode or shared backing store. The clone still needs a
provider filesystem boundary, so it cannot be the sole proof.

### Prompt-only or provider approval policy — rejected

Prompt instructions, runtime approvals, and app-server permission profiles do
not prevent direct syscalls, path aliases, subprocesses, background children,
or a confused provider process from reaching a known absolute path. They are
UX and policy inputs layered inside the native boundary.

### Native sandbox capability gate — chosen

The provider runs only inside a platform adapter that can prove the required
filesystem and descendant boundary. Platform adapters may use a native
filesystem sandbox plus a native process/job boundary, but the implementation
must register evidence for the exact roots and turn. If the adapter cannot
prove the boundary on an OS, proposal mode is unavailable there while
discussion remains available through an immutable snapshot or prompt-only
turn.

## Threat matrix and required controls

| Threat | Required control | Failure disposition |
| --- | --- | --- |
| Direct write to user worktree or index | Destination is outside provider roots and native write-deny evidence covers it | Proposal unavailable |
| `git -C` against source | No source path is reachable; provider has no source/admin root | Proposal unavailable |
| `.git` indirection/common dir/worktree admin | Result is not a linked worktree; admin root is Nudge-only | Proposal unavailable |
| Refs, hooks, config, alternates, object replacement | Private admin state; hooks/filters/network disabled for Nudge Git operations | Proposal unavailable or review-only |
| Symlink/junction/reparse traversal | Canonical containment plus native no-follow/deny evidence | Proposal unavailable |
| Mount, bind, or link swap | Native mount/reparse evidence and held-root/parent operations; no string-only recheck | Non-ready/repair-required |
| Cross-boundary hard link | No shared inode evidence; never trust copy/reflink optimization without proof | Proposal unavailable |
| Unsafe reflink/clone sharing | Copy into independent storage unless COW separation is registered and proven | Proposal unavailable |
| Inherited descriptors/handles | Sandbox/process adapter inventories or prevents inherited writable handles | Proposal unavailable |
| Detached/background descendants | Job/cgroup/sandbox descendant containment and turn-level empty proof | Non-ready/repair-required |
| Environment escape | Minimal explicit environment; no ambient repository/workspace roots or helper overrides | Proposal unavailable |
| Case, normalization, or path aliases | Canonical native identity and collision checks on all four roots | Proposal unavailable |
| Provider-created Git state | Ignore provider refs/index/markers; derive from immutable baseline/result snapshots | Result non-ready |
| Oversized result growth | Reservation, reserve, bounded rechecks, cancellation, and quiescence proof | `workspace_growth_limit` |

Canonical path checks are evidence, not authorization. They cannot repair a
shared inode, a mount replacement, an inherited handle, or a descendant that
escaped the process boundary.

## Construction and turn algorithm

1. Acquire the session writer, capacity reservation, and stable workspace owner
   lock in the ADR-012 order.
2. Revalidate the repository binding and materialize the baseline from the
   accepted `CaptureID` or pinned Git objects; never reread live bytes to
   recreate a generation.
3. Create the private admin root and result root under Nudge-owned storage,
   write an ownership marker containing the workspace identity and policy
   version, and record canonical/native identity for every root.
4. Admit the provider turn only after the platform adapter proves the native
   boundary, no alias, network denial, and descendant containment. Pass only
   the result root as the provider repository root.
5. Recheck the capacity reservation at the configured byte/interval bounds.
   On a growth breach, cancel the complete contained process tree and return
   `workspace_growth_limit`.
6. After a terminal provider event, revoke the turn and prove descendants are
   empty, writable handles are closed, and the result root is stable. If any
   part is unknown, do not publish a proposal; return non-ready or
   `repair_required` according to the evidence.
7. T110 independently snapshots the result root. T111 derives the complete
   patch/index artifact from the baseline/result pair. T038 alone publishes an
   immutable proposal version.

## Storage enforcement

V1 treats ordinary free-space observations as **monitored**, never as a hard
write-time quota. The default qualified policy is a 1 GiB result-delta ceiling,
a 2 GiB minimum free-space reserve, and a 256 MiB protected recovery reserve.
The reservation contains the checked peak charge and these reserve values. The
owner rechecks before the byte and interval bounds are crossed; crossing the
result limit cancels the contained process tree and requires quiescence proof.
A failed cancellation or ambiguous free-space state is non-ready/repair-
required, never claimed success.

`VolumeCapacityHard` may be recorded only when a native adapter proves a
per-result-root quota with the same root and descendant semantics. No current
generic fallback may claim that property. The user-visible support code for a
monitored breach is `workspace_growth_limit`.

## Consequences

- Proposal mode is capability-driven and may be unavailable on a build/OS even
  though read-only discussion and the CLI remain usable.
- The provider does not need access to Nudge's baseline/admin data or the user
  destination, and Nudge never treats a linked worktree as isolation.
- Native adapters must provide evidence for hard-link/reparse/mount/clone and
  descendant behavior rather than relying on path strings or prompts.
- T034-T047 must use the four-root terms and must not create a workspace before
  this decision is recorded.
