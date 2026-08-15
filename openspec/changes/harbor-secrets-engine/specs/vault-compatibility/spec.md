## Purpose

Defines the Vault, OpenBao and Harbor versions the plugin supports and the automated integration testing that proves it, so "works with the latest Vault" is a verified property rather than a claim.

## ADDED Requirements

### Requirement: Supported version window
The plugin SHALL support Vault ≥ 1.16 through the current 2.0.x line, OpenBao ≥ 2.x, and Harbor ≥ 2.12.1 (the first release allowing robot accounts to manage robots) with best-effort support for Harbor ≥ 2.2 in user mode. The README SHALL state the tested matrix for each release.

#### Scenario: Version matrix documented
- **WHEN** a user reads the release notes or README for a given plugin version
- **THEN** the exact Vault, OpenBao and Harbor versions it was tested against are listed

### Requirement: Integration test matrix in CI
CI SHALL run, on every pull request and tag, an integration suite against a real Harbor started via docker compose and against real Vault/OpenBao servers for at least: the oldest supported Vault (1.16.x), the latest 1.x, the latest 2.0.x, and the latest OpenBao 2.x. The suite SHALL cover: config write/verify in both auth modes, rotate-root in both modes, role CRUD, creds issuance with a real `docker login` and pull, lease renew extending robot expiry, lease revoke deleting the robot, and WAL rollback.

#### Scenario: PR against unsupported behaviour
- **WHEN** a change breaks credential issuance on Vault 2.0.x
- **THEN** the CI matrix fails on that cell and the PR cannot merge

#### Scenario: OpenBao runtime compatibility
- **WHEN** the plugin binary built against the HashiCorp Vault SDK is registered in OpenBao
- **THEN** it enables, configures, and issues credentials identically to Vault

### Requirement: Unit tests without external services
Backend logic SHALL be unit-testable against a fake Harbor HTTP server (no docker), covering validation, name normalization, duration math, renew/revoke handling, and error mapping, so contributors can run `go test ./...` locally in under a minute.

#### Scenario: Local test run
- **WHEN** a contributor runs `go test ./...` without Docker
- **THEN** all unit tests pass and no integration tests are attempted (they are gated by a build tag or env var)
