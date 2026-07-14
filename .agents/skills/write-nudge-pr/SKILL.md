---
name: write-nudge-pr
description: "Create and update Nudge GitHub pull requests with a diff-grounded title and body, meaningful existing labels, and an accountable assignee. Use whenever Codex opens, drafts, publishes, or materially refreshes a pull request for this repository, including PRs created through another GitHub publishing workflow. This skill owns pull-request presentation and metadata only; it does not own implementation semantics or task IDs."
---

# Write Nudge Pull Requests

Present the actual change clearly enough that a maintainer can understand its purpose, behavior, risk, and evidence without reconstructing the branch locally.

## Workflow

1. Read `AGENTS.md`, the owning task when one exists, and the actual base-to-head diff and commit range. Ground every claim in the current branch rather than the plan or conversation.
2. Confirm the destination repository, base branch, authenticated GitHub account, and whether the pull request already exists. Do not guess remote state.
3. Inspect the repository's current labels. Select the smallest truthful set, normally one to three existing labels covering the change type and important area or risk. GitHub calls these labels, not tags.
4. Assign the authenticated pull-request author by default. Use another assignee only when the user explicitly names them or a verified repository convention makes them accountable. Assignees are not reviewers.
5. Draft the body with the template below. Keep the core sections concrete, remove irrelevant conditional sections, and remove all instructional comments and placeholders.
6. Create or update the pull request with a concise outcome-oriented title, completed body, chosen labels, and assignee. Let the active GitHub publishing workflow own branch, commit, and push mechanics. Use draft status only when the change is genuinely incomplete or the user requests it; do not merge or mark ready without authority.
7. Read the resulting pull request once and correct any missing, stale, or malformed title, body, labels, or assignee. Treat the operation as incomplete until the metadata is accurate or a specific GitHub permission or label-taxonomy blocker is reported.
8. After material new commits, refresh any affected behavior, testing, compatibility, screenshots, review notes, labels, and title before requesting review.

## Body rules

- **Summary:** Lead with the user-facing or architectural outcome, then state what changed.
- **Motivation:** Explain the concrete need. Add `Closes #123` only when an existing issue was explicitly supplied and this change fully resolves it. Otherwise omit issue-closing language; never create an issue to fill the template.
- **Changes:** List the few coherent implementation changes that matter to reviewers, not every file or commit.
- **Behavior:** Describe before and after. When behavior is intentionally unchanged, say so and name the contract being preserved.
- **Design Notes:** Include only decisions relevant to this change. Do not reproduce the subject prompts as a checklist.
- **Testing:** Report only checks actually run and their outcomes. Convert applicable successful checks to `[x]`, remove irrelevant checklist lines, and describe any relevant check not run with its reason instead of leaving a generic unchecked box. Never run broad tests, smoke tests, diagnostics, dry runs, or manual flows merely to populate the body.
- **Compatibility:** State the actual public API, config/storage, CLI, and breaking-change impact. Use `None` when an explicit declaration helps reviewers; do not speculate.
- **Screenshots / Recordings:** Keep only for material TUI changes. Attach sanitized evidence from the production-terminal visual review required by the owning task; do not create demo screens, capture tooling, or committed screenshot sprawl for the pull request.
- **Review Notes:** Point reviewers to the highest-risk or most judgment-heavy parts of the diff. Remove the section when there is nothing useful to add.
- **Checklist:** Check an item only when it is true. Remove an inapplicable item rather than adding work merely to make it checkable.

## Pull-request body template

```markdown
## Summary

What changed, and what user-facing or architectural outcome does it enable?

## Motivation

Why is this change needed?

## Changes

-

## Behavior

Describe the user-visible behavior before and after this change.

## Design Notes

Document only important, relevant decisions involving review targets, anchors and
re-anchoring, review threads, provider abstraction, Codex integration, proposal
isolation, patch approval, persistence, or TUI state and events. Omit this section
when there are no material design decisions to explain.

## Testing

- [ ] `go test ./...`
- [ ] `go vet ./...`
- [ ] Relevant manual TUI flow verified
- [ ] Failure and cancellation paths verified

## Compatibility

- Public API impact:
- Config or storage impact:
- CLI behavior impact:
- Breaking changes:

## Screenshots / Recordings

Include sanitized production-terminal captures for material TUI changes. Otherwise
omit this section.

## Review Notes

Call out the areas where reviewer attention would be most valuable. Omit this
section when there are no useful review notes.

## Checklist

- [ ] Scope is focused
- [ ] Tests cover meaningful behavior
- [ ] Documentation is updated
- [ ] No credentials or repository content are logged
- [ ] Real working-tree changes still require explicit approval
```

## Metadata rules

- Use existing labels only. Do not silently invent or create a label taxonomy. If no existing label truthfully describes the change, report that blocker rather than applying a misleading label.
- Require at least one accountable assignee. If GitHub permissions prevent assignment, report the exact limitation instead of pretending the operation is complete.
- Keep labels minimal. Avoid generic decoration, mutually contradictory labels, issue-workflow labels that do not describe the pull request, and labels based only on a task number.
- Keep issue links optional. Do not leave `Closes #`, `Fixes #`, or an empty related-issue field in the body.
- Never include credentials, private repository content, prompts, raw provider traffic, source excerpts unrelated to the public change, or local filesystem paths.

This skill does not authorize branch creation, commits, pushes, issue creation, label-taxonomy changes, implementation changes, release publication, or merging. Obtain those authorities from the user's request and the applicable workflow.
