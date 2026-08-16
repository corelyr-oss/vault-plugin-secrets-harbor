## Context

Greenfield Go project (see proposal.md — Why). Constraints that shape the approach:

- **Vault side**: `hashicorp/vault/sdk` v0.25.x (Go 1.25). Plugins are external gRPC processes; multiplexing (`plugin.ServeMultiplex`) and version reporting are the current baseline. Vault 2.0 kept the plugin protocol — the "works with latest Vault" property is delivered by a modern SDK plus a real test matrix, not by protocol work. OpenBao ships its own SDK module (`openbao/openbao/sdk/v2`) but speaks the same protocol, so we build once against the HashiCorp SDK and test on both.
- **Harbor side** (verified against Harbor 2.15 source): robot names must match `^[a-z0-9]+(?:[._-][a-z0-9]+)*$`; the login name is `<robotPrefix><name>` (system) or `<robotPrefix><project>+<name>` (project) where `robotPrefix` (default `robot$`) is a Harbor server setting; `duration` is in **days** (`-1` or positive int) and `PUT /robots/{id}` recomputes `expires_at = creation_time + duration days`; secrets must be 8–128 chars with lower+upper+digit; `PATCH /robots/{id}` refreshes a robot secret; `PUT /users/{id}/password` changes a user password; since Harbor 2.12.1 a robot with `robot:create` may create robots whose scope ⊆ its own.

## Goals / Non-Goals

**Goals:**
- Minimal, auditable dependency surface (this is a credential-minting component).
- Correct lease semantics for both short-lived CI creds and long-lived, continuously renewed K8s pull secrets.
- No orphaned robots under crash/network partition.
- Verified compatibility (matrix in CI) rather than asserted.

**Non-Goals:**
- Static roles / scheduled rotation of pre-existing robots (possible follow-up change).
- Managing Harbor projects, users, or policies beyond robots.
- Vault Enterprise-only features (namespaces are transparent to plugins anyway).

## Decisions

### D1. Clean-room implementation, not a fork
The existing MIT plugin is small but shaped around sdk 0.11 idioms and carries bugs (naming, no WAL, admin-only). Rewriting lets us structure around today's SDK and the robot-mode feature. We reuse its README structure and test ideas only. *Alternative*: fork + modernize — faster start, but we would spend most of the effort re-shaping it anyway.

### D2. Hand-rolled Harbor HTTP client (no third-party Harbor SDK)
We need six endpoints (`/ping`, `/users/current`, `/users/{id}/password`, `/robots` POST, `/robots/{id}` GET/PUT/PATCH/DELETE). A ~300-line `internal/harbor` package with typed request/response structs, `net/http`, context timeouts, TLS config, and structured error mapping (`*harbor.APIError{Status, Code, Message}`) is smaller and more auditable than `mittwald/goharbor-client` (pulls go-openapi, docker/docker…) or the generated `goharbor/go-client`. Model structs are copied verbatim from Harbor's swagger for the fields we use. *Alternative*: `goharbor/go-client` — official, but generated code with a large transitive graph and slow release cadence.

### D3. Two root-credential modes behind one `config`
`auth_type=user|robot`. Both use HTTP Basic auth against Harbor; the difference is (a) how connectivity is verified (`/users/current` only works for users; for robots we do `GET /robots?page_size=1` and accept 200 *or 403* — Harbor answers 401 for a bad robot credential and 403 for a valid one lacking system-wide `robot:list`), and (b) rotate-root: user mode changes the password; robot mode is **not possible** — verified on Harbor 2.15: the robot permission vocabulary is `robot:{create,read,list,delete}` (no `update`), so a robot cannot `PATCH` its own secret nor `PUT` a child robot. Robot mode requires an issuer robot with `robot:{create,read,list,delete}` in each target project; roles are constrained by Harbor's scope check, whose error we pass through. Verified in CI: Harbor 2.12.x looks the creator robot up by name *within the target project* (handler `CreateRobot`, `robotCtl.List(name, project_id)`), so only a project-level issuer works there and a system-level one gets a bare `403 DENIED`; Harbor ≥ 2.13 takes the creator from the security context and accepts a system-level issuer with project-kind permissions serving several projects. The integration suite covers both shapes. *Alternative*: robot mode only — cleaner, but locks out Harbor < 2.12.1 and admins who prefer a service user.

### D4. Robot naming
`<name_prefix>-<normalized-role>-<8 hex>` where `name_prefix` is a `config` field (default `vault`), normalization lowercases, replaces any run of chars outside `[a-z0-9]` with `-`, trims leading/trailing separators, and truncates so the total stays well under Harbor's 255-char limit. The credential `username` is always taken from the create response, never assembled, so Harbor's configurable robot prefix and the project-`+` form are always right.

### D5. TTL ↔ Harbor `duration` mapping
On create: `duration = max(1, ceil(max_ttl / 24h))`. Since Vault never renews past `issue + max_ttl`, the robot's expiry covers every renewal by construction; extension is only needed if the role's/mount's max TTL is raised later. On renew: compute `wantExpiry = now + newTTL`; if `wantExpiry > robot.expires_at`, `PUT /robots/{id}` with `duration = ceil((wantExpiry - robot.creation_time) / 24h)` (verified: Harbor recomputes `expires_at` from `creation_time`), then re-read. If the update fails (robot-mode issuers get 403), cap the lease at the robot's remaining lifetime with a warning rather than failing — the credential never outlives the robot and consumers re-request before then. Store `robot_id`, `creation_time`, and `expires_at` in the secret's internal data; on 404 fail renew.

### D6. Orphan protection with `framework.WAL`
Sequence for `creds/<role>`: `PutWAL({name, level, namespace, role})` → `POST /robots` → `DeleteWAL` → build `Secret` response. If `DeleteWAL` fails the robot is deleted and an error returned, so a surviving WAL entry always means no lease exists. `WALRollback` (framework rollback loop, entries older than `WALRollbackMinAge` = 10 min) finds the robot by short name — project-level robots via `GET /robots?q=Level=project,ProjectID=<id>,name=~<short>` (verified: the unfiltered listing shows system-level robots only, and `name=` is exact while `name=~` is fuzzy) — and deletes it. Robots also expire on their own via `duration`, so worst-case leakage is bounded even if rollback never runs. *Alternative*: rely on `duration` alone — simple, but a crashed create with `max_ttl=30d` leaves a live credential for a month.

### D7. Backend layout (`framework.Backend`)
```
cmd/vault-plugin-secrets-harbor/main.go     -- ServeMultiplex, version from ldflags
internal/backend/
   backend.go        Factory, paths, Secrets{robotSecret}, WALRollback, PeriodicFunc(none)
   path_config.go    config, config/rotate-root
   path_roles.go     roles/, roles/<name>
   path_creds.go     creds/<name>
   secret_robot.go   renew/revoke handlers
   client.go         cached *harbor.Client per mount, invalidated on config write/rotate
   naming.go, duration.go
internal/harbor/    thin HTTP client + models + errors
test/integration/   compose-based tests (build tag `integration`)
```
Client is cached in the backend struct behind a `sync.RWMutex`, rebuilt when `config` changes; `Invalidate` hook drops it on storage change (HA/perf-standby correctness).

### D8. Testing
- Unit: `httptest.Server` fake Harbor implementing the six endpoints with Harbor's validation rules (name regex, secret policy, duration) so unit tests catch spec violations. `logical.TestBackendConfig` + in-memory storage.
- Integration: `docker compose` (Harbor core+db+redis+registry+jobservice+portal via nginx, from the official installer's compose, pinned Harbor version) + Vault/OpenBao dev servers in `-dev-plugin-dir` mode; matrix over Vault `1.16.x`, latest `1.x`, latest `2.0.x`, OpenBao latest `2.x`. Test does a real `docker login`/`crane pull` with issued creds. Gated behind `//go:build integration`.
- Spike result (2026-08-16, Harbor v2.15.2 via the official online installer's compose, Docker Desktop): **healthy in 22 s** after images are cached, ~2–3 min including image pull. No caching tricks needed. Two portability fixes baked into `test/integration/harbor/up.sh`: strip the `syslog` `logging:` blocks from the generated compose (rejected by some Docker runtimes; json-file logs are also more useful in CI) and use `hostname: localhost` (Harbor refuses the literal `127.0.0.1`; Docker treats loopback registries as insecure so HTTP works). The API is exercised via `127.0.0.1:<port>`; the registry token realm is `localhost:<port>`.
- Registry checks are done natively over the Docker Registry v2 protocol (`/service/token` bearer flow, push a tiny OCI image as admin, pull-manifest with issued creds), so the integration suite does not depend on a `docker` CLI; when `docker` is present and `INTEGRATION_DOCKER=1`, a real `docker login`/`pull` is added.

### D8b. Plugin version reporting
Vault refuses to enable a plugin that self-reports a non-semver version (`-dev-plugin-dir` mode even aborts the dev server), and `vault plugin register -version` must equal the self-reported version. The backend therefore normalizes non-semver build strings (`dev`, `abc123-dirty`) to `v0.0.0-<sanitized>`; releases inject the tag via ldflags.

### D9. Release engineering
goreleaser: builds (linux/darwin × amd64/arm64, `CGO_ENABLED=0`, `-trimpath`, ldflags version), archives, `SHA256SUMS`, cosign keyless signing of checksums (GitHub OIDC), SBOM (syft), changelog from conventional commits, GHCR multi-arch image (`ko` or goreleaser docker manifests, distroless base) signed with cosign, GitHub attestations for provenance. Release on `v*` tags only. Dependabot for Go modules and Actions.

### D10. Naming and module path
Repository and module: `github.com/corelyr-oss/vault-plugin-secrets-harbor`; binary `vault-plugin-secrets-harbor`; plugin type `secret`, suggested name `harbor`. Matches HashiCorp's `vault-plugin-<type>-<name>` convention and how the Plugin Portal lists plugins.

## Risks / Trade-offs

- [Harbor compose is heavy/slow in CI] → spike boot time first; cache images; run the matrix in parallel jobs sharing one Harbor via a service network; keep unit tests independent of Docker.
- [Harbor changes robot API semantics between versions (e.g. duration recomputation from creation_time)] → integration matrix pins the oldest and newest supported Harbor; renew logic tolerates either recomputation base by re-reading `expires_at` after update and failing loudly if it is still short.
- [Robot mode scope check differs across Harbor versions] → document minimum Harbor 2.12.1 for robot mode; pass Harbor's error through unchanged.
- [Rotate-root in user mode locks Vault out if the write succeeds in Harbor but storage fails] → write the new credential to storage *before* calling Harbor with a "pending" marker, then finalize; on startup, if a pending marker exists, verify which credential works and settle. Document that admins should keep a break-glass path.
- [OpenBao drift from Vault protocol] → matrix cell for OpenBao; if it diverges we detect it, not users.
- [Days-granularity `duration` makes Harbor-side expiry up to 24h later than the lease] → acceptable; Vault revocation is authoritative, Harbor expiry is a safety net.

## Migration Plan

Greenfield: no migration. First release `v0.1.0` after the integration matrix is green; `v1.0.0` after one Harbor and one Vault minor release cycle without API changes. Rollback for consumers is `vault secrets tune -plugin-version=<previous>` + `vault plugin reload`.

## Open Questions

- Should the OCI image also be published to Docker Hub in addition to GHCR? (packaging only; can decide at first release)
- Whether to expose a `robots/` list/cleanup admin path (`LIST creds` of live robots) — useful for operators, but not required by any spec; revisit after v0.1.
