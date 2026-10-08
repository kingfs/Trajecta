#!/usr/bin/env bash
#
# Post-migration acceptance checks for one Trajecta deployment.
#
# Run it after a legacy migration, after a derived-id repair, or after any
# operation that rewrites the cassette index. It asserts only invariants that
# hold for every deployment, never instance-specific row counts:
#
#   * every derived row (observations, parse jobs, findings, analysis runs,
#     system events) references a trace_id that `logs` still has
#   * every logs.path lives under the cassette root or under the legacy prefix
#   * no live SQLite database is left in the cassette root, and every archived
#     one is reported with its size
#   * schema_migrations is applied and not dirty, and no index is invalid or
#     not ready
#   * the parse_jobs trace_id probe uses its index, the dedup statement from
#     migration 20261006000000 still plans as an anti join (written as
#     `NOT IN (subquery)` PostgreSQL keeps a SubPlan and rescans the grouped ids
#     once per row as soon as they stop fitting the hash budget), and the
#     semantic_nodes anti-join still plans as a Merge Anti Join (the column
#     statistics override from migration 20260929100000 is what keeps it there)
#   * a reconcile dry run reports no superseded row and no pending prune
#   * the monitor API logs in, lists traces and serves one detail document
#
# Values it cannot judge -- total rows, rows under the legacy prefix, sampled
# orphans, cassettes missing on disk -- are printed as observations instead of
# expectations: after a real migration the honest expectation for those depends
# on the source data, not on this script. Row counts recorded as the baseline
# for one 248,164-row deployment live in docs/POSTGRES_OPERATIONS.md.
#
# Exit status is 1 when any check failed, 0 otherwise.
#
# Usage:
#   scripts/postgres/acceptance.sh [--sample N] [--skip-reconcile]
#
# --sample N        how many logs.path values to check on disk (default 12)
# --skip-reconcile  do not run the (read-only) reconcile dry run
#
# Environment: see scripts/postgres/common.sh.
set -Eeuo pipefail
. "$(cd "$(dirname "$0")" && pwd)/common.sh"

SAMPLE=${TRAJECTA_OPS_PATH_SAMPLE:-12}
SKIP_RECONCILE=0
while [ $# -gt 0 ]; do
  case "$1" in
    --sample) SAMPLE=${2:?--sample needs a number}; shift 2 ;;
    --skip-reconcile) SKIP_RECONCILE=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1 (see --help)" ;;
  esac
done

DERIVED_TABLES=${TRAJECTA_OPS_DERIVED_TABLES:-"trace_observations parse_jobs trace_findings analysis_runs system_events"}
LOG_WINDOW=${TRAJECTA_OPS_LOG_WINDOW:-30m}

section "0) deployment"
note "compose        $TRAJECTA_OPS_COMPOSE_FILE"
note "app service    $TRAJECTA_OPS_SERVICE"
note "postgres       $TRAJECTA_OPS_POSTGRES_SERVICE ($TRAJECTA_OPS_PG_USER@$TRAJECTA_OPS_PG_DB)"
note "cassette root  $TRAJECTA_OPS_DATA_ROOT -> $TRAJECTA_OPS_DATA_ROOT_HOST"
note "monitor        $TRAJECTA_OPS_BASE_URL"
dc ps "$TRAJECTA_OPS_SERVICE" 2>&1 | tail -n +2 | sed 's/^/  /' || true
service_running || fail "service '$TRAJECTA_OPS_SERVICE' is not running"

section "1) derived tables reference an existing trace"
for table in $DERIVED_TABLES; do
  if ! table_exists "$table"; then
    warn "$table: table absent in this schema, skipped"
    continue
  fi
  check_zero "$table rows without a logs.trace_id" \
    "$(psql_scalar "SELECT count(*) FROM $table d WHERE NOT EXISTS (SELECT 1 FROM logs l WHERE l.trace_id = d.trace_id)")"
done

section "2) cassette path prefixes"
TOTAL=$(psql_scalar "SELECT count(*) FROM logs")
UNDER_ROOT=$(psql_scalar "SELECT count(*) FROM logs WHERE path LIKE '$TRAJECTA_OPS_DATA_ROOT/%'")
UNDER_LEGACY=$(psql_scalar "SELECT count(*) FROM logs WHERE path LIKE '$TRAJECTA_OPS_LEGACY_ROOT/%'")
OUTSIDE=$(psql_scalar "SELECT count(*) FROM logs WHERE path NOT LIKE '$TRAJECTA_OPS_DATA_ROOT/%' AND path NOT LIKE '$TRAJECTA_OPS_LEGACY_ROOT/%'")
note "logs total                = $TOTAL"
note "under $TRAJECTA_OPS_DATA_ROOT = $UNDER_ROOT"
note "under $TRAJECTA_OPS_LEGACY_ROOT = $UNDER_LEGACY"
if [ "$UNDER_LEGACY" != "0" ]; then
  warn "rows still point at the legacy prefix: their files are not under the cassette root and their detail views will 404 from inside the container (docs/LEGACY_MIGRATION.md)"
fi
check_zero "logs.path outside both known roots" "$OUTSIDE"

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
[ "$MISSING" -eq 0 ] || warn "indexed cassettes missing on disk are kept as index rows (replaceable, not corrupt; see docs/POSTGRES_OPERATIONS.md)"

section "3) no live SQLite database in the cassette root"
if compgen -G "$TRAJECTA_OPS_DATA_ROOT_HOST/*.sqlite3" >/dev/null; then
  fail "live sqlite file(s) present: $(ls "$TRAJECTA_OPS_DATA_ROOT_HOST"/*.sqlite3 | tr '\n' ' ')"
else
  ok "no *.sqlite3 under $TRAJECTA_OPS_DATA_ROOT_HOST"
fi
if compgen -G "$TRAJECTA_OPS_DATA_ROOT_HOST/*.sqlite3.migrated" >/dev/null; then
  ls -la "$TRAJECTA_OPS_DATA_ROOT_HOST"/*.sqlite3.migrated | awk '{printf "  archive %.1f MB %s\n", $5/1048576, $9}'
else
  note "no *.sqlite3.migrated archive in $TRAJECTA_OPS_DATA_ROOT_HOST"
fi

section "4) migrations and index health"
VERSION=$(psql_scalar "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1")
DIRTY=$(psql_scalar "SELECT dirty FROM schema_migrations ORDER BY version DESC LIMIT 1")
note "schema_migrations: version=$VERSION dirty=$DIRTY"
if [ "$DIRTY" = "f" ]; then ok "migration state is not dirty"; else fail "migration state is dirty ($DIRTY)"; fi
check_zero "invalid indexes" "$(psql_scalar "SELECT count(*) FROM pg_index WHERE NOT indisvalid")"
check_zero "not-ready indexes" "$(psql_scalar "SELECT count(*) FROM pg_index WHERE NOT indisready")"
note "logs indexes = $(psql_scalar "SELECT count(*) FROM pg_indexes WHERE tablename = 'logs'")"

section "5) parse_jobs trace_id probe and dedup anti-join"
if table_exists parse_jobs; then
  PLAN=$(psql_lines "EXPLAIN (COSTS OFF) SELECT 1 FROM parse_jobs WHERE trace_id = 'probe'" | tr '\n' ' ')
  case "$PLAN" in
    *parsejob_trace_id_status*) ok "probe uses parsejob_trace_id_status" ;;
    *"Seq Scan"*) warn "probe still seq scans (expected for a tiny table; the index matters with more rows): $PLAN" ;;
    *) note "probe plan: $PLAN" ;;
  esac
  # The statement from 20261006000000_unique_parse_jobs_trace_id.up.sql. Written
  # as `id NOT IN (SELECT MAX(id) ... GROUP BY trace_id)` PostgreSQL cannot turn
  # it into an anti join, so it keeps a SubPlan: a hashed one while the grouped
  # ids fit the hash budget, a Materialize that is rescanned for every row once
  # they do not. That is what held a deployment's startup migration for over
  # half an hour at a full core with nothing logged. EXPLAIN without ANALYZE does
  # not execute the DELETE.
  DEDUP_SQL='DELETE FROM "parse_jobs" AS p WHERE NOT EXISTS (SELECT 1 FROM (SELECT MAX("id") AS "keep_id" FROM "parse_jobs" GROUP BY "trace_id") AS "kept" WHERE "kept"."keep_id" = p."id")'
  DEDUP_PLAN=$(psql_lines "EXPLAIN (COSTS OFF) $DEDUP_SQL" | tr '\n' ' ')
  case "$DEDUP_PLAN" in
    *SubPlan*) fail "the parse_jobs dedup statement keeps a per-row SubPlan (a NOT IN subquery); it is quadratic once the grouped ids exceed the hash budget: $DEDUP_PLAN" ;;
    *"Anti Join"*) ok "dedup statement plans as an anti join" ;;
    *) note "dedup statement plan: $DEDUP_PLAN" ;;
  esac
else
  warn "parse_jobs absent in this schema, skipped"
fi

section "6) semantic_nodes statistics and anti-join plan"
if table_exists semantic_nodes; then
  STATS=$(psql_scalar "SELECT coalesce((SELECT attoptions[1] FROM pg_attribute WHERE attrelid = 'semantic_nodes'::regclass AND attname = 'trace_id'), '(none)')")
  case "$STATS" in
    *n_distinct=*) ok "column statistics override present ($STATS)" ;;
    *) warn "no n_distinct override on semantic_nodes.trace_id ($STATS): the anti-join can fall back to a Nested Loop (docs/POSTGRES_OPERATIONS.md)" ;;
  esac
  PLAN=$(psql_lines "EXPLAIN (COSTS OFF) SELECT t.trace_id FROM (SELECT DISTINCT d.trace_id FROM semantic_nodes d WHERE d.trace_id IS NOT NULL AND d.trace_id <> '') t LEFT JOIN logs l ON l.trace_id = t.trace_id WHERE l.trace_id IS NULL ORDER BY t.trace_id" | tr '\n' ' ')
  case "$PLAN" in
    *"Merge Anti Join"*) ok "orphan enumeration plans as Merge Anti Join" ;;
    *"Nested Loop"*) fail "orphan enumeration fell back to Nested Loop: $PLAN" ;;
    *) note "orphan enumeration plan: $PLAN" ;;
  esac
  note "sampled orphans (TABLESAMPLE SYSTEM (0.01)) = $(psql_scalar "SELECT count(*) FROM (SELECT d.trace_id FROM semantic_nodes d TABLESAMPLE SYSTEM (0.01) WHERE NOT EXISTS (SELECT 1 FROM logs l WHERE l.trace_id = d.trace_id)) s")"
  note "(rows whose trace_id has no logs entry are kept by design when no legacy trace maps to them)"
else
  warn "semantic_nodes absent in this schema, skipped"
fi

section "7) reconcile dry run"
SQLITE_ARGS=()
for archive in "$TRAJECTA_OPS_DATA_ROOT_HOST"/*.sqlite3.migrated; do
  [ -e "$archive" ] || continue
  SQLITE_ARGS+=(--sqlite "$(host_to_container "$archive")")
done
if [ "$SKIP_RECONCILE" = "1" ]; then
  note "skipped (--skip-reconcile)"
elif [ "${#SQLITE_ARGS[@]}" -eq 0 ]; then
  note "no *.sqlite3.migrated archive to reconcile against, skipped"
else
  note "archives: ${SQLITE_ARGS[*]}"
  require_cmd python3
  OUTPUT=$(cli upgrade db --reconcile-derived-trace-ids \
    --data-root "$TRAJECTA_OPS_DATA_ROOT" "${SQLITE_ARGS[@]}" --sqlite-open immutable \
    --max-samples 3 --table analysis_runs --table parse_jobs \
    -c "$TRAJECTA_OPS_CONFIG" --format json 2>&1) || true
  REPORT=$(printf '%s\n' "$OUTPUT" | sed -n '/^{/,$p')
  if [ -z "$REPORT" ]; then
    fail "reconcile produced no JSON report; last output:"
    printf '%s\n' "$OUTPUT" | tail -5 | sed 's/^/    /'
  else
    while IFS='=' read -r key value; do
      [ -n "$key" ] || continue
      case "$key" in
        superseded) check_zero "superseded index rows" "$value" ;;
        prune_pending_rows) check_zero "index rows the prune targets" "$value" ;;
        pruned_index_rows) check_zero "index rows pruned in a dry run" "$value" ;;
        ambiguous) check_zero "ambiguous index rows" "$value" ;;
        missing_files) note "indexed cassettes missing at the recorded path = $value (kept as rows by design)" ;;
        orphan_files) note "indexed cassettes existing nowhere under the root = $value (kept as rows by design)" ;;
        ok) [ "$value" = "1" ] && ok "reconcile reports ok" || warn "reconcile reports ok=$value" ;;
        table_*_unresolved) note "$key = $value" ;;
        *) note "$key = $value" ;;
      esac
    done <<EOF
$(printf '%s' "$REPORT" | python3 -c '
import json, sys
envelope = json.load(sys.stdin)
report = envelope.get("result") or {}
print("ok=%d" % (1 if envelope.get("ok") else 0))
stats = report.get("superseded_index") or {}
for key in ("superseded", "ambiguous", "missing_files", "orphan_files", "pruned_index_rows", "prune_pending_rows"):
    print("%s=%s" % (key, stats.get(key, 0)))
for table in report.get("tables") or []:
    print("table_%s_unresolved=%s" % (table.get("table"), table.get("unresolved_rows", 0)))
')
EOF
  fi
fi

section "8) monitor API"
if [ -z "$AUTH_USER" ]; then
  warn "no AUTH_USER in the environment or $TRAJECTA_OPS_DEPLOY_DIR/.env; API checks skipped"
elif TOKEN=$(api_login) && [ -n "$TOKEN" ]; then
  ok "login ok (user $AUTH_USER)"
  LIST=$(api_get "$TOKEN" /api/traces)
  note "/api/traces total = $(printf '%s' "$LIST" | sed -n 's/.*"total":\([0-9]*\).*/\1/p' | head -1)"
  ID=$(printf '%s' "$LIST" | sed -n 's/.*"items":\[{"id":"\([^"]*\)".*/\1/p' | head -1)
  if [ -n "$ID" ]; then
    check_eq "trace detail HTTP" "$(api_code "$TOKEN" "/api/traces/$ID")" "200"
  else
    note "no trace id parsed from the list response; detail check skipped"
  fi
  check_eq "/api/overview HTTP" "$(api_code "$TOKEN" /api/overview)" "200"
  if [ -n "${TRAJECTA_OPS_LEGACY_TRACE_LIST:-}" ] && [ -f "$TRAJECTA_OPS_LEGACY_TRACE_LIST" ]; then
    note "sampling traces from $TRAJECTA_OPS_LEGACY_TRACE_LIST (ids that used to 404 before their cassettes were imported):"
    head -3 "$TRAJECTA_OPS_LEGACY_TRACE_LIST" | while IFS='|' read -r legacy_id _old_path; do
      [ -n "$legacy_id" ] || continue
      printf '  %s... detail HTTP %s raw HTTP %s\n' "$(printf '%s' "$legacy_id" | cut -c1-8)" \
        "$(api_code "$TOKEN" "/api/traces/$legacy_id")" "$(api_code "$TOKEN" "/api/traces/$legacy_id/raw")"
    done
  fi
else
  fail "login failed for user '$AUTH_USER' at $TRAJECTA_OPS_BASE_URL"
fi

section "9) service log"
ERRORS=$(dc logs --since "$LOG_WINDOW" "$TRAJECTA_OPS_SERVICE" 2>&1 | grep -cE 'level=(ERROR|WARN)' || true)
if [ "${ERRORS:-0}" = "0" ]; then
  ok "no ERROR/WARN in the last $LOG_WINDOW"
else
  warn "$ERRORS ERROR/WARN line(s) in the last $LOG_WINDOW: dc logs --since $LOG_WINDOW $TRAJECTA_OPS_SERVICE"
fi

summary
