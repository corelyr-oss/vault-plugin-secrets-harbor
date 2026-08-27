# github-action/distribution Specification

## Purpose
Defines how the GitHub Action is built, versioned and published from this repository so that workflows can reference a stable, reviewable version of it independently of the Vault plugin's own releases.
## Requirements
### Requirement: Action location and reference
The action's metadata and bundled runtime SHALL live under `action/` in this repository, and SHALL be consumable as `corelyr-oss/vault-plugin-secrets-harbor/action@<ref>`. Documentation SHALL state that this layout means the action is not listed in the GitHub Marketplace, which requires a repository-root action.

#### Scenario: Referencing the action
- **WHEN** a workflow declares `uses: corelyr-oss/vault-plugin-secrets-harbor/action@action/v1`
- **THEN** the action resolves and runs

### Requirement: Independent version tags
Action releases SHALL use tags of the form `action/vX.Y.Z`, distinct from the plugin's `vX.Y.Z` tags, and a moving `action/vX` tag SHALL be updated on each release to point at the newest compatible release. Plugin releases SHALL NOT move action tags, and action releases SHALL NOT move plugin tags.

#### Scenario: Major tag tracks the newest release
- **WHEN** `action/v1.2.0` is released after `action/v1.1.0`
- **THEN** `action/v1` points at `action/v1.2.0` and `v0.1.0` (a plugin tag) is unchanged

#### Scenario: Plugin release leaves the action alone
- **WHEN** the plugin is released as `v0.2.0`
- **THEN** no `action/*` tag changes

### Requirement: Bundled runtime is committed and drift-checked
The repository SHALL contain the action's bundled JavaScript runtime, produced reproducibly from the TypeScript sources, so that a referenced tag runs without a build step. CI SHALL rebuild the bundle on every pull request and fail when the committed bundle differs from the rebuilt output.

#### Scenario: Stale bundle is caught
- **WHEN** a pull request changes the action's TypeScript sources without rebuilding the bundle
- **THEN** CI fails with a message stating that the bundle is out of date and how to regenerate it

#### Scenario: Tagged reference runs without building
- **WHEN** a workflow uses a released action tag on a runner with no network access to a package registry
- **THEN** the action still runs

### Requirement: Verified compatibility
Every action release SHALL document the runner operating systems, Vault/OpenBao versions and Harbor versions it was tested against, and CI SHALL exercise the action end to end against a real Harbor and a real Vault before the release is published.

#### Scenario: End-to-end coverage
- **WHEN** CI runs on a pull request touching the action
- **THEN** a job authenticates via GitHub OIDC to a Vault running this plugin, obtains credentials through the action, pulls an image from a real Harbor, and asserts after the job's cleanup that the robot no longer exists

#### Scenario: Documented matrix
- **WHEN** a user reads an action release's notes
- **THEN** the tested runner, Vault/OpenBao and Harbor versions are listed

### Requirement: Usage documentation
The repository SHALL document, for the action: a minimal workflow example, the Vault-side prerequisites (JWT auth method configured for GitHub's OIDC issuer, a role bound to the repository, and a policy granting read on `<mount>/creds/<role>` and update on `sys/leases/revoke`), every input and output, and the cleanup behaviour. The documented prerequisites SHALL include sizing the auth role's token TTL: the lease the action obtains is a child of the Vault token it authenticates with, so a `token_ttl` shorter than the job causes Vault to revoke the lease — deleting the robot in Harbor — while the job is still running, regardless of the engine role's own TTL. The documentation SHALL state that this is independent of the action's post-step revocation, which runs either way.

#### Scenario: Operator can set up Vault from the docs
- **WHEN** an operator follows the documented Vault prerequisites
- **THEN** a workflow using the action authenticates and obtains credentials without further trial and error

#### Scenario: Workflow author sizes the auth role TTL
- **WHEN** a workflow author configures the Vault auth role the action logs in with
- **THEN** the documentation tells them the token's TTL must cover the longest job that uses the credential, and names an expiring parent token as the cause of a mid-job `401` from Harbor

