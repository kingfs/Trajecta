#!/usr/bin/env bash
#
# Read-only evidence report about the storage state of one deployment.
#
# This is the report to paste into a migration record or an incident note: it
# proves which driver the deployment runs, which schema version is applied, that
# no live SQLite database is left, how many cassettes are indexed under which
# root, that the derived tables have no orphans, and that the monitor API still
# serves the data. Every statement is a SELECT except the login and the HTTP
# reads, so it is safe to run against a live deployment.
#
# Unlike acceptance.sh it never fails a check: it reports. Use acceptance.sh
# when you want an exit status to gate on.
#
# One structural check depends on the deployment's own config file, which is
# read from the host at $TRAJECTA_OPS_HOST_CONFIG (default
# $TRAJECTA_OPS_DEPLOY_DIR/config.yaml) and skipped when absent.
#
# Usage:
#   scripts/postgres/evidence.sh [--sample N] [--magic-sample N]
#
# --sample N        how many indexed paths to check on disk (default 12)
# --magic-sample N  how many cassette files to inspect for their prelude magic
#                   (default 200)
#
# Environment: see scripts/postgres/common.sh. Set
# TRAJECTA_OPS_LEGACY_TRACE_LIST to a "trace_id|old_path" list (one per line) to
# sample traces whose cassettes were imported from a legacy mount and used to
# answer 404.
set -Eeuo pipefail
. "$(cd "$(dirname "$0")" && pwd)/common.sh"

SAMPLE=${TRAJECTA_OPS_PATH_SAMPLE:-12}
MAGIC_SAMPLE=${TRAJECTA_OPS_MAGIC_SAMPLE:-200}
HOST_CONFIG=${TRAJECTA_OPS_HOST_CONFIG:-$TRAJECTA_OPS_DEPLOY_DIR/config.yaml}
LOG_WINDOW=${TRAJECTA_OPS_LOG_WINDOW:-30m}

while [ $# -gt 0 ]; do
  case "$1" in
    --sample) SAMPLE=${2:?--sample needs a number}; shift 2 ;;
    --magic-sample) MAGIC_SAMPLE=${2:?--magic-sample needs a number}; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1 (see --help)" ;;
  esac
done

section "0) deployment"
note "compose        $TRAJECTA_OPS_COMPOSE_FILE"
note "app service    $TRAJECTA_OPS_SERVICE"
note "postgres       $TRAJECTA_OPS_POSTGRES_SERVICE ($TRAJECTA_OPS_PG_USER@$TRAJECTA_OPS_PG_DB)"
note "cassette root  $TRAJECTA_OPS_DATA_ROOT -> $TRAJECTA_OPS_DATA_ROOT_HOST"
note "monitor        $TRAJECTA_OPS_BASE_URL"

section "1) storage driver"
if [ -f "$HOST_CONFIG" ]; then
  grep -nE '^[[:space:]]*(driver|dsn|auto_migrate):' "$HOST_CONFIG" | sed 's/^/  /' || true
  note "sqlite mentions in $HOST_CONFIG: $(grep -ci sqlite "$HOST_CONFIG" || true)"
else
  warn "config file not found: $HOST_CONFIG (set TRAJECTA_OPS_HOST_CONFIG)"
fi
note "TRAJECTA_DATABASE_DRIVER in the resolved compose config: $(dc config 2>/dev/null | grep -o 'TRAJECTA_DATABASE_DRIVER: .*' | head -1 | cut -d' ' -f2-)"
for file in "$TRAJECTA_OPS_DEPLOY_DIR/.env" "$TRAJECTA_OPS_COMPOSE_FILE"; do
  [ -f "$file" ] || continue
  note "sqlite mentions in $(basename "$file"): $(grep -ci sqlite "$file" || true)"
done

section "2) applied migration"
note "schema_migrations is golang-migrate's single-row current-version table"
psql_columns "SELECT version, dirty FROM schema_migrations ORDER BY version" | sed 's/^/  /'
note "expect dirty=f; the version is the newest applied migration in ent/postgres-migrations"

section "3) SQLite state"
if compgen -G "$TRAJECTA_OPS_DATA_ROOT_HOST/*.sqlite3" >/dev/null; then
  ls -la "$TRAJECTA_OPS_DATA_ROOT_HOST"/*.sqlite3 | awk '{printf "  live: %.1f MB %s\n", $5/1048576, $9}'
else
  note "live .sqlite3: none"
fi
if compgen -G "$TRAJECTA_OPS_DATA_ROOT_HOST/*.sqlite3.migrated" >/dev/null; then
  ls -la "$TRAJECTA_OPS_DATA_ROOT_HOST"/*.sqlite3.migrated | awk '{printf "  archive: %.1f MB %s\n", $5/1048576, $9}'
else
  note "archives: none"
fi

section "4) index health"
note "$(psql_scalar "SELECT count(*) FILTER (WHERE NOT indisvalid) || ' invalid / ' || count(*) FILTER (WHERE NOT indisready) || ' not ready / ' || count(*) || ' total' FROM pg_index")"
psql_columns "SELECT indexname || ' ' || pg_size_pretty(pg_relation_size(indexname::regclass)) FROM pg_indexes WHERE tablename = 'logs' ORDER BY indexname" | sed 's/^/  /'

section "5) cassette index"
psql_columns "
SELECT 'logs_total', count(*)::text FROM logs
UNION ALL SELECT 'under_data_root', count(*)::text FROM logs WHERE path LIKE '$TRAJECTA_OPS_DATA_ROOT/%'
UNION ALL SELECT 'under_legacy_root', count(*)::text FROM logs WHERE path LIKE '$TRAJECTA_OPS_LEGACY_ROOT/%'
UNION ALL SELECT 'outside_known_roots', count(*)::text FROM logs WHERE path NOT LIKE '$TRAJECTA_OPS_DATA_ROOT/%' AND path NOT LIKE '$TRAJECTA_OPS_LEGACY_ROOT/%'
UNION ALL SELECT 'upstream_exchanges', count(*)::text FROM upstream_exchanges" | sed 's/^/  /'
if table_exists overview_metric_bucket_members; then
  note "overview_metric_bucket_members (legacy, no longer written or read): $(psql_scalar "SELECT count(*) FROM overview_metric_bucket_members") rows"
else
  note "overview_metric_bucket_members absent (removed derived table), skipped"
fi
PRESENT=0
MISSING=0
while IFS= read -r path; do
  [ -n "$path" ] || continue
  if [ -f "$(container_to_host "$path")" ]; then
    PRESENT=$((PRESENT + 1))
  else
    MISSING=$((MISSING + 1))
    note "missing on disk: $path"
  fi
done <<EOF
$(psql_lines "SELECT path FROM logs WHERE path LIKE '$TRAJECTA_OPS_DATA_ROOT/%' ORDER BY random() LIMIT $SAMPLE")
EOF
note "sampled $SAMPLE indexed paths: present=$PRESENT missing=$MISSING"

section "6) derived table orphans"
psql_columns "
SELECT 'trace_observations_without_trace', count(*)::text FROM trace_observations d WHERE NOT EXISTS (SELECT 1 FROM logs l WHERE l.trace_id = d.trace_id)
UNION ALL SELECT 'parse_jobs_without_trace', count(*)::text FROM parse_jobs d WHERE NOT EXISTS (SELECT 1 FROM logs l WHERE l.trace_id = d.trace_id)
UNION ALL SELECT 'trace_findings_without_trace', count(*)::text FROM trace_findings d WHERE NOT EXISTS (SELECT 1 FROM logs l WHERE l.trace_id = d.trace_id)
UNION ALL SELECT 'analysis_runs_without_trace', count(*)::text FROM analysis_runs d WHERE NOT EXISTS (SELECT 1 FROM logs l WHERE l.trace_id = d.trace_id)
UNION ALL SELECT 'system_events_without_trace', count(*)::text FROM system_events d WHERE NOT EXISTS (SELECT 1 FROM logs l WHERE l.trace_id = d.trace_id)" | sed 's/^/  /'
note "semantic_nodes is excluded on purpose: rows whose trace_id has no legacy counterpart stay (see docs/POSTGRES_OPERATIONS.md)"

section "7) monitor API"
TOKEN=$(api_login || true)
if [ -z "$TOKEN" ]; then
  warn "login failed at $TRAJECTA_OPS_BASE_URL (set AUTH_USER/AUTH_PASSWORD or put them in $TRAJECTA_OPS_DEPLOY_DIR/.env)"
else
  note "login ok (user $AUTH_USER)"
  LIST=$(api_get "$TOKEN" /api/traces)
  note "/api/traces total = $(printf '%s' "$LIST" | sed -n 's/.*"total":\([0-9]*\).*/\1/p' | head -1)"
  ID=$(printf '%s' "$LIST" | sed -n 's/.*"items":\[{"id":"\([^"]*\)".*/\1/p' | head -1)
  if [ -n "$ID" ]; then
    note "trace detail ($ID) HTTP $(api_code "$TOKEN" "/api/traces/$ID")"
  fi
  note "/api/overview HTTP $(api_code "$TOKEN" /api/overview)"
  note "monitor / HTTP $(curl -s -o /dev/null -w '%{http_code}' -m 30 "$TRAJECTA_OPS_BASE_URL/")"
  if [ -n "${TRAJECTA_OPS_LEGACY_TRACE_LIST:-}" ] && [ -f "$TRAJECTA_OPS_LEGACY_TRACE_LIST" ]; then
    note "traces whose cassettes came from a legacy mount (sampled from $TRAJECTA_OPS_LEGACY_TRACE_LIST):"
    head -3 "$TRAJECTA_OPS_LEGACY_TRACE_LIST" | while IFS='|' read -r legacy_id _old_path; do
      [ -n "$legacy_id" ] || continue
      printf '  %s... detail HTTP %s raw HTTP %s %sB\n' "$(printf '%s' "$legacy_id" | cut -c1-8)" \
        "$(api_code "$TOKEN" "/api/traces/$legacy_id")" \
        "$(curl -s -o /dev/null -w '%{http_code}' -m 90 -H "Authorization: Bearer $TOKEN" "$TRAJECTA_OPS_BASE_URL/api/traces/$legacy_id/raw")" \
        "$(curl -s -o /dev/null -w '%{size_download}' -m 90 -H "Authorization: Bearer $TOKEN" "$TRAJECTA_OPS_BASE_URL/api/traces/$legacy_id/raw")"
    done
  else
    note "legacy trace sampling skipped (set TRAJECTA_OPS_LEGACY_TRACE_LIST to a trace_id|old_path list)"
  fi
fi

section "8) service log"
note "ERROR/WARN lines in the last $LOG_WINDOW = $(dc logs --since "$LOG_WINDOW" "$TRAJECTA_OPS_SERVICE" 2>&1 | grep -cE 'level=(ERROR|WARN)' || true)"
note "inspect with: dc logs --since $LOG_WINDOW $TRAJECTA_OPS_SERVICE   (docker compose --project-directory $TRAJECTA_OPS_DEPLOY_DIR -f $TRAJECTA_OPS_COMPOSE_FILE logs ...)"

section "9) cassette prelude magic on disk"
V3=0
OLD=0
OTHER=0
SAMPLED=0
while IFS= read -r file; do
  [ -n "$file" ] || continue
  SAMPLED=$((SAMPLED + 1))
  case "$(head -c 64 "$file" 2>/dev/null | head -1)" in
    '# trajecta/v3'*) V3=$((V3 + 1)) ;;
    *'llm-tracelab/v3'*) OLD=$((OLD + 1)) ;;
    *) OTHER=$((OTHER + 1)) ;;
  esac
done <<EOF
$(find "$TRAJECTA_OPS_DATA_ROOT_HOST" -type f -name '*.http' 2>/dev/null | head -"$MAGIC_SAMPLE")
EOF
note "sampled $SAMPLED files: current magic=$V3 pre-rename magic=$OLD other=$OTHER"
note "readers accept all three; writers emit '# trajecta/v3' only, and LLM_PROXY_V2 files (counted as other) need no rewrite"
