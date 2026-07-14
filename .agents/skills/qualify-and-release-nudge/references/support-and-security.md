# Security disposition and final support truth

## T064: candidate-bound security gate

- Freeze the source tree/commit, dependency checksums, Go/tool versions, generated Codex schema provenance, capability/Git/resource policy versions, accepted isolation/read-containment ADRs, candidate matrix, native required checks, and T074 workload result under review.
- Review executable/path trust; raw paths and aliases; Git helpers/hooks/filters/network/conversion policy; provider roots/descendants; immutable provenance; locks/epochs/journals and external-writer races; repair/cleanup authority; privacy/log/export boundaries; queues/resource/storage pressure; and exact proposal application.
- Run the standard dependency vulnerability analysis (`govulncheck ./...`) and inspect relevant direct/transitive dependency changes. Do not create a scanner framework or probe credentials/live accounts.
- Classify findings critical/high/medium/low/informational. Any unresolved critical/high finding on an advertised path blocks the candidate. Medium/low needs a named owner and remediation, support qualification, or explicit owner-accepted rationale.
- Record actionable findings as focused owner issues/changes and record the release disposition in the release issue/PR. T064 routes fixes; it does not implement them, weaken limits, or publish a generic AI-written report.
- Re-review the security-relevant delta after a fix or support change. Material candidate changes invalidate the prior disposition; unavailable tooling or required evidence remains unknown/blocking, never clean.
- Add `SECURITY.md` only when the repository owner supplies a real private disclosure route and supported-version policy.

## T063: final support matrix

- Start from the T075 rows plus current T074 workload, T076 native required checks, T064 disposition, and T078 live-health semantics. Resolve every row to exact `supported`, `qualified(reason/code)`, or `unsupported(reason/code)` status.
- Require explicit human approval before a row is advertised or T052 packages it. A green build, cross-compilation, generic test run, or version string is not support approval.
- Record exact GOOS/GOARCH, minimum OS, native runner, terminal/filesystem baseline, Git versions/object formats and primitives, Codex schema/capability range, Go toolchain, SQLite driver, isolation primitive, and release status.
- Keep capability axes independent: review, anchor, immutable materialization, canonical read containment, rootless discussion, filesystem discussion, proposal, and apply. A safe review path may remain supported when mutation is qualified or unavailable.
- Treat Codex compatibility as generated schema plus required method, permission, and capability semantics, not version comparison alone. Unknown incompatible semantics fail closed.
- Doctor/version surface the accepted row and stable degraded/unsupported reasons without mutating state. Do not claim reproducibility, signing, or broader OS/provider behavior beyond accepted evidence.

## One advertised source

The accepted `release/support-matrix.json` drives T076 parity, doctor/version projection, rendered support documentation, installation claims, and T052 target selection. No workflow, document, CLI surface, or package configuration owns a second target list.

When an external protocol, dependency, tool, or platform fact may have changed, consult current primary documentation before finalizing a row. If required evidence is unavailable, keep the row blocked or qualified rather than filling it from memory.
