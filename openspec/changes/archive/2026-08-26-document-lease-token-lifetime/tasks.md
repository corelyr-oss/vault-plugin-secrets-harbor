## 1. Credential lifetime in the main README

- [x] 1.1 Add a "Credential lifetime" subsection under `## Configuration` → after `### creds/<role>` (README.md:201), stating that the effective lifetime is the lesser of the lease TTL and the remaining lifetime of the Vault token that read the path, and that revocation of that token deletes the robot.
- [x] 1.2 In the same subsection, state that the bound is invisible for service tokens (`lease_duration` reports the role's full TTL and the lease is revoked early anyway) and visible for batch tokens (`token_type=batch` is clamped at issuance).
- [x] 1.3 Extend `### Kubernetes pull secret with Vault Secrets Operator` (README.md:208) with the renewing-vs-non-renewing distinction: VSO/Vault Agent hold one lease and renew it; a non-renewing consumer such as the External Secrets Operator `VaultDynamicSecret` generator re-reads on an interval and must keep that interval below the effective lifetime. Name the churn counter-pressure — a non-renewing consumer does not revoke the credential it replaces.

## 2. Troubleshooting section

- [x] 2.1 Add `## Troubleshooting` to README.md between `## Security notes` (README.md:263) and `## Development` (README.md:270).
- [x] 2.2 Write the entry "Credentials stop working long before `lease_duration`": symptom (`401 Unauthorized` from Harbor, typically surfacing as `docker pull` / `helm` / `ImagePullBackOff` failures rather than as an auth error), cause, and the checks — `vault read auth/<mount>/role/<role>` for `token_ttl`, `token_max_ttl` and `token_type`, plus `vault read sys/auth/<mount>/tune` when the role inherits.
- [x] 2.3 In the same entry, record the elimination that shortens the hunt: Harbor's robot `duration` is in days (see docs/compatibility.md), so no Harbor-side mechanism — `duration` or the `robot_token_duration` system setting — can end a robot's life within hours. A robot dying in hours is being deleted by lease revocation, never expiring, which excludes Harbor and the issuer credential.
- [x] 2.4 State the fix ordering: raise the parent token's TTL (or use a renewing consumer) to address the cause; shortening a refresh interval only masks it and multiplies robot churn once the cause is fixed.
- [x] 2.5 Note that a correct role, correct mount tune and healthy Harbor do not exclude this cause — the failure looks identical to a Harbor problem from every consumer-side signal.

## 3. Operational semantics in docs/compatibility.md

- [x] 3.1 Add a `## Verified Vault semantics` section after the existing `## Verified Harbor API semantics` list, in the same terse bullet style.
- [x] 3.2 Record: a lease cannot outlive its parent token, and token expiry revokes every lease the token created; service tokens are not clamped at issuance while batch tokens are; Harbor `duration` day-granularity as the diagnostic that separates "deleted" from "expired".

## 4. GitHub Action documentation

- [x] 4.1 Extend `## Vault prerequisites` in action/README.md (action/README.md:70) with token TTL sizing: the action's lease is a child of the token from the OIDC exchange, so `token_ttl` on the JWT auth role must cover the longest job that uses the credential.
- [x] 4.2 Add the mid-job failure mode to `## Behaviour and limits` (action/README.md:138), and state that it is independent of the post-step revocation, which runs either way.

## 5. Verification

- [x] 5.1 Re-read all four sections end to end as an operator hitting the symptom cold; confirm the path from symptom → check → cause → fix is followable without prior knowledge.
- [x] 5.2 Confirm every internal cross-reference and heading anchor resolves, and that no line contradicts the existing `### Requirement: Harbor-side expiry covers the lease` behaviour described elsewhere in the README.
- [x] 5.3 Run `openspec validate --changes document-lease-token-lifetime --strict` and confirm the change is clean.
- [x] 5.4 Confirm no Go source, test, or CI file was touched (`git diff --stat` shows only `README.md`, `docs/compatibility.md`, `action/README.md`, and the OpenSpec change).
