#!/usr/bin/env bash
#
# Restart one deployment on its image and verify that it came back healthy.
#
# The sequence is the one a storage change needs: recreate the container, prove
# the container really runs the image you think it runs (a same-tag rebuild is
# invisible to `docker compose up -d` unless the container is force-recreated),
# wait until the monitor answers, apply the embedded migrations explicitly, read
# the version table back, and then hand over to acceptance.sh for the data
# invariants.
#
# Usage:
#   scripts/postgres/restart-and-verify.sh [--build] [--expect-image TAG]
#                                          [--timeout SECONDS] [--no-acceptance]
#
# --build                 rebuild the image first (scripts/postgres/rebuild-image.sh)
# --expect-image TAG      fail unless the recreated container runs TAG; implied by
#                         --build together with TRAJECTA_OPS_IMAGE (default trajecta:local)
# --timeout SECONDS       how long to wait for the monitor (default 150)
# --no-acceptance         stop after the migration/readiness checks
#
# Environment: see scripts/postgres/common.sh.
set -Eeuo pipefail
. "$(cd "$(dirname "$0")" && pwd)/common.sh"

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
DO_BUILD=0
EXPECT_IMAGE=
TIMEOUT=${TRAJECTA_OPS_READY_TIMEOUT:-150}
RUN_ACCEPTANCE=1

while [ $# -gt 0 ]; do
  case "$1" in
    --build) DO_BUILD=1; EXPECT_IMAGE=${TRAJECTA_OPS_IMAGE:-trajecta:local}; shift ;;
    --expect-image) EXPECT_IMAGE=${2:?--expect-image needs a tag}; shift 2 ;;
    --timeout) TIMEOUT=${2:?--timeout needs seconds}; shift 2 ;;
    --no-acceptance) RUN_ACCEPTANCE=0; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1 (see --help)" ;;
  esac
done

if [ "$DO_BUILD" = "1" ]; then
  section "1) rebuild the image"
  "$SCRIPT_DIR/rebuild-image.sh"
else
  section "1) reuse the current image"
  note "skipped (pass --build to rebuild from the working tree)"
fi

section "2) recreate the container on that image"
CID_BEFORE=$(dc ps -q "$TRAJECTA_OPS_SERVICE" 2>/dev/null || true)
if [ -n "$CID_BEFORE" ]; then
  note "running before: $(docker inspect "$CID_BEFORE" --format '{{.Image}}')"
fi
dc up -d --force-recreate "$TRAJECTA_OPS_SERVICE" 2>&1 | tail -3 | sed 's/^/  /'
CID=$(dc ps -q "$TRAJECTA_OPS_SERVICE")
[ -n "$CID" ] || die "service '$TRAJECTA_OPS_SERVICE' did not start"
CURRENT_IMAGE=$(docker inspect "$CID" --format '{{.Image}}')
note "running after:  $CURRENT_IMAGE"
if [ -n "$EXPECT_IMAGE" ]; then
  EXPECT_ID=$(docker image inspect "$EXPECT_IMAGE" --format '{{.Id}}' 2>/dev/null || true)
  if [ -z "$EXPECT_ID" ]; then
    fail "expected image $EXPECT_IMAGE is not present locally"
  elif [ "$EXPECT_ID" = "$CURRENT_IMAGE" ]; then
    ok "container runs $EXPECT_IMAGE ($EXPECT_ID)"
  else
    fail "container runs $CURRENT_IMAGE, not $EXPECT_IMAGE ($EXPECT_ID)"
  fi
fi

section "3) wait for the monitor (up to ${TIMEOUT}s)"
SECONDS_WAITED=0
CODE=000
while [ "$SECONDS_WAITED" -lt "$TIMEOUT" ]; do
  CODE=$(curl -s -o /dev/null -w '%{http_code}' -m 5 "$TRAJECTA_OPS_BASE_URL/" || true)
  [ "$CODE" = "200" ] && break
  sleep 5
  SECONDS_WAITED=$((SECONDS_WAITED + 5))
done
if [ "$CODE" = "200" ]; then ok "monitor / answered 200 after ~${SECONDS_WAITED}s"; else fail "monitor / answered $CODE after ${TIMEOUT}s"; fi
dc ps "$TRAJECTA_OPS_SERVICE" 2>&1 | tail -n +2 | sed 's/^/  /' || true

section "4) apply the embedded migrations (idempotent) and read the version table"
server db migrate up -c "$TRAJECTA_OPS_CONFIG" 2>&1 | grep -vE '^( Container|time=)' | tail -5 | sed 's/^/  /' || true
note "schema_migrations holds one current-version row, not a history:"
psql_columns "SELECT version, dirty FROM schema_migrations ORDER BY version" | sed 's/^/  /'

if [ "$RUN_ACCEPTANCE" = "1" ]; then
  section "5) acceptance"
  note "delegating to $SCRIPT_DIR/acceptance.sh"
  TRAJECTA_OPS_DEPLOY_DIR="$TRAJECTA_OPS_DEPLOY_DIR" \
  TRAJECTA_OPS_COMPOSE_FILE="$TRAJECTA_OPS_COMPOSE_FILE" \
  TRAJECTA_OPS_SERVICE="$TRAJECTA_OPS_SERVICE" \
  TRAJECTA_OPS_POSTGRES_SERVICE="$TRAJECTA_OPS_POSTGRES_SERVICE" \
  TRAJECTA_OPS_PG_USER="$TRAJECTA_OPS_PG_USER" \
  TRAJECTA_OPS_PG_DB="$TRAJECTA_OPS_PG_DB" \
  TRAJECTA_OPS_DATA_ROOT="$TRAJECTA_OPS_DATA_ROOT" \
  TRAJECTA_OPS_DATA_ROOT_HOST="$TRAJECTA_OPS_DATA_ROOT_HOST" \
  TRAJECTA_OPS_LEGACY_ROOT="$TRAJECTA_OPS_LEGACY_ROOT" \
  TRAJECTA_OPS_BASE_URL="$TRAJECTA_OPS_BASE_URL" \
    "$SCRIPT_DIR/acceptance.sh" || FAILURES=$((FAILURES + 1))
else
  note "acceptance skipped (--no-acceptance)"
fi

summary
