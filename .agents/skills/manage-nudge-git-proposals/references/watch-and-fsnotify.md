# Watcher and fsnotify guidance

Read design sections 7.10 and 12.8, T107, T070's queue/timing/resource policy, T008's resolved worktree/common-directory identities, and T024's authoritative reconciliation contract. Load `$go-concurrency` with this reference.

Library references: [fsnotify package documentation](https://pkg.go.dev/github.com/fsnotify/fsnotify) and the [fsnotify repository/FAQ](https://github.com/fsnotify/fsnotify).

## Boundary

- `fsnotify` is a lossy cross-platform hint source. An event says only that an authoritative refresh may be needed; it never identifies the complete changed set, proves a write finished, advances target generation, or authorizes partial tree/diff/anchor mutation.
- T107 owns the adapter, immutable watched-set identity, reason normalization, quiet-delay/maximum-delay coordination, and truth-loss latch. T024 alone resolves Git, compares fingerprints, accepts a capture, assigns/reuses generation, reconciles anchors, and clears visible staleness.
- Keep raw `fsnotify.Event`, native path strings, and backend errors inside the adapter. Emit bounded typed `RefreshReason`/`TruthLost` evidence tied to `RepositoryID`, `WorktreeID`, and watched-set identity.

## Watched-set construction

- Resolve the exact worktree Git directory and common directory through T008. Register only the bounded directories required for worktree content and the relevant `HEAD`, index, ref, packed-ref, and config replacement paths.
- fsnotify directory watches are not recursive. Never assume watching the worktree root covers descendants. Register eligible directories deliberately under T070 count/resource bounds; maintain the set as eligible directories appear/disappear; and latch truth loss if complete intended coverage cannot be represented.
- Prefer watching a file's parent directory and filtering `Event.Name`. Editors and Git commonly replace files atomically, and a direct watch can disappear when its target is renamed or removed.
- A watched path that is renamed/deleted may lose its watch, with backend differences. Root/admin-directory replacement, repository identity change, or accepted authoritative capture triggers bounded watched-set re-resolution; recreation itself is not freshness proof.
- Do not traverse ignored or ineligible trees without a bound, follow symlinks/reparse points, infer ownership from display paths, or watch Nudge-owned workspaces/logs as user-worktree content. If identity/containment is ambiguous, request full reconciliation and remain stale.
- Network/special filesystems may not deliver useful notifications. Represent unsupported/degraded watcher capability explicitly and rely on the designed lifecycle/maximum-age refresh path without claiming watcher completeness or silently inventing polling.

## Event loop and normalization

- One adapter owner creates the watcher, registers the set, and promptly consumes both `Events` and `Errors` in one bounded select loop. It hands only normalized reasons to the coordinator; it does not start work per event.
- Operations are bitmasks; test with `Event.Has`. Treat `Create`, `Write`, `Remove`, `Rename`, and relevant `Chmod` only as coalescible reasons. A `Write` does not mean writing is complete, and rename/remove/directory-write behavior varies by backend.
- Do not depend on `Chmod`: some platforms omit or overproduce it. When relevant to Git mode semantics, coalesce it as an ambiguous refresh reason rather than inferring a mode change.
- Recognize `fsnotify.ErrEventOverflow` with `errors.Is`. Overflow, adapter error that compromises coverage, unexpected channel closure, root replacement, or platform rescan/loss signal latches `TruthLost` and requests one full refresh plus watched-set reconstruction.
- A larger OS/library buffer is not correctness. Any buffer selection is a bounded platform-policy decision; it cannot turn notifications into a complete log or replace overflow handling.
- Never log or render raw event paths directly. If a bounded safe reason must reach a frontend, route it through Nudge's terminal-safe projection and logging safe-field policy.

## Debounce and backpressure

- Keep one fixed-capacity reason set, one quiet-delay timer, one maximum-delay timer, at most one active refresh request, and at most one coalesced follow-up per session.
- Bursts reset the quiet delay but not the maximum-delay deadline for the pending burst. Continuous events therefore produce bounded periodic requests instead of starvation or spinning.
- Hints arriving while T024 is active merge into the single follow-up. Do not queue one request per event, cancel committed reconciliation work, or run Git in the watcher goroutine.
- Overflow/truth loss is non-coalescible state even if its many raw causes collapse to one bounded reason. Keep the last accepted projection visibly stale until T024 accepts a complete capture for the current watched-set/repository identity.
- Explicit refresh, focus/resume, settled provider completion, proposal reset/apply, HEAD/ref trigger, and focused-session maximum age use the same coordinator. Provider token/file streaming and keystrokes do not trigger Git refresh.
- While a local session is focused, schedule the design-required authoritative maximum-age request no later than 30 seconds; configuration may lower but not raise it. Pause that schedule when unfocused or closed.

## Shutdown and replacement

- Closing the session first stops new admission, cancels the coordinator, stops owned timers, closes the fsnotify watcher to wake its reader, and joins the adapter/coordinator goroutines before releasing watched-set identity.
- The sender/adapter owns its output channel closure. Callers never close fsnotify's channels or race to close adapter outputs.
- Make close behavior deterministic and safe under the task's expected repeat/concurrent-call contract. No goroutine, channel, timer, descriptor, watched path, or queued reason may grow per event burst or survive owner shutdown.
- On watched-set replacement, build/validate the new bounded set, switch under one owner, retire the old watcher, and request authoritative reconciliation. Never merge events from sets without preserving which identity produced them.

## Focused regression coverage

Use a deterministic watcher port and controlled clock/synctest-compatible coordinator for quiet/max delay, one-active/one-follow-up behavior, truth loss, replacement, cancellation, and close. Add only narrow native fsnotify adapter cases that protect real cross-platform event/closure/overflow semantics; do not create a scanner, demo watcher, diagnostic command, polling fallback, or broad timing harness.
