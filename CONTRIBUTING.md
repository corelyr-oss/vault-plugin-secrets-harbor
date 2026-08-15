# Contributing

Thanks for helping! Bug reports, feature requests and pull requests are welcome.

## Development setup

- Go (see `go.mod`), `golangci-lint`, Docker (for integration tests), and a
  `vault` or `bao` binary on your PATH.
- `make test` runs the unit tests against an in-memory Harbor fake
  (`internal/harbor/harbortest`) that enforces Harbor's validation rules — no
  Docker needed.
- `make lint` must be clean.

## Integration tests

```sh
test/integration/harbor/up.sh          # real Harbor via docker compose (~30 s warm, ~3 min cold)
make integration                       # VAULT_BIN=bao make integration for OpenBao
test/integration/harbor/down.sh
```

The suite builds the plugin, starts a dev server, registers the plugin by
SHA256/version and exercises every spec scenario, including a real registry
push/pull over the Docker Registry protocol and WAL rollback.

## Planning

This repository uses [OpenSpec](https://github.com/Fission-AI/OpenSpec):
behaviour is specified under `openspec/`. For non-trivial changes, open or
update a change (`openspec/changes/<name>`) before implementing.

## Commit messages

Conventional commits (`feat:`, `fix:`, `docs:`, `test:`, `ci:`, `chore:`);
they drive the release changelog.

## Releasing

Tag `vX.Y.Z` on `main`. The release workflow builds, signs (Sigstore keyless),
publishes archives + SBOMs + the GHCR image, and runs a post-release
verification and smoke test.
