# Compatibility

`vault-plugin-secrets-harbor` is built against `github.com/hashicorp/vault/sdk` and
communicates with Vault/OpenBao over the multiplexed gRPC plugin protocol. Every
pull request and tag runs the integration suite (`test/integration`) against a
real Harbor (docker compose, official installer) and real Vault/OpenBao dev
servers.

## Matrix (CI, `.github/workflows/ci.yml`)

| Cell | Server | Harbor | Notes |
|---|---|---|---|
| `vault-1.16` | Vault 1.16.3 | 2.15.2 | oldest supported Vault |
| `vault-1.21` | Vault 1.21.4 | 2.15.2 | latest 1.x |
| `vault-2.0` | Vault 2.0.4 | 2.15.2 | current line |
| `vault-2.0-harbor-2.12` | Vault 2.0.4 | 2.12.4 | oldest Harbor supporting robot mode |
| `openbao-2.6` | OpenBao 2.6.1 | 2.15.2 | protocol compatibility |

Bump the pinned versions in the workflow when new releases appear; the
`install-vault.sh` script downloads any Vault (`releases.hashicorp.com`) or
OpenBao (GitHub releases) version.

## GitHub Action matrix (`.github/workflows/action-ci.yml`)

The action in `action/` is tested end to end against a real Harbor and a real
Vault on every pull request and before every `action/v*` release.

| Cell | What it proves |
|---|---|
| `e2e` | GitHub OIDC → Vault JWT auth → credentials → `docker login` → `docker pull`, then the post step deletes the robot |
| `e2e-token-auth` | The same flow with a supplied Vault token; runs for pull requests from forks, which cannot use OIDC |

| Component | Tested |
|---|---|
| Runner | `ubuntu-latest` (GitHub-hosted) |
| Action runtime | `node24` |
| Vault | 2.0.4 |
| Harbor | 2.15.2 |

Cleanup is asserted by a test-only action (`test/e2e/assert-cleanup`) whose post
step runs after the login action's post step, because post steps run in reverse
order. It fails the job if the robot still exists or if the revoked credential
can still pull.

## Requirements per feature

| Feature | Requirement |
|---|---|
| Dynamic robot accounts (user mode) | Harbor ≥ 2.2 (robot accounts v2) |
| Robot-mode issuer (`auth_type=robot`), project-level issuer | Harbor ≥ 2.12.1 (robots may create robots within their own scope; 2.12.x looks the creator up per project) |
| Robot-mode issuer, system-level issuer serving several projects | Harbor ≥ 2.13 (creator taken from the security context) |
| `config/rotate-root` | user mode only — Harbor has no `robot:update` permission |
| Plugin version reporting / `secrets tune -plugin-version` | Vault ≥ 1.12 |
| Containerized plugin runtime (OCI image) | Vault ≥ 1.15 on Linux with gVisor |
| GitHub Action, OIDC login | Vault JWT auth method configured for GitHub's OIDC issuer; job permission `id-token: write` |
| GitHub Action, automatic revocation | Policy granting `update` on `sys/leases/revoke` |

## Verified Harbor API semantics (2.15.2)

Documented here because the plugin depends on them:

- Robot short names must match `^[a-z0-9]+(?:[._-][a-z0-9]+)*$`; Harbor prefixes `robot$` (configurable) and, for project robots, `<project>+`.
- `duration` is in days (`-1` = never); `PUT /robots/{id}` recomputes `expires_at = creation_time + duration days`.
- Robot secrets must be 8–128 chars with lower, upper and digit.
- Robot permission actions: `robot:{create,read,list,delete}` — no `update`.
- `GET /robots` without `q=Level=project,ProjectID=<id>` lists system-level robots only; robot principals need the project filter.
- `q=name=<x>` is an exact match, `q=name=~<x>` is fuzzy.
- Robots created by a robot must have permissions ⊆ the creator's; Harbor answers `403 DENIED "permission scope is invalid…"` otherwise (2.12.x answers a bare `403 DENIED: denied`, and also when the creator is a system-level robot).
