#!/usr/bin/env bash
# Bring up the live functional-test stack, seed auth, and run the black-box
# suite. Requires Docker and a live upstream key.
#
#   export TRAJECTA_LIVE_API_KEY=sk-...
#   tests/live/run-live.sh
#
# Everything the script writes outside the repository lives under
# $TRAJECTA_LIVE_WORKDIR (default /tmp/trajecta-test).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORKDIR="${TRAJECTA_LIVE_WORKDIR:-/tmp/trajecta-test}"
COMPOSE_FILE="$REPO_ROOT/tests/live/docker-compose.live.yml"

export TRAJECTA_LIVE_TRACE_DIR="${TRAJECTA_LIVE_TRACE_DIR:-$WORKDIR/traces}"
export TRAJECTA_LIVE_SERVER_URL="${TRAJECTA_LIVE_SERVER_URL:-http://127.0.0.1:18080}"
export TRAJECTA_LIVE_MONITOR_URL="${TRAJECTA_LIVE_MONITOR_URL:-http://127.0.0.1:18081}"
export TRAJECTA_LIVE_MONITOR_USER="${TRAJECTA_LIVE_MONITOR_USER:-admin}"
export TRAJECTA_LIVE_MONITOR_PASSWORD="${TRAJECTA_LIVE_MONITOR_PASSWORD:-change-me-live-pass}"
export TRAJECTA_LIVE_MODEL="${TRAJECTA_LIVE_MODEL:-deepseek-flash}"
export TRAJECTA_LIVE_UPSTREAM=1

# Sandbox-friendly cache locations: the default Go caches may be read-only.
export GOPATH="${GOPATH:-$WORKDIR/gopath}"
export GOMODCACHE="${GOMODCACHE:-$WORKDIR/go-mod}"
export GOCACHE="${GOCACHE:-$WORKDIR/go-build}"
export GOPROXY="${GOPROXY:-https://goproxy.cn,https://proxy.golang.org,direct}"
export GOSUMDB="${GOSUMDB:-off}"

if [[ -z "${TRAJECTA_LIVE_API_KEY:-}" ]]; then
  echo "TRAJECTA_LIVE_API_KEY is required" >&2
  exit 2
fi

mkdir -p "$TRAJECTA_LIVE_TRACE_DIR"

echo "==> starting stack ($COMPOSE_FILE)"
docker compose -f "$COMPOSE_FILE" up -d --build

echo "==> waiting for the proxy port"
for _ in $(seq 1 120); do
  if curl -fsS -m 2 -o /dev/null "$TRAJECTA_LIVE_SERVER_URL/v1/models" 2>/dev/null; then
    break
  fi
  sleep 2
done

echo "==> waiting for the monitor port"
for _ in $(seq 1 60); do
  if curl -fsS -m 2 -o /dev/null "$TRAJECTA_LIVE_MONITOR_URL/api/auth/status" 2>/dev/null; then
    break
  fi
  sleep 2
done

echo "==> seeding the monitor user"
docker compose -f "$COMPOSE_FILE" exec -T trajecta-live \
  /app/bin/server -c /app/config/config.yaml auth init-user \
  --username "$TRAJECTA_LIVE_MONITOR_USER" --password "$TRAJECTA_LIVE_MONITOR_PASSWORD" || true

echo "==> issuing a proxy API token"
TRAJECTA_LIVE_PROXY_TOKEN="$(
  docker compose -f "$COMPOSE_FILE" exec -T trajecta-live \
    /app/bin/server -c /app/config/config.yaml auth create-token \
    --username "$TRAJECTA_LIVE_MONITOR_USER" --name live-suite 2>/dev/null | tail -n 1 | tr -d '\r'
)"
export TRAJECTA_LIVE_PROXY_TOKEN
if [[ -z "$TRAJECTA_LIVE_PROXY_TOKEN" ]]; then
  echo "warning: could not issue a proxy API token; proxy tests will skip" >&2
fi

echo "==> running the live suite"
cd "$REPO_ROOT"
go test ./tests/live -count=1 -v "$@"
