## Context

See proposal.md — Why. Constraints that shape the approach, verified rather than assumed:

- **Only JavaScript and Docker actions can declare `post:` steps**; composite actions cannot (GitHub metadata syntax reference: `runs.post`/`runs.post-if` appear solely under "runs for JavaScript actions"). Automatic lease revocation is the whole point of the action, so it must be a JavaScript action. `runs.using: node24` is supported.
- **GitHub Marketplace requires a repository-root `action.yml` and no other actions in the repository.** With the chosen `action/` layout the action is consumable as `owner/repo/action@ref` but cannot be listed. Recorded as a trade-off, not a defect.
- **Vault API surface** (verified against the docs and this plugin's own integration suite): `POST /v1/auth/<mount>/login` with `{role, jwt}` returns `auth.client_token`; `GET /v1/<mount>/creds/<role>` returns `{lease_id, lease_duration, renewable, data:{username, secret, auth, robot_id, expires_at}}`; `POST /v1/sys/leases/revoke` takes `{lease_id, sync}` where `sync: true` completes the revocation before returning; `POST /v1/auth/token/revoke-self` drops the action's own token.
- **The engine deletes the Harbor robot on revocation**, so revoking the lease synchronously is what actually kills the credential — not merely a bookkeeping step.
- The repository already runs a real Harbor in CI (`test/integration/harbor/up.sh`, healthy in ~25 s warm) and a Vault dev server with the plugin registered, which the action's end-to-end job can reuse.

## Goals / Non-Goals

**Goals:**
- One workflow step from GitHub OIDC to a working `docker login`, with no long-lived secrets in the repository.
- The credential is dead before the job finishes, in the normal case and after a failed job.
- Dependency surface small enough to review, in keeping with the plugin's hand-rolled-client philosophy.
- The action's failure messages tell the user which side (GitHub, Vault, Harbor) refused and why.

**Non-Goals:**
- Re-implementing `hashicorp/vault-action`: only the auth methods needed for GitHub runners (OIDC, supplied token).
- Renewing leases mid-job. A job outlives its credential only if the role's TTL is shorter than the job, which is a role configuration problem.
- Publishing to the Marketplace (excluded by the layout decision).
- Any change to the secrets engine.

## Decisions

### D1. TypeScript action on `node24`, bundled with `@vercel/ncc`
Forced by the `post:` requirement above. `node24` matches the runtime the repository's other actions already require after the recent dependency bumps (the Node-24 majors of `setup-go`, `golangci-lint-action`, `login-action`, `setup-qemu-action` all run green on these runners), so no runner compatibility risk is being introduced. The bundle is committed under `action/dist/` so a tag is runnable without a build. *Alternative*: a composite action — simpler and with no `dist/` to review, but it cannot clean up after itself, which would leave every CI run trailing a live registry credential.

### D2. Thin `fetch`-based Vault client, no Vault SDK
Node 24 ships a global `fetch`. The action needs four endpoints (D-Context), so a ~150-line typed client is smaller and more auditable than `node-vault` and its transitive graph — the same reasoning that produced `internal/harbor` on the plugin side. Custom CA support is implemented with an `undici` `Agent` built from the `ca-cert` input; `tls-skip-verify` sets `rejectUnauthorized: false` and logs a warning. *Alternative*: `NODE_EXTRA_CA_CERTS` — process-wide and must be set before the process starts, so it cannot be driven from an input.

### D3. Authentication precedence: explicit token → `VAULT_TOKEN` → OIDC
Explicit configuration wins over ambient configuration, and ambient over inference. Ownership matters for cleanup: only a token the action minted is revoked in the post step, because revoking a caller's token would break later steps that use it. The OIDC token is fetched with `@actions/core`'s `getIDToken(audience?)`; when `audience` is unset GitHub's default audience applies and the Vault role's `bound_audiences` must match it — a common misconfiguration, so the error path names both the audience used and the role.

### D4. `docker login --password-stdin`, not a hand-written `config.json`
Shelling out to the Docker CLI is what `docker/login-action` does, and it is the only way that respects credential stores/helpers configured on the runner; writing `~/.docker/config.json` directly silently bypasses them and breaks on runners with a credential helper. The secret goes over stdin so it never appears in a process listing. If the CLI is missing the action fails with a message pointing at `login: false`. *Alternative*: write the config file — rejected for the reason above, although it would remove the Docker dependency.

### D5. `registry` is a required input rather than derived
The engine's credential response does not include the Harbor host, and reading `<mount>/config` to discover it would require CI policies to grant access to the backend configuration — a privilege escalation for a convenience. Requiring the host matches `docker/login-action`'s ergonomics. Recorded as a future option: the engine could add a `registry` field to the creds response, after which the input could become optional; that is deliberately not part of this change.

### D6. State passing and post-step semantics
The main step records `lease_id`, `registry`, whether it minted the Vault token, and whether it logged in, via `@actions/core`'s `saveState`; the post step reads them with `getState`. Revocation uses `sync: true` so the Harbor robot is actually deleted before the runner tears the job down — an asynchronous revoke could lose the race with the job ending. Cleanup failures are `core.warning`, never `setFailed`: a revoke that fails should not turn a green job red, and the credential is still bounded by its TTL and the robot's Harbor-side expiry. The warning names the lease so an operator can revoke it by hand. *Alternative*: fail the job on revoke errors — louder, but it converts a Vault availability blip into a false build failure while the security exposure is already time-bounded by two independent mechanisms.

### D7. Layout, tagging and release
```
action/
  action.yml            # runs.using: node24, main: dist/index.js, post: dist/index.js
  src/{main,post,vault,docker,inputs}.ts
  __tests__/            # unit tests against a fake Vault
  dist/index.js         # committed bundle (ncc), drift-checked in CI
  package.json, tsconfig.json, README.md
```
`action.yml` points `main` and `post` at the same bundle, which branches on a state variable — the standard single-bundle pattern, half the artifact size of two bundles. Releases are tagged `action/vX.Y.Z` with a moving `action/vX`, keeping the action's version line independent of the plugin's `vX.Y.Z` (D-Requirement in the distribution spec). A dedicated workflow triggered on `action/v*` performs the bundle check, the end-to-end test and the major-tag move.

### D8. Testing
- **Unit** (vitest): input parsing and precedence, the Vault client against an `httptest`-style fake (auth, creds, revoke, error mapping), Docker argument construction, post-step branching. Runs on every PR, no Docker needed.
- **End-to-end** (the real thing): a CI job starts Harbor via the existing `up.sh`, starts a Vault dev server with the plugin, enables and configures the JWT auth method against `https://token.actions.githubusercontent.com` with a role bound to this repository, then uses the action from the checked-out working tree (`uses: ./action`) to log in, pulls an image pushed earlier in the job, and — after the job's own cleanup — asserts through Harbor's API that the robot is gone. This exercises the real OIDC exchange rather than a mock, which is the part most likely to break.
- The end-to-end job needs `permissions: id-token: write`, and therefore does not run for pull requests from forks; the unit tests and bundle check still do. That gap is stated in CI rather than hidden.

## Risks / Trade-offs

- [Committed `dist/` can drift from source, or hide a supply-chain change] → CI rebuilds and diffs on every PR; the bundle is produced only by the pinned `ncc` version from a clean `npm ci`.
- [Marketplace listing impossible with the `action/` layout] → documented in the README and the proposal; migration path is a dedicated repository, which only changes the `uses:` reference for consumers.
- [Fork PRs cannot run the OIDC end-to-end job] → the token-input path is exercised instead for forks, and maintainers re-run the full job on the merge commit.
- [Runner without a Docker CLI, or with a credential helper that rejects programmatic login] → `login: false` plus outputs covers the first; the second is why the CLI is used rather than a hand-written config file.
- [A job longer than the role's TTL loses access mid-run] → documented as a role-configuration concern with guidance to set `max_ttl` above the expected job duration; renewal is explicitly out of scope.
- [Vault unreachable in the post step leaves a robot alive] → bounded by the lease TTL and by the robot's Harbor-side expiry (`duration` derived from `max_ttl`); the warning names the lease id.

## Migration Plan

Purely additive: no existing behaviour changes and the plugin is untouched. First release is `action/v0.1.0` once the end-to-end job is green, with `action/v0` moving; `action/v1.0.0` after the input surface has survived a release cycle unchanged. Consumers roll back by pinning a previous `action/vX.Y.Z` tag.

## Open Questions

- Whether to also publish the bundle as a container action image for self-hosted runners without Node — deferrable, no effect on the input surface or the task breakdown.
