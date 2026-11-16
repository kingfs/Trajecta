#!/usr/bin/env bash
# Run the realistic agent-traffic benchmarks (conversion, parse/decode, audit
# detection, hot masking, 256 KiB end-to-end) and snapshot the results so
# performance can be tracked across releases.
#
#   scripts/bench-xir-trend.sh                 # count 5, 300ms per benchmark
#   BENCH_COUNT=3 BENCH_TIME=200ms scripts/bench-xir-trend.sh
#
# Snapshots live under bench-results/<UTC timestamp>/ (gitignored); the run also
# writes summary.txt and, when a previous snapshot exists, compare.txt through
# benchstat.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

OUT_ROOT="${BENCH_OUT:-$REPO_ROOT/bench-results}"
RUN_DIR="$OUT_ROOT/$(date -u +%Y%m%dT%H%M%SZ)"
COUNT="${BENCH_COUNT:-6}"
BENCH_TIME="${BENCH_TIME:-300ms}"
mkdir -p "$RUN_DIR"

run_group() {
  local name="$1"
  shift
  echo "==> $name"
  go test -run '^$' -bench "$1" -benchmem -benchtime "$BENCH_TIME" -count "$COUNT" "${@:2}" \
    | tee "$RUN_DIR/$name.txt"
}

run_group convert 'BenchmarkCorpus' ./pkg/xir/convert
run_group parse 'BenchmarkCorpusParseDecode|BenchmarkCorpusAuditBuild|BenchmarkCorpusStreamAssemble' ./pkg/xir
run_group audit 'BenchmarkAuditDetectCorpus' ./internal/audit
run_group masking 'BenchmarkMaskCorpus' ./internal/detector
run_group proxy 'BenchmarkHandlerAgentTrajectory256K' ./internal/proxy

if command -v benchstat >/dev/null 2>&1; then
  benchstat "$RUN_DIR"/*.txt > "$RUN_DIR/summary.txt" 2>/dev/null || true
  PREVIOUS="$(find "$OUT_ROOT" -mindepth 1 -maxdepth 1 -type d ! -path "$RUN_DIR" | sort | tail -n 1)"
  if [[ -n "${PREVIOUS:-}" ]]; then
    benchstat "$PREVIOUS"/*.txt "$RUN_DIR"/*.txt > "$RUN_DIR/compare.txt" 2>/dev/null || true
    echo "==> comparison with $(basename "$PREVIOUS")"
    grep -E 'sec/op|B/op|allocs/op|geomean|vs base' "$RUN_DIR/compare.txt" | head -n 40 || true
  fi
else
  echo "benchstat not installed; summaries skipped (go install golang.org/x/perf/cmd/benchstat@latest)" >&2
fi

echo
echo "snapshot: $RUN_DIR"
echo "summary:  $RUN_DIR/summary.txt"
[[ -f "$RUN_DIR/compare.txt" ]] && echo "compare:  $RUN_DIR/compare.txt"
