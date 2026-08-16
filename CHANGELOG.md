# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/) and the project adheres to
[Semantic Versioning](https://semver.org/). Release notes are also generated
from conventional commits by goreleaser.

## [Unreleased]

### Added
- GitHub Action (`action/`, released as `action/vX.Y.Z`) that authenticates to
  Vault with GitHub OIDC or a supplied token, issues Harbor robot credentials,
  runs `docker login`, and revokes the lease — deleting the robot — when the job
  ends.

## [0.1.0] - 2026-08-16

### Added
- Harbor secrets engine: `config` (user and robot root-credential modes),
  `config/rotate-root` (user mode), `roles/<name>`, `creds/<name>` with
  renewable leases, Harbor-side expiry, WAL-based orphan cleanup.
- Signed multi-platform releases (Sigstore keyless), SBOMs, provenance
  attestations, GHCR image for the containerized plugin runtime.
- Integration test matrix: Vault 1.16 / 1.21 / 2.0, OpenBao 2.6, Harbor 2.12 / 2.15.
