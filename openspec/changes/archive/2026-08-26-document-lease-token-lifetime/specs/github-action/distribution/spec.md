## MODIFIED Requirements

### Requirement: Usage documentation
The repository SHALL document, for the action: a minimal workflow example, the Vault-side prerequisites (JWT auth method configured for GitHub's OIDC issuer, a role bound to the repository, and a policy granting read on `<mount>/creds/<role>` and update on `sys/leases/revoke`), every input and output, and the cleanup behaviour. The documented prerequisites SHALL include sizing the auth role's token TTL: the lease the action obtains is a child of the Vault token it authenticates with, so a `token_ttl` shorter than the job causes Vault to revoke the lease — deleting the robot in Harbor — while the job is still running, regardless of the engine role's own TTL. The documentation SHALL state that this is independent of the action's post-step revocation, which runs either way.

#### Scenario: Operator can set up Vault from the docs
- **WHEN** an operator follows the documented Vault prerequisites
- **THEN** a workflow using the action authenticates and obtains credentials without further trial and error

#### Scenario: Workflow author sizes the auth role TTL
- **WHEN** a workflow author configures the Vault auth role the action logs in with
- **THEN** the documentation tells them the token's TTL must cover the longest job that uses the credential, and names an expiring parent token as the cause of a mid-job `401` from Harbor
