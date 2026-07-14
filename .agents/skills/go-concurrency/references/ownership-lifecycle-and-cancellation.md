# Ownership, lifecycle, and cancellation

Primary references: [Go memory model](https://go.dev/ref/mem), [`context`](https://pkg.go.dev/context), [Go pipelines and cancellation](https://go.dev/blog/pipelines), and [`sync`](https://pkg.go.dev/sync).

## Structured goroutine ownership

Before starting a goroutine, write down:

- the component whose lifetime contains it;
- the input it consumes and result/failure it produces;
- the resource-policy bounds it obeys;
- what cancels it and how a blocked operation wakes;
- who waits for it to exit; and
- whether shutdown drains, rejects, or abandons queued work.

If any answer is absent, keep the operation synchronous or redesign the owner API. A returned owner may expose `Close`/`Stop` plus `Wait`, or one close operation that cancels and joins; its exact idempotency and error semantics must be documented.

## Start and partial failure

- Acquire required resources before publishing a running owner. If startup partly succeeds, cancel and join started goroutines and release resources in reverse order.
- Do not let a goroutine race constructor return before the owner has stored the handles/state needed to stop it.
- Separate "accepting work", "stopping", and "stopped" states only when callers need those distinctions. Make repeated or concurrent stop calls safe by contract.
- Never use finalizers as lifecycle management.

## Cancellation

- Derive operation context from its owning session/turn/apply/root context. The owner calls the cancel function and always releases it.
- Cancellation means the result is no longer wanted or the lifecycle ended. Deadlines limit external waits. Use both when both semantics exist.
- Preserve cancellation causes when the application needs to distinguish superseded work, user cancellation, shutdown, deadline, provider failure, or stale generation.
- Check cancellation before expensive phases and at bounded chunks inside CPU loops. Do not check on every trivial instruction or ignore it during a potentially blocking send.
- A goroutine must not retain a request context after the request owner has joined it. Long-lived services receive their own lifecycle context, not the context of the constructor call.

## Shutdown order

Use an explicit order appropriate to the owner:

1. Atomically stop or reject new admission.
2. Cancel in-flight operations and unblock producer/consumer waits.
3. Close external resources whose close wakes readers, such as watchers or process pipes, under the owning adapter contract.
4. Let the sending owner close result channels after all sends are complete.
5. Join all goroutines.
6. Return the primary operation or shutdown error without leaking sensitive detail.

Do not wait while holding the lock or actor turn needed by the goroutine being joined. Do not close a shared input channel merely to stop one consumer.

## Results and failure

- A worker result includes the operation/generation/correlation identity needed by the owner to reject stale completion.
- Return exactly one terminal outcome for accepted work, even when cancellation races completion. The owner decides which committed state wins; workers do not publish directly.
- Panics are programmer failures, not asynchronous error transport. Do not recover broadly to claim success; at process/plugin boundaries, convert only under an explicit crash-containment policy and retain safe evidence.
- When multiple workers fail, define first-error, all-errors, or partial-result semantics up front. Cancelling siblings on first failure is valid only if no required terminal evidence is lost.
