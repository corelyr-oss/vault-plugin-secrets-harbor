## Purpose

Defines how operators describe the shape of robot accounts the engine may mint: robot level, Harbor permissions, lease TTLs, and how role names are normalized to Harbor's robot naming rules.

## ADDED Requirements

### Requirement: Role definition
The engine SHALL accept a write to `<mount>/roles/<name>` with `level` (`system` | `project`, required), `permissions` (required; JSON list of Harbor robot permissions, each `{kind, namespace, access:[{resource, action, effect?}]}`), `ttl` (optional, default from mount), `max_ttl` (optional, default from mount), and `description` (optional, free text stored on the created robot; default `Managed by Vault (<mount>/<role>)`). Roles SHALL be listable at `<mount>/roles/` and readable/deletable at `<mount>/roles/<name>`.

#### Scenario: Create a project-scoped role
- **WHEN** an operator writes a role with `level=project` and a single permission of `kind=project`, `namespace=library`, `access=[{resource: repository, action: pull}]`
- **THEN** the role is stored and a read returns the same fields

#### Scenario: Create a system-scoped role
- **WHEN** an operator writes a role with `level=system` and permissions spanning multiple projects or `kind=system`
- **THEN** the role is stored

#### Scenario: List and delete
- **WHEN** an operator lists `roles/` after creating roles `a` and `b`, then deletes `a`
- **THEN** the list returns `a, b` and afterwards only `b`

### Requirement: Role validation
The engine SHALL reject a role write when: `level` is not `system` or `project`; `permissions` is empty or unparsable; a `project`-level role contains a permission whose `kind` is not `project` or contains more than one distinct `namespace`; any permission entry lacks `resource` or `action`; `ttl` exceeds `max_ttl` when both are set. Validation errors SHALL name the offending field.

#### Scenario: Project-level role with system permission
- **WHEN** an operator writes `level=project` with a permission `kind=system`
- **THEN** the write is rejected with an error naming the permission kind mismatch

#### Scenario: ttl greater than max_ttl
- **WHEN** an operator writes `ttl=2h` and `max_ttl=1h`
- **THEN** the write is rejected

#### Scenario: Empty permissions
- **WHEN** an operator writes a role with `permissions=[]`
- **THEN** the write is rejected

### Requirement: Robot names comply with Harbor naming rules
Robot account names the engine sends to Harbor SHALL match `^[a-z0-9]+(?:[._-][a-z0-9]+)*$`. The engine SHALL derive the name from a configurable prefix (default `vault`), the normalized role name, and a random suffix, lowercasing and replacing disallowed characters so any Vault-legal role name yields a valid robot name. The engine SHALL NOT prepend Harbor's robot prefix (`robot$`) itself; it SHALL use the name Harbor returns after creation as the credential username.

#### Scenario: Role name with uppercase and underscore
- **WHEN** a role is named `CI_Builder` and credentials are requested
- **THEN** the created robot's name matches the regex (for example `vault-ci-builder-3f9a1c2b`) and Harbor accepts it

#### Scenario: Robot prefix comes from Harbor
- **WHEN** Harbor is configured with a non-default robot name prefix
- **THEN** the returned credential `username` reflects Harbor's actual prefix

### Requirement: TTL bounds
Effective lease TTL for a role SHALL be `ttl` capped by `max_ttl`, both capped by the mount's `max_lease_ttl`. When neither is set the mount defaults apply.

#### Scenario: Role TTL above mount max
- **WHEN** a role has `ttl=72h` and the mount `max_lease_ttl` is `24h`
- **THEN** issued leases have a TTL of at most 24h and the response warns that the TTL was capped
