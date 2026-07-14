# Review target semantics

Canonical repository/worktree paths are lookup keys, not durable identity. Reuse Nudge-generated repository/worktree IDs only after versioned object-format and native common-dir/root/per-worktree-Git-dir evidence matches. Same-path replacement creates a new binding or explicit fail-closed rebind state; never write an identity marker into user Git metadata.

Consult sections 6.2, 7, and 10.1-10.2 of the technical design for the complete contract.

## Repository and worktree identity

- Resolve with Git semantics, including `--show-toplevel`, `--git-common-dir`, `--show-prefix`, and `--is-inside-work-tree` equivalents.
- Use the canonical common Git directory for repository identity and the canonical top level plus per-worktree Git directory for one worktree identity.
- Preserve the launch-directory prefix as optional initial tree focus.
- Keep repository and worktree records separate so linked worktrees do not collide in persistence.

## Local target

- Base: `HEAD`, or the empty tree for an unborn branch.
- Head: a fingerprinted working-tree snapshot containing tracked changes plus untracked, non-ignored files.
- Preserve staged and unstaged classifications as metadata; the reviewed content is the combined `HEAD -> working tree` result.
- T106 returns one internally consistent `LocalCaptureCandidate` containing status/delta/untracked/content evidence. T009 alone lets the application actor atomically adopt it into `LocalCaptureStore`, assign/reuse a monotonic `TargetGeneration`, and persist the `CaptureID`; Git never assigns a generation.
- Accepted capture blobs/manifest are the source of truth after adoption. Content/diff/reconciliation/workspace code must read by `CaptureID` and `RepoPath`, never reread an unchanged-looking live path to recreate that generation.
- T107 filesystem/lifecycle events are bounded lossy hints only. T024 alone may use T106/T009 plus complete anchor staging to activate an authoritative generation epoch; subscription recreation never proves freshness.
- Rename/copy identity is config-independent `RenamePolicyV1`: 60% threshold, at most 1,000 delete-source/add-target candidates, changed-source copies only, and no harder unchanged-source search. Persist the exact policy/outcome; a limited case remains visible delete/add and supplies no anchor map.
- Preserve unmerged porcelain evidence with stage-1/2/3 modes/OIDs. It is reviewable inert evidence only; anchor/proposal/apply remains disabled with `unmerged_index`.

## Commit target

- Resolve the requested revision once to an immutable object ID while retaining the user's expression for display.
- Normal commit: parent to commit. Root commit: empty tree to commit.
- V1 merge behaviour is clearly labelled first-parent only unless a separate design decision adds parent selection.
- Historical commits are read-only. Current `HEAD` is editable only when destination preconditions hold.

## Branch target

- Base: best common ancestor of the selected base and current `HEAD`.
- Head: current `HEAD`.
- Base precedence is explicit `--branch <base>` -> current session selection -> saved per-repository preference -> deterministic local-ref discovery -> explicit user selection. Saving/clearing is itself explicit. The saved value is a bounded revision expression, re-resolved and frozen for each generation; it is never a promise of remote freshness or a durable object identity.
- Exclude working-tree changes from the reviewed diff and warn when they exist.
- Do not fetch automatically. State that local refs may be stale.
- Obtain unborn/root empty-tree identity from installed Git for the repository's SHA-1/SHA-256 object format without writing an object; never hard-code the SHA-1 constant or fabricate zeros.
- Machine Git reads set optional locks off, suppress lazy fetch/prompt/fsmonitor/pager/editor/helpers, and fail typed when the installed Git cannot prove those controls. Normal status is not complete for assume-unchanged/skip-worktree/sparse paths: independently compare materialized flagged paths under caps or mark the local capture incomplete and disable content actions.
- Permit edits only on the current branch. Path-touching working-tree changes make a proposal stale; unrelated dirt must remain untouched.

## Review snapshots and capability

- Provider discussion uses a leased immutable `ReviewSnapshot`, never the user worktree: capture-backed for local targets and pinned-object-backed for branch/commit targets.
- Repository `CapabilityDecision` separates review, anchoring, immutable review-snapshot materialization, proposal, and apply. Desired policy must intersect registered implementation evidence and runtime platform/session capability. Provider/account/disclosure state is composed later as application-level `DiscussionAvailability`; it must not leak into or invalidate repository capability evidence.
- Snapshot materialization does not prove provider read containment. Filesystem discussion requires the leased snapshot to be the sole repository-readable root with canonical-resolution containment; otherwise use an explicitly rootless bounded prompt-only turn or disable provider dispatch.
- Keep unsupported entries visible with escaped raw-path/type evidence. Limits or unproven cases disable the affected action with a typed reason; they never disappear or become partial complete artifacts.
- Preserve every safely parsed raw Git path as identity/countable review evidence even when it is not natively actionable. Platform-invalid, reserved, traversal-like, Git-admin-alias, collision, or metadata-unavailable cases receive stable review-only dispositions; native qualification never drops the Git entry.

## Process and parsing

- Use the platform-resolved trusted Git executable plus explicit args, controlled locale when needed, bounded stdout/stderr, cancellation, and typed errors. Revalidate canonical regular-file/native identity immediately before spawn; never resolve from the repository/current directory.
- Disable color, hooks, external diff/textconv, filters/LFS execution, and implicit network for internal parsed/materialization operations.
- Keep binary/full-index patch bytes and raw source text free of terminal escapes.
- Keep `ObjectID` opaque across SHA-1/SHA-256 repositories and let installed Git validate object format/existence. Treat all-zero patch tokens as absent sides, not objects.
- Model a rename as one changed file with old and new paths.
- Preserve porcelain-v2 unmerged records and stage-1/2/3 mode/object evidence for review. Do not flatten conflicts or permit anchor/proposal/apply on them in v1.
- Treat binary files, symlinks, gitlinks, type/mode changes, copies, deleted entries, CRLF, and large files explicitly.
- T057 owns one persisted `ContentClassV1`: explicit binary/NUL regular content is binary, valid UTF-8/BOM routes to T087 text semantics, and remaining invalid-UTF-8/unregistered bytes are opaque byte content under T057. No renderer/proposal path reclassifies content independently.
- Canonical old/new modes and object IDs remain on `ChangedFile`. T091 adds only special-kind/completeness/reason evidence; even path-only metadata remains visible, counted, and `Review=true` while every mutation axis stays false.
