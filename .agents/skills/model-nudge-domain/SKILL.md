---
name: model-nudge-domain
description: "Model Nudge domain entities, application commands and events, composed workflow status, consumer-owned ports, persistence contracts, cross-cutting policy contracts, and canonical state. Use for T002-T004, T007, T017, T020-T021, T034, T050, T054, and T070, or related internal/domain and internal/app contract work. Do not implement SQLite, raw Git, Codex protocol, storage adapters, or terminal rendering here."
---

# Model Nudge Domain

Use the product vocabulary and state model consistently while keeping the domain independent of adapters and frontend mechanics.

Primary task family: T002-T004, T007, T017, T020-T021, T034, T050, T054, and T070. Other skills may consume these contracts but do not redefine them.

## Workflow

1. Read `docs/Nudge_PRD_Technical_Design.md`, especially sections 5, 9, 10, 14, 17, and Appendices A-D. Treat it as authoritative.
2. Read only the references required by the task:
   - [terminology-and-invariants.md](references/terminology-and-invariants.md) for names, distinctions, statuses, and hard product rules.
   - [commands-events-and-state.md](references/commands-events-and-state.md) for reducer ownership, commands, events, snapshots, concurrency, and port direction.
   - [persistence-contract.md](references/persistence-contract.md) for durable ownership, entity relationships, transactions, and restoration.
3. Identify the product entity and invariant that own the requested behaviour before selecting a package.
4. Define strong IDs and explicit enum dimensions. Keep impossible transitions out of constructors and application command handlers.
5. Keep interfaces with the application consumer; let Git, Codex, SQLite, watchers, and the TUI implement or consume them at adapter boundaries.
6. Route every state mutation through the single application state owner. Return typed results with operation, correlation, and target-generation identity.
7. Persist the minimum durable state needed to restore behaviour independently of provider history. Use an application journal for operations that cross SQLite and filesystem mutation.
8. Add only targeted tests that protect state invariants, idempotency, restoration, or a realistic regression.

## Hard guards

- Keep review session, review thread, local provider conversation/turn records, opaque provider refs, review snapshot, proposal intent/version, review target, edit destination, and target generation distinct.
- Use distinct opaque string ID types plus injectable `Clock`/`IDSource`. The production source uses Go 1.25 `crypto/rand.Text()` and treats its output as opaque; do not add UUID/ULID/sortable-ID dependencies or assert a fixed alphabet/length.
- Keep `AppError` safe/private fields explicit and implement `Unwrap` for `errors.Is`/`errors.As`. Normal error text never exposes private detail or nested causes.
- Compose resolution, conversation, proposal, anchor, and read state; never create a combined mega-status enum.
- Keep ready proposals immutable and apply-once.
- Keep resolution, rejection, patch approval, and runtime approval orthogonal.
- Keep local working-tree snapshots fingerprinted; never fabricate commit object IDs.
- Keep local provider IDs strong, external provider refs opaque/separate, and raw protocol DTOs outside domain/application types.
- Keep source-generation provenance separate from destination global constraints and touched-path preconditions.
- Keep store pages/snapshots bounded by item and encoded-byte caps; never load every thread/message/blob into canonical state.
- Own application-facing persistence interfaces and semantics only. `$persist-nudge-state` owns SQLite schemas, migrations, queries, WAL behavior, and lease implementation; `$manage-nudge-owned-storage` owns capacity/artifact accounting adapters.
- Keep UI components on IDs and immutable projections, not authoritative entity copies.
- Keep one inert `internal/presentation` terminal-text projection for invalid UTF-8, controls, escapes, bidi, tabs, and newlines. Canonical IDs/paths, JSON/Markdown encoders, persistence, and logs never reuse display text as identity.
- Do not create a generic `utils` package, global clock/ID singleton, or speculative base framework. Prefer small standard-library values and the package that owns the contract.
- Keep the full technical design as the specification. Do not duplicate or reinterpret its entire schema in this skill.
