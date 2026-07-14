# Concurrency testing

Primary references: [`testing/synctest`](https://pkg.go.dev/testing/synctest), [race detector](https://go.dev/doc/articles/race_detector), and [Go memory model](https://go.dev/ref/mem).

## Test the contract, not the scheduler

- Assert observable ordering, bounded admission, cancellation, terminal results, stale-result rejection, and deterministic shutdown. Do not assert that an unspecified goroutine "usually runs first".
- Use channels, barriers, controlled adapters, and owner-visible state to place a test at meaningful protocol points.
- Use a test deadline only to stop a hung test and report failure. Never use a timeout or `time.Sleep` as the event that makes the test pass.
- Ensure every goroutine started by a test exits before the test returns. Register cleanup at acquisition, but still assert the production stop/join behavior when it is the subject.

## `testing/synctest`

- On the Go 1.25 baseline, use `synctest.Test` for self-contained in-process concurrent behavior whose goroutines, channels, timers, and tickers can live inside one bubble.
- Use `synctest.Wait` to reach a durable-blocking point before inspecting shared state. The synchronization it provides also establishes the memory ordering needed by the inspection.
- Keep external processes, real filesystem watcher backends, real network I/O, and unrelated goroutines outside a synctest bubble; inject controlled ports for owner/coordinator tests.
- Synctest proves the modeled in-process lifecycle and timing behavior. It does not replace narrow native adapter coverage where OS semantics are the contract.

## Race detector

- Run `go test -race` on the affected concurrent package and its meaningful integration boundary when a task introduces or changes shared access. Native/release tasks own broader race evidence.
- A race report is a correctness failure; fix ownership or synchronization rather than suppressing it. A race-clean run does not prove queue bounds, deadlock freedom, fairness, cancellation, or goroutine cleanup.
- Do not use `runtime.Gosched`, probabilistic loops, `GOMAXPROCS` tricks, or repeated test execution as the primary assertion mechanism.

## High-value scenarios

Add only scenarios that protect the task's real contract, such as:

- cancellation racing a successful terminal result, with exactly one accepted outcome;
- producer saturation while control/failure remains observable;
- close during a blocked send/receive, with all owned goroutines joined;
- continuous events hitting quiet-delay and maximum-delay bounds without queue growth;
- stale generation completion rejected after newer work commits;
- timer/ticker start-stop-reset without duplicate chains;
- partial startup unwinding already-created workers/resources; and
- lock/CAS behavior preserving the named multi-field invariant.

Avoid generic leak-test dependencies and global goroutine-count assertions: the process and runtime own unrelated goroutines. Prove cleanup through the owner's join/closed contract and the observable resource being released.
