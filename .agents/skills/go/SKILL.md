---
name: go
description: "Write and review idiomatic production Go for Nudge. Use for every change to Go source, go.mod/go.sum, package or API design, errors, contexts, tests, refactors, tooling, or code review. This is the Go 1.25 language baseline and must be loaded alongside the primary Nudge owner skill; add $go-concurrency or $go-oss only when their triggers apply. It does not own product or architecture semantics."
---

# Go Engineering

Use clear, unsurprising Go while preserving the package, domain, and safety contracts owned by Nudge's design and primary feature skill.

## Workflow

1. Read `AGENTS.md`, the task file, and the one primary Nudge owner skill before designing the Go change.
2. Confirm the compatibility contract: `go.mod` declares `go 1.25.0`; production code must compile with the latest patched Go 1.25 toolchain and must not depend on standard-library APIs introduced later.
3. Name the owning package, its consumer, and any data, resource, or goroutine lifetime before adding types or dependencies.
4. Design the smallest concrete API that makes ownership, zero-value behavior, errors, cancellation, mutability, and cleanup explicit.
5. Implement formatted code with comments only where they state a contract, exported API, safety reason, or non-obvious decision.
6. Add or modify only focused tests that protect the requested behavior or a realistic regression. Validate the affected package first, then widen only as required by the task or owning release gate.

## Load the relevant references

- Read [idioms-and-api-design.md](references/idioms-and-api-design.md) for naming, interfaces, constructors, errors, context, ownership, cleanup, and documentation.
- Read [modules-packages-and-platform.md](references/modules-packages-and-platform.md) for module layout, `internal`, dependency policy, build tags, generated code, and Go-version compatibility.
- Read [testing-and-maintenance.md](references/testing-and-maintenance.md) when changing tests, time-dependent behavior, fuzzing, benchmarks, static analysis, or dependency maintenance.
- Load `$go-concurrency` as well when goroutines, channels, locks, atomics, timers, cancellation coordination, queues, streaming, or backpressure materially affect correctness.
- Load `$go-oss` as well when changing public repository structure, module identity, contributor-facing documentation, compatibility promises, CI policy, or releases.

## Hard guards

- Keep packages cohesive and responsibility-named. Do not create `utils`, `common`, `misc`, `helpers`, `types`, `interfaces`, `api`, `manager`, or `service` packages as generic dumping grounds.
- Define small behavior-oriented interfaces with the consumer that needs substitution. Accept interfaces where useful and normally return concrete types; do not mirror every implementation with a producer-owned interface.
- Prefer useful zero values. Add a constructor only when it establishes invariants, owns a resource/lifecycle, or makes invalid construction impossible.
- Prefer a typed configuration struct over functional options for a small fixed set of required settings. Use options only when they make a genuinely extensible optional surface clearer.
- Wrap causes with `%w`, branch with `errors.Is`/`errors.As`, and expose sentinel or typed errors only for stable programmatic decisions. Never match error strings.
- Put `context.Context` first on blocking or I/O operations, never pass `nil`, and do not store it in a struct unless that object explicitly owns the represented lifecycle.
- Make slice, map, buffer, iterator, and callback ownership explicit. Copy mutable data at trust or lifetime boundaries; do not return aliases that let callers mutate canonical state.
- Avoid package-global mutable state, hidden `init` side effects, reflection-heavy registries, and speculative abstraction. Use generics only when one coherent constraint removes real duplication without obscuring domain meaning.
- Return errors for user input, external systems, cancellation, and recoverable runtime failure. Reserve panic for impossible programmer invariants or startup composition failures that cannot be represented honestly.
- Never hold resources past their owner. Place cleanup next to acquisition, define who closes, preserve the primary error, and make repeated close/stop behavior explicit where callers can race or retry.
- Do not introduce post-Go-1.25 language or standard-library dependencies into production code. A newer CI toolchain is compatibility evidence, not permission to raise the minimum silently.
- Do not create or run smoke tests, diagnostic scripts, dry runs, temporary validators, demo programs, or broad test churn. Test Nudge behavior, not the standard library or a dependency's implementation.
