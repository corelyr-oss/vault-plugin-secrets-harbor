#!/usr/bin/env bash
# Stop and remove the integration Harbor started by up.sh (volumes included).
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
WORK="${HARBOR_WORKDIR:-$HERE/../.harbor}"
if [ -f "$WORK/harbor/docker-compose.yml" ]; then
  ( cd "$WORK/harbor" && docker compose down -v --remove-orphans ) || true
fi
if [ "${HARBOR_KEEP_DATA:-}" != "1" ]; then
  # Harbor's containers write as root; use a container to remove the data dir.
  docker run --rm -v "$WORK:/w" alpine:3 sh -c 'rm -rf /w/data /w/log' 2>/dev/null || rm -rf "$WORK/data" "$WORK/log" || true
fi
