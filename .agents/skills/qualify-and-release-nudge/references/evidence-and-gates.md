# Meaningful journey, workload, and native evidence

## Evidence flow

Keep one directional flow without creating a parallel evidence platform:

```text
T075 provisional candidate rows
  + T053 one production journey
  + T074 versioned release workload result
  -> T076 ordinary required native checks
  -> T064 security disposition
  -> T063 human-approved final support rows
```

No consumer owns a second platform list, reinterprets a stale check, or promotes a candidate row outside its stage.

## T053: one meaningful production journey

- Exercise production application use cases, Git/process/store/workspace adapters, and the headless Bubble Tea root model. Replace only the live provider, provider-sandbox mutator, real terminal, clock, and IDs with deterministic test fixtures.
- Prove nested launch, immutable local capture, tree/diff selection, one anchored thread, streamed discussion, restart restoration, explicit isolated proposal, complete paged review, exact-version approval, exactly-once application, index/ref preservation, reconciliation, a touching external edit, and re-anchor/stale behavior.
- Use one small committed text fixture. Wait on typed events rather than sleeps and assert stable identities plus the user-worktree/index boundary at every irreversible transition.
- Do not duplicate rejection, zero-delta, branch/commit, binary/special-file, capacity, protocol, or journal-failpoint matrices already protected by focused owner tasks.
- Do not add production APIs solely for observation, a demo binary, live Codex/network calls, or screenshot-only proof.

## T074: one versioned release workload

- Bind the workload to source, resource-policy version, compact fixture definition, Go/Git/SQLite versions, platform/reference facts, timer boundaries, sample policy, and cold/warm state.
- Reuse production paths plus standard Go tests/benchmarks in their owning packages. Fixture generation stays inside test temporary storage; do not ship a benchmark app, profiler command, or load generator.
- Preserve the public targets from the design: visible shell within 300 ms, typical changed list within 1 second, and ordinary interaction/frame p95 within 16.7 ms on the declared native reference class.
- Keep deterministic page/range/queue/artifact/window/WAL/cancellation/capacity bounds separate from timing qualification; slower hardware does not justify unsafe behavior.
- Produce one small bounded release result containing the workload/source/policy/tool/runner identity, samples/percentiles, hard-bound outcomes, and qualification. Use standard Go output for detail; do not invent a generic evidence schema.
- A relevant source, policy, fixture, dependency, tool, platform, or workload change invalidates the result. Do not discard valid outliers or retry until green.

## T075: one provisional support source

- Keep `release/support-matrix.json` as the sole candidate and eventual final target list. A narrow release-only decoder may validate it; production domain/application code does not own support-matrix types.
- Candidate rows name stable row ID, GOOS/GOARCH, native runner, minimum OS/tool prerequisites, required owner capability checks, WSL treatment, and explicit provisional disposition/reason.
- T076 derives its job matrix from this file. T063 updates/finalizes the same source after native, workload, and security evidence; T052 packages only accepted final rows.
- Name meaningful owner checks/contracts, not brittle individual test names or a custom evidence DSL. Unknown prerequisites use an explicit blocked reason, never `TBD` or an invented claim.
- Treat WSL as Linux only when Nudge, Git, Codex, paths, and repository all live in the same WSL environment.

## T076: ordinary native required checks

- Derive the GitHub Actions matrix from `release/support-matrix.json`; workflow YAML does not carry another target list.
- On each exact native runner, run normal formatting, vet/static analysis, build/tests, required owner-native cases, T053, and T074 as declared by the row.
- Use ordinary GitHub required-check conclusions and bounded redacted standard output. Upload only the small T074 release result needed for support decisions; do not build a test-case registry, custom per-test manifest, report encoder, or workflow validator.
- Cross-compilation, emulation, compile-only work, or launch-only jobs cannot prove native path, lock, process-tree, filesystem, SQLite, terminal, isolation, or provider behavior.
- Keep permissions least-privilege, use pinned actions, no live provider/account action or repository secrets, and bounded timeout/retention.

## Gate failure handling

Route a failure to the smallest owning behavior task/package. Add a focused regression only when it protects the real defect, land the owner fix, and rerun the same required check/workload. Never add a release-only fast path, patch production behavior inside T064, hide a failed check, or weaken a bound/support claim to make the gate pass.
