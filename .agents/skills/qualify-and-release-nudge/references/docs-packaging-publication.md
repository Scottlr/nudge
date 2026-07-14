# Documentation, packaging, and publication

## T077: accurate user and operator documentation

- Describe implemented, T053-observed behavior and the human-accepted T063 support contract. Return any behavior contradiction to its owner instead of changing code or inventing prose-level compatibility.
- Keep exactly the small documentation set owned by T077: a concise `README.md`, one `docs/user-guide.md` for installation/use/configuration/accessibility/privacy/limits, and one `docs/operations.md` for health, repair, cleanup/export, recovery, and troubleshooting. Do not pre-create topical stubs or an architecture tour.
- Use canonical Nudge language and exact Cobra commands/flags. Label query-only doctor, explicit live Codex health, separate setup/login, exact-plan repair, cleanup, proposal approval/application, and external publication as distinct actions.
- Explain local/branch/commit targets; immutable captures; read-only discussion; explicit isolated proposals; complete-patch review; staleness and same-conversation refresh; exact-once apply; index/ref preservation; accessibility and limits; privacy/storage; and non-goals.
- Link the final support source/rendering rather than copy rows. Never invent support, provider capabilities, mutation authority, defaults, recovery guarantees, telemetry, signing, distribution, or cloud behavior.
- Keep docs-only scope. Use existing CLI/help contract tests where they protect cited syntax and inspect local links/source references directly; do not create a documentation validator.

## T052: exact local release packages

- Select every and only the final `supported` T063 release rows. A qualified capability may remain documented inside a supported row; an unsupported OS/architecture row produces no archive.
- Make one version source feed CLI, doctor, logs, user agent, and package manifests. Record source commit/declared cleanliness, Go and dependency/toolchain identity, build flags, generated Codex schema hash, support-matrix version, documentation version, and timestamp policy.
- Produce platform-correct single-binary archives, SHA-256 checksums, and a versioned immutable release-candidate manifest. Bind row, archive name, byte count, hash, embedded provenance, documentation, and checksum-manifest identity.
- Include only the binary, established license, and minimal pointer to accepted documentation. Exclude config, SQLite data, logs, workspaces, credentials, caches, bundled Git/Codex, and build-host absolute paths.
- Mark the package set ready only when all expected targets, contents, checksums, and provenance agree. Keep partial or mismatched outputs non-ready and quarantined/replaceable as owned build artifacts.
- T052 does not publish, sign, notarize, add package-manager distribution, or prove native support. Claim bit-for-bit reproducibility only after separate independent evidence.

## T082: separately authorized immutable publication

- Begin only with an accepted T052 immutable draft/candidate manifest and current T063, T064, T074, T076, and T077 identities. Publication is an external mutation requiring explicit user authority and a protected human approval.
- Bind a versioned release manifest to version, immutable tag, source commit, support-matrix version, protocol schema hash, documentation commit, package workflow run, draft release ID, exact artifacts with byte counts/SHA-256, and accepted gate evidence hashes/timestamps.
- Accept only exact manifest/draft/version/tag identifiers. Before approval, verify tag-to-source, matrix/docs/gates, exactly one expected artifact per supported row, no extra assets, and every remote/downloaded byte count and checksum.
- Present exact source, matrix, documentation, gates, draft URL, asset names/sizes/checksum prefixes, and prerelease/stable status to the protected reviewer.
- After approval, promote the existing draft and its existing assets. Do not rebuild, resign, rename, replace, append, reupload, or substitute checksum-equivalent outputs.
- On pre-visibility failure, preserve the draft and report the gate. On uncertain or partial remote mutation, stop for human inspection; never automatically delete/recreate the release or retry uploads.
- After success, read the public release back and record its URL/ID, visibility, tag/source, exact asset set, sizes, checksums, and manifest checksum. Keep credentials out of logs, manifests, and artifacts.

## Authority boundary

Preparing release logic or a draft does not authorize publication. If credentials, environment protection, tag/source identity, current gates, checksums, or explicit human approval are unavailable, stop with the accepted draft intact. Do not simulate publication with a dry run, fake public release, temporary publisher, or smoke release.
