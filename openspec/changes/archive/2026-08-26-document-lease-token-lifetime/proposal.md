## Why

The engine's headline promise is "credentials are bound to a Vault lease: renewing the lease extends the robot's expiry, revoking it deletes the robot". That promise has an unstated precondition: **a Vault lease can never outlive the token that created it**. When the consumer authenticates with a short-lived Vault token, the token expires, Vault revokes its child leases, this engine's `Revoke` handler deletes the robot, and every consumer of that credential gets `401 Unauthorized` from Harbor — long before the `lease_duration` the engine reported.

Nothing surfaces this. For `token_type=default` (service) tokens Vault does not clamp the child lease's TTL at issuance, so `vault read <mount>/creds/<role>` honestly reports the role's full TTL and the lease then vanishes early. The engine is behaving correctly at every step, which is exactly what makes it hard to diagnose: the role is right, the mount is right, Harbor is right, and the credential still dies.

This was diagnosed on a production cluster on 2026-08-26 and cost roughly a day across two debugging sessions. The credential presented as a Harbor problem (`401` from `/service/token?service=harbor-registry`, surfacing as an Argo CD chart-rendering failure and as cluster-wide `ImagePullBackOff`), and the investigation spent most of its time inside Harbor and inside this plugin before reaching the Vault auth role. The root cause was `token_ttl=1h` on the Kubernetes auth role the External Secrets Operator logged in with, against a Harbor role declaring `ttl=24h max_ttl=48h`.

## What Changes

- **New "Troubleshooting" section in `README.md`**, led by the symptom operators will actually search for — *credentials stop working long before `lease_duration`* — covering:
  - the lease-outlives-token rule and how to check it (`vault read auth/<mount>/role/<role>` → `token_ttl`, `token_max_ttl`, `token_type`; `vault read sys/auth/<mount>/tune` when the role inherits);
  - the **service vs. batch token** distinction: batch tokens *are* clamped at issuance so `lease_duration` tells the truth, while `token_type=default`/`service` is the silent variant;
  - the elimination that makes this fast: Harbor's robot `duration` is in **days**, so no Harbor mechanism can express a sub-day expiry. A robot that dies in hours is being **deleted** by lease revocation, never expiring — which rules out Harbor, the `robot_token_duration` system setting, and the issuer robot in one step.
- **New "Consumer requirements" guidance in `README.md`** distinguishing renewing consumers (Vault Secrets Operator, Vault Agent — they hold one lease and renew it) from **non-renewing** consumers (the External Secrets Operator `VaultDynamicSecret` generator mints a fresh credential per reconcile and never renews). For a non-renewing consumer the usable credential life is `min(lease TTL, parent token TTL)`, so its refresh interval must sit comfortably below that minimum. Includes the trade-off: a refresh interval far below the lease TTL multiplies robot churn, because nothing revokes the credential it replaces.
- **New "Operational semantics" section in `docs/compatibility.md`**, alongside the existing "Verified Harbor API semantics", recording the Vault-side facts the plugin depends on but does not control — lease/parent-token lifetime, service vs. batch clamping, and the day-granularity of Harbor `duration`.
- **A note in `action/README.md`**: the bundled GitHub Action has the same exposure. Its lease is a child of the token it obtains from the OIDC exchange, so a short `token_ttl` on the JWT auth role can kill the credential mid-job. The action's post-step revocation is unaffected either way.
- No code changes. The engine's behaviour is correct as-is; this change closes a documentation gap.

## Capabilities

### New Capabilities
<!-- none -->

### Modified Capabilities
- `secrets-engine/dynamic-credentials`: adds requirements that the documented lease↔robot lifetime contract state its precondition — the effective credential lifetime is bounded by the lifetime of the Vault token that requested it — and that the documentation give operators the diagnostic and the consumer-side refresh-interval rule. This is a spec-level obligation in the same sense as `vault-compatibility`'s existing "The README SHALL state the tested matrix for each release".
- `github-action/distribution`: extends the existing "Usage documentation" requirement so the action's documented Vault prerequisites include sizing the auth role's token TTL to cover the job, since the action's lease is a child of that token.

## Impact

- Documentation only: `README.md`, `docs/compatibility.md`, `action/README.md`. No Go source, no tests, no CI, no API surface, no release artifacts.
- No behavioural change, so no version bump is required on its own; the content ships with whatever release follows.
- Considered and rejected as out of scope: emitting a Vault response warning when the caller's token is shorter-lived than the requested lease. The `logical.Request` the backend receives exposes `ClientTokenAccessor` but not the token's remaining TTL, so the engine cannot detect this condition without a self-referential Vault API call from inside the plugin. A separately motivated warning — when `path_creds.go` clamps a role's TTL down to the mount or system ceiling — would not have caught this failure and is tracked independently.
- Repository `main` is protected by a ruleset requiring pull requests, so this lands via PR.
