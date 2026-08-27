# Harbor login action

Log in to a [Harbor](https://goharbor.io/) registry from GitHub Actions using a
**short-lived robot account** issued by the
[Vault Harbor secrets engine](../README.md) — and have it revoked automatically
when the job finishes.

```yaml
jobs:
  build:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      id-token: write            # required for the OIDC login below
    steps:
      - uses: corelyr-oss/vault-plugin-secrets-harbor/action@action/v0
        with:
          registry: harbor.example.com
          role: ci-pull
          vault-url: https://vault.example.com

      - run: docker pull harbor.example.com/library/app:latest
```

No long-lived registry credentials, and nothing left behind: when the job ends
the action revokes the Vault lease, which deletes the robot account in Harbor.

> This action is referenced by path (`…/action@ref`) and is not listed in the
> GitHub Marketplace, which requires an action at a repository root.

## What it does

1. Authenticates to Vault with the workflow's GitHub OIDC token (or a token you supply).
2. Reads `<mount>/creds/<role>`, which mints a Harbor robot account.
3. Masks the secret, publishes the credential as step outputs, and runs `docker login`.
4. In its post step: revokes the lease (`sync=true`, so the robot is deleted before
   the job ends), revokes the Vault token it obtained, and runs `docker logout`.

## Inputs

| Input | Required | Default | Description |
|---|---|---|---|
| `registry` | yes | | Harbor registry host, optionally `host:port` |
| `role` | yes | | Secrets engine role, read as `<mount>/creds/<role>` |
| `vault-url` | | `$VAULT_ADDR` | Vault or OpenBao base URL |
| `mount` | | `harbor` | Mount path of the Harbor secrets engine |
| `vault-token` | | `$VAULT_TOKEN` | Use this token instead of OIDC. Never revoked by the action |
| `auth-mount` | | `jwt` | Mount path of the JWT auth method |
| `auth-role` | | value of `role` | Vault JWT auth role |
| `audience` | | repository default | Audience for the OIDC token; must match the role's `bound_audiences` |
| `namespace` | | | Vault Enterprise namespace |
| `ca-cert` | | | PEM CA bundle for Vault's TLS certificate |
| `tls-skip-verify` | | `false` | Skip Vault TLS verification. Not recommended |
| `login` | | `true` | Run `docker login` |
| `revoke` | | `true` | Revoke the lease when the job ends |
| `logout` | | `true` | Run `docker logout` when the job ends |

## Outputs

| Output | Description |
|---|---|
| `username` | Full robot name, e.g. `robot$library+vault-ci-pull-1a2b3c4d` |
| `secret` | Robot secret (masked) |
| `auth` | `base64(username:secret)` for a Docker `config.json` (masked) |
| `registry` | Registry the credentials belong to |
| `robot-id` | Harbor robot account ID |
| `expires-at` | Harbor-side expiry (RFC 3339), or `-1` |
| `lease-id` | Vault lease backing the credentials |

## Vault prerequisites

The engine itself is set up as described in the [root README](../README.md).
For this action you additionally need a JWT auth method for GitHub OIDC and a
policy for the workflow.

```sh
# 1. Policy: read one role's credentials, and revoke the lease afterwards.
vault policy write harbor-ci - <<'POLICY'
path "harbor/creds/ci-pull" { capabilities = ["read"] }
path "sys/leases/revoke"    { capabilities = ["update"] }
POLICY

# 2. JWT auth method trusting GitHub's OIDC issuer.
vault auth enable jwt
vault write auth/jwt/config oidc_discovery_url=https://token.actions.githubusercontent.com

# 3. A role bound to your repository. bound_claims is a map, so pass JSON.
jq -n '{
  role_type: "jwt",
  user_claim: "workflow_ref",
  bound_audiences: ["https://github.com/my-org"],
  bound_claims_type: "glob",
  bound_claims: { repository: "my-org/my-repo" },
  token_policies: ["harbor-ci"],
  token_ttl: "20m"
}' | vault write auth/jwt/role/ci-pull -
```

Notes:
- `bound_audiences` must match the audience the workflow requests. With no
  `audience` input that is your organisation URL (`https://github.com/my-org`).
- Tighten `bound_claims` further for production, e.g.
  `{ "repository": "my-org/my-repo", "ref": "refs/heads/main" }`.
- **`token_ttl` bounds the credential too.** The lease the action obtains is a
  child of this token, so when it expires Vault revokes the lease and the robot
  is deleted in Harbor — mid-job if the job is still running, whatever the
  engine role's `ttl`/`max_ttl` say. The credential's effective lifetime is the
  lesser of the two. Size `token_ttl` to cover the longest job that uses the
  credential; the `20m` above suits a short pull-and-build job, not an hour-long
  matrix. This is independent of the action's post-step revocation, which runs
  either way.

## Recipes

**Use the credential with something other than Docker**

```yaml
- uses: corelyr-oss/vault-plugin-secrets-harbor/action@action/v0
  id: harbor
  with:
    registry: harbor.example.com
    role: ci-pull
    vault-url: https://vault.example.com
    login: false                       # no docker login; outputs only
- run: helm registry login harbor.example.com -u "$USER" -p "$PASS"
  env:
    USER: ${{ steps.harbor.outputs.username }}
    PASS: ${{ steps.harbor.outputs.secret }}
```

**Supply a Vault token instead of using OIDC** (self-hosted runners without OIDC):

```yaml
- uses: corelyr-oss/vault-plugin-secrets-harbor/action@action/v0
  with:
    registry: harbor.example.com
    role: ci-pull
    vault-url: https://vault.example.com
    vault-token: ${{ secrets.VAULT_TOKEN }}
```

A token supplied this way is used as-is and is never revoked by the action.

## Behaviour and limits

- **Cleanup never fails your job.** If Vault is unreachable in the post step the
  action warns and names the lease so you can revoke it by hand; the credential
  is still bounded by the lease TTL and by the robot's Harbor-side expiry.
- **No renewal.** If a job can outlive the credential, raise `ttl`/`max_ttl` on
  the engine role *and* `token_ttl` on the auth role rather than expecting the
  action to renew. Raising only the engine role leaves the parent token as the
  binding limit.
- **A mid-job `401` from Harbor is usually the Vault token, not Harbor.** If
  `docker pull` succeeds early in a job and fails later, check `token_ttl` on the
  auth role before looking at Harbor — see
  [Troubleshooting](../README.md#troubleshooting) in the root README.
- **A Docker CLI is required** for `login: true`; otherwise set `login: false`
  and use the outputs.
- **The bundle in `dist/` is deliberately not minified** so that it can be read
  and diffed during review. CI fails if it does not match the sources.

## Development

```sh
cd action
npm ci
npm run all      # format check, lint, typecheck, unit tests, bundle
```

Unit tests run against an in-memory fake Vault and a fake Docker CLI; no
network, no Docker. The end-to-end jobs in `.github/workflows/action-ci.yml`
run the action against a real Harbor and a real Vault, over both the OIDC and
the token path, and assert afterwards that the robot really is gone.
