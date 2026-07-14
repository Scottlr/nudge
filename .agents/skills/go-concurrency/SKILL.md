---
name: go-concurrency
description: "Design, implement, and review bounded, cancellable, race-safe Go concurrency for Nudge. Use in addition to $go and the primary Nudge owner whenever work involves goroutines, channels, mutexes, atomics, actor/event loops, worker pools, timers, streaming, queues, backpressure, cancellation coordination, or deterministic concurrent tests. It governs mechanics and lifecycle, not domain workflow semantics."
---

# Go Concurrency

Make every concurrent activity owned, bounded, stoppable, and observable without moving Nudge's canonical state or product policy into concurrency machinery.

## Workflow

1. State why concurrency is required and why a sequential operation or the existing owner loop is insufficient.
2. Name the owner of every goroutine and mutable invariant. Define start, cancellation, stop, join, result, and failure semantics before spawning work.
3. Bound goroutine count, queued items, buffered bytes, batch size, retry count, and wait duration from the owning resource policy.
4. Choose the primitive from the invariant: actor/single writer, channel handoff, mutex-protected state, atomic scalar, condition, or synchronous call.
5. Define overload and cancellation behavior explicitly. Preserve non-coalescible lifecycle/failure truth and reserve a way for control/failure to make progress.
6. Make shutdown deterministic: stop new admission, propagate cancellation, unblock waits, close only from the sending owner, drain only what policy permits, and join every owned goroutine.
7. Add focused deterministic tests for the behavior being introduced and use the race detector where the task changes material concurrency.

## Load the relevant references

- Read [ownership-lifecycle-and-cancellation.md](references/ownership-lifecycle-and-cancellation.md) for goroutine ownership, structured lifetimes, cancellation, startup, and shutdown.
- Read [channels-locks-and-bounds.md](references/channels-locks-and-bounds.md) for primitive selection, actors, channel ownership, mutex/atomic use, queues, coalescing, and backpressure.
- Read [concurrency-testing.md](references/concurrency-testing.md) when testing timers, scheduling, cancellation, race behavior, leaks, queues, or shutdown.
- Also read the primary Nudge owner skill. For T107/fsnotify work, read `$manage-nudge-git-proposals` and its watcher reference; for provider streaming use `$integrate-nudge-codex`; for TUI scheduling use `$build-nudge-tui`; for locks/leases use the owning platform, persistence, storage, or proposal skill.

## Hard guards

- Every goroutine has one owner, one finite exit path, and a join point. Do not fire-and-forget production work or spawn one goroutine per unbounded event.
- The actor/application reducer remains the single writer of canonical Nudge workflow state. Background workers return typed results/events tagged with operation and generation identity; they do not mutate projections or aggregates behind the owner.
- The sender owns channel closure. Use channel close to announce no more values, never as a broadcast that arbitrary receivers may race to perform, and never send on a channel another component can close.
- Every potentially blocking send, receive, acquisition, and wait participates in owner cancellation or has a proven bounded lifetime. A timeout is a safety bound, not a substitute for cancellation or protocol completion.
- Do not hold a mutex across filesystem/network/process/SQLite I/O, a channel operation that may block, a callback, a render, or a call into code with unknown locking. Copy required state, unlock, perform work, then reconcile by version/CAS.
- A mutex protects a named multi-field invariant. Use atomics only for simple independent scalars with documented ordering/ownership; do not build a lock-free state machine for ordinary application state.
- Channels transfer work, values, or ownership. Do not use them to hide shared mutable state, and do not add a channel when a synchronous call or mutex is clearer.
- Every queue is bounded by both count and, for variable payloads, resident bytes. Define full-queue behavior. Coalesce only explicitly lossy state updates; never drop terminal results, approvals, denials, errors, lifecycle transitions, or truth-loss signals.
- Preserve a bounded control/failure path under data saturation. Do not let a full data queue prevent cancellation, terminal failure, or shutdown from being observed.
- Own timers and tickers at the lifecycle root, stop/reset them safely, and keep at most the design-permitted number active. Do not create a timer or goroutine per burst event.
- Go 1.25 `sync.WaitGroup.Go` is suitable for a bounded set of no-error tasks whose functions do not panic. Use explicit typed result/error collection when outcomes matter; never let worker panic crash or silently disappear across an adapter boundary.
- Do not test scheduling with sleeps, retry-until-lucky loops, goroutine-count polling, or permanent leak detectors. Use observable synchronization and `testing/synctest` where its in-process bubble semantics fit.
- Do not create concurrency frameworks, generic worker-pool packages, diagnostic utilities, or tests without a concrete Nudge owner and behavior.
