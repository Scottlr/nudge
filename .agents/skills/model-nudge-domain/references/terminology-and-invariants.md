# Terminology and invariants

Use this reference for domain names, product-facing wording, and state rules. Consult sections 5 and 10.11 of `docs/Nudge_PRD_Technical_Design.md` for the full specification.

## Essential distinctions

- `ReviewSession`: one persisted review of one repository and one target.
- `ReviewThread`: one anchored concern belonging to one session.
- `ProviderConversationRef`: opaque provider state linked to a review thread; never the product identity.
- `ReviewTargetSpec`: user intent (`local`, `commit`, or `branch`).
- `ResolvedTarget`: exact base, head, generation, fingerprint, editability, and destination for one resolution.
- `EditDestination`: the worktree eligible to receive a validated approved patch; not automatically the reviewed location.
- `CodeAnchor`: path, side, range, generation, selected-text identity, hunk identity, and context evidence. Line number alone is insufficient.
- `ProposedPatch`: immutable baseline-to-result patch derived by Nudge.
- `RuntimeApprovalResponse`: provider execution permission, never patch approval.

## Composed thread state

Define independent values for:

- resolution: `open`, `resolved`;
- conversation: `idle`, `streaming`, `awaiting_runtime_approval`, `failed`;
- proposal: `none`, `generating`, `ready`, `stale`, `applying`, `applied`, `rejected`, `failed`;
- anchor: `valid`, `relocated`, `ambiguous`, `orphaned`;
- read state: `read`, `unread`.

Store failure phase and error code separately instead of multiplying proposal states such as `ApplyFailed` and `GenerationFailed` into an unbounded enum.

## Product wording

| Avoid | Use |
|---|---|
| Agent session | Review thread |
| Run task | Discuss or Request change |
| Accept response | Approve proposal |
| Apply Codex output | Apply proposed patch |
| Add provider | Connect Codex |
| Auto-fix | Proposed change |

Qualify “approval” whenever runtime and proposal approval could be confused.

## Invariants

- One thread belongs to exactly one session.
- One process holds writable ownership of a durable session; other instances open it explicitly read-only or use a distinct session identity.
- One thread has at most one active provider turn and one active conversation per provider.
- A ready proposal never mutates; a later result creates another version.
- A proposal applies at most once and only to an editable destination with satisfied preconditions.
- Resolving never approves or rejects. Runtime approval never approves a patch.
- Orphaning never deletes the thread or transcript.
- Every UI surface resolves the active entity through the same stable ID and canonical snapshot.
- Nudge never stages, commits, pushes, creates a branch, or publishes a pull request in v1.
