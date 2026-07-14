---
name: integrate-nudge-codex
description: "Implement and review Nudge's provider-neutral review port and Codex app-server adapter: process/protocol, initialization, authentication, conversations/turns, prompts, permissions, runtime approvals, proposal-turn grants, normalized events, backpressure, recovery, Connect Codex, and explicit live health. Use for T026-T032, T037, and T078. TUI components consume neutral projections through the TUI skill."
---

# Integrate Nudge Codex

Keep Codex behind a narrow provider port that serves focused review discussions and isolated proposal turns without leaking app-server concepts into the product.

Primary task family: T026-T032, T037, and T078. T026 defines the application-consumer-owned neutral port; bounded flow and recovery land with the first process/protocol/streaming owners rather than a later hardening task.

## Workflow

1. Read sections 10.8-10.9 and 11 of `docs/Nudge_PRD_Technical_Design.md`.
2. Verify current behaviour against primary Codex app-server documentation and the exact supported installed version. Generate version-matched schemas with `codex app-server generate-json-schema` or `generate-ts` when available.
3. Read only the references needed by the task:
   - [process-auth-and-conversations.md](references/process-auth-and-conversations.md) for process lifecycle, handshake, account state, and thread/turn mapping.
   - [permissions-and-normalization.md](references/permissions-and-normalization.md) for discussion/proposal boundaries, runtime approvals, prompts, normalized events, and flow control.
   - [compatibility-boundary.md](references/compatibility-boundary.md) for generated DTOs, version policy, stable versus experimental API, fixtures, and fail-closed behaviour.
4. Keep raw request/response/notification DTOs in `internal/provider/codex/protocol`; map them into provider-neutral types before application state sees them.
5. Correlate every request, Nudge-local conversation/turn ID, opaque provider ref, operation, immutable snapshot/workspace grant, and review thread explicitly.
6. Keep stdout dedicated to protocol frames and stderr separate. Bound frames, pipe buffers, queues, stored metadata, and rendered deltas.
7. Persist normalized messages and opaque provider references independently of Codex history.
8. Add protocol-fixture or fake-provider tests only for the messages and recovery behaviour introduced by the task.

## Hard guards

- Use `codex app-server`; do not call the model API directly or screen-scrape CLI output.
- Keep a thin `encoding/json` JSONL router with Nudge-owned frame/queue semantics; app-server framing is not a reason to add a generic JSON-RPC framework.
- Check in exact supported-version schema provenance. Hand-maintain only used wire DTOs or pin one deterministic generator with a Go `tool` directive/`go:generate`; generated files carry the standard generated header and handwritten files never claim to be generated.
- Never read, parse, copy, log, or transmit Codex credential files.
- Use official account/login surfaces; keep opaque provider refs separate from Nudge-local strong IDs.
- Keep the `ReviewProvider` interface with its `internal/app` consumer; neutral values/adapters live under `internal/provider`.
- Allow one live connection and one handshake per connection; serialize stdin and fail typed on non-coalescible overflow rather than dropping lifecycle truth.
- Keep the fake provider filesystem-neutral; integration-owned mutators validate isolated roots/policy separately.
- Own runtime-approval and Connect Codex semantics, exact provider scope, and neutral application intents. `$build-nudge-tui` owns only their Bubble Tea focus, layout, rendering, and command-handler projections.
- Enforce discussion read-only and proposal workspace-write/no-network boundaries technically, not only in prompts.
- Keep runtime approval visually and semantically distinct from proposal approval.
- Never add `ApprovePatch` to the provider interface or trust provider file events as a patch.
- Ignore unknown additive notifications safely; fail closed on incompatible permission or method changes.
- Do not opt into experimental API fields without an explicit task and compatibility decision.
