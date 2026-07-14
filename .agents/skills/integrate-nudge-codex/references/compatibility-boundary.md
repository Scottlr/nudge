# Compatibility boundary

App-server is an evolving infrastructure boundary. Keep exact wire shapes version-specific and regenerable.

## Schema workflow

- Record the minimum supported Codex version as an explicit product decision.
- Generate JSON Schema or TypeScript schema artifacts from that version when the executable supports it.
- Convert only required stable DTOs into Go under `internal/provider/codex/protocol`.
- Keep representative request, response, notification, server-request, malformed-frame, and unknown-field fixtures.
- Do not leak raw DTOs beyond the adapter.

## Stable and experimental API

Initialize without `experimentalApi` by default. Add experimental capability only when a concrete required method is unavailable on the stable surface and the task explicitly defines version detection, fallback, user messaging, and removal criteria.

## Failure policy

- Tolerate additive fields and unknown notification methods.
- Reject oversized or malformed JSONL frames without corrupting request routing.
- Treat a valid-size frame as still untrusted: token-bound opaque refs, strings, argument totals, member/list counts, and nesting before constructing full DTO collections. Accepted opaque refs remain exact; overflow is rejected, never truncated/rehashed into another identity.
- Fail pending requests precisely on matching error responses.
- Fail closed when sandbox or approval fields no longer express Nudge's required permission boundary.
- Include installed version, required capability, and safe next action in compatibility errors.
- Do not silently downgrade proposal mode to broader permissions or discussion mode to write access.
