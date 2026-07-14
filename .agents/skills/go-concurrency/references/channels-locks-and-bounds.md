# Channels, locks, actors, and bounds

Primary references: [Go memory model](https://go.dev/ref/mem), [Share memory by communicating](https://go.dev/blog/codelab-share), and [`sync/atomic`](https://pkg.go.dev/sync/atomic).

## Choose by invariant

| Need | Prefer | Guard |
|---|---|---|
| One owner serializes canonical workflow transitions | Existing application actor/reducer | Commands in, typed versioned results/events out |
| Transfer a bounded stream or ownership | Channel | One close owner; explicit full/cancel behavior |
| Protect a small in-process invariant | `sync.Mutex` | Keep critical section short; no blocking external work |
| Many reads with demonstrable contention | `sync.RWMutex` | Use only after the read/write invariant remains clear |
| Independent counter/flag/pointer snapshot | `sync/atomic` typed value | Document ordering and never compose unsynchronized fields |
| Wait for a bounded task group | `sync.WaitGroup` | Add/start before wait; errors travel separately |
| One-time initialization/result | `sync.Once`/`OnceValue`/`OnceValues` | Failure/retry semantics must match one-time behavior |
| State predicate under a mutex | Usually an explicit channel/event; sometimes `sync.Cond` | Loop on predicate and prove missed wakeups cannot occur |

Prefer the simplest primitive that makes the happens-before relationship and ownership obvious. Race freedom alone does not make ordering, freshness, or shutdown correct.

## Actor and snapshot pattern

- Keep canonical state inside the application owner. Commands carry immutable inputs and correlation IDs.
- Perform slow Git/provider/SQLite/filesystem work outside the owner turn against an immutable input snapshot.
- Return a typed result tagged with input generation/revision. The owner accepts it only if preconditions still hold, then publishes a new immutable projection.
- Do not let components acquire the actor's internal mutex or mutate maps through shared aliases.

## Channel contracts

- Document producer count, consumer count, ownership of close, capacity in items/bytes, whether ordering is guaranteed, and what happens on full, cancel, and close.
- Use a nil channel only as a local `select` technique with clear lifecycle; never publish a nil channel as ambiguous "disabled" state.
- On receive, handle channel closure explicitly when closure is possible. On send, select on cancellation when the receiver may stop first.
- Do not infer that a buffered send means work was processed. Admission, commit, and terminal completion are distinct states.
- Avoid fan-out/fan-in unless bounded parallelism materially helps and terminal/error ordering is defined. Do not create a goroutine solely to avoid thinking about a blocking send.

## Backpressure and coalescing

For every queue, define:

- maximum item count and resident bytes;
- admission accounting before allocation/decoding where possible;
- block, reject, coalesce, or fail behavior at capacity;
- a reserved control/error route that remains observable under saturation; and
- how shutdown resolves every already-accepted item.

Coalescing is valid for replaceable state such as repeated render invalidations, provider text deltas already preserved elsewhere, or filesystem refresh hints. Keep the latest complete evidence/reason set required by the owner. Never coalesce away approval decisions, proposal/application terminal states, normalized message completion, truth-loss/overflow, or errors that determine readiness.

## Lock discipline

- A lock guards named fields and their invariant; put that statement near the type. Do not expose the lock or copy a value containing one after first use.
- Keep lock order consistent with ADR-012 and the owning skill. Never acquire a higher-level gate while holding a lower-level capacity/owner lock.
- Do not use `TryLock` to avoid defining ownership/order. A failed try-lock is not a lifecycle or availability policy.
- Copy immutable work under the lock, release it, perform blocking work, then reacquire only to compare version/CAS and commit.
- Shard locks only with measured contention and a stable key/order. More locks increase deadlock and invariant risk.

## Atomics

- Use typed atomics for genuinely independent state such as a counter, monotonic flag, or pointer to a complete immutable snapshot.
- Do not publish a pointer before the referenced value is fully initialized and immutable to readers.
- Do not use multiple atomics to represent a multi-field invariant unless a single immutable snapshot pointer makes the state transition atomic.
- Explain non-obvious compare-and-swap loops and prove bounded progress. Ordinary Nudge workflow state belongs to the actor or a mutex, not a lock-free design.
