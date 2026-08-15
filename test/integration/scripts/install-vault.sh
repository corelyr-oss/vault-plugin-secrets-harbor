#!/usr/bin/env bash
# Install a Vault or OpenBao binary for the integration matrix.
# Usage: install-vault.sh <vault|bao> <version-without-v> <dest-dir>
# Prints the path of the installed binary.
set -euo pipefail
KIND="$1"; VERSION="$2"; DEST="$3"
mkdir -p "$DEST"
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
esac
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
case "$KIND" in
  vault)
    URL="https://releases.hashicorp.com/vault/${VERSION}/vault_${VERSION}_${OS}_${ARCH}.zip"
    curl -fsSL "$URL" -o "$TMP/vault.zip"
    ( cd "$TMP" && unzip -q vault.zip vault )
    install -m 0755 "$TMP/vault" "$DEST/vault"
    echo "$DEST/vault"
    ;;
  bao)
    URL="https://github.com/openbao/openbao/releases/download/v${VERSION}/openbao_${VERSION}_${OS}_${ARCH}.tar.gz"
    curl -fsSL "$URL" -o "$TMP/bao.tgz"
    tar -xzf "$TMP/bao.tgz" -C "$TMP" bao
    install -m 0755 "$TMP/bao" "$DEST/bao"
    echo "$DEST/bao"
    ;;
  *)
    echo "unknown kind: $KIND (vault|bao)" >&2; exit 2 ;;
esac
