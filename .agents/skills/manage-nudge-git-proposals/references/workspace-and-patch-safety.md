# Workspace and patch safety

Consult section 8 and sections 15.4-15.6 of the technical design. The proposal-isolation task plus accepted ADR-003/ADR-012 must resolve the security boundary and lifecycle before implementation. ADR-009 remains the design's stale-proposal/no-auto-merge decision.

## Shared Git metadata risk

An ordinary `git worktree add` creates a `.git` pointer into shared repository metadata. A provider that can execute Git may attempt to mutate refs, indexes, worktree records, or objects outside the visible proposal directory. “The provider cwd is a worktree” is not proof of isolation.

ADR-003 must select and enforce one of these classes of design:

- separate repository metadata/object storage;
- an independent Git directory and index for the proposal filesystem;
- a proven sandbox that makes shared metadata read-only while allowing only Nudge-owned independent state;
- another design with equivalent enforceable evidence.

Do not allow proposal workspace implementation to weaken this gate.

## Baseline and result

- T108 establishes and proves the four-root ownership identity. T035 owns active baseline/result installation, reset, advancement, and non-retirement recovery. T109 alone owns automatic retention and retirement; elapsed time or storage pressure never grants destructive authority.
- Keep four canonical roots distinct: immutable Nudge-only baseline, private Nudge-only admin/Git state, provider-writable result, and user edit destination. The provider receives only the result root.
- Create a Nudge-owned immutable baseline from accepted `CaptureID` artifacts or pinned Git objects; never copy later live local bytes.
- Copy untracked data without following escaping symlinks, junctions, or nested repository metadata.
- Disable repository hooks for internal snapshot operations.
- T110 freezes/quiesces and independently snapshots the complete result tree, including untracked additions. T111 converts that immutable baseline/result pair into the deterministic binary patch plus verified review index. T038 alone derives applicability and publishes a proposal version.
- Ignore provider-mutated `HEAD`, refs, index, baseline markers, and claimed file lists.
- Compute a binary-capable full-index patch and parse every affected file and hunk.
- Use T069 `ContentConversionPolicyV1`: sanitize system/global/private attribute inputs and core autocrlf/eol/safecrlf settings; inspect repository/worktree `.gitattributes` without executing filters; permit mutation only for registered byte-neutral `text`/`crlf`/`eol`/`working-tree-encoding`/`ident`/`filter` states. Unknown/non-neutral conversion and proposals changing `.gitattributes` remain review-only.
- Quiesce the complete contained provider descendant tree and all writable result handles before snapshotting. A terminal event or chmod/ACL change alone is not revocation; if emptiness cannot be proven, the result is non-ready/repair-required.
- Persist immutable proposal versions and flag scope outside the original anchor.
- An independently verified zero delta creates no proposal version. Journal and verify defensive `ResetToBaseline`, then record terminal `no_changes`; interruption remains actionable.
- Provider source-generation identity is provenance only. Store target-kind destination constraints and complete per-touched-path preconditions separately for applicability.

## Preconditions and apply

For every affected source/destination path, represent expected existence/absence, file kind, content or link-target hash, mode, and case/normalization collision behaviour. Additions require explicit absence. Preserve raw `RepoPath` identity; escaped display is not a precondition key.

T088's `internal/paths` executor is the sole native path-effect seam. It retains verified root/parent handles from final evidence revalidation through a closed typed no-follow leaf operation. T035 decides workspace lifecycle content, T112 prepares exact apply authority, T113 performs and verifies the effect, and T090 decides symlink target policy; all consume that executor rather than reopening a joined string path or defining parallel primitives. T041 only coordinates aggregate state and cannot bypass the executor.

T112 owns steps 1-3 and the prepared durable journal. T113 owns steps 4-6 plus crash classification. T041 coordinates exact user confirmation, proposal state, and idempotent finalization; it does not construct Git commands or mutate paths. Apply under the destination lock and journal:

1. refresh and validate target editability;
2. verify target-kind global constraints, every path precondition, raw index-byte identity, semantic stages/flags/sparse identity, and the persisted conversion/config/attribute fingerprints;
3. validate the exact patch under config-independent `ApplyPolicyV1` without whitespace fixing/ignoring, unsafe paths, recount/inaccurate-EOF, path filtering, or three-way fallback;
4. apply to the working tree only;
5. verify expected touched result identities, global constraints, and preserved raw/semantic index state;
6. mark the journal applied exactly once;
7. capture the authoritative post-apply destination—including unrelated valid user changes—and advance the accepted baseline from that capture.

The destination lock excludes other Nudge instances, not external editors/Git. On touching/index/global races or indeterminate/mixed crash state, record observed evidence as `repair_required`, stop, and explain it. Never claim atomic external exclusion, retry, roll back, or reapply blindly.

## Reject and cleanup

- Rejection records history, resets to the Nudge baseline, removes proposal-created untracked files, and preserves the review thread and conversation.
- Terminal failed/non-ready result residue has a distinct confirmed discard action that uses the same defensive no-follow reset without pretending a ready patch was rejected.
- Failed or cancelled provider turns must not silently produce an approvable proposal; follow the explicit task-level policy.
- Verify ownership markers before repair or removal.
- T109 uses typed ownership-proven retirement/removal for managed workspaces and never removes arbitrary computed paths recursively.
- Workspace lifecycle cleanup in this skill is distinct from T060 aggregate owned-storage cleanup and T102 residue quarantine, which belong to `$manage-nudge-owned-storage`.
- Plain doctor is read-only. The explicit `nudge doctor --repair <plan-id> --health-revision <revision>` mode and cleanup need an exact versioned plan/revision, revalidation, explicit confirmation, positive ownership, owning lock/journal, idempotent phases, and redacted audit. Never repair credentials/user Git/mixed apply or delete ambiguous ownership; no implicit repair or `repair-all`.
