# Process, authentication, and conversations

Consult section 11 of the technical design and current official Codex app-server documentation before implementation.

## Process lifecycle

1. Resolve an absolute configured executable or sanitized `PATH` entry that excludes empty/relative/current-directory/repository/workspace candidates; retain canonical regular-executable identity and revalidate it immediately before spawn.
2. Spawn `codex app-server` through the managed-duplex runner with explicit args, serialized stdin writes, piped framed stdout, separate bounded stderr tail, cancellation, and process-tree ownership.
3. Allow at most one live connection. Send exactly one `initialize` request and `initialized` notification per connection; a reconnect is a new connection/handshake, never a second handshake on the old one.
4. Query account state using the supported version's schema.
5. Route responses, notifications, and server-initiated requests concurrently through concrete frame/resident/message/turn limits without blocking stdout draining. Non-coalescible saturation terminates the connection with a typed overflow outcome and preserves accepted partial state.
6. On exit, fail active operations with typed errors, retain review state, and offer restart/resume.
7. Shut down pipes and process trees predictably on macOS, Linux, Windows, and WSL.

For a mutating proposal turn, do not hand result state to derivation until the accepted isolation primitive proves all descendants and writable handles are gone. If the boundary cannot quiesce one turn, terminate/empty the entire contained app-server tree and resume on a fresh connection; inability to prove emptiness is non-ready.

The default transport is local stdio JSONL. Do not expose a WebSocket listener for Nudge v1.

## Authentication

- Reuse Codex-managed ChatGPT or API-key authentication.
- Display non-secret account/auth mode and plan information when available.
- Start browser or device-code login only through supported app-server account methods.
- Never open credential storage such as `auth.json`.
- Keep repository browsing usable when Codex is missing, disconnected, or unauthenticated; persist the user's review thread before attempting provider work.
- Plain doctor never starts the process or claims current account truth. Explicit `--live-codex` may initialize/query current status; only a separate user-selected `Connect Codex` action may begin browser/device-code login.

## Mapping

- One Nudge review thread maps to at most one attached Codex conversation for the adapter.
- Keep strong Nudge-local `ProviderConversationID`/`ProviderTurnID` records separate from opaque `ProviderConversationRef`/`ProviderTurnRef` values.
- Persist a local `creating` conversation intent or `prepared` turn (operation/correlation/mode/snapshot/workspace policy included) before the external start call; attach the returned opaque ref in a second compare-and-set phase.
- Retry a remote create only when the live schema proves an idempotency key and the same key is reused. Otherwise an uncertain call records `possible_remote_orphan`, avoids blind duplication, and requires explicit recovery.
- Start, resume, steer, interrupt, and recover turns through version-supported methods.
- Persist a Nudge normalized transcript and provider turn records; do not rely on provider history pagination for startup.
- Define whether a reply during an active turn steers or queues; do not infer the choice from raw protocol support.
- The filesystem-neutral fake scripts calls/events only. Tests that need provider filesystem effects use a separately owned mutator that validates the exact isolated result root and permission policy first.
