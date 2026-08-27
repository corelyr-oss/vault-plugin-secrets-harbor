## ADDED Requirements

### Requirement: Documented bound on effective credential lifetime
The documentation SHALL state that a credential's effective lifetime is the lesser of its lease TTL and the remaining lifetime of the Vault token that read `creds/<role>`, because a Vault lease cannot outlive its parent token: when that token expires or is revoked, Vault revokes every lease it created, the engine's revocation deletes the robot, and Harbor rejects the credential from that moment. It SHALL state that this bound is invisible in the issuance response for service tokens — Vault does not clamp a service token's child lease at issuance, so `lease_duration` reports the role's full TTL and the lease is revoked early anyway — while batch tokens (`token_type=batch`) are clamped at issuance and therefore report the true, shorter `lease_duration`.

#### Scenario: Operator diagnoses a credential dying before its lease expires
- **WHEN** an operator whose issued credential stopped working long before its reported `lease_duration` consults the documentation
- **THEN** they find the parent-token bound named as a cause, the fields that reveal it (`token_ttl`, `token_max_ttl`, `token_type` on the auth role, and the auth mount's tune when the role inherits), and the fact that a correct role, mount and Harbor configuration do not exclude this cause

#### Scenario: Operator distinguishes service from batch tokens
- **WHEN** an operator compares the `lease_duration` in an issuance response against the credential's observed lifetime
- **THEN** the documentation explains that agreement is expected for `token_type=batch` and that a `lease_duration` longer than the observed lifetime is the expected signature of a service token with a shorter TTL

#### Scenario: Operator rules out Harbor as the cause
- **WHEN** a robot issued by the engine stops being accepted within hours of issuance
- **THEN** the documentation states that Harbor's robot `duration` has day granularity, so no Harbor-side expiry mechanism — including the `robot_token_duration` system setting — can end a robot's life within hours, and that such a robot is therefore being deleted by lease revocation rather than expiring, which excludes Harbor and the issuer credential as causes

### Requirement: Documented consumer refresh requirements
The documentation SHALL distinguish consumers that renew a lease from consumers that do not, and SHALL state the refresh rule each kind must satisfy. For a renewing consumer (for example Vault Secrets Operator or Vault Agent), one lease is held and extended, and the engine's renewal keeps the robot's Harbor expiry ahead of it. For a non-renewing consumer (for example the External Secrets Operator `VaultDynamicSecret` generator, which mints a fresh credential on each reconcile and never renews), the documentation SHALL state that the refresh interval MUST be shorter than the effective credential lifetime defined above, and SHALL name the counter-pressure: an interval far below the lease TTL multiplies live robot accounts, because a non-renewing consumer does not revoke the credential it replaces.

#### Scenario: Operator configures a non-renewing consumer
- **WHEN** an operator wires a consumer that re-reads `creds/<role>` on an interval rather than renewing a lease
- **THEN** the documentation tells them to keep that interval below both the role's TTL and the lifetime of the token the consumer authenticates with, and explains that an interval equal to the lease TTL leaves no margin

#### Scenario: Operator weighs a shorter refresh interval
- **WHEN** an operator considers lowering a refresh interval to work around credentials expiring early
- **THEN** the documentation states the resulting robot churn and that raising the parent token's lifetime addresses the cause while shortening the interval only masks it
