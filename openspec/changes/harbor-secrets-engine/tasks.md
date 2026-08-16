## 1. Project bootstrap

- [x] 1.1 Rename local repository directory to `vault-plugin-secrets-harbor`; create the GitHub repo `corelyr-oss/vault-plugin-secrets-harbor` (public, MIT license) and push `main`
- [x] 1.2 `go mod init github.com/corelyr-oss/vault-plugin-secrets-harbor` on Go 1.25; add `hashicorp/vault/sdk` (latest v0.25.x), `hashicorp/go-hclog`, `stretchr/testify`
- [x] 1.3 Add `.golangci.yml`, `Makefile` (build/test/lint/integration/dev targets), `.gitignore`, `LICENSE`, initial README skeleton, `CODEOWNERS`, Dependabot config for gomod + github-actions
- [x] 1.4 `cmd/vault-plugin-secrets-harbor/main.go`: `plugin.ServeMultiplex` with TLS provider, version from ldflags, `-version` flag printing the version

## 2. Harbor client (`internal/harbor`)

- [x] 2.1 Client struct: base URL, basic-auth credential, `http.Client` with timeout, custom CA / insecure TLS, User-Agent; context-aware `do()` helper with structured `*APIError{Status, Code, Message}` mapping from Harbor's error envelope
- [x] 2.2 Models copied from Harbor 2.15 swagger for: `Robot`, `RobotCreate`, `RobotCreated`, `RobotSec`, `RobotPermission`, `Access`, `UserResp`, `PasswordReq`
- [x] 2.3 Endpoints: `Ping`, `CurrentUser`, `ChangeUserPassword(userID, old, new)`, `CreateRobot`, `GetRobot`, `UpdateRobot` (duration/description), `RefreshRobotSecret`, `DeleteRobot`, `ListRobots(query, pageSize)`
- [x] 2.4 Fake Harbor server (`internal/harbor/harbortest`) implementing the same endpoints with Harbor's validation (name regex, secret policy, duration rules, robot prefix, project `+` naming, scope check for robot creators) for use in unit tests
- [x] 2.5 Unit tests for the client against the fake (happy paths, auth failure, TLS with custom CA, timeouts, error mapping)

## 3. Backend core

- [x] 3.1 `internal/backend/backend.go`: `Factory`, `framework.Backend` with `Paths`, `Secrets`, `WALRollback`, `WALRollbackMinAge`, `Invalidate` (drop cached client), `BackendType: logical.TypeLogical`, help text
- [x] 3.2 Cached Harbor client per backend behind `sync.RWMutex`, rebuilt from stored config on demand and invalidated on config write/delete/rotate
- [x] 3.3 `naming.go`: robot name normalization + random suffix, unit-tested against Harbor's regex with property-style cases (uppercase, `_`, unicode, leading/trailing separators, very long role names)
- [x] 3.4 `duration.go`: `ttlToDays`, `durationForRenew(creationTime, wantExpiry)`; table-driven unit tests

## 4. Configuration paths

- [x] 4.1 `path_config.go`: `config` create/update (retain omitted secrets), read (non-secret fields only, `ca_cert_set`, `last_rotated`), delete; `auth_type` and field validation with named-field errors
- [x] 4.2 Connectivity verification on write per auth mode (`/users/current` for user, `GET /robots?page_size=1` for robot); `verify_connection=false` bypass
- [x] 4.3 `config/rotate-root`: user mode via `PUT /users/{id}/password`, robot mode via `PATCH /robots/{id}`; compliant secret generator; pending-marker two-phase write per design D-risk; response never contains the secret
- [x] 4.4 Unit tests for all config scenarios in the configuration spec (both modes, missing fields, invalid auth_type, wrong creds, custom CA, skip verify, read-before-write, delete then creds, rotate failures)

## 5. Roles

- [x] 5.1 `path_roles.go`: `roles/` list, `roles/<name>` write/read/delete with fields `level`, `permissions` (JSON), `ttl`, `max_ttl`, `description`
- [x] 5.2 Role validation: level enum, non-empty parsable permissions, project-level ⇒ single `kind=project` namespace, resource/action presence, `ttl ≤ max_ttl`
- [x] 5.3 Unit tests for all roles spec scenarios including TTL capping warnings against mount max

## 6. Dynamic credentials

- [x] 6.1 `secret_robot.go`: `framework.Secret` type `harbor_robot` with internal data `{robot_id, name, creation_time, expires_at, role}`, `Renew` and `Revoke` handlers; `secret`/`auth` flagged sensitive
- [x] 6.2 `path_creds.go`: WAL put → create robot (duration from `max_ttl`, description from role) → WAL update with robot_id → response `{username, secret, robot_id, expires_at, auth}` with lease TTL/max → WAL delete; pass Harbor scope errors through verbatim
- [x] 6.3 Renew: extend lease; if new expiry > `expires_at`, `PUT /robots/{id}` with recomputed duration, re-read `expires_at`, fail if still short or robot 404
- [x] 6.4 Revoke: `DELETE /robots/{id}`; 404 ⇒ success; other errors returned for retry
- [x] 6.5 `WALRollback`: delete robots for stale WAL entries by id or by exact name lookup; ignore 404
- [x] 6.6 Unit tests for every dynamic-credentials spec scenario against the fake Harbor, including simulated crash between create and commit (WAL rollback path) and audit-log HMAC flags

## 7. Integration testing

- [x] 7.1 Spike: bring up pinned Harbor via docker compose in GitHub Actions and record cold-boot time; decide caching strategy (record result in design.md)
- [x] 7.2 `test/integration` (build tag `integration`): helpers to start Harbor compose, seed a project, an admin/service user and an issuer robot (with `robot:*` scope), start Vault or OpenBao in dev mode with `-dev-plugin-dir`, register the plugin binary
- [x] 7.3 Integration tests: config write/verify both modes, rotate-root both modes, role CRUD, creds issuance + real registry login/pull with `crane`, renew extends `expires_at`, revoke deletes robot, WAL rollback cleans orphan
- [x] 7.4 GitHub Actions `ci.yml`: lint + unit on PR; integration matrix over Vault 1.16.x, latest 1.x, latest 2.0.x, OpenBao latest 2.x; required for merge

## 8. Release engineering

- [x] 8.1 `.goreleaser.yaml`: linux/darwin × amd64/arm64, `CGO_ENABLED=0`, `-trimpath`, version ldflags, archives, `SHA256SUMS`, cosign keyless signing of checksums, syft SBOM, conventional-commit changelog
- [x] 8.2 Multi-arch OCI image (distroless, entrypoint `vault-plugin-secrets-harbor`) pushed to `ghcr.io/corelyr-oss/vault-plugin-secrets-harbor`, cosign-signed; GitHub artifact attestations
- [x] 8.3 `release.yml` workflow on `v*` tags with `id-token: write`; verify with `cosign verify-blob` / `cosign verify` in a post-release job
- [x] 8.4 Smoke test job: verify keyless signatures, register the released binary by SHA256 in a Vault container and enable/configure it; run the OCI image and check it reports the tag (registration through a containerized plugin runtime needs gVisor on the host and is documented rather than automated)

## 9. Documentation

- [x] 9.1 README: install (binary + OCI), quick start, config in user and robot mode with minimum Harbor permissions for each, role permission examples (pull-only CI, push CI, K8s pull secret via VSO), rotate-root, upgrade/`secrets tune -plugin-version`
- [x] 9.2 `docs/compatibility.md`: tested Vault/OpenBao/Harbor matrix, updated by CI per release
- [x] 9.3 `CHANGELOG.md`, `CONTRIBUTING.md`, `SECURITY.md` (vulnerability reporting)
- [x] 9.4 Tag `v0.1.0` once the matrix is green
