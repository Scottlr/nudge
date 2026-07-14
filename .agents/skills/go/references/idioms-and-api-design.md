# Idioms and API design

Primary references: [Effective Go](https://go.dev/doc/effective_go), [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments), [Go Doc Comments](https://go.dev/doc/comment), and [Go 1 compatibility](https://go.dev/doc/go1compat).

## Packages and names

- Give packages short, lowercase names that state one responsibility. A caller should read naturally: `review.Thread`, `workspace.Manager`, `gitcli.Runner`.
- Avoid package-name stutter (`theme.ThemeConfig`), implementation names in interfaces, and meaningless nouns such as `Util`, `Common`, `Data`, or `Thing`.
- Keep unexported names concise in a small scope and make exported names clear without repeating the package name.
- Keep acronyms consistent (`ID`, `URL`, `HTTP`). Prefer positive booleans whose zero value has a safe meaning.
- Split a package when responsibilities, dependencies, or lifecycles differ—not merely because a file is long. Keep files responsibility-focused; do not force one type per file.

## API ownership

- Put an interface in the consuming package. Name it for the behavior the consumer needs, keep it small, and add methods only when a real consumer requires them.
- Return a concrete implementation unless callers require interchangeable behavior. A producer package does not publish an interface solely to make mocking possible.
- Keep Nudge packages internal by default. Export within `internal` only across a real package boundary; unexport within the owning package whenever practical.
- Expose stable data, not implementation machinery. Avoid boilerplate getters when a small immutable value struct is the honest contract.
- Prefer functions for stateless operations and methods when behavior depends on an owned invariant or resource.
- Use a constructor when zero-value use would be invalid or when creation acquires/owns resources. Validate required dependencies immediately and state whether `Close`/`Stop` is required.
- Prefer one explicit config value for a closed setting set. Functional options are appropriate only for independently optional, future-extensible policy and must reject invalid combinations.

## Errors and safe detail

- Add context while preserving the cause: `fmt.Errorf("resolve repository: %w", err)`. Do not restate the same failure at every layer.
- Use `errors.Is` and `errors.As`; never parse or compare human-facing text.
- Create a sentinel only when callers need a stable category. Use a typed error when callers need safe structured evidence. Keep private/sensitive detail out of `Error()` when it may reach CLI, TUI, or logs.
- Translate adapter errors once at the owning boundary. Preserve the original cause for diagnostics while exposing domain/application codes that do not leak source, paths, prompts, credentials, or protocol bodies.
- Log or present an error at the responsible boundary, not at every return site. Avoid both duplicate logging and discarded causes.
- Use `errors.Join` only when multiple independent failures matter. For cleanup, preserve the primary operation error and join cleanup failure only when the caller can act on both.

## Context and lifetimes

- Use `context.Context` for request/operation cancellation, deadlines, and narrowly scoped metadata—not as a dependency bag or optional parameter container.
- Put it first, pass it through blocking boundaries, and check it in potentially long CPU loops at meaningful chunk boundaries.
- Do not replace cancellation with a timeout. Deadlines bound waits; owner cancellation ends work when the result is no longer wanted.
- Do not start background work from a method unless the returned owner exposes a deterministic stop/join contract. Load `$go-concurrency` for that design.

## Mutable data and callbacks

- State whether a caller retains ownership, transfers ownership, or receives an immutable snapshot. Copy maps/slices/bytes when data crosses actors, generations, persistence boundaries, or untrusted adapters.
- Do not retain caller buffers after return unless the API explicitly transfers ownership. Do not return internal maps or slices that can mutate canonical state.
- Keep callbacks synchronous unless documented otherwise. Never call an untrusted callback while holding a lock.
- Prefer typed values and explicit conversion at boundaries over `map[string]any`, reflection, or magic string registries.
- Use generics for reusable algorithms with one meaningful constraint. Do not erase domain types or build collection frameworks around a single call site.

## Resource cleanup

- Place `defer` immediately after successful acquisition when lexical lifetime is correct. In loops, use a helper scope so defers do not accumulate.
- Define close ownership. A sender owns channel closure; the creator usually owns closing files, watchers, transactions, and child-process pipes unless ownership is explicitly transferred.
- Make partial construction unwind already-acquired resources in reverse order.
- Treat `Close`, `Stop`, and `Wait` semantics as part of the API: whether they are idempotent, whether they block, and which error wins must be clear.

## Comments and documentation

- Exported declarations receive idiomatic doc comments when they form a package contract. Start with the declared name and describe behavior, invariants, errors, concurrency safety, and ownership where relevant.
- Package comments explain purpose and boundaries. Internal comments explain why a surprising constraint exists; they do not narrate syntax.
- Examples belong in tests only when they document real supported usage. Do not create example binaries or placeholder programs.
