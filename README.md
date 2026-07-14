# Nudge

Nudge is a local, terminal-native code change reviewer. It starts from an existing Git change, supports anchored review threads and read-only discussion, and keeps proposed edits isolated until the developer explicitly approves them.

Nudge is pre-release software. The current command surface is intentionally small while the review workflow is being built.

## Build

```text
go build ./cmd/nudge
go test ./...
```

Run the current build metadata command with:

```text
go run ./cmd/nudge version
```

Product behavior and architecture follow the repository's technical design and accepted decisions. The feature task bundle decomposes that authority but does not redefine it.

Nudge is not an autonomous coding agent, general Git client, cloud service, issue tracker, or pull-request publisher. It does not stage, commit, push, create branches, or open pull requests as part of its review behavior.
