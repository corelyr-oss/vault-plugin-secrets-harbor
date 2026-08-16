# vault-plugin-secrets-harbor

[![CI](https://github.com/corelyr-oss/vault-plugin-secrets-harbor/actions/workflows/ci.yml/badge.svg)](https://github.com/corelyr-oss/vault-plugin-secrets-harbor/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/corelyr-oss/vault-plugin-secrets-harbor?sort=semver)](https://github.com/corelyr-oss/vault-plugin-secrets-harbor/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

A [HashiCorp Vault](https://www.vaultproject.io/) / [OpenBao](https://openbao.org/) **secrets engine plugin for [Harbor](https://goharbor.io/)**.
It mints short-lived Harbor **robot accounts** on demand, ties them to Vault leases, and deletes them when the lease is revoked or expires — so CI pipelines and Kubernetes image pull secrets never carry long-lived registry credentials.

```
vault read harbor/creds/ci
Key                Value
---                -----
lease_id           harbor/creds/ci/U9fDX2Eeg9coNhg8Akx8brMT
lease_duration     1h
lease_renewable    true
username           robot$library+vault-ci-4621ba5e
secret             ********
robot_id           2
expires_at         2026-08-16T21:43:40Z
auth               cm9ib3QkbGl...   # base64(username:secret), ready for a Docker config.json
```

Built clean-room on the current `hashicorp/vault/sdk`, tested in CI against **Vault 1.16 → 2.0.x**, **OpenBao 2.x** and **Harbor 2.12 → 2.15** (see [Compatibility](#compatibility)).

## Features

- **Dynamic credentials** – one robot account per `creds/<role>` read, with the role's Harbor permissions and a Vault lease.
- **Renewal-aware** – the robot's Harbor-side expiry always covers the lease; long-running Kubernetes pull secrets managed by [Vault Secrets Operator](https://developer.hashicorp.com/vault/docs/platform/k8s/vso) or Vault Agent keep renewing one lease.
- **Revocation deletes the robot** – explicitly or when the lease expires.
- **Two root-credential modes** – a Harbor *user* (admin or project admin), or a Harbor *robot account* so Vault never holds admin credentials.
- **`config/rotate-root`** – rotate the Harbor user password Vault uses, crash-safely.
- **Orphan protection** – WAL-based rollback deletes robots whose issuance never completed, and every robot also has a Harbor-side expiry.
- **Harbor naming rules handled** – any Vault role name yields a valid robot name.
- **Signed releases** – Sigstore/cosign keyless signatures, SBOMs, provenance attestations, and a multi-arch OCI image for Vault's containerized plugin runtime.

## Install

### From a release (binary)

```sh
VERSION=v0.1.0   # see the releases page
OS=linux ARCH=amd64
curl -fsSLO "https://github.com/corelyr-oss/vault-plugin-secrets-harbor/releases/download/${VERSION}/vault-plugin-secrets-harbor_${VERSION#v}_${OS}_${ARCH}.tar.gz"
curl -fsSLO "https://github.com/corelyr-oss/vault-plugin-secrets-harbor/releases/download/${VERSION}/SHA256SUMS"
sha256sum --ignore-missing -c SHA256SUMS
tar -xzf "vault-plugin-secrets-harbor_${VERSION#v}_${OS}_${ARCH}.tar.gz" vault-plugin-secrets-harbor

# Copy into Vault's plugin_directory (see your server config), then:
SHA=$(sha256sum /etc/vault/plugins/vault-plugin-secrets-harbor | cut -d' ' -f1)
vault plugin register -sha256="$SHA" -version="$VERSION" secret vault-plugin-secrets-harbor
vault secrets enable -path=harbor vault-plugin-secrets-harbor
```

Verify the release before trusting it (keyless Sigstore signature over the checksums file):

```sh
cosign verify-blob --certificate SHA256SUMS.pem --signature SHA256SUMS.sig \
  --certificate-identity-regexp='^https://github.com/corelyr-oss/vault-plugin-secrets-harbor/' \
  --certificate-oidc-issuer=https://token.actions.githubusercontent.com SHA256SUMS
```

### From the OCI image (containerized plugin runtime)

If your Vault has a [plugin runtime](https://developer.hashicorp.com/vault/docs/plugins/containerized-plugins) configured (Linux, gVisor):

```sh
cosign verify ghcr.io/corelyr-oss/vault-plugin-secrets-harbor:0.1.0 \
  --certificate-identity-regexp='^https://github.com/corelyr-oss/vault-plugin-secrets-harbor/' \
  --certificate-oidc-issuer=https://token.actions.githubusercontent.com
vault plugin register -oci_image=ghcr.io/corelyr-oss/vault-plugin-secrets-harbor \
  -version=v0.1.0 -runtime=<your-runtime> secret vault-plugin-secrets-harbor
vault secrets enable -path=harbor vault-plugin-secrets-harbor
```

### Upgrading

```sh
vault plugin register -sha256=<new sha> -version=v0.2.0 secret vault-plugin-secrets-harbor
vault secrets tune -plugin-version=v0.2.0 harbor/
vault plugin reload -plugin vault-plugin-secrets-harbor
```

## Quick start

```sh
# 1. Point the engine at Harbor (user mode; see below for robot mode)
vault write harbor/config \
    url=https://harbor.example.com \
    username=vault-svc password='S3cr3t-Passw0rd'

# 2. Define a role: robot level, Harbor permissions, TTLs
vault write harbor/roles/ci-pull \
    level=project \
    permissions='[{"kind":"project","namespace":"library","access":[{"resource":"repository","action":"pull"}]}]' \
    ttl=1h max_ttl=24h

# 3. Get credentials
vault read harbor/creds/ci-pull
```

Use them like any registry credential:

```sh
docker login harbor.example.com -u 'robot$library+vault-ci-pull-4621ba5e' -p '<secret>'
```

or drop `auth` straight into a Docker `config.json` / Kubernetes `kubernetes.io/dockerconfigjson` secret:

```json
{"auths":{"harbor.example.com":{"auth":"<auth from vault>"}}}
```

## Configuration

### `config`

| Field | Required | Default | Description |
|---|---|---|---|
| `url` | yes | | Harbor base URL, e.g. `https://harbor.example.com` |
| `username` | yes | | Harbor user name, or the **full robot name** (`robot$vault-issuer`) when `auth_type=robot` |
| `password` | yes | | User password or robot secret |
| `auth_type` | | `user` | `user` or `robot` (see [Root credential modes](#root-credential-modes)) |
| `ca_cert` | | | PEM CA bundle for Harbor's TLS certificate |
| `insecure_skip_verify` | | `false` | Skip TLS verification (not recommended) |
| `timeout` | | `30s` | Per-request timeout |
| `robot_name_prefix` | | `vault` | Prefix for generated robot names (Harbor adds its own `robot$`) |
| `verify_connection` | | `true` | Verify the credential against Harbor before storing |

Reads of `config` never return secrets. Writing again keeps secret fields you omit.

### Root credential modes

**`auth_type=user`** — a Harbor local user. A *system administrator* can issue robots at any level; a *project administrator* can issue project-level robots for their projects. `config/rotate-root` rotates this user's password:

```sh
vault write -f harbor/config/rotate-root
```

**`auth_type=robot`** — a Harbor robot account, so Vault never stores admin credentials (Harbor ≥ 2.12.1). The issuer robot needs, for each project you want to issue for, the project-scoped permissions
`robot:create`, `robot:read`, `robot:list`, `robot:delete`, plus **every permission you intend to grant** to issued robots (Harbor requires an issued robot's permissions to be a subset of its creator's).

- **Harbor ≥ 2.13**: a single **system-level** issuer robot with `kind=project` permissions can serve several projects.
- **Harbor 2.12.x**: the issuer must be a **project-level** robot of the target project (Harbor looks the creator up per project) — use one issuer/mount per project.

Example issuer for project `library` (pull only), system-level:

```json
{"name":"vault-issuer","level":"system","duration":-1,"permissions":[
  {"kind":"project","namespace":"library","access":[
    {"resource":"robot","action":"create"},{"resource":"robot","action":"read"},
    {"resource":"robot","action":"list"},{"resource":"robot","action":"delete"},
    {"resource":"repository","action":"pull"}]}]}
```

(For a project-level issuer use `"level":"project"`; its login name is then `robot$library+vault-issuer`.)

```sh
vault write harbor/config url=https://harbor.example.com \
    auth_type=robot username='robot$vault-issuer' password='<robot secret>'
```

Robot-mode caveats (Harbor has no `robot:update` permission):
- `config/rotate-root` is **not available** — refresh the issuer robot's secret in Harbor as an administrator and write it to `config`.
- A renewal that would need to *extend* a robot's expiry (only possible if you raise a role's `max_ttl` after issuance) cannot be applied; the lease is capped at the robot's remaining lifetime with a warning instead of failing.

### `roles/<name>`

| Field | Required | Default | Description |
|---|---|---|---|
| `level` | yes | | `project` or `system` |
| `permissions` | yes | | JSON array of Harbor robot permissions (`vault write ... permissions=@perms.json` works) |
| `ttl` | | mount default | Lease TTL |
| `max_ttl` | | mount max | Max lease TTL; also sets the robot's Harbor expiry (`ceil(max_ttl / 24h)` days) |
| `description` | | `Managed by Vault (<mount>/<role>)` | Description stored on the robot |

Each permission is `{"kind": "project"|"system", "namespace": "<project>"|"/", "access": [{"resource": "...", "action": "...", "effect": "allow"|"deny"}]}`.
A `project`-level role holds exactly one `kind=project` permission for one project; a `system`-level role may mix `system` and `project` permissions across projects. Harbor lists the valid resource/action pairs at `GET /api/v2.0/permissions`.

Common roles:

```sh
# CI pull-only for one project
vault write harbor/roles/ci-pull level=project ttl=1h max_ttl=4h \
  permissions='[{"kind":"project","namespace":"library","access":[{"resource":"repository","action":"pull"}]}]'

# CI push+pull
vault write harbor/roles/ci-push level=project ttl=1h max_ttl=4h \
  permissions='[{"kind":"project","namespace":"library","access":[{"resource":"repository","action":"pull"},{"resource":"repository","action":"push"}]}]'

# Kubernetes image pull secret (long-lived, renewed by Vault Secrets Operator)
vault write harbor/roles/k8s-pull level=project ttl=24h max_ttl=720h \
  permissions='[{"kind":"project","namespace":"prod","access":[{"resource":"repository","action":"pull"}]}]'

# System-level: pull from several projects
vault write harbor/roles/multi level=system ttl=1h max_ttl=24h permissions='[
  {"kind":"project","namespace":"library","access":[{"resource":"repository","action":"pull"}]},
  {"kind":"project","namespace":"tools","access":[{"resource":"repository","action":"pull"}]}]'
```

### `creds/<role>`

Returns `username`, `secret`, `robot_id`, `expires_at` (Harbor-side expiry) and `auth` (`base64(username:secret)`), plus a renewable lease.

- **Renew** (`vault lease renew`) extends the lease within `max_ttl`; the robot's Harbor expiry always covers it.
- **Revoke** (`vault lease revoke`, or lease expiry) deletes the robot in Harbor.

### Kubernetes pull secret with Vault Secrets Operator

```yaml
apiVersion: secrets.hashicorp.com/v1beta1
kind: VaultDynamicSecret
metadata: {name: harbor-pull, namespace: app}
spec:
  mount: harbor
  path: creds/k8s-pull
  renewalPercent: 67
  destination:
    name: harbor-pull
    create: true
    type: kubernetes.io/dockerconfigjson
    transformation:
      templates:
        .dockerconfigjson:
          text: '{{ printf "{\"auths\":{\"harbor.example.com\":{\"auth\":\"%s\"}}}" (get .Secrets "auth") }}'
```

## GitHub Actions

A companion action in [`action/`](action/README.md) turns the whole flow into one
step: GitHub OIDC → Vault → Harbor robot → `docker login`, with the lease revoked
(and the robot deleted) when the job ends.

```yaml
permissions:
  contents: read
  id-token: write
steps:
  - uses: corelyr-oss/vault-plugin-secrets-harbor/action@action/v0
    with:
      registry: harbor.example.com
      role: ci-pull
      vault-url: https://vault.example.com
  - run: docker pull harbor.example.com/library/app:latest
```

The action is versioned independently of the plugin under `action/vX.Y.Z` tags
(with a moving `action/vX`). It is referenced by path and is not listed in the
GitHub Marketplace, which requires an action at a repository root. See
[action/README.md](action/README.md) for inputs, outputs and the Vault
prerequisites.

## Compatibility

| Component | Supported | Tested in CI |
|---|---|---|
| Vault | ≥ 1.16 through 2.0.x | 1.16.3, 1.21.4, 2.0.4 |
| OpenBao | ≥ 2.0 | 2.6.1 |
| Harbor | ≥ 2.12.1 (robot mode); ≥ 2.2 best effort in user mode | 2.12.4, 2.15.2 |

See [docs/compatibility.md](docs/compatibility.md) for details and per-release notes.

## Security notes

- Response fields `secret` and `auth` are marked sensitive; Vault HMACs all response data in audit logs by default.
- Robots get a Harbor-side expiry (`duration`) derived from `max_ttl`, so even a lost lease cannot leave a robot alive indefinitely.
- Prefer `auth_type=robot` or a project-admin user over the Harbor `admin` account.
- Report vulnerabilities as described in [SECURITY.md](SECURITY.md).

## Development

```sh
make test          # unit tests (fake Harbor, no Docker)
make action-test   # the GitHub Action's unit tests and bundle
make lint
make build         # bin/plugins/vault-plugin-secrets-harbor
make dev-harbor    # in-memory fake Harbor on :8089 (admin / Harbor12345)
make dev           # vault dev server with -dev-plugin-dir=bin/plugins

# integration: real Harbor via docker compose + real vault/bao
test/integration/harbor/up.sh
make integration   # or: VAULT_BIN=bao make integration
test/integration/harbor/down.sh
```

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE)
