#!/usr/bin/env bash
#
# Create the hot-query partial indexes through the project's own entry point and
# then re-measure the two list queries those indexes exist for.
#
# `server db migrate optimize-indexes` is idempotent and issues
# CREATE INDEX CONCURRENTLY IF NOT EXISTS, so it does not lock `logs` for
# writes. It is deliberately not part of the versioned migrations: CREATE INDEX
# CONCURRENTLY cannot run inside a transaction block, which golang-migrate wraps
# every migration in.
#
# Why exactly these two queries: the monitor's failure list (status_code >= 400
# ordered by recorded_at DESC) and its slow-request list (duration_ms DESC) both
# used to degrade into a sequential scan plus a top-N sort. Measured on a
# 248,164-row logs table (388 MB): 934 ms and 250 ms before, then an index-only
# scan with Heap Fetches: 0 at 0.218 ms and 0.128 ms after. The newest-trace
# list was already 0.16 ms and needs no partial index.
#
# The EXPLAIN probes run with max_parallel_workers_per_gather = 0 for the same
# reason acceptance.sh does: they are correctness probes, and a small container
# /dev/shm must not fail them (docs/POSTGRES_OPERATIONS.md).
#
# Usage: scripts/postgres/optimize-indexes.sh
# Environment: see scripts/postgres/common.sh.
set -Eeuo pipefail
. "$(cd "$(dirname "$0")" && pwd)/common.sh"

section "1) create the partial indexes (concurrent, idempotent)"
server db migrate optimize-indexes -c "$TRAJECTA_OPS_CONFIG" 2>&1 | grep -vE '^( Container|time=)' | tail -8 | sed 's/^/  /' || true

section "2) logs indexes"
psql_columns "SELECT indexname || ' ' || pg_size_pretty(pg_relation_size(indexname::regclass)) FROM pg_indexes WHERE tablename = 'logs' ORDER BY indexname" | sed 's/^/  /'

section "3) index health"
check_zero "invalid indexes" "$(psql_scalar "SELECT count(*) FROM pg_index WHERE NOT indisvalid")"
check_zero "not-ready indexes" "$(psql_scalar "SELECT count(*) FROM pg_index WHERE NOT indisready")"

section "4) failure list (status_code >= 400, recorded_at DESC, LIMIT 50)"
psql_lines "EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, BUFFERS)
SELECT trace_id, status_code FROM logs
WHERE status_code >= 400 AND COALESCE(exchange_kind, '') IN ('', 'entry', 'proxy')
ORDER BY recorded_at DESC LIMIT 50" | grep -vE '^\s*$' | head -14 | sed 's/^/  /'
note "expect: Index Scan or Index Only Scan using tracelog_failure_recent_client_visible_idx"

section "5) slow-request list (duration_ms DESC, LIMIT 50)"
psql_lines "EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, BUFFERS)
SELECT trace_id, duration_ms FROM logs
WHERE COALESCE(exchange_kind, '') IN ('', 'entry', 'proxy')
ORDER BY duration_ms DESC LIMIT 50" | grep -vE '^\s*$' | head -14 | sed 's/^/  /'
note "expect: Index Only Scan using tracelog_duration_slow_client_visible_idx with Heap Fetches: 0"

section "6) refresh statistics"
psql_lines "ANALYZE logs" >/dev/null
ok "ANALYZE logs"
note "planner row estimate for logs: $(psql_scalar "SELECT reltuples::bigint FROM pg_class WHERE relname = 'logs'")"

summary
