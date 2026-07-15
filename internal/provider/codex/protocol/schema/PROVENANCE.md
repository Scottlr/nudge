# Codex app-server schema provenance

This directory contains the stable (non-experimental) JSON Schema bundle
generated from the Codex executable supported by this repository.

- Codex CLI version: `codex-cli 0.144.0-alpha.4`
- Upstream source tag: `rust-v0.144.0-alpha.4`
- Upstream source commit: `049586f41571e74b44c841868bca3a2233214a71`
- Generation command: `codex app-server generate-json-schema --out <this-directory>`
- Experimental API: not enabled
- Generated: 2026-07-15

The Windows desktop package exposes the exact CLI as a packaged executable;
the generator was run from a temporary copy of that executable because the
WindowsApps installation path is not directly launchable by PowerShell. No
credentials or runtime state were read.

Regenerate only from the same supported executable version, review the full
schema diff, and update this provenance before changing wire DTOs.
