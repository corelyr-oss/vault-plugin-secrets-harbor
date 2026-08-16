## Purpose

Defines how an operator connects the secrets engine to a Harbor instance: the endpoint, the root credential Vault uses to manage robot accounts (a Harbor user or a Harbor robot account), TLS settings, and rotation of that root credential.

## ADDED Requirements

### Requirement: Backend configuration is written at `config`
The engine SHALL accept a write to `<mount>/config` with `url` (required, Harbor base URL, `http` or `https`), `username` (required), `password` (required; the user password or robot secret), `auth_type` (`user` | `robot`, default `user`), `ca_cert` (optional PEM bundle), `insecure_skip_verify` (optional, default false), and `timeout` (optional request timeout, default 30s). Writing again SHALL replace the stored configuration; omitted secret fields SHALL be retained from the previous configuration.

#### Scenario: Valid configuration in user mode
- **WHEN** an operator writes `config` with `url`, `username`, `password` and `auth_type=user`
- **THEN** the engine stores the configuration and returns success

#### Scenario: Valid configuration in robot mode
- **WHEN** an operator writes `config` with `auth_type=robot`, a robot account name (for example `robot$vault-issuer`) and its secret
- **THEN** the engine stores the configuration and returns success

#### Scenario: Missing required field
- **WHEN** an operator writes `config` without `url`, `username` or `password` on first configuration
- **THEN** the engine rejects the write with an error naming the missing field and stores nothing

#### Scenario: Invalid auth_type
- **WHEN** an operator writes `config` with `auth_type` other than `user` or `robot`
- **THEN** the engine rejects the write with an error naming the allowed values

### Requirement: Configuration write verifies connectivity
On every `config` write the engine SHALL authenticate against Harbor using the supplied credential (`GET /api/v2.0/users/current` for user mode; a robot listing for robot mode, where Harbor answers 401 for a bad credential and 403 for a valid credential without system-wide `robot:list` — 403 therefore counts as verified) and SHALL reject the write if Harbor is unreachable, the TLS handshake fails, or authentication fails. Verification MAY be skipped with `verify_connection=false`.

#### Scenario: Wrong credentials
- **WHEN** an operator writes `config` with a credential Harbor rejects
- **THEN** the write fails with an authentication error and the previous configuration (if any) is unchanged

#### Scenario: Custom CA
- **WHEN** Harbor presents a certificate signed by a private CA and the operator supplies that CA in `ca_cert`
- **THEN** the connectivity check succeeds without `insecure_skip_verify`

#### Scenario: Verification skipped
- **WHEN** an operator writes `config` with `verify_connection=false` while Harbor is unreachable
- **THEN** the configuration is stored and the write succeeds

### Requirement: Configuration read never exposes secrets
A read of `<mount>/config` SHALL return `url`, `username`, `auth_type`, `insecure_skip_verify`, `timeout`, whether a CA certificate is set, and the timestamp of the last root rotation (if any). It SHALL NOT return `password`, the robot secret, or the raw CA PEM.

#### Scenario: Read after write
- **WHEN** an operator reads `config` after configuring the engine
- **THEN** the response contains the non-secret fields and no credential material

#### Scenario: Read before write
- **WHEN** an operator reads `config` on an unconfigured mount
- **THEN** the engine returns an empty response (HTTP 204 / no data)

### Requirement: Configuration can be deleted
A delete of `<mount>/config` SHALL remove the stored configuration. Subsequent credential requests SHALL fail with an error stating the engine is not configured.

#### Scenario: Creds after config delete
- **WHEN** `config` has been deleted and a client reads `creds/<role>`
- **THEN** the engine returns an error indicating the backend is not configured

### Requirement: Root credential rotation
A write to `<mount>/config/rotate-root` on a user-mode configuration SHALL replace the stored root credential with a newly generated one by changing the Harbor user's password (`PUT /users/{id}/password` with the current password as `old_password`). The generated secret SHALL satisfy Harbor's password policy (8–128 characters, at least one lowercase, one uppercase, one digit). The new secret SHALL be persisted before it is installed in Harbor (pending marker) and committed afterwards, SHALL never be returned to the caller, and a pending rotation SHALL be settled automatically on next use by probing which credential Harbor accepts. The stored `username` and `url` are unchanged. In robot mode the engine SHALL reject rotation with an explanatory error, because Harbor has no `robot:update` permission and therefore does not allow a robot account to refresh its own secret; operators refresh the issuer robot's secret as an administrator and write it to `config`.

#### Scenario: Rotate in user mode
- **WHEN** an operator writes `config/rotate-root` on a user-mode configuration
- **THEN** Harbor accepts the password change, subsequent robot operations use the new password, and the response contains no secret

#### Scenario: Rotate in robot mode is rejected with guidance
- **WHEN** an operator writes `config/rotate-root` on a robot-mode configuration
- **THEN** the engine returns an error stating rotation is unsupported for `auth_type=robot` and how to rotate manually, and the stored credential is unchanged

#### Scenario: Pending rotation is settled
- **WHEN** a previous rotation persisted a new password and Harbor accepted it but the commit did not complete
- **THEN** the next Harbor operation detects that only the pending password is accepted, commits it, and proceeds

#### Scenario: Harbor rejects rotation
- **WHEN** Harbor returns an error during rotation
- **THEN** the stored credential is unchanged and the error is returned to the operator

#### Scenario: Rotate on unconfigured mount
- **WHEN** `config/rotate-root` is written before `config`
- **THEN** the engine returns an error indicating the backend is not configured

### Requirement: Robot-mode configuration is scope-aware
When `auth_type=robot`, the engine SHALL surface Harbor's permission-scope errors verbatim when a role's permissions exceed the issuer robot's scope, so operators can tell the failure is a Harbor scope constraint rather than a Vault error. The engine SHALL accept both project-level issuer robots (Harbor ≥ 2.12.1) and system-level issuer robots (Harbor ≥ 2.13); documentation SHALL state which Harbor versions support which.

#### Scenario: Role broader than issuer robot
- **WHEN** a role requests permissions the issuer robot does not hold and a client reads `creds/<role>`
- **THEN** the engine returns an error containing Harbor's `403 DENIED` response verbatim (e.g. "permission scope is invalid…" on Harbor 2.15, "denied" on 2.12) and no robot is created
