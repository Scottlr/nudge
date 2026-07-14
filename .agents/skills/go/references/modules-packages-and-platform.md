# Modules, packages, and platform code

Primary references: [Organizing a Go module](https://go.dev/doc/modules/layout), [Managing dependencies](https://go.dev/doc/modules/managing-dependencies), [Module release and versioning](https://go.dev/doc/modules/release-workflow), and [Build constraints](https://pkg.go.dev/cmd/go#hdr-Build_constraints).

## Nudge module shape

- Keep one Go module for the installable Nudge CLI unless an independently versioned, independently consumed module becomes a concrete requirement.
- Keep `cmd/nudge` thin: injected build variables, root context/signal setup, dependency composition, execution, safe error printing, and exit selection only.
- Put application code under cohesive `internal/...` packages following `AGENTS.md` and design section 19. Do not add `pkg`, `src`, `lib`, or a public SDK surface for an application that does not promise imports.
- Materialize directories only when their first real owner lands. The design's repository tree is an ownership map, not a request for empty packages, stubs, scripts, or placeholders.
- Keep migrations, documentation, test data, and workflow files at their documented roots. Test fixtures belong near the behavior they protect unless they are deliberately shared, stable cross-package evidence.

## Module and toolchain contract

- Resolve and owner-confirm the durable module path before bootstrap. The `go install` path, repository path, package docs, and release metadata must agree.
- Declare `go 1.25.0`. Run compatibility work on the latest patched Go 1.25 release and current stable Go as specified by T001; verify current releases at [Go release history](https://go.dev/doc/devel/release).
- The `go` directive is the language/module minimum, but production standard-library calls must also exist in Go 1.25. Do not use a newer API merely because the current developer toolchain compiles it.
- T001 does not add a `toolchain` directive or depend on automatic toolchain download. CI selects its explicit Go versions. A later repository-enforced toolchain/download policy requires an accepted ADR and must not silently raise the minimum or add unexpected network behavior.
- Commit `go.mod` and `go.sum`. Use `go mod tidy` only when dependency changes are intentional, and inspect the resulting direct/transitive delta.
- Pin developer tools with Go's `tool` directive at reviewed versions. Do not add installer scripts or wrapper frameworks for standard Go tooling.

## Dependency policy

- Prefer the standard library. Add a dependency only when it owns a substantial, well-defined capability selected by the design or materially reduces correctness risk.
- Import the focused library directly behind the Nudge-owned boundary that carries product policy. Do not wrap a library merely to hide its name, and do not leak dependency DTOs across domain/application boundaries.
- Before adding or upgrading, check stable major/import path, Go floor, license, maintenance, CGo, transitive size, platform support, network behavior, file/process access, and security history relevant to the capability.
- Pin stable majors. Do not adopt alpha/prerelease majors or a generic framework when Nudge needs one narrow seam.
- Let each owner skill govern library semantics: platform for Cobra/TOML/`slog`, TUI for Charm/Chroma/ANSI, persistence for SQLite, Git proposals for `fsnotify`, and Codex integration for the app-server protocol.

## Platform boundaries

- Put policy in neutral files and isolate true OS mechanics in small `_windows.go`, `_unix.go`, or exact-OS files. Build tags belong only where the implementation or supported contract genuinely differs.
- Keep platform-specific types behind neutral consumer-owned ports. Do not scatter `runtime.GOOS` switches across domain or application code.
- Use the filename suffix alone when sufficient. When a build constraint is needed, place `//go:build` at the top with the required blank line and keep the expression as narrow as the supported evidence.
- Native support requires behavior on an approved native runner. Cross-compilation proves compilation, not path, permissions, locks, signals, process-tree, watcher, symlink, terminal, or SQLite behavior.
- Keep CGo disabled only when the verified dependency graph and release row support it; do not infer it from intent.

## Generated code

- Commit generated outputs only when the owning task requires reproducible source artifacts. Include the standard generated-file header and record the authoritative input/provenance.
- Place `//go:generate` with the owning package only when maintainers need a stable regeneration command. Pin its tool and make drift reviewable.
- Generated code does not bypass package boundaries, compatibility review, limits, or safe-data rules.
