#!/usr/bin/env bash
#
# Verification sequence for the cassette vault after a magic rewrite.
#
# `trajecta upgrade cassettes rewrite` walks every .http file and puts the
# current prelude magic on it. This script proves the result afterwards, in the
# order the vault should be trusted:
#
#   1. census      -- what the reader sees per file, cheap and read-only
#   2. check       -- structural validation, with the pre-rename magic treated
#                     as an error (--fail-on-legacy), so its exit status is the
#                     one hard assertion here
#   3. start       -- bring the service back up and wait for the monitor
#   4. sqlite      -- no live SQLite database may be left in the cassette root
#   5. API probes  -- login, one list page, the overview aggregate
#
# Readers accept all three casette generations (trajecta/v3, the pre-rename
# llm-tracelab/v3 and LLM_PROXY_V2); only writers are restricted to the current
# magic, so a count of "other" formats is information, not a failure. V2 files
# need no rewrite at all -- they carry a fixed 2KB JSON header instead of a
# magic line.
#
# Usage:
#   scripts/postgres/verify-after-rewrite.sh [--no-start] [--wait SECONDS]
#
# --no-start     do not start the service (use when it is already running)
# --wait SECONDS how long to wait for the monitor (default 150)
#
# Environment: see scripts/postgres/common.sh.
set -Eeuo pipefail
. "$(cd "$(dirname "$0")" && pwd)/common.sh"

NO_START=0
WAIT=${TRAJECTA_OPS_READY_TIMEOUT:-150}

while [ $# -gt 0 ]; do
  case "$1" in
    --no-start) NO_START=1; shift ;;
    --wait) WAIT=${2:?--wait needs seconds}; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1 (see --help)" ;;
  esac
done

section "1) cassette census"
cli upgrade cassettes census --root "$TRAJECTA_OPS_DATA_ROOT" -c "$TRAJECTA_OPS_CONFIG" 2>&1 | tail -12 | sed 's/^/  /' || true

section "2) structural check (the pre-rename magic counts as an error)"
set +e
OUTPUT=$(cli upgrade cassettes check --fail-on-legacy --root "$TRAJECTA_OPS_DATA_ROOT" -c "$TRAJECTA_OPS_CONFIG" 2>&1)
STATUS=$?
set -e
printf '%s\n' "$OUTPUT" | tail -20 | sed 's/^/  /'
check_zero "cassette check exit code" "$STATUS"

section "3) service"
if [ "$NO_START" = "1" ]; then
  note "start skipped (--no-start)"
else
  dc up -d "$TRAJECTA_OPS_SERVICE" 2>&1 | tail -2 | sed 's/^/  /' || true
  CODE=000
  WAITED=0
  while [ "$WAITED" -lt "$WAIT" ]; do
    CODE=$(curl -s -o /dev/null -w '%{http_code}' -m 5 "$TRAJECTA_OPS_BASE_URL/" || true)
    [ "$CODE" = "200" ] && break
    sleep 5
    WAITED=$((WAITED + 5))
  done
  if [ "$CODE" = "200" ]; then ok "monitor / answered 200 after ~${WAITED}s"; else fail "monitor / answered $CODE after ${WAIT}s"; fi
  dc ps "$TRAJECTA_OPS_SERVICE" 2>&1 | tail -n +2 | sed 's/^/  /' || true
fi

section "4) no live SQLite database in the cassette root"
if compgen -G "$TRAJECTA_OPS_DATA_ROOT_HOST/*.sqlite3" >/dev/null; then
  fail "live sqlite file(s) present: $(ls "$TRAJECTA_OPS_DATA_ROOT_HOST"/*.sqlite3 | tr '\n' ' ')"
else
  ok "no *.sqlite3 under $TRAJECTA_OPS_DATA_ROOT_HOST"
fi

section "5) API probes"
if [ -z "$AUTH_USER" ]; then
  warn "no AUTH_USER in the environment or $TRAJECTA_OPS_DEPLOY_DIR/.env; probes skipped"
elif TOKEN=$(api_login) && [ -n "$TOKEN" ]; then
  ok "login ok (user $AUTH_USER)"
  note "/api/traces?limit=2: $(api_get "$TOKEN" '/api/traces?limit=2' | head -c 200)"
  check_eq "/api/overview HTTP" "$(api_code "$TOKEN" /api/overview)" "200"
else
  fail "login failed for user '$AUTH_USER' at $TRAJECTA_OPS_BASE_URL"
fi

summary
