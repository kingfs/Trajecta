#!/usr/bin/env bash
#
# Repair the derived trace ids after cassettes moved, through the project's own
# command (trajecta upgrade db --reconcile-derived-trace-ids).
#
# What it fixes: a migration or a layout apply can leave derived rows
# (trace_observations, parse_jobs, trace_findings, analysis_runs, system_events)
# pointing at a trace_id that the legacy SQLite index still knows under its old
# value, and can leave `logs` rows whose cassette moved away while the same
# recording stayed indexed at a path that exists ("superseded" rows). The
# reconcile maps the legacy ids onto the current ones, deletes the duplicate
# derived rows it superseded, and -- only with --prune-superseded-index-rows --
# deletes a superseded index row once nothing derives from it any more.
#
# Long run, by design: the orphan enumeration scans the whole derived index,
# and on the 74M-row instance this repair took 7h36m for semantic_nodes alone.
# Start it in a durable session (tmux/nohup/systemd-run); the log written here
# is what you read afterwards.
#
# Session parameters cannot be smuggled in through the DSN. The Postgres driver
# is github.com/lib/pq, which does not forward a DSN `options=` value, so a
# `...?options=-c work_mem%3D...` URL is silently ignored by the application
# while the same URL works in psql. Set such parameters server-side instead
# (per database or role) or raise the postgres container /dev/shm, which
# docker-compose.yml already does with shm_size -- see
# docs/POSTGRES_OPERATIONS.md, "大表聚合的内存与并行度".
#
# Default is a dry run; pass --apply to write.
#
# Usage:
#   scripts/postgres/reconcile-apply.sh [--apply] [--prune-superseded-index-rows]
#                                       [--sqlite PATH]... [--data-root PATH]
#                                       [--table NAME]... [--max-samples N]
#                                       [--log FILE] [--json]
#
# The legacy archives default to every *.sqlite3.migrated under
# TRAJECTA_OPS_DATA_ROOT_HOST (mapped to its container path); --sqlite adds one
# explicitly (container path). Without --table the whole derived schema is
# reconciled; naming tables keeps a check fast because the expensive ones are
# skipped.
#
# Environment: see scripts/postgres/common.sh, plus
#   TRAJECTA_OPS_LOG_DIR  where the run log is written (default $DEPLOY_DIR/backups)
set -Eeuo pipefail
. "$(cd "$(dirname "$0")" && pwd)/common.sh"

APPLY=0
PRUNE=0
MAX_SAMPLES=2000
LOG_DIR=${TRAJECTA_OPS_LOG_DIR:-$TRAJECTA_OPS_DEPLOY_DIR/backups}
LOG_FILE=
FORMAT=text
SQLITE_ARGS=()
TABLE_ARGS=()
DATA_ROOT=$TRAJECTA_OPS_DATA_ROOT

while [ $# -gt 0 ]; do
  case "$1" in
    --apply) APPLY=1; shift ;;
    --prune-superseded-index-rows) PRUNE=1; shift ;;
    --sqlite) SQLITE_ARGS+=(--sqlite "${2:?--sqlite needs a path}"); shift 2 ;;
    --data-root) DATA_ROOT=${2:?--data-root needs a path}; shift 2 ;;
    --table) TABLE_ARGS+=(--table "${2:?--table needs a name}"); shift 2 ;;
    --max-samples) MAX_SAMPLES=${2:?--max-samples needs a number}; shift 2 ;;
    --log) LOG_FILE=${2:?--log needs a file}; shift 2 ;;
    --json) FORMAT=json; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1 (see --help)" ;;
  esac
done

if [ "${#SQLITE_ARGS[@]}" -eq 0 ]; then
  for archive in "$TRAJECTA_OPS_DATA_ROOT_HOST"/*.sqlite3.migrated; do
    [ -e "$archive" ] || continue
    SQLITE_ARGS+=(--sqlite "$(host_to_container "$archive")")
  done
fi
[ "${#SQLITE_ARGS[@]}" -gt 0 ] || die "no legacy SQLite archive found under $TRAJECTA_OPS_DATA_ROOT_HOST; pass --sqlite"

if [ -z "$LOG_FILE" ]; then
  mkdir -p "$LOG_DIR"
  LOG_FILE="$LOG_DIR/reconcile-$(date -u +%Y%m%dT%H%M%SZ)$([ "$APPLY" = "1" ] && printf apply || printf dryrun).log"
fi

ARGS=(upgrade db --reconcile-derived-trace-ids
  --data-root "$DATA_ROOT" "${SQLITE_ARGS[@]}"
  --sqlite-open immutable --max-samples "$MAX_SAMPLES" "${TABLE_ARGS[@]}"
  -c "$TRAJECTA_OPS_CONFIG" --format "$FORMAT")
[ "$PRUNE" = "1" ] && ARGS+=(--prune-superseded-index-rows) || true
[ "$APPLY" = "1" ] && ARGS+=(--apply) || true

section "reconcile derived trace ids"
note "mode        $([ "$APPLY" = "1" ] && printf apply || printf 'dry run (pass --apply to write)')"
note "prune       $([ "$PRUNE" = "1" ] && printf 'superseded index rows' || printf off)"
note "data root   $DATA_ROOT"
note "archives    ${SQLITE_ARGS[*]}"
note "tables      ${TABLE_ARGS[*]:-all derived tables}"
note "log         $LOG_FILE"
note "command     $TRAJECTA_OPS_CLI ${ARGS[*]}"

START=$(date -u +%Y-%m-%dT%H:%M:%SZ)
printf '=== reconcile start %s ===\n' "$START" > "$LOG_FILE"
set +e
cli "${ARGS[@]}" 2>&1 | tee -a "$LOG_FILE" | grep -vE '^( Container|time=)' | tail -25 | sed 's/^/  /'
STATUS=${PIPESTATUS[0]}
set -e
printf '=== reconcile end %s exit=%s ===\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$STATUS" >> "$LOG_FILE"
note "exit=$STATUS, full log: $LOG_FILE"
case "$STATUS" in
  0) ok "reconcile finished" ;;
  *) fail "reconcile exited $STATUS; read $LOG_FILE (orphan rows and table errors are reported as a non-zero exit)" ;;
esac

section "state after the run"
psql_columns "
SELECT 'logs_total', count(*)::text FROM logs
UNION ALL SELECT 'trace_observations_without_trace', count(*)::text FROM trace_observations d WHERE NOT EXISTS (SELECT 1 FROM logs l WHERE l.trace_id = d.trace_id)
UNION ALL SELECT 'parse_jobs_without_trace', count(*)::text FROM parse_jobs d WHERE NOT EXISTS (SELECT 1 FROM logs l WHERE l.trace_id = d.trace_id)
UNION ALL SELECT 'trace_findings_without_trace', count(*)::text FROM trace_findings d WHERE NOT EXISTS (SELECT 1 FROM logs l WHERE l.trace_id = d.trace_id)
UNION ALL SELECT 'semantic_nodes_dead_tuples', coalesce((SELECT n_dead_tup::text FROM pg_stat_user_tables WHERE relname = 'semantic_nodes'), '(none)')
UNION ALL SELECT 'parse_jobs_dead_tuples', coalesce((SELECT n_dead_tup::text FROM pg_stat_user_tables WHERE relname = 'parse_jobs'), '(none)')" | sed 's/^/  /'
note "run scripts/postgres/vacuum-after-repair.sh next: the repair leaves dead tuples and stale statistics behind"

summary
