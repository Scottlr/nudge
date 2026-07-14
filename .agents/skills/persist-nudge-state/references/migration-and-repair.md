# Migrations and exact store repair

Use this reference for T018 migration work and T099 repair. T058 owns repair registry, confirmation, revalidation, and audit; this skill supplies only the durable-store owner effect.

## Migration catalog

- Embed migrations in the binary with explicit version, aggregate owner, canonical checksum, and deterministic order. The applied record stores the exact version/checksum identity.
- Keep migrations additive and forward-only. Never edit an applied migration, silently reset an incompatible database, stamp a version without its postcondition, or infer compatibility from a subset of expected tables.
- Order migrations by the aggregate that owns the data: core review first, then provider lifecycle, snapshots/turn association, proposal/workspace, apply, capacity ledger, reconciliation/repair/cleanup, and later aggregates as their tasks are implemented.
- Each task adds only its owned schema. T018 does not speculate about future aggregate tables, and this skill does not take ownership of T067 artifact-ledger migrations.
- Writable startup may apply known pending migrations through the normal migration runner. Plain `nudge doctor` remains query-only and performs no migration or repair.
- Unknown/newer versions, altered applied checksums, malformed migration metadata, integrity failures, or an unrecognized partial schema block writable startup with a typed health result.
- Use the normal connection policy, foreign keys, writer exclusion, and transactions. If a migration cannot be wholly transactional, design an explicit durable phase marker and idempotent restart contract before release.

## Recognized interrupted-migration repair

- Register only deliberately supported interruption recipes kept beside the embedded migration they interpret. A generic SQL console, arbitrary statement payload, or similarity-based schema fixer is forbidden.
- A T099 plan binds finding/plan ID, health and store revisions, stable database identity, current schema/user version, migration version/checksum, recognized phase, bounded schema fingerprint, recipe version, and exact postcondition. It contains no executable SQL.
- Acquire the normal database writer gate before final inspection. Re-read database identity, catalog/checksum, phase, schema fingerprint, foreign-key state, integrity evidence, and revisions immediately before mutation.
- Execute only the registered idempotent completion path. Prefer the original transactional migration. For a deliberately nontransactional migration, resume only from an enumerated durable phase whose next steps are proven repeatable.
- Record an applied migration only in the same successful transaction or phase protocol that establishes its exact schema/data postcondition.
- After completion, rerun the normal bounded migration inspection and required foreign-key/integrity checks. A mismatch remains explicit; do not cascade into another repair or mark the store healthy heuristically.
- Duplicate execution returns `already_repaired` only when the same exact postcondition holds. A new phase, revision, schema fingerprint, or catalog identity requires a new health finding and plan.
- Persist only stable codes, versions, checksums, phases, durations, and sanitized database identity. Never log product rows, message bodies, patch bytes, prompts, provider frames, credentials, or unrestricted paths.

## Repair boundaries

- T099 owns interrupted migration completion. T100 owns stale session-lease fencing and uses the normal lease manager described in [session-leases.md](session-leases.md).
- T092 permission repair, T093 snapshot rebuild, T094 apply-journal closure, T095 accounting repair, and T101-T103 artifact/workspace handlers remain with their named owners.
- A repair plan never authorizes a pending migration generally, bypasses checksum failure, downgrades a database, mutates another database path, or broadens into a repair-all operation.

## Focused tests

Protect every intentionally supported interruption phase, transactional rollback, checksum/schema/revision drift, exact postconditions, duplicate execution, and crash/retry idempotency using the production migration runner. Do not add arbitrary-SQL hooks, migration smoke commands, diagnostic database scripts, dry runs, temporary validators, or broad schema-repair scaffolding.
