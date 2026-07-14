# Public contracts and versioning

Primary references: [Go module release workflow](https://go.dev/doc/modules/release-workflow), [Module version numbers](https://go.dev/doc/modules/version-numbers), [Keeping modules compatible](https://go.dev/blog/module-compatibility), and [Go 1 compatibility](https://go.dev/doc/go1compat).

## Classify the surface

For every visible change, state which category it belongs to:

| Surface | Nudge compatibility posture |
|---|---|
| `internal/...` Go identifiers | Repository implementation detail; preserve consumers in-tree, not external imports |
| `nudge` commands, flags, help, exit/error codes | Public CLI contract once documented/shipped |
| Config schema, defaults, precedence, paths | Public behavior with explicit validation and migration/deprecation policy |
| SQLite schema and workspace internals | Private representation, but upgrade/data-preservation behavior is a user contract |
| Human TUI/text output | Human-facing product contract; not machine-stable unless explicitly documented |
| JSON/export/file formats | Versioned machine/human interchange contract only when the owning task declares it |
| Keymap, accessibility, privacy, network, safety claims | Documented product contract; changes require their owner and honest release notes |
| Supported OS/architecture/provider rows | Evidence-bound release contract owned by T063/T076 |

Do not accidentally turn log text, raw provider DTOs, database tables, package internals, or incidental human formatting into supported APIs.

## Module and install identity

- T001 resolves the durable module path before creating `go.mod`. Use the same identity in repository links, build metadata, `go install`, documentation, and release automation.
- The Go install form for this application is `<module>/cmd/nudge@<version>` unless an accepted layout change supplies a different command package.
- A future v2+ module follows semantic import versioning (`/v2` in the module path). Do not tag a new major first and repair the path afterward.
- Keep `go 1.25.0` as the declared floor until an explicit compatibility decision raises it; note any floor change as a public installation/build change.

## Versioning and change policy

- Use SemVer-shaped immutable tags. Before v1, make breaking changes intentionally and disclose them; do not use `v0` as permission for silent churn.
- At v1, preserve documented commands, config meaning, persisted upgrade paths, formats, and safety/privacy guarantees unless a deliberate migration/deprecation or new major permits change.
- Prefer additive evolution. For renamed/deprecated CLI/config surfaces, define conflict handling, warning lifetime, migration behavior, and removal version with the owning task.
- Persist version/schema/policy identity where artifacts must be interpreted later. Never reinterpret old immutable evidence under new policy without explicit migration or reevaluation.
- Release notes describe user-visible changes, compatibility impact, support rows, migrations, security fixes, and known limitations. They do not claim unverified performance, reproducibility, signing, or platform support.

## Publication boundary

- A version string or successful build is not a release. `$qualify-and-release-nudge` owns source freeze, support/security/performance evidence, exact packages/checksums, and separately authorized publication.
- Never reuse or move a published tag. Never rebuild under the same version to change bytes. A correction receives a new version.
- Keep pre-release identifiers for actual candidates and do not present them as stable. Package only the human-approved support rows.
- Public package-manager manifests, signing/notarization, SBOM/provenance claims, and reproducibility claims require their own accepted evidence and owner; they are not default OSS polish.
