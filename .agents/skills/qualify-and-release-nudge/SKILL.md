---
name: qualify-and-release-nudge
description: "Qualify, document, package, and release Nudge through its evidence-bound v1 gates. Use when implementing or reviewing T052-T053, T063-T064, T074-T077, or T082; defining candidate versus final support rows; collecting native or performance evidence; conducting the candidate-bound security review; preparing exact checksummed packages; or performing a separately authorized immutable publication. Route product defects back to their owning task instead of weakening evidence or behavior."
---

# Qualify and Release Nudge

Carry one exact release candidate from meaningful product behavior to truthful public artifacts without turning evidence collection into implementation authority.

## Workflow

1. Read `docs/Nudge_PRD_Technical_Design.md`, `AGENTS.md`, and the task file that owns the current stage. Treat accepted ADRs and implemented product invariants as release inputs, not negotiable gate settings.
2. Load only the reference needed for the stage:
   - [evidence-and-gates.md](references/evidence-and-gates.md) for T053, T074, T075, and T076.
   - [support-and-security.md](references/support-and-security.md) for T064 and T063.
   - [docs-packaging-publication.md](references/docs-packaging-publication.md) for T077, T052, and T082.
3. Freeze the exact source, policy, protocol schema, support matrix, workload, fixture, runner/tool, documentation, and required-check identities consumed by the stage. Treat stale, missing, skipped, unavailable, or mismatched required evidence as non-passing.
4. Before T053 or T074, require T115's candidate-bound integrated production-TUI visual acceptance for the same relevant source. Route any visual defect back to `$build-nudge-tui`; do not reinterpret screenshots as release test output.
5. Preserve the stage boundary: candidate rows are provisional, native/performance jobs produce evidence, security review produces a disposition, T063 plus human review finalizes advertised support, T077 documents it, T052 packages it, and T082 alone publishes it.
6. On a failed gate, identify the first failing behavior or provenance boundary and route a focused fix to its implementation owner. Rerun the unchanged gate after the fix; never add a release-only fast path or relax a limit, workload, invariant, or evidence rule to make it pass.
7. Add or run only tests and evidence that protect real release behavior. Do not create smoke tests, diagnostic scripts, dry runs, temporary validators, demo applications, fake release commands, or broad duplicate suites.

## Hard guards

- Protect the one deterministic production review-to-approval journey in T053; a launch or compile success is not release evidence.
- T115 must accept the integrated production TUI before T053/T074 freeze cross-boundary or performance evidence. Its sanitized captures remain visual-review evidence, not CI artifacts, golden tests, package contents, or a substitute for behavior checks.
- Keep T075 candidate rows non-advertised. T076 cannot accept support, T064 cannot implement fixes, and T063 cannot approve a row without current native, workload, and security evidence plus explicit human review.
- T076 uses ordinary required GitHub checks and bounded standard output plus T074's small release result. Do not build a parallel test registry, per-test manifest/report encoder, workflow validator, or evidence DSL.
- Keep correctness and deterministic resource bounds distinct from timing qualification. Slower hardware never justifies unsafe or unbounded behavior.
- Build T052 archives for every and only the human-approved T063 release rows. Bind every archive to exact source, toolchain, protocol schema, support matrix, documentation, content, size, and SHA-256 identities.
- Never claim signing, notarization, reproducibility, package-manager distribution, support, or publication without the corresponding accepted evidence and explicit authority.
- Treat T082 as a separate external mutation. Publish the already-reviewed immutable assets only after protected human approval; do not rebuild, rename, replace, append, delete/recreate, or silently retry uncertain remote state.
- Keep credentials, source excerpts, prompts, provider frames, patches, user paths, environment dumps, and user state out of release evidence, logs, manifests, and packages.
- T077 owns only a concise README, one user guide, and one operations guide. Do not generate topical stubs, architecture tours, badges, screenshots, or feature catalogs before they have a real owner and implemented behavior.

## Ownership

- T053: meaningful cross-boundary business regression.
- T074: versioned performance/resource workload and evidence contract.
- T075: provisional candidate-row and evidence-requirement contract.
- T076: native row-bound evidence collection only.
- T064: candidate-bound security review and release disposition only.
- T063: final machine-readable support matrix and human acceptance.
- T077: accurate user/operator documentation only.
- T052: exact local packages, checksums, and immutable candidate manifest only.
- T082: separately authorized publication and public read-back only.
