## Why

Teams running Harbor want short-lived, least-privilege registry credentials minted by Vault instead of long-lived robot accounts pasted into CI variables and Kubernetes pull secrets. The only existing plugin (`manhtukhang/vault-plugin-harbor`, MIT) has been dormant since April 2024: it targets `vault/sdk` v0.11 / Go 1.22, never shipped a stable release, has open correctness bugs (robot naming rules), cannot operate without Harbor admin credentials, and has never been tested against Vault 2.x. HashiCorp Vault is now at 2.0.4 (`vault/sdk` v0.25.x, Go 1.25) and Harbor at 2.15, so a maintained, clean-room secrets engine is needed.

## What Changes

- New Go project `github.com/corelyr-oss/vault-plugin-secrets-harbor` (repository renamed from `vault-harbor-plugin` to match HashiCorp naming), a **Vault secrets engine plugin** for Harbor built on the current `hashicorp/vault/sdk`, served via `plugin.ServeMultiplex` with plugin version reporting.
- **Backend configuration** (`config`): Harbor URL, root credential, TLS options. Root credential can be either a Harbor **user** (admin or project-admin) or a Harbor **system/project robot account** (`auth_type=robot`) so Vault never needs admin credentials. Connectivity is verified on write.
- **Root credential rotation** (`config/rotate-root`): rotates the Harbor user's password (user mode) with a crash-safe two-phase write; the new value is never returned. Not possible in robot mode (Harbor has no `robot:update` permission, so robots cannot refresh their own secret) — the engine returns guidance instead.
- **Roles** (`roles/<name>`): robot level (`system` | `project`), a list of Harbor robot permissions, `ttl` / `max_ttl`, optional description template. Role input is validated against Harbor's permission model and naming rules before it is stored.
- **Dynamic credentials** (`creds/<name>`): mints a Harbor robot account per request, returns `username`, `secret`, `robot_id`, `expires_at`, and a Docker-ready base64 `auth` value; issues a Vault lease. **Renewal** extends the robot's Harbor-side expiry so long-running Kubernetes pull secrets managed by Vault Secrets Operator / Vault Agent keep working; **revocation** deletes the robot. Two orphan-protection layers: Harbor `duration` derived from `max_ttl`, plus WAL-based rollback for creates that never committed.
- **Distribution**: goreleaser builds for linux/darwin (amd64/arm64), SHA256SUMS, cosign keyless (Sigstore) signatures, SBOM, and an OCI image usable with Vault's containerized plugin runtime.
- **Compatibility guarantee**: CI runs unit tests plus an integration matrix against a real Harbor (docker compose) and real Vault (1.16 floor, current 1.x, 2.0.x) and OpenBao 2.x.
- Out of scope for this change: static roles with scheduled rotation, Harbor OIDC/LDAP users, plugin-portal listing.

## Capabilities

### New Capabilities
- `secrets-engine/configuration`: backend `config` path — Harbor endpoint, root credential in user or robot mode, TLS/timeouts, connectivity check, and `config/rotate-root`.
- `secrets-engine/roles`: role definition and validation — robot level, permissions, TTLs, name normalization to Harbor's naming rules.
- `secrets-engine/dynamic-credentials`: credential issuance via `creds/<role>`, response shape, lease renew (Harbor expiry extension), lease revoke (robot deletion), WAL rollback of orphaned robots.
- `plugin-distribution`: versioned/multiplexed plugin binary, signed release artifacts, checksums, SBOM, OCI image, registration instructions.
- `vault-compatibility`: supported Vault/OpenBao/Harbor version window and the integration test matrix that proves it.

### Modified Capabilities
<!-- none — greenfield project -->

## Impact

- New repository contents: Go module, `cmd/vault-plugin-secrets-harbor`, backend package, thin Harbor HTTP client (no third-party Harbor SDK), test fixtures, `docker-compose` for Harbor in CI, GitHub Actions workflows, goreleaser config, README/docs.
- External dependencies: `hashicorp/vault/sdk`, `hashicorp/vault/api` (tests), `hashicorp/go-hclog`, `stretchr/testify`; Harbor REST API v2.0 endpoints `/ping`, `/users/current`, `/users/{id}/password`, `/robots`, `/robots/{id}` (GET/PUT/PATCH/DELETE).
- Operational: consumers register the plugin with the published SHA256 (or run the OCI image via a plugin runtime), enable it at a mount, and configure a Harbor user or robot with permission to manage robots.
- Repository rename `vault-harbor-plugin` → `vault-plugin-secrets-harbor` (local directory and future GitHub repo) before first push.
