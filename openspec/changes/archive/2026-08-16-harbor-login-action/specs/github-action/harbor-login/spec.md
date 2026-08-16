## Purpose

Defines the observable contract of the GitHub Action that obtains a short-lived Harbor robot account from this Vault secrets engine, logs the runner's container tooling into Harbor, and releases the credential when the job ends.

## ADDED Requirements

### Requirement: Action inputs
The action SHALL accept the inputs below. `registry` and `role` are required; every other input is optional with the stated default.

| Input | Default | Meaning |
|---|---|---|
| `registry` | — | Harbor registry host (optionally `host:port`) to log in to |
| `role` | — | Secrets engine role name read as `<mount>/creds/<role>` |
| `vault-url` | `$VAULT_ADDR` | Vault/OpenBao base URL |
| `mount` | `harbor` | Mount path of the Harbor secrets engine |
| `vault-token` | `$VAULT_TOKEN` | Pre-existing Vault token; when absent the action authenticates via GitHub OIDC |
| `auth-mount` | `jwt` | Mount path of the JWT auth method used for OIDC |
| `auth-role` | value of `role` | Vault JWT auth role name |
| `audience` | unset | Audience requested for the GitHub OIDC token |
| `namespace` | unset | Vault Enterprise namespace (`X-Vault-Namespace`) |
| `ca-cert` | unset | PEM CA bundle used to verify Vault's TLS certificate |
| `tls-skip-verify` | `false` | Skip Vault TLS verification |
| `login` | `true` | Run `docker login` against `registry` |
| `revoke` | `true` | Revoke the Vault lease in the post step |
| `logout` | `true` | Run `docker logout` in the post step |

The action SHALL fail with a message naming the offending input when a required input is missing, when neither `vault-url` nor `VAULT_ADDR` is set, or when `vault-token` is absent and the workflow has not granted `id-token: write`.

#### Scenario: Minimal usage
- **WHEN** a workflow step uses the action with `registry`, `role` and `vault-url` set, and the job grants `id-token: write`
- **THEN** the action authenticates via OIDC, issues credentials and logs in, without further configuration

#### Scenario: Missing required input
- **WHEN** the action runs without `registry`
- **THEN** it fails with an error naming `registry` and performs no Vault request

#### Scenario: OIDC unavailable
- **WHEN** no token is supplied and the job does not grant `id-token: write`
- **THEN** the action fails with an error explaining that `permissions: id-token: write` is required, or that a `vault-token` may be supplied instead

### Requirement: Vault authentication precedence
The action SHALL use an explicitly supplied `vault-token` input if present, otherwise the `VAULT_TOKEN` environment variable, otherwise it SHALL obtain a GitHub OIDC token for the configured `audience` and exchange it at `<auth-mount>/login` for a Vault token. A token obtained by the action SHALL be treated as owned by the action; a caller-supplied token SHALL NOT be revoked by it.

#### Scenario: OIDC exchange
- **WHEN** no token is supplied
- **THEN** the action requests a GitHub OIDC token and posts it with the JWT role to Vault's JWT auth mount, using the returned client token for subsequent requests

#### Scenario: Caller-supplied token is respected
- **WHEN** `vault-token` is supplied
- **THEN** no OIDC token is requested, and the post step does not revoke that token

#### Scenario: Authentication failure is actionable
- **WHEN** Vault rejects the OIDC token (unknown role, unbound audience, expired issuer key)
- **THEN** the action fails with Vault's error message, the auth mount path and the role name, and no credentials are issued

### Requirement: Credential issuance and secret masking
The action SHALL read `<mount>/creds/<role>` and SHALL register the returned `secret` and `auth` values as masked secrets with the runner before they can appear in any log line. Vault errors SHALL be surfaced verbatim, including Harbor's message when Harbor rejects the robot creation.

#### Scenario: Secret never appears in logs
- **WHEN** the action issues credentials and any later step echoes the secret
- **THEN** the runner replaces it with `***` in the log

#### Scenario: Role does not exist
- **WHEN** `role` does not exist on the mount
- **THEN** the action fails with Vault's error naming the role, and no login is attempted

#### Scenario: Harbor rejects issuance
- **WHEN** Harbor refuses the robot creation (for example a permission scope error in robot mode)
- **THEN** the action fails with Harbor's message as returned by the engine

### Requirement: Docker login
When `login` is `true`, the action SHALL authenticate the runner's Docker CLI to `registry` with the issued credentials, using a mechanism that keeps working for tools that read the standard Docker configuration (`docker`, `buildx`, `podman`). The secret SHALL be passed to the CLI over stdin, never as a command-line argument. When `login` is `false`, the action SHALL still issue credentials and set outputs.

#### Scenario: Subsequent pull succeeds
- **WHEN** the action completes with `login: true` and a later step pulls an image the role may pull
- **THEN** the pull succeeds without further authentication

#### Scenario: Login disabled
- **WHEN** `login` is `false`
- **THEN** no Docker configuration is modified and the outputs are still populated

#### Scenario: Docker unavailable
- **WHEN** `login` is `true` and no Docker CLI is present on the runner
- **THEN** the action fails with an error stating that a Docker CLI is required, or that `login: false` may be used

### Requirement: Outputs
The action SHALL set the outputs `username`, `secret`, `auth`, `registry`, `robot-id`, `expires-at` and `lease-id` from the credential response, so that other tooling can reuse the same credential without a second Vault read.

#### Scenario: Reuse by another tool
- **WHEN** a later step passes `steps.<id>.outputs.username` and `outputs.secret` to `helm registry login`
- **THEN** that login succeeds against the same registry

#### Scenario: Outputs carry the Harbor-side identity
- **WHEN** the action completes
- **THEN** `username` is the full robot name Harbor returned (for example `robot$library+vault-ci-1a2b3c4d`), `robot-id` its Harbor ID, and `expires-at` its Harbor-side expiry

### Requirement: Post-job cleanup
The action SHALL register a post step that runs even when the job fails. When `revoke` is `true` it SHALL revoke the credential lease synchronously, so that the Harbor robot is deleted before the job completes; when the Vault token was obtained by the action it SHALL also revoke that token; when `logout` is `true` it SHALL log the Docker CLI out of `registry`. A cleanup failure SHALL be reported as a warning naming what could not be cleaned up, and SHALL NOT change the job's result.

#### Scenario: Robot is gone after the job
- **WHEN** a job using the action finishes successfully
- **THEN** the Vault lease is revoked, the Harbor robot no longer exists, and the issued credentials are rejected by the registry

#### Scenario: Cleanup after a failed job
- **WHEN** a step after the action fails and the job fails
- **THEN** the post step still runs and still revokes the lease

#### Scenario: Revocation problem does not mask the job result
- **WHEN** Vault is unreachable during the post step
- **THEN** the action emits a warning that names the lease that could not be revoked, and the job keeps the result it already had

#### Scenario: Cleanup opted out
- **WHEN** `revoke` is `false`
- **THEN** the lease is left in place and the action notes in the log that the credential remains valid until its TTL expires
