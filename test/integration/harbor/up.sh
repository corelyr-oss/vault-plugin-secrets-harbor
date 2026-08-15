#!/usr/bin/env bash
# Start a real Harbor via the official online installer's docker compose.
# Usage: HARBOR_VERSION=v2.15.2 HARBOR_PORT=8090 ./up.sh
# Prints the Harbor URL when /api/v2.0/health reports every component healthy.
set -euo pipefail

HARBOR_VERSION="${HARBOR_VERSION:-v2.15.2}"
HARBOR_PORT="${HARBOR_PORT:-8090}"
# Harbor refuses the literal "127.0.0.1" as hostname; "localhost" is accepted.
# The hostname becomes Harbor's external URL (registry token realm), so it must
# be resolvable by the Docker daemon. Docker treats loopback registries as
# insecure, so plain HTTP works for docker login/push/pull.
HARBOR_HOSTNAME="${HARBOR_HOSTNAME:-localhost}"
HERE="$(cd "$(dirname "$0")" && pwd)"
WORK="${HARBOR_WORKDIR:-$HERE/../.harbor}"
mkdir -p "$WORK"
WORK="$(cd "$WORK" && pwd)"

INSTALLER="$WORK/harbor"
if [ ! -x "$INSTALLER/prepare" ]; then
  echo ">> downloading harbor online installer $HARBOR_VERSION"
  curl -fsSL "https://github.com/goharbor/harbor/releases/download/${HARBOR_VERSION}/harbor-online-installer-${HARBOR_VERSION}.tgz" \
    | tar -xz -C "$WORK"
fi

mkdir -p "$WORK/data" "$WORK/log"
sed -e "s|__HOSTNAME__|$HARBOR_HOSTNAME|" \
    -e "s|__PORT__|$HARBOR_PORT|" \
    -e "s|__DATA_DIR__|$WORK/data|" \
    -e "s|__LOG_DIR__|$WORK/log|" \
    "$HERE/harbor.yml.tmpl" > "$INSTALLER/harbor.yml"

echo ">> preparing harbor config"
( cd "$INSTALLER" && ./prepare >/dev/null )

# Harbor's compose sends every container's logs to its rsyslog container via
# the "syslog" log driver, which some Docker runtimes (e.g. Docker Desktop)
# reject. Strip the logging blocks so the default json-file driver is used and
# `docker compose logs` works for debugging.
python3 - "$INSTALLER/docker-compose.yml" <<'PY'
import re, sys
path = sys.argv[1]
out, skip_indent = [], None
for line in open(path):
    indent = len(line) - len(line.lstrip(" "))
    if skip_indent is not None:
        if line.strip() == "" or indent > skip_indent:
            continue
        skip_indent = None
    if re.match(r"^\s+logging:\s*$", line):
        skip_indent = indent
        continue
    out.append(line)
open(path, "w").write("".join(out))
PY

echo ">> starting harbor"
START=$(date +%s)
if ! ( cd "$INSTALLER" && docker compose up -d --quiet-pull ); then
  echo "!! docker compose up failed" >&2
  exit 1
fi

# API checks always go via 127.0.0.1; HARBOR_HOSTNAME may not resolve here.
URL="http://127.0.0.1:$HARBOR_PORT"
echo ">> waiting for $URL/api/v2.0/health"
for i in $(seq 1 180); do
  if body=$(curl -fsS "$URL/api/v2.0/health" 2>/dev/null); then
    if echo "$body" | grep -q '"status":"healthy"' && ! echo "$body" | grep -q '"status":"unhealthy"'; then
      echo ">> harbor healthy after $(( $(date +%s) - START ))s"
      # Give core a moment to finish DB migrations after the health flip.
      sleep 3
      curl -fsS -u admin:Harbor12345 "$URL/api/v2.0/users/current" >/dev/null
      echo "HARBOR_URL=$URL"
      echo "HARBOR_REGISTRY=$HARBOR_HOSTNAME:$HARBOR_PORT"
      exit 0
    fi
  fi
  sleep 2
done
echo "!! harbor did not become healthy in time" >&2
( cd "$INSTALLER" && docker compose ps && docker compose logs --tail=50 core ) >&2 || true
exit 1
