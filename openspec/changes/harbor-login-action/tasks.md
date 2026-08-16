## 1. Action project bootstrap

- [x] 1.1 Create `action/` with `package.json` (private, type module, Node 24 engines), `tsconfig.json` (ES2023, strict), and pinned devDependencies: `typescript`, `@vercel/ncc`, `vitest`, `@types/node`
- [x] 1.2 Add runtime dependencies `@actions/core`, `@actions/exec`, `undici`; add `npm` lockfile and commit it
- [x] 1.3 Add lint/format config for the action tree (eslint flat config or biome) and wire `npm run lint`, `npm run test`, `npm run build` (ncc → `action/dist/index.js`), `npm run all`
- [x] 1.4 Write `action/action.yml`: metadata, all inputs and defaults from the harbor-login spec, all outputs, `runs.using: node24`, `main` and `post` both `dist/index.js`, `post-if: always()`

## 2. Vault client (`action/src/vault.ts`)

- [x] 2.1 Typed client over global `fetch`: base URL, optional `X-Vault-Namespace`, per-request timeout, custom CA via an `undici` Agent from `ca-cert`, `tls-skip-verify` with a logged warning
- [x] 2.2 Structured error mapping from Vault's `{"errors":[...]}` envelope into an error carrying status, path and message; helpers for 403/404 discrimination
- [x] 2.3 `loginJwt(authMount, role, jwt)` → client token + accessor; `readCreds(mount, role)` → lease id, TTL and data; `revokeLease(leaseId, {sync: true})`; `revokeSelfToken()`
- [x] 2.4 Unit tests against a fake Vault HTTP server: success paths, wrong role, unbound audience, 403, unreachable host, namespace header, custom CA

## 3. Inputs and authentication (`action/src/inputs.ts`, `action/src/auth.ts`)

- [x] 3.1 Parse and validate inputs per the spec table; resolve `vault-url` from input or `VAULT_ADDR`, `auth-role` defaulting to `role`; fail with the offending input named
- [x] 3.2 Authentication precedence: `vault-token` input → `VAULT_TOKEN` → OIDC via `core.getIDToken(audience?)`; record whether the token is action-owned
- [x] 3.3 Detect the missing-OIDC case (no `ACTIONS_ID_TOKEN_REQUEST_*`) and fail with guidance to add `permissions: id-token: write` or supply `vault-token`
- [x] 3.4 Unit tests for precedence, defaults, missing inputs, and the OIDC-unavailable message

## 4. Main step (`action/src/main.ts`)

- [x] 4.1 Authenticate, read `<mount>/creds/<role>`, and `core.setSecret` the `secret` and `auth` values before anything else touches them
- [x] 4.2 Set outputs `username`, `secret`, `auth`, `registry`, `robot-id`, `expires-at`, `lease-id`; log a non-secret summary (robot name, id, Harbor expiry)
- [x] 4.3 `docker login --username <user> --password-stdin <registry>` via `@actions/exec` when `login` is true; fail with a pointer to `login: false` when no Docker CLI is present
- [x] 4.4 `saveState` for lease id, registry, token ownership and login state; skip login cleanly when `login` is false
- [x] 4.5 Surface Vault/Harbor errors verbatim (including Harbor's permission-scope message) via `core.setFailed`
- [x] 4.6 Unit tests covering issuance, masking, output shape, login invocation and the failure paths

## 5. Post step (`action/src/post.ts`)

- [x] 5.1 Branch main vs post on saved state within the single bundle
- [x] 5.2 Revoke the lease with `sync: true` when `revoke` is true; revoke the action-owned Vault token; never revoke a caller-supplied token
- [x] 5.3 `docker logout <registry>` when `logout` is true and the action logged in
- [x] 5.4 Report every cleanup failure as `core.warning` naming the lease id; never change the job result; note the remaining TTL when `revoke` is false
- [x] 5.5 Unit tests: happy path, revoke disabled, Vault unreachable, caller-supplied token untouched, post-after-failed-main

## 6. End-to-end CI

- [x] 6.1 Workflow job (`permissions: id-token: write`) that starts Harbor via `test/integration/harbor/up.sh`, seeds a project and an image, and starts a Vault dev server with the built plugin registered
- [x] 6.2 Configure the engine (`config`, `roles/ci-pull`) and enable the JWT auth method against `https://token.actions.githubusercontent.com` with a role bound to this repository and a policy granting `read` on `<mount>/creds/*` and `update` on `sys/leases/revoke`
- [x] 6.3 Use the action from the working tree (`uses: ./action`), then pull the seeded image in a later step to prove the login works
- [x] 6.4 Assert cleanup: a follow-up job (or a step reading Harbor's API after a nested job) verifies the robot no longer exists and the issued credential is rejected
- [x] 6.5 Fork-safe fallback path exercising `vault-token` instead of OIDC, and a documented note in CI about which cells fork PRs skip

## 7. Build integrity and repository CI

- [x] 7.1 CI job: `npm ci`, lint, unit tests, `npm run build`, then fail if `git diff --exit-code action/dist` shows drift, with a message naming the regeneration command
- [x] 7.2 Add the action's paths to the existing CI workflow triggers and keep the plugin matrix unaffected by action-only changes
- [x] 7.3 Extend Dependabot to the `action/` npm ecosystem

## 8. Release

- [x] 8.1 `action/v*`-triggered release workflow: verify the bundle matches sources, run the end-to-end job, create the GitHub release with the tested matrix in the notes
- [x] 8.2 Move the `action/vX` major tag as part of the release workflow, and confirm plugin tags are untouched
- [x] 8.3 Tag `action/v0.1.0` once the end-to-end job is green

## 9. Documentation

- [x] 9.1 `action/README.md`: minimal example, full input/output tables, cleanup behaviour, `login: false` usage with other tools (helm/oras/skopeo)
- [x] 9.2 Vault prerequisites: JWT auth method for GitHub OIDC, role bound to the repository, policy for `creds` read and `sys/leases/revoke` update, audience guidance
- [x] 9.3 Root README section linking to the action, stating the `owner/repo/action@action/vX` reference form and that no Marketplace listing exists with this layout
- [x] 9.4 Note the tested runner/Vault/OpenBao/Harbor matrix in `docs/compatibility.md`
