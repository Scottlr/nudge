---
name: go-oss
description: "Shape and maintain Nudge as an idiomatic open-source Go repository. Use in addition to $go and the primary Nudge owner for module/repository layout, public CLI or compatibility surfaces, README/LICENSE/CONTRIBUTING/SECURITY policy, contributor experience, CI/tooling/dependency policy, SemVer, release metadata, or go-install distribution. Nudge is an installable CLI, not a public Go library; release and product semantics remain with their focused owner skills."
---

# Go OSS Repository

Keep the public repository small, honest, navigable, and reproducible for both users and contributors without manufacturing a public Go API or cargo-cult project scaffolding.

## Workflow

1. Identify the product shape and audience. Nudge ships a Go CLI; its supported public surface is the binary, documented behavior, configuration/data compatibility, packages, and release evidence—not imports from `internal`.
2. Separate current public promises from implementation detail and aspirational design. Never make unfinished tasks appear released or supported.
3. Place code and project files in the smallest conventional layout that reflects real owners. Add a directory, workflow, template, or policy file only when it is used now.
4. Keep the durable module path, install command, repository URL, release tags, build metadata, and documentation mutually consistent.
5. Make the newcomer path direct: understand the product, build the CLI, run the meaningful checks for a change, find architecture/task guidance, and report security issues through a real route.
6. Hand packaging, support claims, candidate evidence, and publication to `$qualify-and-release-nudge`; this skill shapes the public contract but grants no release authority.

## Load the relevant references

- Read [repository-layout.md](references/repository-layout.md) for the module tree, `cmd`/`internal`, project files, contributor navigation, and anti-patterns.
- Read [public-contracts-and-versioning.md](references/public-contracts-and-versioning.md) when changing module identity, CLI/config/output behavior, compatibility, versioning, install paths, or releases.
- Read [community-ci-and-dependency-hygiene.md](references/community-ci-and-dependency-hygiene.md) for README/community files, CI, dependency review, vulnerability handling, and maintenance policy.
- Also load `$build-nudge-platform` for CLI/config/module bootstrap work and `$qualify-and-release-nudge` for support, documentation, packaging, security qualification, or publication tasks.

## Hard guards

- Use one module with `cmd/nudge` and responsibility-named `internal/...` packages. Do not add `pkg`, `src`, a public SDK, multiple modules, or exported extension points without a concrete independently supported consumer and versioning decision.
- Do not copy a generic Go project layout. No empty package tree, placeholder ADRs, unused `scripts`, sample app, `examples`, badge wall, generated architecture tour, generic Makefile/task runner, or community template collection.
- Keep `cmd/nudge` a thin composition root. Repository familiarity must not move domain, Git, provider, persistence, TUI, or repair logic into `main` or Cobra handlers.
- Treat public CLI commands/flags, stable exit/error codes, config keys/defaults/precedence, documented persisted upgrade behavior, exported human-readable formats, install path, privacy claims, and supported-platform statements as deliberate compatibility contracts. Internal Go APIs, SQLite tables, and workspace layouts remain implementation details; only their promised migration/data-preservation outcomes are public.
- Do not describe planned behavior as present. Pre-v1 status allows intentional change, not undocumented breakage or false support.
- The repository/module path and `go install <module>/cmd/nudge@<version>` must agree. Follow Go semantic import versioning before any future v2 module tag.
- Use SemVer-shaped tags and exact immutable source for releases. Do not publish, retag, replace assets, claim support, signing, reproducibility, or package-manager availability outside the release skill and explicit user authority.
- Add only an owner-selected license. Add `SECURITY.md` only with a real private disclosure route and supported-version policy; add governance/contribution policies only when the project will uphold them.
- Keep CI proportional: meaningful format, analysis, test, build, compatibility, and native platform checks owned by T001/T076. Do not create a matrix explosion, bespoke validation framework, diagnostic script, smoke test, or duplicated standard tooling.
- Review every dependency for necessity, stable major/import path, license, maintenance, Go floor, CGo, transitive/platform/network effects, and relevant vulnerabilities. Avoid generic frameworks and unrelated upgrade churn.
- Do not vendor dependencies, commit binaries/build caches, or leave release-affecting `replace` directives without an explicit offline, legal, provenance, or release requirement.
