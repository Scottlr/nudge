# Permissions and event normalization

Consult sections 8.4-8.6 and 11.5-11.8 of the technical design.

## Turn boundaries

- Filesystem discussion leases an immutable `ReviewSnapshot` (accepted-capture-backed or pinned-object-backed), makes it the sole repository-readable root, exposes no writable root, and requires canonical read-containment evidence. Prompt-only discussion exposes zero repository-readable roots and only bounded serialized capture context; if a cwd is required, use an owner-only empty runtime root. Never use the live worktree as cwd.
- Proposal mode requires explicit durable `Request change` intent and exposes only the isolated provider result root under a workspace-write-only `TurnPermissionPolicy`; baseline/admin/destination roots and network remain unavailable.
- `TurnPermissionPolicy` enumerates exact readable roots, writable roots, and narrowly registered provider runtime/tool roots. Deny absolute/out-of-root reads and writes plus symlink, junction, mount, reparse-point, hard-link-alias, or canonical-resolution escapes. Empty repository roots mean no ambient host access.
- Do not broaden `TurnPermissionPolicy` because the model requests it. A one-shot `RuntimeApprovalRequest` is a separate ephemeral object; present its exact command/network/tool scope transiently when interaction is required. V1 denies every provider-turn network request and never adds a read/write root through runtime approval; any future relaxation requires the provider-permission ADR and support-matrix evidence.
- Durable approval/audit/log fields use only the explicit kind/scope class/executable name or hash/network host class/decision/timestamps whitelist. Never persist exact args, tool input, URL/target, environment, or raw request by default.
- Treat runtime approval denial as a provider-turn outcome; preserve the Nudge thread and messages.
- T032 owns the neutral grant-once/deny application intents for one exact pending runtime request and registers T084's distinct runtime command IDs/handlers. `$build-nudge-tui` renders the overlay and focus state; it never reuses proposal approval IDs, handlers, confirmation copy, or domain transitions.

## Prompt contract

Discussion context names the repository-relative path, diff side, selected range, selected text, relevant hunk, target base/head summary, and user concern. State that this is focused review and that no files may be edited.

Application `DiscussionAvailability` composes repository review/anchor/materialization evidence with the snapshot lease, read containment, provider compatibility/account, disclosure acknowledgement, and turn permission. Keep these ownership domains separate so provider reconnect/account changes do not rewrite repository capability evidence.

A 16 MiB frame cap does not bound its fields. Before full DTO/domain/store/display admission, enforce versioned per-scalar, argument-total, map/list-count, and nesting limits from the resource policy. Preserve accepted opaque conversation/turn/item/request refs exactly; never truncate, normalize, or hash one into a different external identity. Reject overflow before it becomes a key, durable value, log field, or terminal label.

Proposal context adds the user-confirmed intent, expected paths as warnings rather than a clipping authorization, isolated workspace permission, prohibition on unrelated formatting/refactors, and notice that Nudge derives the complete patch. Prompts are defence in depth; the sandbox and patch-review gate are authoritative.

Count serialized UTF-8 input before JSON encoding. Mandatory concern/selected evidence is never silently truncated; optional transcript/context compacts only by the documented whole-unit policy with visible omitted counts. Reject oversize input before dispatch.

## Normalized events

Map raw events into provider-neutral conversation, turn, message, command, file-activity, runtime-approval, tool, rate-limit, connection, and account events. Include provider refs plus Nudge correlation IDs.

- Coalesce only adjacent text deltas for the same identity and replaceable monotonic progress. Preserve connection/turn/message boundaries, approvals, errors, and terminal lifecycle events exactly and in order.
- Bound frames, resident queue bytes/items, coalesced chunks, cumulative message/turn content, and presentation subscriptions. If non-coalescible truth cannot be accepted, latch a typed terminal overflow, cancel/drain the connection, and preserve accepted partial transcript state; never silently drop lifecycle truth.
- Persist partial messages with explicit failed/cancelled status after disconnect.
- Treat file events as progress only.
- Log unknown methods at debug level without sensitive payloads and ignore them unless they answer a pending request.
