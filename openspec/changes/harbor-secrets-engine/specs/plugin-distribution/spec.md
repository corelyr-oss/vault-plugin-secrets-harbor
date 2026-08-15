## Purpose

Defines how the plugin is built, versioned, signed and published so operators can register it in Vault (as a binary or via the containerized plugin runtime) and verify its provenance.

## ADDED Requirements

### Requirement: Versioned, multiplexed plugin binary
The binary SHALL be named `vault-plugin-secrets-harbor`, serve the backend via the multiplexed plugin protocol, and report its semantic version (injected at build time) so `vault plugin list -detailed` and `vault secrets list -detailed` show it and `vault secrets tune -plugin-version` works.

#### Scenario: Version visible after registration
- **WHEN** an operator registers `vault-plugin-secrets-harbor` with `-version=v1.2.3` and enables it
- **THEN** `vault secrets list -detailed` shows plugin version `v1.2.3` for the mount

#### Scenario: Multiple mounts share one process
- **WHEN** the plugin is enabled at two mount paths
- **THEN** a single plugin process serves both (multiplexing) and each mount keeps its own configuration

### Requirement: Release artifacts
Every tagged release SHALL publish, via automated CI: archives for `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`; a `SHA256SUMS` file; a Sigstore/cosign keyless signature and certificate for the checksums file; an SPDX SBOM per archive; and a changelog. Artifacts SHALL be reproducible from the tagged commit.

#### Scenario: Verify a release
- **WHEN** an operator downloads `SHA256SUMS`, its `.sig` and `.pem`, and runs `cosign verify-blob` against the repository's GitHub OIDC identity
- **THEN** verification succeeds and the archive checksum matches

#### Scenario: Register from release checksum
- **WHEN** an operator copies the binary to Vault's plugin directory and runs `vault plugin register -sha256=<from SHA256SUMS> -version=<tag> secret harbor`
- **THEN** registration succeeds and `vault secrets enable harbor` works

### Requirement: OCI image for containerized plugin runtime
Every tagged release SHALL publish a multi-arch (`amd64`, `arm64`) OCI image to GHCR containing the plugin binary as entrypoint, signed with cosign, so it can be registered with `vault plugin register -oci_image=... -runtime=<runtime>`.

#### Scenario: Register via OCI image
- **WHEN** an operator has a Vault plugin runtime configured and registers the plugin with `-oci_image ghcr.io/corelyr-oss/vault-plugin-secrets-harbor -version <tag>`
- **THEN** the plugin can be enabled and issues credentials

### Requirement: Documentation
The repository SHALL include a README covering: installation (binary and OCI), configuration in user and robot mode including the minimum Harbor permissions each needs, role permission examples for common cases (pull-only CI, push CI, K8s pull secret), rotate-root, and the supported version matrix.

#### Scenario: Robot-mode setup guide
- **WHEN** an operator follows the README's robot-mode section
- **THEN** they can create the issuer robot in Harbor with the documented permissions and configure the engine without admin credentials
