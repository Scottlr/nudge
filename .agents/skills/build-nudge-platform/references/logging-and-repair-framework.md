# Operational logging and explicit repair framework

## Protected logs

- Use owner-marked per-process log files, owner-only permissions at creation, OS locks, bounded rotation, and registered T067 storage classes/reservations supplied by `$manage-nudge-owned-storage`.
- Admit fields through a closed typed safe vocabulary of stable IDs, codes, bounded counts, durations, versions, and hashes. Generic strings cannot smuggle reviewed content or sensitive provider/process data.
- Keep explicit debug in a separate protected sink with an expiry and hard byte/time budget. It remains subject to the same forbidden categories.
- On capacity, rotation, or sink failure, stop that sink and publish a bounded non-recursive redacted health counter. Never recursively log the failure, exceed storage policy, delete accepted history, or touch another process's active file.

## T058 framework

- A repair plan binds plan/finding/handler IDs, health revision, resource/owner identity, preconditions, exact effect summary, postcondition, policy versions, expiry, and audit-safe fields.
- Confirmation names one exact plan and health revision. Immediately before effect, reacquire the owning lock and compare every precondition; drift invalidates the plan without mutation.
- The framework dispatches only to a registered typed handler. It cannot accept arbitrary paths, SQL, shell commands, or switch effect/handler after confirmation.
- Persist intent/audit before an external effect when required, make replay idempotent, distinguish `already_repaired`, and verify the exact postcondition before terminal success.

## T092 protected-path handler

- Operate only beneath a positively identified canonical Nudge-owned root with expected native identity, owner, ACL/mode evidence, and no-follow containment.
- Narrow permissions/ownership to the documented secure state; never grant broader access, traverse links/reparse points, recurse beyond the bounded manifest, or touch repository/user-selected paths.
- Refuse on identity, owner, root, revision, manifest, or platform-policy drift. Record only safe hashes/codes and verify the final permission state.
