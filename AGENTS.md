# Nudge Repository Instructions

## Authority and scope

- Treat `docs/Nudge_PRD_Technical_Design.md` as the authority for product behaviour, terminology, invariants, security boundaries, and architectural intent.
- Treat accepted ADRs as the authority for implementation choices that the design deliberately leaves open.
- Treat `features/nudge-v1/tasks.md` and its linked task files as implementation decomposition, not permission to redefine the design.
- Treat repository skills as operational guidance. If a skill conflicts with the design or an accepted ADR, correct the skill before continuing.
- Check current primary documentation before depending on external protocol or library details that can evolve.

## Repository skill routing

For every Go source, module, package, API, tooling, or test change, load `$go`, then load exactly one primary Nudge owner from the table below. Load a second primary owner only when the task crosses a documented consumer/owner boundary; the primary task owner still decides semantics. The supplemental Go skills described after the table do not own task IDs and do not change this exact-once primary assignment. These assignments cover every retained v1 task exactly once; gaps are folded or deferred IDs recorded in `features/nudge-v1/tasks.md`.

| Skill | Primary feature tasks | Ownership boundary |
|---|---|---|
| `$model-nudge-domain` | T002-T004, T007, T017, T020-T021, T034, T050, T054, T070 | Vocabulary, entities, commands/events, canonical state, consumer-owned ports, privacy/capability/resource contracts; no adapter implementation. |
| `$build-nudge-platform` | T001, T005-T006, T049, T058, T071, T080, T092 | Module/CLI composition, config/protected paths, bounded processes, shared OS locks, trusted executables, query-only doctor, repair framework, permission repair, and safe logs. |
| `$persist-nudge-state` | T018-T019, T083, T099-T100 | SQLite migrations/queries/WAL, durable sessions and writer fencing, saved preferences, migration repair, and stale-lease repair; no artifact accounting. |
| `$manage-nudge-owned-storage` | T059-T060, T065-T067, T079, T095, T101-T102 | Capacity reservations, spools/adoption, artifact ledger, reconciliation, export, cleanup, accounting repair, rebuild, and residue quarantine. |
| `$manage-nudge-git-proposals` | T008-T011, T016, T023-T024, T033, T035-T036, T038, T040-T048, T055-T057, T069, T072, T081, T087-T091, T093-T094, T103, T106-T113 | Installed-Git semantics, captures/targets/diffs/anchors, review/proposal workspace lifecycle, patch derivation/application, edge-file behavior, and owner-specific repair. |
| `$integrate-nudge-codex` | T026-T032, T037, T078 | Provider-neutral port, Codex process/protocol/auth/conversations, permissions/runtime approvals, proposal turns, flow control, and explicit live health. |
| `$build-nudge-tui` | T012-T015, T022, T025, T039, T073, T084-T085, T114-T115 | Bubble Tea v2 projections, bounded viewports, themes/highlighting, default keymap/focus, responsive layout, motion, accessibility, and production rendered review; no workflow truth. |
| `$qualify-and-release-nudge` | T052-T053, T063-T064, T074-T077, T082 | Integrated journey evidence, performance/native/security gates, support disposition, docs, packages, and separately authorized publication. |

### Supplemental Go skill routing

| Skill | Load when | Relationship to task ownership |
|---|---|---|
| `$go` | Every change to Go source, `go.mod`/`go.sum`, package/API design, errors, contexts, tests, refactors, Go tooling, or Go code review. | Always accompanies the one primary Nudge owner; supplies the Go 1.25 idiom and compatibility baseline only. |
| `$go-concurrency` | Goroutines, channels, mutexes, atomics, actor/event loops, timers, worker pools, streaming, queues, backpressure, cancellation coordination, or concurrent tests materially affect correctness. | Adds lifecycle, bounds, synchronization, and deterministic-testing guidance; the primary Nudge owner still defines workflow/state semantics. |
| `$go-oss` | Module/repository layout, module identity, public CLI/config/format compatibility, README/LICENSE/CONTRIBUTING/SECURITY, contributor CI/tooling, dependency policy, SemVer, install paths, or release-facing metadata changes. | Adds public-repository and compatibility discipline; `$build-nudge-platform` or `$qualify-and-release-nudge` retains implementation/release authority. |

Supplemental skills compose: for example, T001 uses `$go` + `$go-oss` + `$build-nudge-platform`, while T107 uses `$go` + `$go-concurrency` + `$manage-nudge-git-proposals`. Do not add supplemental skills to the primary task table or let them redefine design/task behavior.

### GitHub pull request skill routing

Load `$write-nudge-pr` whenever creating, publishing, or materially updating GitHub pull-request metadata for this repository, including documentation-only pull requests and pull requests opened through another publishing workflow. It owns no task IDs and does not alter the implementation authority of the primary Nudge owner.

A pull-request operation is incomplete until its title and body accurately represent the current diff, a minimal truthful set of existing labels is applied, and an accountable assignee is set, or a concrete GitHub permission or label-taxonomy blocker is reported. Do not create an issue merely to populate closing language, and do not let the pull-request template cause irrelevant tests, diagnostics, documentation, or screenshots.

When a gate or consumer exposes a defect, route the fix to the skill that owns the behavior. Do not weaken policy, evidence, or acceptance criteria inside a release/TUI/adapter task merely to make that consumer pass.

## Planning and implementation discipline

- Prioritise the requested behaviour and avoid unnecessary validation work.
- Do not propose, create, or run smoke tests, diagnostic scripts, dry runs, temporary validation utilities, or similar artefacts unless the user explicitly requests them.
- Only add or modify tests when they protect meaningful business logic, prevent a realistic regression, or are explicitly required.
- Keep plans focused on essential implementation steps. Do not pad them with generic testing, diagnostics, or verification tasks.
- `nudge doctor` is a specified product capability; that does not authorize temporary diagnostic tooling during development.

## Product posture

Nudge is a local, terminal-native code change reviewer. It starts from an existing Git change, lets the developer attach a concern to exact code, supports read-only discussion with Codex, and allows Codex to edit only an isolated proposal workspace after an explicit request. The developer reviews a complete derived patch and decides whether it reaches the edit destination.

Do not turn Nudge into an autonomous coding agent, general Git client, editor, provider platform, cloud service, issue tracker, or PR publisher. Do not add deferred features merely because the architecture could support them.

## Canonical language

- A **review session** covers one repository and one review target; a **review thread** is one anchored concern inside that session.
- A **review thread** is a Nudge object; a **provider conversation** is opaque provider state linked to it.
- `ProviderConversationID`/`ProviderTurnID` identify Nudge-local records; `ProviderConversationRef`/`ProviderTurnRef` are separate opaque external references.
- A **review target** is what is examined; an **edit destination** is where an approved patch may be applied.
- **Discussion mode** is technically read-only; **proposal mode** is explicitly authorized and workspace-write-only.
- A **proposed patch** is derived from the recorded proposal baseline and resulting filesystem state. Provider prose and file events are not the patch.
- **Runtime approval**, **proposal approval**, **rejection**, and **resolution** are separate actions.
- A **target generation** is a resolved revision of a stable target identity. A local working-tree snapshot is not a commit and must not receive a fabricated object ID.
- A **saved base preference** is a bounded user expression keyed to a verified repository binding. It is re-resolved for each branch generation and is never itself a frozen object identity.

Use “Review thread,” “Discuss,” “Request change,” “Approve proposal,” “Apply proposed patch,” “Connect Codex,” and “Proposed change.” Avoid “Agent session,” “Run task,” “Accept response,” “Apply Codex output,” “Add provider,” and “Auto-fix” in product-facing text.

## Non-negotiable invariants

- The user worktree remains unchanged until Nudge validates and applies a concrete displayed proposal.
- Do not assume an ordinary linked Git worktree is a complete security boundary; its `.git` indirection may expose shared repository metadata. Follow the accepted isolation ADR.
- Derive proposals from a Nudge-owned immutable baseline plus an independent result snapshot. Do not trust provider-modified refs, index state, baseline markers, prose, or file-event summaries.
- Ready proposal versions are immutable and can be applied at most once.
- Stale or ambiguous proposals fail closed. Do not silently three-way merge in v1.
- Proposal application must preserve the user's index and must not stage, commit, push, create branches, or create pull requests.
- Resolving a thread does not approve or reject a proposal. A runtime approval never approves a patch.
- An orphaned anchor retains its thread, messages, and stored excerpt.
- Thread status is composed from resolution, conversation, proposal, anchor, and read-state dimensions. Do not create a cross-product mega-enum.
- One reducer/actor is the only writer of canonical domain state; every UI surface is a projection keyed by stable IDs.
- One process owns a writable review session at a time. Hold an OS session lease for the actor lifetime and fence every durable session mutation with lease ID, writer epoch, and expected revision; a second process is explicitly read-only or starts a distinct session.
- A local generation exists only after the actor accepts a `LocalCaptureCandidate`, persists its immutable `CaptureID` artifacts, and assigns/reuses the monotonic generation. Never reconstruct it from later live reads.
- T106 computes a capture candidate and T009 adopts it; T107 emits lossy refresh hints and T024 alone activates authoritative reconciled state. Do not let adapters collapse those boundaries.
- Local rename/copy identity uses only persisted `RenamePolicyV1` evidence (60%, 1,000 source/target candidates, changed-source copies, no harder search). A rename map changes anchor search path but never proves placement; limited detection stays visible delete/add.
- Preserve unmerged index stage-1/2/3 evidence for review. V1 never anchors, proposes, applies, or resolves a conflicted path/session.
- Discussion never reads the live worktree. Filesystem mode leases an immutable `ReviewSnapshot` that is the sole repository-readable root and requires canonical read-containment evidence; prompt-only mode has zero repository-readable roots and receives only bounded captured context. Local snapshots reproduce an accepted capture; branch/commit snapshots reproduce pinned Git objects.
- A mutating provider result becomes derivable only after the OS containment proves every descendant/writable handle quiescent. If turn-level proof is unavailable, terminate and empty the contained app-server tree; otherwise fail non-ready.
- Desired capability policy does not enable behavior by itself. Effective review/anchor/snapshot-materialization/propose/apply support also requires registered implementation evidence and current platform/session capability. Application-level discussion availability separately composes materialization, lease, canonical read containment, provider/account/disclosure, and permission evidence; limit failures never become truncated complete artifacts.
- Source-generation provenance is distinct from destination applicability. Recheck target-kind global constraints and every touched-path precondition; unrelated local changes are not blanket staleness.
- T069 owns one machine-Git/rename/patch/conversion/apply policy. Sanitize ambient Git config/attributes, detect conversion-affecting `.gitattributes` and core settings without executing filters, permit mutation only for registered byte-neutral states, persist fingerprints, and revalidate immediately before apply. Unknown/non-neutral conversion or a proposal that changes `.gitattributes` remains visibly review-only in v1.
- Nudge destination locks exclude other Nudge instances only. External editors/Git can race; touching/index/global uncertainty or mixed state becomes `repair_required`, not claimed success or blind rollback.
- Long anchor/proposal populations reconcile through journaled bounded staged batches and one completed epoch pointer. Until the epoch is complete, affected actions remain pending/non-approvable; never put the whole population in actor state or one transaction.
- Edge support is evidence-specific: T046 rename/copy, T057 regular binary, T087 text/conversion, T088 raw/native paths, T089 modes/type transitions, T090 symlinks, and T091 review-only gitlinks/special objects. Passing one never activates another.
- T057 owns the single persisted `ContentClassV1` decision. Valid UTF-8/BOM routes to T087; explicit binary/NUL and opaque invalid-UTF-8/unregistered byte content remain T057-owned. No renderer, proposal, or apply path classifies the same bytes independently.
- T088's `internal/paths` executor is the sole native path-effect seam. Preserve safely parsed raw Git entries even when native-invalid; actionable effects revalidate and execute through held root/parent handles and closed typed no-follow leaf operations. T090 supplies symlink policy but never opens a parallel string-path seam.
- T091 keeps canonical modes/object IDs on `ChangedFile` and adds only completeness/special-kind/reason evidence. Safely parsed metadata-unavailable entries remain visible, counted, and reviewable at path level while every action axis is false.
- A normal zero-delta provider turn records `no_changes` only after T035's journalled defensive reset verifies baseline. It creates no empty approvable proposal version.
- T108 proves proposal-root ownership, T035 owns the active baseline/result lifecycle, and T109 alone owns automatic retirement. T110 freezes result truth, T111 builds the complete patch/index artifact, and T038 publishes applicability plus the immutable version.
- T112 may only prepare a locked, non-mutating apply operation. T113 owns exact mutation, verification, and crash classification; T041 alone composes that result into proposal aggregate state and idempotent command behavior.
- Every heavy artifact obeys per-artifact limits, checked peak duplication/WAL cost, per-volume reserve, and repository/global owned-storage budgets. Storage pressure never authorizes deletion of accepted history.
- Git and Codex launch only through the trusted-executable resolver: an explicit absolute configured path or a sanitized `PATH` search that excludes the current directory, repository, worktree, and Nudge-owned workspaces. Canonical regular-executable identity is revalidated immediately before every launch.
- Full-tree fuzzy search is an application query over one immutable snapshot with bounded pages/cursors; the TUI never searches only loaded nodes or eagerly materializes the repository.
- Over-threshold content opens only after explicit confirmation and remains bound to immutable capture/object/content identity. Reads are bounded ranges; pathological single lines render as labelled bounded segments, never one multi-megabyte row.
- Plain `nudge doctor` is query-only. The design-required explicit `nudge doctor --repair <plan-id> --health-revision <revision>` mode may mutate only with an exact versioned plan, revalidation, explicit confirmation, ownership proof, the owning lock/journal, idempotency, and redacted audit. There is no v1 `repair-all` or implicit repair.
- The T058 repair framework has no generic mutation authority. Protected paths, migrations, session leases, review snapshots, proposal workspaces, apply-journal closure, ledger/reservation correction, derived-artifact rebuild, and quarantine are separate registered handlers; a plan cannot switch handler/effect after health generation.
- `nudge doctor --live-codex` is a separately explicit, non-repairing provider/account health action. It may start and initialize the trusted Codex app-server and query supported account state, but it never logs in unless the user separately chooses `Connect Codex`; plain doctor never connects.

## Architecture and ownership

- `cmd/nudge`: thin executable entrypoint and injected build/version variables only.
- `internal/cli`: Cobra parsing/help, exact shipped flags/subcommands, config overlays, and dependency composition.
- `internal/domain/repository`: repository, worktree, target, snapshot, tree, change, and diff contracts.
- `internal/domain/review`: sessions, threads, anchors, messages, proposals, and invariants.
- `internal/app`: use cases, ports, reducer, commands, events, operations, and immutable snapshots.
- `internal/gitcli`: explicit Git command construction and parsing.
- `internal/diff`: parsing and transformations for the neutral structured diff model.
- `internal/workspace`: capture/review-snapshot/proposal four-root lifecycle, ownership, leases, and independent manifests.
- `internal/provider`: provider-neutral values/events plus adapter implementations; it does not own the consumer port.
- `internal/provider/codex`: Codex app-server adapter.
- `internal/provider/codex/protocol`: raw version-specific app-server DTOs only.
- `internal/store/sqlite`: SQLite implementation of application-owned storage interfaces.
- `internal/capacity`: pure checked peak/volume arithmetic only. `internal/app` owns planner/reservation/spool ports, platform adapters own filesystem primitives, and `internal/store/sqlite` owns durable reservation/ledger queries and migrations.
- `internal/privacy`: sensitive-value types, disclosure/redaction policy, protected logging, and permission policy only.
- `internal/export`: bounded streaming human-readable Markdown encoding only; application selection/authorization remains in `internal/app`.
- `internal/release`: release-only support-matrix and packaging helpers; production application packages never import it.
- `internal/presentation`: frontend-neutral inert terminal-text projection shared by CLI and TUI; it never owns canonical identities, JSON/export encoding, or styles.
- `internal/process`: the sole trusted-executable resolver and bounded child-process primitive shared by Git and Codex adapters.
- `internal/filelock`: platform-specific OS locks for repository maintenance, session writers, global/repository capacity reservation, workspaces, destination apply, and protected log ownership, each keyed by the corresponding stable identity; timeout/PID guesses never steal ownership.
- `internal/config`, `paths`, `watch`, `highlight`, `theme`, and `tui`: the named responsibilities only.

Domain packages may import the standard library and other domain packages. They must not import Bubble Tea, SQLite, Codex protocol DTOs, or execute Git. TUI components consume application snapshots and IDs and never call Git, provider, or store adapters directly. Keep interfaces with their consumer. Do not create a generic `utils` package.

## Source-of-truth rules

- Installed Git CLI behaviour is authoritative for repository/revision/diff/file-mode semantics and patch generation/checking/application. The persisted immutable patch bytes plus verified baseline/result manifests are authoritative for the proposal version displayed and applied.
- Git reconciliation is authoritative; filesystem watcher events are hints.
- Accepted `CaptureID` artifacts are authoritative for local-generation bytes after capture; never reread live paths to recreate them.
- `ReviewTargetSpec` records intent; `ResolvedTarget` records the exact Git interpretation at one generation.
- A leased immutable `ReviewSnapshot` is authoritative for provider-visible discussion bytes.
- Nudge's normalized transcript is authoritative for restoration; provider history is supplementary.
- SQLite owns durable review metadata; proposal filesystem contents remain independently verified.
- One canonical application state owns current workflow truth; projection indexes are derived.

## Engineering idioms

- Start child processes with an executable plus explicit argument arrays. Never invoke shell command strings for Git or Codex.
- Resolve executables from an absolute configured path or a sanitized `PATH` that removes empty/relative entries and any candidate under the current directory, repository/worktree, or Nudge workspace. Require a canonical regular executable with platform-appropriate ownership/mode evidence, retain its identity, and revalidate it immediately before spawn; repository shadow binaries never win.
- Pass `--` before Git paths, prefer NUL-delimited machine output, preserve raw `RepoPath` bytes, and disable color, hooks, external diff/textconv, clean/smudge filters, and implicit LFS/network behavior for internal operations.
- Central machine Git reads also disable optional locks/index refresh, lazy fetch and terminal prompts, fsmonitor/untracked-cache writes, pagers/editors/helpers, and interactive behavior. Missing objects are typed unavailable; opening/capturing/reconciling must not rewrite index bytes or contact a remote.
- Provider permissions enumerate exact readable and writable roots. A read-only label is insufficient: absolute paths and symlink, junction, mount, reparse-point, hard-link-alias, or canonical-resolution escapes must fail closed. V1 provider turns never grant network, including through runtime approvals.
- Keep Git object IDs opaque to domain code and validate them against the installed repository object format. Preserve absent add/delete sides and unmerged stage-1/2/3 evidence; never fabricate all-zero IDs or flatten conflicts into ordinary changes.
- Use cancellable operations, generation/correlation IDs, bounded queues, coalesced provider deltas, one active turn per thread, and one active apply operation per canonical edit destination.
- Keep one live app-server connection at a time, one handshake per connection, serialized managed-duplex stdin, hard frame/resident/message/turn/input bounds, and a reserved typed failure/control path. Never silently drop non-coalescible lifecycle truth.
- Keep Bubble Tea as the TUI event loop, not the domain architecture. Child components emit UI intents; the root issues application commands.
- T073 owns only shared visible-window arithmetic, frame budgets, and the root render/tick scheduler. T014/T081 repository/search, T015/T072 code, T022 thread/discussion, T039 proposal review, and T013 layout/overlays own their projections and never add pane-local schedulers or unbounded caches.
- T084 uses Bubbles v2 bindings for the documented default keymap and derives dispatch/help from registered implemented commands. T032 runtime grant/deny and T039 proposal approve/reject use distinct IDs, contexts, handlers, overlays, and application intents; unavailable commands are absent rather than placeholder metadata.
- V1 is keyboard-complete and defers mouse support. Do not add mouse configuration, terminal modes, gestures, hit maps, or `--no-mouse` until a later focused product task is accepted.
- Render logical rows, tokenize whole files with Chroma, cache by immutable content identity, use semantic theme roles, and measure terminal cell width rather than bytes or runes.
- Keep hierarchical tree pages, repository-wide search pages, and explicit content ranges as separate immutable projections. Search never depends on expansion state, and explicit large-file pages never reread a live path.
- Route repository/provider/path/ref/error text through the one frontend-neutral terminal-safe projection before any CLI or TUI rendering; never render untrusted control sequences. JSON/export use their own encoders and canonical identities remain unmodified.
- Use `database/sql`, a CGo-free SQLite driver, foreign keys, transactions, embedded checksum migrations, and an explicit application journal for filesystem-crossing apply operations.
- SQLite writer serialization is not cross-process session ownership. Acquire the OS lease first, then advance the persisted writer epoch and use revision CAS on every session-scoped transaction.
- Lock order is explicit in ADR-012. Ordinary actor work already holds its session-writer lease, then acquires the global capacity-reservation lock, repository capacity lock, and stable-ordered destination/workspace/log owner locks. Maintenance flows first hold the repository gate and all affected session locks in stable order, then the same global-to-repository capacity locks and owner locks. Reservations precede heavy workspace/provider mutation; no path acquires the maintenance gate while holding a capacity/owner lock.
- Keep migrations additive and owner-ordered: core review, provider lifecycle, immutable message chunks, review snapshots/turn association, proposal/workspace, apply, capacity-ledger core, and later reconciliation/repair/cleanup schemas land only when their aggregate exists.
- Store accepted provider text as immutable ordered chunks and freeze a terminal body identity; streaming bodies are not export snapshots. Review large patches through identity/hash-bound paged metadata and 256 KiB ranges, not eager BLOB loads.
- Do not log source excerpts, prompt bodies, credentials, or raw protocol by default. Do not add telemetry or a cloud dependency.
- Use per-process owner-marked log files and OS locks. Repository cleanup may remove only exact repository-owned log subtrees after quiescence; it never infers ownership from mixed log text or deletes global/live-instance logs.
- Admit operational log fields through a typed safe-field vocabulary; generic strings cannot carry source, prompt, patch, command/runtime scope, URL, environment, account, or credential data. Explicit debug uses a separate protected time-limited sink and remains subject to the same forbidden categories.
- Reserve/ledger log growth under repository/global budgets. On unavailable capacity, rotation, or sink failure, stop that sink and expose a bounded redacted health counter without recursive logging, exceeding limits, deleting review history, or touching another process's active files.
- Target Go 1.25.0, prefer the standard library, pin focused stable dependencies by major/import path, and avoid generic frameworks where Nudge needs only a narrow policy-bearing seam. Production code must compile against the latest patched Go 1.25 release and cannot depend on later standard-library APIs merely because current stable CI accepts them. Production Go is formatted; exported APIs have idiomatic doc comments, and internal comments explain contracts or non-obvious reasoning rather than narrating code.
- Keep Nudge as one installable-CLI module: `cmd/nudge` is thin, implementation packages stay responsibility-named under `internal`, and no `pkg`, public SDK, multi-module split, generic layout scaffold, or empty placeholder tree appears without a concrete supported consumer and compatibility decision.
- Every goroutine has an owner, bound, cancellation/exit path, and join point. Every queue is bounded by count and resident bytes where payloads vary, preserves a control/failure path under saturation, and never drops non-coalescible lifecycle truth; load `$go-concurrency` for the exact primitive and test discipline.
- Release performance evidence uses T074's versioned workload: exact fixtures, cold/warm state, timer boundaries, sample/percentile policy, reference runner, deterministic resource bounds, and one source/policy/tool/runner-bound result. Owner benchmarks guide implementation; they do not independently establish support.

## TUI visual identity and rendered review

- Built-in Nudge themes are neutral-dominant: terminal/default or graphite/charcoal/ivory structure, muted mulberry/plum identity and focus (never indigo/blue-violet), sage/forest positive state, rose/red destructive/error state, and muted straw/citrine yellow warnings. Nudge-authored built-in UI and bundled default syntax values contain no blue, cyan, teal, orange, amber, or copper. Terminal-default may inherit colors chosen by the user's terminal; user-authored themes may choose other colors.
- Keep normal frames calm. Let one identity accent and, when genuinely needed, one state accent dominate; use spacing, alignment, borders, labels, text treatment, and stable placement for hierarchy instead of unrelated high-chroma combinations.
- Treat current Claude Code, GitHub Copilot CLI, and OpenCode views as quality references for hierarchy, density, status visibility, input/action prominence, and progressive disclosure only. Check current official views when the comparison matters; never copy their branding, palette, mascot, glyph language, or exact layout.
- T114 establishes the loop after T016 produces the first real local-review binary. Run the production `nudge` executable in an ordinary terminal against disposable real repositories, use computer use or equivalent real-terminal capture, label images with build/geometry/theme/state, feed them to Codex or a human reviewer, fix blocking and important findings at the owning component, and recapture the affected views.
- Every later task that adds or materially changes a TUI surface runs the applicable part of the same loop in that task. Do not create demo screens, fake pane data, screenshot generators, recorders, product capture modes, or committed screenshot sprawl.
- T115 repeats a sanitized integrated matrix over the complete v1 TUI before T053/T074 freeze journey and performance evidence. Keep the gate incomplete while a blocking/important finding remains, and route the correction to its existing theme/layout/pane/projection owner.
- Automated tests protect meaningful layout, focus, semantic-role, and interaction regressions. They do not replace visual review with aesthetic screenshot churn.

## Change hygiene

- Implement the smallest coherent task and keep unrelated dirty-worktree changes untouched.
- Preserve architectural boundaries even when a shortcut would compile.
- Add targeted tests in the same task as the behaviour they protect; do not create broad test churn or standalone validation scaffolding.
- Record unresolved design choices instead of silently selecting a weaker safety or compatibility behaviour.
