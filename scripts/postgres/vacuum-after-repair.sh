#!/usr/bin/env bash
#
# Hygiene after a derived-id repair: refresh statistics and reclaim dead tuples.
#
# A reconcile apply deletes and rewrites derived rows, which leaves two things
# behind that both hurt the next query: stale planner statistics (the plans the
# repair depended on are chosen from row counts it just changed) and dead tuples
# that only autovacuum reclaims eventually.
#
# The split between VACUUM (ANALYZE) and ANALYZE is deliberate. The small
# derived tables are cheap to vacuum completely. The big one -- semantic_nodes,
# a 55 GB heap plus roughly 15 GB of indexes on the reference instance -- costs
# hours to vacuum on a cold disk, while autovacuum keeps reclaiming it
# incrementally, so it gets ANALYZE only and its dead-tuple count is reported
# instead. Raise TRAJECTA_OPS_BIG_TABLES_VACUUM=1 when you do want the full
# vacuum, ideally outside working hours.
#
# Usage: scripts/postgres/vacuum-after-repair.sh
# Environment: see scripts/postgres/common.sh, plus
#   TRAJECTA_OPS_SMALL_TABLES   tables to VACUUM (ANALYZE) (default: the derived tables)
#   TRAJECTA_OPS_BIG_TABLES     tables to ANALYZE only (default: semantic_nodes)
#   TRAJECTA_OPS_BIG_TABLES_VACUUM  1 to VACUUM (ANALYZE) the big tables as well
set -Eeuo pipefail
. "$(cd "$(dirname "$0")" && pwd)/common.sh"

SMALL=${TRAJECTA_OPS_SMALL_TABLES:-"analysis_runs dataset_examples parse_jobs scores system_events trace_findings trace_observations"}
BIG=${TRAJECTA_OPS_BIG_TABLES:-semantic_nodes}
BIG_VACUUM=${TRAJECTA_OPS_BIG_TABLES_VACUUM:-0}

stats_query() {
  local names=()
  local table
  for table in $SMALL $BIG; do
    names+=("'$table'")
  done
  psql_columns "SELECT relname || ' live=' || n_live_tup || ' dead=' || n_dead_tup ||
      ' last_analyze=' || coalesce(last_analyze::timestamp(0)::text, 'never') ||
      ' last_vacuum=' || coalesce(last_vacuum::timestamp(0)::text, 'never')
    FROM pg_stat_user_tables WHERE relname IN ($(printf '%s' "${names[*]}" | tr ' ' ',')) ORDER BY n_dead_tup DESC"
}

section "1) before"
stats_query | sed 's/^/  /'

section "2) VACUUM (ANALYZE) on the small derived tables"
for table in $SMALL; do
  if table_exists "$table"; then
    printf '  %-24s ' "$table"
    psql_lines "VACUUM (ANALYZE) $table" | tail -1 | sed 's/^$/ok/'
  else
    note "$table: absent, skipped"
  fi
done

section "3) ANALYZE on the big derived tables"
for table in $BIG; do
  if ! table_exists "$table"; then
    note "$table: absent, skipped"
    continue
  fi
  if [ "$BIG_VACUUM" = "1" ]; then
    printf '  %-24s VACUUM (ANALYZE)\n' "$table"
    psql_lines "VACUUM (ANALYZE) $table" | tail -1 | sed 's/^$/ok/'
  else
    printf '  %-24s ANALYZE\n' "$table"
    psql_lines "ANALYZE $table" >/dev/null
    note "dead tuples kept for autovacuum: $(psql_scalar "SELECT n_dead_tup FROM pg_stat_user_tables WHERE relname = '$table'")"
  fi
done

section "4) after"
stats_query | sed 's/^/  /'

section "5) sizes"
psql_columns "SELECT relname || ' ' || pg_size_pretty(pg_total_relation_size(relid)) FROM pg_stat_user_tables WHERE relname IN ('logs', 'semantic_nodes', 'parse_jobs', 'trace_observations', 'trace_findings') ORDER BY pg_total_relation_size(relid) DESC" | sed 's/^/  /'

summary
