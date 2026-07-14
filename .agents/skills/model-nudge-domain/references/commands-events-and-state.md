# Commands, events, and canonical state

Use this reference when adding application commands, events, operations, snapshots, or ports. Consult sections 9.4-9.7 and Appendix D of the technical design.

## Ownership

- Let one reducer or actor own mutable `State`.
- Run Git, provider, store, watcher, highlighting, and workspace operations in cancellable goroutines.
- Return typed results through the reducer mailbox with `OperationID`, correlation ID, and relevant `TargetGeneration`.
- Discard superseded completions rather than letting stale async work overwrite current state.
- Publish immutable `AppSnapshot` revisions to frontends.
- Page large code content and transcript history separately with snapshot-revision checks.

## Naming

Use present-tense intent for commands: `CreateThread`, `ReplyToThread`, `RequestProposal`, `ApproveProposal`, `RejectProposal`, and `ResolveThread`.

Use completed facts or emitted stream facts for events: `ThreadCreated`, `MessageDelta`, `ProposalReady`, `TargetReconciled`, and `OperationFailed`.

Keep raw methods such as `thread/start` and `turn/start` inside the Codex adapter.

## Port direction

- Define `GitRepository`, `ReviewProvider`, `ReviewStore`, `ProposalWorkspaceManager`, `Clock`, `IDGenerator`, and `FileWatcher` around application needs.
- Do not add `ApprovePatch` to `ReviewProvider`; Nudge owns patch approval.
- Keep `FileDiff` and related core models in a neutral domain package so the Git port does not expose adapter-owned types.
- Keep the future desktop boundary at product commands, events, snapshots, error codes, IDs, and capabilities. Do not invent universal widget abstractions.

## Concurrency rules

- Bound queues and protocol frames.
- Coalesce consecutive message deltas before rendering or high-frequency persistence.
- Permit at most one active provider turn per review thread.
- Permit at most one apply operation per review session and require cross-process protection for one edit destination.
- Permit one writable process per durable review session. Acquire an OS session lease before writer claim, carry `SessionWriteGuard{SessionID,LeaseID,WriterEpoch,ExpectedRevision}` through every mutation, and freeze writes on lease/revision loss.
- A compatible session whose lease is held opens only by explicit read-only choice or as a distinct new session. Never infer ownership from SQLite's writer lock, PID age, or timeout.
- Keep pipe draining independent of a saturated UI queue so app-server cannot deadlock.
