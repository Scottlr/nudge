# Reconciliation

Consult sections 7.10, 8.10, 16.2, 17.3, and Appendix B of the technical design.

## Authoritative refresh

- T107 treats fsnotify/lifecycle events as hints, owns bounded debounce/max-delay scheduling, and emits at most one active request plus one coalesced follow-up.
- Read [watch-and-fsnotify.md](watch-and-fsnotify.md) before implementing the watcher adapter or coordinator; this file owns only the handoff into authoritative reconciliation.
- T024 alone cancels superseded compute, resolves through Git, stages anchor results, and activates authoritative state.
- Tag refresh operations and results with generation and operation IDs.
- Do not advance a generation when the authoritative target fingerprint is unchanged.
- Keep the last known snapshot visibly stale when refresh fails.
- Watcher overflow, replacement, closure, or resubscription keeps the snapshot stale until T024 accepts a complete capture; a recreated watch set is not freshness evidence.
- Stage large anchor populations in bounded keyset/path/byte batches and activate one completed generation epoch; partial batches never mix into canonical visibility.

## Anchor sequence

Choose one search path first: the unchanged raw path, or the sole complete `RenamePolicyV1` mapped destination. Rename mapping is only a path transformation and never sufficient placement. Then use the versioned T023 tier order and stop at the first tier that yields candidates: unchanged captured file/content identity plus exact selected-range hash at the old line; exact selected-range hash plus before/after context at the old line; exact selected+context within ±200 lines; exact selected+context anywhere in the scoped file; unique selected-range hash within ±200 lines; unique selected-range hash anywhere in the scoped file; versioned line-diff candidate only when its resulting selected hash matches, with context used to break ties at that tier. There is no cross-file search beyond choosing that sole mapped path.

- Keep at most 20 deterministically ordered candidates and report overflow/refinement explicitly.
- Relocate automatically only when the highest available tier has exactly one valid candidate. Multiple candidates at that tier are ambiguous even if a lower tier would choose one.
- Missing/mismatched/limited rename policy supplies no map. Even a complete map without content/range/context evidence cannot relocate an anchor.

- Normalize line endings and optional trailing whitespace only as specified.
- Do not erase meaningful indentation or interior whitespace.
- Prefer an orphan over a confidently wrong marker.
- Persist every anchor version, method/tier, candidate evidence, and stored excerpt needed for manual reattachment; never mutate old evidence in place.

## Proposal staleness

Re-evaluate affected paths when external edits, HEAD/branch changes, renames, or another proposal application occur. Unrelated generation changes may leave a proposal valid only if every destination precondition and target editability rule still holds.

- Never silently merge.
- Mark stale with a precise reason and affected paths.
- Refresh by rebuilding a safe workspace at the new generation and asking the same provider conversation to reapply the agreed intent.
- Evaluate large proposal populations through bounded journaled validity epochs. A pending/missing current epoch makes every affected proposal non-approvable until the complete result set activates.
- Allow one live apply operation per canonical edit destination and protect it across Nudge processes; external processes remain possible races handled by applicability/journal recovery.
