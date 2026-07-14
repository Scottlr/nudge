# Community, CI, and dependency hygiene

Primary references: [Go security best practices](https://go.dev/doc/security/best-practices), [Go vulnerability management](https://go.dev/doc/security/vuln/), and [Managing dependencies](https://go.dev/doc/modules/managing-dependencies).

## Honest public documentation

- Keep README claims limited to implemented behavior and accepted support evidence. State pre-release status plainly until the release owner accepts v1.
- Show the shortest build/install/use path with inert example paths. Link to maintained user/operations guides instead of growing a second manual in README.
- Document non-goals that prevent dangerous assumptions: Nudge does not stage, commit, push, create branches/PRs, add telemetry, or cloud-sync user review data in v1.
- Avoid badge collections, generated contributor lists, roadmap promises, screenshots that immediately rot, and architecture prose copied from tasks.

## Contribution surface

- When outside contributions are accepted, document supported Go versions, setup, formatting/analysis/test commands, task/design authority, package ownership, focused-test policy, PR scope, and how maintainers handle generated or dependency changes.
- Give security reports a private route only after the owner establishes one. Do not invite secrets or vulnerability details into public issues.
- Add issue/PR templates only for recurring information maintainers actually use. Keep them short and do not require ceremonial checklists unrelated to the change.
- Choose CODEOWNERS, maintainer policy, DCO/CLA, code of conduct, and governance explicitly; these are social contracts, not boilerplate.

## CI posture

- T001 owns the baseline: formatting, `go vet`, pinned `staticcheck`, `go test`, and `go build`, across current stable Linux/macOS/Windows plus one focused latest-patched-Go-1.25 compatibility job.
- Do not cross-product every toolchain with every OS. Add native jobs only where OS behavior or an accepted support row needs evidence.
- Keep workflow permissions minimal, pin third-party actions according to the repository's accepted supply-chain policy, and never expose release credentials to untrusted contribution code.
- Make required checks stable and behavior-focused. Do not add smoke binaries, diagnostic scripts, dry runs, temporary validators, custom report schemas, or duplicate Go's dependency/build cache logic.
- Generated-code cleanliness, race, vulnerability, performance, packaging, and native support checks belong only where their owner/task requires them.

## Dependency admission and upgrades

For each direct dependency, record or be able to explain:

- the capability it owns and why standard library/current dependencies are insufficient;
- stable major/import path and compatibility with Go 1.25;
- license and redistribution fit;
- maintenance/release health and relevant security history;
- transitive size, CGo, platform, filesystem/process/network, and runtime effects; and
- the Nudge package/skill that owns its policy boundary.

Use `go list -m`, upstream release notes, module metadata, and `govulncheck` at the owning maintenance/security gate. Inspect reachability and behavior; a scanner result alone is not a safety decision.

- Keep upgrades focused and review `go.mod`/`go.sum` deltas. Do not bundle unrelated updates with feature work.
- Avoid `replace` in releasable state unless the release process records and approves the exact fork/provenance. Do not depend on unpublished local paths.
- Do not vendor by default. Vendoring requires a concrete offline, provenance, legal, or reproducibility decision plus an update policy.
- Automation may propose dependency changes, but it does not auto-merge across compatibility, native behavior, generated schema, protocol, SQLite, TUI, or security boundaries.
