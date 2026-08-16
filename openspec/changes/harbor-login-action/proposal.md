## Why

Using the Harbor secrets engine from GitHub Actions today takes several manual steps: authenticate to Vault (usually via `hashicorp/vault-action`), read `harbor/creds/<role>`, wire the fields into `docker login`, and — because nothing revokes the lease — leave a live Harbor robot account behind until its TTL expires. That last part defeats the point of dynamic credentials: a 1-hour TTL means every CI run leaves a usable registry credential alive for up to an hour after the job ends. No existing action closes the loop; `hashicorp/vault-action` fetches secrets but never revokes dynamic leases.

A first-party action turns the whole flow into one step — GitHub OIDC → Vault → Harbor robot → `docker login` — and revokes the lease (deleting the robot in Harbor) when the job finishes.

## What Changes

- New **TypeScript GitHub Action** in an `action/` subdirectory of this repository, consumed as `corelyr-oss/vault-plugin-secrets-harbor/action@action/v1`. It runs on `node24` so it can declare a `post:` step — the only action kind that can, and the reason the lease can be revoked automatically.
- **Vault authentication built in**: exchanges the workflow's GitHub OIDC token via Vault's JWT auth method (`role`, `auth-mount`, `audience` inputs), or uses a caller-supplied token (`vault-token` input / `VAULT_TOKEN`). No long-lived secrets required in the repository.
- **Credential issuance and login**: reads `<mount>/creds/<role>`, registers the secret as a masked value, and runs `docker login` against the Harbor registry so `docker`, `buildx` and `podman` work for the rest of the job.
- **Masked step outputs** — `username`, `secret`, `auth`, `registry`, `robot-id`, `expires-at`, `lease-id` — so other tools (`helm registry login`, `oras`, `skopeo`) can use the same credential without a second Vault read.
- **Automatic cleanup in the post step**: revokes the Vault lease with `sync=true` (so the Harbor robot is deleted before the job ends), revokes the OIDC-obtained Vault token, and runs `docker logout`. Each is individually opt-out-able.
- **Vault Enterprise / TLS support**: `namespace`, `ca-cert` and (discouraged) `tls-skip-verify` inputs.
- **Distribution**: bundled `action/dist` committed and drift-checked in CI, released under `action/vX.Y.Z` tags with a moving `action/vX` tag, versioned independently of the plugin.
- **Self-testing in CI**: an end-to-end job that starts the real Harbor + a Vault dev server with this plugin, configures JWT auth against GitHub's OIDC issuer, runs the action, pulls an image, and asserts the robot is gone afterwards.
- Explicit non-goals: no Kubernetes `dockerconfigjson` output (Vault Secrets Operator is the right tool for cluster pull secrets), no AppRole/userpass auth, no Marketplace listing (see Impact), no changes to the secrets engine itself.

## Capabilities

### New Capabilities
- `github-action/harbor-login`: the action's observable contract — inputs, Vault authentication precedence, credential issuance, Docker login, masked outputs, post-job revocation/logout, and error behaviour.
- `github-action/distribution`: how the action is built and shipped — bundled runtime, tag scheme independent of the plugin, dist drift protection, documented tested matrix, and consumption instructions.

### Modified Capabilities
<!-- none: the secrets engine is unchanged -->

## Impact

- New `action/` tree: `action.yml`, TypeScript sources, unit tests, bundled `dist/`, its own `package.json`/`tsconfig`/lint config, and `action/README.md`.
- New CI work: Node build + unit tests + `dist` drift check on every PR, plus an end-to-end job reusing `test/integration/harbor/up.sh`; a release workflow for `action/v*` tags.
- Vault API surface consumed: `POST /v1/auth/<mount>/login`, `GET /v1/<mount>/creds/<role>`, `POST /v1/sys/leases/revoke`, `POST /v1/auth/token/revoke-self`.
- Operators must configure Vault's JWT auth method for GitHub OIDC and a policy granting `read` on `<mount>/creds/<role>` plus `update` on `sys/leases/revoke`; this needs documenting in the README.
- **Consequence of the chosen layout**: because the action lives in `action/` rather than the repository root, it cannot be listed in the GitHub Marketplace (Marketplace requires a root `action.yml` and no other actions in the repository). It remains fully consumable by path reference. Moving it to a dedicated repository later is the migration path if a Marketplace listing is wanted.
- Repository `main` is protected by a ruleset requiring pull requests, so this work lands via PR rather than direct pushes.
