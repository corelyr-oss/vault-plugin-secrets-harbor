# secrets-engine/dynamic-credentials Specification

## Purpose
Defines credential issuance: reading `creds/<role>` mints a Harbor robot account bound to a Vault lease, renewal extends the robot's Harbor expiry, revocation deletes it, and orphaned robots are cleaned up.
## Requirements
### Requirement: Credential issuance
A read of `<mount>/creds/<role>` SHALL create a Harbor robot account with the role's `level`, `permissions` and `description`, and SHALL return `username` (the full login name Harbor returns, e.g. `robot$vault-ci-3f9a1c2b` or `robot$library+vault-ci-3f9a1c2b`), `secret`, `robot_id`, `expires_at` (Harbor-side expiry, RFC 3339 or `-1`), and `auth` (base64 of `username:secret`, suitable for a Docker `config.json` / Kubernetes `dockerconfigjson`). The response SHALL carry a Vault lease with the role's effective TTL and `max_ttl`, and the lease SHALL be renewable.

#### Scenario: Successful issuance
- **WHEN** a client reads `creds/ci` on a configured mount
- **THEN** a robot exists in Harbor with the role's permissions, the response contains all listed fields, and `docker login` with `username`/`secret` succeeds against Harbor for the granted actions

#### Scenario: Unknown role
- **WHEN** a client reads `creds/does-not-exist`
- **THEN** the engine returns an error stating the role does not exist and no robot is created

#### Scenario: Harbor create fails
- **WHEN** Harbor returns a non-2xx response on robot creation
- **THEN** the engine returns an error including Harbor's status and message, and no lease is issued

### Requirement: Harbor-side expiry covers the lease
The engine SHALL set the robot's `duration` (days) to `ceil(max_ttl / 24h)` (minimum 1) so Harbor itself expires the robot at or after the maximum lease lifetime, and SHALL never issue a lease whose expiry is later than the robot's `expires_at`.

#### Scenario: Sub-day max TTL
- **WHEN** a role has `max_ttl=1h`
- **THEN** the created robot has `duration=1` and `expires_at` roughly 24h in the future

#### Scenario: Multi-day max TTL
- **WHEN** a role has `max_ttl=100h`
- **THEN** the created robot has `duration=5`

### Requirement: Lease renewal extends the robot
On lease renew the engine SHALL extend the lease per Vault's TTL rules and, if the new lease expiry would exceed the robot's current Harbor `expires_at`, SHALL update the robot's `duration` so that `creation_time + duration days ≥ new lease expiry`. If Harbor refuses or fails the update (robot-mode issuers cannot update robots: Harbor has no `robot:update` permission), the engine SHALL instead cap the renewed lease so it ends no later than the robot's `expires_at` and add a warning; if the robot is already expired the renewal SHALL fail. Renewal SHALL fail if the robot no longer exists in Harbor. Because the robot's `duration` is derived from `max_ttl` at issuance, extension is only needed when the role's or mount's max TTL was raised after issuance.

#### Scenario: Long-running pull secret
- **WHEN** a Kubernetes consumer renews the same lease repeatedly over 10 days with a role `ttl=24h`, `max_ttl=30d`
- **THEN** every renewal succeeds and the robot's `expires_at` in Harbor is always at or after the lease expiry

#### Scenario: Robot deleted out of band
- **WHEN** the robot was deleted directly in Harbor and the lease is renewed
- **THEN** the renewal fails with an error naming the missing robot

#### Scenario: Robot-mode issuer cannot extend
- **WHEN** the engine runs in robot mode and a renewal would need to extend the robot's expiry
- **THEN** the renewal succeeds with a TTL capped at the robot's remaining Harbor lifetime and a warning explaining the cap

### Requirement: Lease revocation deletes the robot
On lease revoke or expiry the engine SHALL delete the robot from Harbor by `robot_id`. A 404 from Harbor SHALL be treated as success. Other errors SHALL be returned so Vault retries the revocation.

#### Scenario: Explicit revoke
- **WHEN** a client runs `vault lease revoke` on an issued lease
- **THEN** the robot no longer exists in Harbor and `docker login` with the old credentials fails

#### Scenario: Robot already gone
- **WHEN** the robot was already deleted in Harbor and the lease is revoked
- **THEN** revocation succeeds without error

### Requirement: Orphan protection via write-ahead log
The engine SHALL record a WAL entry (short robot name, level, project, role) before creating a robot and remove it once the robot is created and the response is ready; if the WAL entry cannot be removed the engine SHALL delete the robot and return an error so that a surviving WAL entry always means "no lease was issued". During WAL rollback the engine SHALL locate the robot by its short name — for project-level robots via the project-filtered listing, since Harbor's unfiltered listing shows system-level robots only — and delete it, ignoring 404.

#### Scenario: Crash between create and lease commit
- **WHEN** the plugin process is terminated after Harbor confirms robot creation but before the secret response is returned
- **THEN** on the next rollback cycle the orphaned robot is deleted from Harbor

### Requirement: Credential responses are auditable and safe
The engine SHALL mark `secret` and `auth` as sensitive (Vault HMACs all response fields by default; operators may expose `username`/`robot_id` in clear via the mount's `audit_non_hmac_response_keys`), and SHALL log robot creation/deletion at debug level with `robot_id` and `username` but never the secret.

#### Scenario: Audit log inspection
- **WHEN** an operator inspects the Vault audit log entry for a `creds/<role>` read
- **THEN** `secret` and `auth` appear as HMAC values and never in clear, and the plugin log lines for the operation contain `robot_id` and `username` but no secret

