# Testing and maintenance

Primary references: [`testing`](https://pkg.go.dev/testing), [`testing/synctest`](https://pkg.go.dev/testing/synctest), [race detector](https://go.dev/doc/articles/race_detector), [fuzzing](https://go.dev/doc/security/fuzz/), and [Go vulnerability management](https://go.dev/doc/security/vuln/).

## Tests worth keeping

- Protect requested behavior, invariants, compatibility surfaces, edge cases that caused or could realistically cause a regression, and security/resource boundaries owned by the task.
- Prefer a small direct test at the lowest package that owns the behavior. Add cross-package or native integration coverage only when the contract cannot be proven below that boundary.
- Use table-driven tests when cases exercise the same contract and share meaningful setup/assertions. Do not hide distinct scenarios behind a giant table or opaque helper DSL.
- Choose package-internal tests when private invariants need direct coverage and external-package tests when the public package contract is the subject. Avoid duplicating both views.
- Keep fixtures minimal, inert, deterministic, and owned. Do not create demo applications, smoke tests, diagnostic scripts, dry runs, temporary validators, or generic test harnesses.

## Determinism and concurrency

- Inject clocks, randomness, IDs, process runners, and external adapters at their consumer boundary when deterministic behavior requires it; do not expose test-only production hooks.
- Never coordinate a test with `time.Sleep`. Use observable state, channels, barriers, controlled clocks, deadlines only as a final failure bound, and Go 1.25 `testing/synctest` for supported in-process time/goroutine semantics.
- Run the race detector for packages whose task introduces or changes material concurrency and at the native/release gates that own that evidence. Race-clean execution is necessary, not proof of lifecycle correctness.
- Test shutdown, cancellation, bounded queues, overflow/backpressure, and ownership at the behavior boundary when they are part of the task. Load `$go-concurrency` for the design.

## Fuzzing and benchmarks

- Fuzz only durable parsers or trust boundaries where generated inputs can expose meaningful crashes, hangs, allocation bugs, or unsafe acceptance. Seed with representative valid and invalid cases and preserve only useful regressions.
- Benchmarks belong to named owner hot paths or T074's versioned workload. Report allocations and workload identity where relevant; do not add benchmarks as performance theatre or turn them into unofficial support evidence.
- Avoid asserting exact timings in ordinary tests. Assert bounds, cancellation, ordering, or controlled-clock behavior instead.

## Proportional validation

- Format production Go with `gofmt` (and imports with the repository's chosen standard tool if one is established).
- Run focused affected-package tests first. Widen to `go test ./...`, `go vet ./...`, pinned `staticcheck`, the race detector, native checks, or `govulncheck` only when the task, changed boundary, or release/security gate calls for them.
- Inspect failures rather than adding retries or sleeps. Do not weaken assertions to accommodate nondeterminism that the production contract should remove.
- Keep CI commands portable and meaningful. Do not multiply matrices, duplicate standard tooling, or invent validation-report frameworks.

## Dependency maintenance

- Upgrade deliberately for a required feature, compatibility need, bug, or security fix. Read upstream release notes and inspect API, platform, Go-floor, license, CGo, and transitive changes.
- Keep unrelated upgrades out of a feature task. Preserve `go.mod`/`go.sum` reviewability and avoid dependency churn for its own sake.
- `govulncheck` is part of the owned security/release review, not a substitute for understanding reachable behavior or permission boundaries.
