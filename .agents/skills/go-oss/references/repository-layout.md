# OSS repository layout

Primary reference: [Organizing a Go module](https://go.dev/doc/modules/layout). Nudge's authoritative package ownership is design section 19 and `AGENTS.md`.

## Intended Nudge shape

```text
nudge/
|- cmd/nudge/                 # thin executable entrypoint
|- internal/                  # all application and adapter packages
|- migrations/               # checksummed embedded SQLite migrations when owned
|- docs/                      # design now; focused user/operations/ADR docs as owned
|- features/nudge-v1/         # implementation planning bundle
|- testdata/                  # only shared durable fixtures with a real consumer
|- .agents/skills/            # repository-local agent guidance
|- .github/workflows/         # owned CI/release workflows
|- AGENTS.md
|- go.mod
|- go.sum
|- LICENSE
`- README.md
```

This is a destination map, not bootstrap output. Create each directory/file when the first owning task needs it. Keep package-local fixtures beside their tests unless the evidence is deliberately shared.

## Go layout rules

- `cmd/nudge/main.go` owns process entry only. Put Cobra composition in `internal/cli` and product behavior in its architectural owner.
- `internal` intentionally prevents unsupported external imports. It may contain exported identifiers for cross-package use without turning them into a public module API.
- Do not create `pkg` for perceived professionalism. Add a public package only after identifying its external consumers, compatibility policy, documentation, support cost, and independent value beyond the CLI.
- Keep one module. Multiple modules are justified only by independent consumers, release cadence, dependency boundaries, and compatibility promises—not by directory size.
- Avoid `src`, `lib`, `app`, and generic layer names that obscure the responsibility map. Preserve Nudge's domain/application/adapter ownership rather than imposing a generic clean-architecture template.
- Do not mirror every package under a central test tree. Put black-box package tests in `package_test`, real native/integration evidence with its owning task, and only stable shared fixtures in root `testdata`.

## Root project files

- `README.md`: concise product purpose, honest release status, install/build/start path, supported-surface pointer, and links to maintained docs.
- `LICENSE`: exact owner-approved OSS license text.
- `CONTRIBUTING.md`: add when accepting outside changes; explain setup, task/architecture authority, focused validation, style, and PR expectations without copying internal implementation plans.
- `SECURITY.md`: add only when T064 has an owner-approved private reporting route and supported-version policy.
- `CODE_OF_CONDUCT.md`, issue/PR templates, funding, DCO/CLA, maintainers/CODEOWNERS, and changelog files require an actual governance/maintenance decision. Do not generate them to fill out the repository.
- Prefer standard `go` commands and the Go `tool` directive. Add a Makefile/task runner or script only for a durable multi-step operation that cannot remain clear and portable as documented commands.

## Discoverability

- Lead users to the shortest supported path; lead contributors from `AGENTS.md` to the design, task bundle, and owner skills.
- Keep package names and repository paths aligned with product terminology so code search reveals the owner.
- Do not duplicate the technical design into package docs or README. Link to the authority and document only the stable public or package contract at each surface.
- Keep generated files, binaries, coverage, caches, databases, logs, workspaces, secrets, local config, and IDE state out of Git through a focused `.gitignore` as those artifacts first appear.
