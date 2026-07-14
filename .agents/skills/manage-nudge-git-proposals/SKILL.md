---
name: manage-nudge-git-proposals
description: "Implement and review Nudge Git resolution, immutable targets/captures, structured diffs, anchors, review and proposal workspace lifecycles, proposal derivation, edge-file semantics, patch application, rejection, fsnotify change-hint ingestion, owner-specific repair, and authoritative reconciliation. Use for the Git/proposal task family or work under internal/gitcli, internal/diff, internal/workspace, and internal/watch. Do not own aggregate storage cleanup/accounting or treat a linked worktree as proven isolation."
---

# Manage Nudge Git Proposals

Preserve the exact Git meaning of the review target and make every provider edit reviewable before it can reach the user's worktree.

Primary task family: T008-T011, T016, T023-T024, T033, T035-T036, T038, T040-T048, T055-T057, T069, T072, T081, T087-T091, T093-T094, T103, and T106-T113. Repository query bounds land with T011/T072/T081 and their first consumers rather than a later cache-hardening task.

## Workflow

1. Read sections 7, 8, 15.3-15.6, 17.3, and Appendix B of `docs/Nudge_PRD_Technical_Design.md`.
2. Read only the references relevant to the task:
   - [target-semantics.md](references/target-semantics.md) for repository identity, target capture, trees, diffs, and special files.
   - [workspace-and-patch-safety.md](references/workspace-and-patch-safety.md) for isolation, independent snapshots, immutable proposals, preconditions, application, and cleanup.
   - [reconciliation.md](references/reconciliation.md) for generations, watchers, anchors, staleness, and concurrent proposals.
   - [watch-and-fsnotify.md](references/watch-and-fsnotify.md) for T107's lossy watcher adapter, bounded watched-set lifecycle, debounce/max-delay coordinator, truth-loss handling, and shutdown.
3. Name the accepted capture/pinned-object source, actor-owned target generation, immutable review/proposal baseline, edit destination, and applicable global/path preconditions before writing adapter code.
4. Resolve/revalidate the trusted Git executable through the platform contract, then construct commands centrally with explicit argument arrays and machine-readable output. Keep parsing separate from process execution.
5. Keep local capture phases explicit: T106 computes one immutable candidate, T009 alone adopts it and assigns/reuses a monotonic target generation, and T107 supplies only lossy refresh hints to T024's authoritative reconciliation. Load `$go-concurrency` for T107 and any other work where goroutine/channel/timer ownership materially affects correctness.
6. Keep workspace authority explicit: T108 proves four-root ownership, T035 owns active baseline/result lifecycle, and T109 alone owns automatic retention and retirement. Do not proceed on a workspace-write task while the T033 isolation decision is unresolved.
7. Keep proposal derivation phases explicit: T110 freezes independent result truth, T111 builds the deterministic complete patch/index artifact, and T038 derives applicability and publishes one immutable version. Provider events and mutable workspace Git metadata are never patch truth.
8. Keep application phases explicit: T112 performs locked non-mutating preflight and prepares the journal, T113 owns mutation/verification/recovery, and T041 composes proposal aggregate transitions and idempotent user-command results.
9. Add real-Git integration tests only for the semantics introduced by the task.

## Hard guards

- Use the installed Git CLI as source of truth; do not replace core semantics with `go-git`.
- Never invoke a shell string. Pass `--` before paths and prefer NUL-delimited output.
- Never let discussion or proposal generation write to the user worktree.
- Never assume `.git` inside a linked worktree is isolated from the repository common directory.
- Never treat fsnotify event paths/operations, quiet periods, watcher recreation, or queue drainage as repository truth. Events only request T024 reconciliation; overflow, channel closure, watched-root replacement, unsupported filesystem behavior, or bounded coverage loss latches truth loss until an accepted capture restores freshness.
- Do not assume fsnotify is recursive or that a watched file survives editor/Git atomic replacement. Resolve a bounded explicit directory set, filter child names, and close/cancel/join the single owned watcher loop deterministically.
- Never trust provider-created commits, refs, index state, or baseline markers.
- Route every repository read through versioned `MachineGitReadPolicyV1`: explicit argv, optional locks off, lazy fetch/prompt/fsmonitor/pager/editor/helpers disabled, controlled config/locale, and typed missing-object outcomes. Opening Nudge must not rewrite the index or contact a remote.
- Route patch derivation/check/application through T069's exact policy. Resolve conversion-affecting attributes/core settings without executing filters, allow mutation only for registered byte-neutral states, persist fingerprints, and revalidate before mutation. Unknown/non-neutral conversion or proposal changes to `.gitattributes` are review-only.
- A saved branch-base preference is a bounded expression keyed to verified `RepositoryID`; explicit CLI value wins, then session selection, saved preference, deterministic local-ref discovery, and user selection. Re-resolve/freeze the expression for every generation and never fetch implicitly.
- Never enable propose/apply from desired policy alone; require effective implementation/platform evidence for every entry.
- Keep edge evidence independent: rename/copy, regular binary, text/conversion, raw/native path, mode/type, symlink, and review-only gitlink/special-object tasks register only their own capability cells. Do not treat one green round trip as generic file support.
- T057 owns one persisted content-class decision before T087 text handling. Invalid-UTF-8/unregistered opaque bytes never receive a competing text classification; consumers reuse the recorded class.
- Preserve every safely parsed raw Git entry for identity/counting even when native qualification or special metadata fails. Native mutation goes only through T088's held-handle executor; T090 supplies typed symlink operations through that seam, and T091 duplicates no canonical `ChangedFile` endpoint data.
- Never claim destination locks exclude external editors/Git or that unrelated paths are proven unchanged; mixed/touching/index/global uncertainty is repair-required.
- Never apply a stale proposal, silently merge conflicts, or apply one proposal twice.
- Never stage, unstage, commit, push, or mutate a non-current branch.
- Never clean workspaces through blind recursive deletion; verify Nudge ownership and use the selected Git-aware lifecycle.
- Do not collapse the split task boundaries back into convenience helpers: candidates are not generations, hints are not reconciliation truth, owned roots are not active or retireable workspaces, result snapshots are not proposal versions, and prepared apply operations are not successful applications.
- This skill owns cleanup/reset/quarantine only for the review/proposal workspace lifecycle named by its tasks. `$manage-nudge-owned-storage` owns aggregate export, retained-artifact accounting, repository cleanup, reconciliation, rebuild, and residue quarantine.
