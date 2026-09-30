#!/usr/bin/env bash
#
# Wait for a long-running rewrite job container to stop and validate the
# counters on its last output line.
#
# `trajecta upgrade cassettes rewrite` normally runs as a throwaway container
# (`docker compose run --name <job> ...`) because it walks the whole vault. This
# script waits for that container to exit, reports how it exited, and checks the
# one invariant that proves the classification was complete:
#
#     scanned == rewritten + current + other
#
# Every cassette must land in exactly one bucket, so a mismatch means files were
# skipped or counted twice. `other` is the number of files the rewrite left
# alone; on a vault that also carries LLM_PROXY_V2 recordings (a fixed 2KB JSON
# header, no magic line, never rewritten) it equals that count, which is why
# --expect-other exists -- passing it turns that count into an assertion instead
# of an observation.
#
# Usage:
#   scripts/postgres/wait-for-rewrite.sh CONTAINER [--timeout SECONDS]
#                                                [--poll SECONDS] [--expect-other N]
#
# --timeout SECONDS  give up after this many seconds (default 3000, i.e. 50m)
# --poll SECONDS     poll interval (default 10)
# --expect-other N   fail unless the `other` counter equals N
#
# Environment: see scripts/postgres/common.sh, plus
#   TRAJECTA_OPS_JOB_TIMEOUT / TRAJECTA_OPS_JOB_POLL for the defaults above.
set -Eeuo pipefail
. "$(cd "$(dirname "$0")" && pwd)/common.sh"

CONTAINER=${1:-}
[ -n "$CONTAINER" ] || die "usage: wait-for-rewrite.sh CONTAINER [--timeout SECONDS] [--poll SECONDS] [--expect-other N]"
shift

TIMEOUT=${TRAJECTA_OPS_JOB_TIMEOUT:-3000}
POLL=${TRAJECTA_OPS_JOB_POLL:-10}
EXPECT_OTHER=

while [ $# -gt 0 ]; do
  case "$1" in
    --timeout) TIMEOUT=${2:?--timeout needs seconds}; shift 2 ;;
    --poll) POLL=${2:?--poll needs seconds}; shift 2 ;;
    --expect-other) EXPECT_OTHER=${2:?--expect-other needs a number}; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1 (see --help)" ;;
  esac
done

require_cmd python3

job_status() { docker inspect "$CONTAINER" --format '{{.State.Status}}' 2>/dev/null || true; }

STATUS=$(job_status)
[ -n "$STATUS" ] || die "container not found: $CONTAINER"

section "waiting for $CONTAINER"
WAITED=0
while [ "$STATUS" = "running" ] && [ "$WAITED" -lt "$TIMEOUT" ]; do
  sleep "$POLL"
  WAITED=$((WAITED + POLL))
  STATUS=$(job_status)
done
EXIT_CODE=$(docker inspect "$CONTAINER" --format '{{.State.ExitCode}}' 2>/dev/null || printf '?')
note "status=$STATUS exit=$EXIT_CODE after ~${WAITED}s"
if [ "$STATUS" = "running" ]; then
  fail "still running after ${TIMEOUT}s; raise --timeout or inspect it with: docker logs -f $CONTAINER"
else
  check_zero "job exit code" "$EXIT_CODE"
fi

section "counters on the last output line"
LAST=$(docker logs "$CONTAINER" 2>&1 | tail -1)
note "last line: $LAST"
PARSED=$(printf '%s' "$LAST" | python3 -c '
import re, sys
line = sys.stdin.read()
counters = {k: int(v) for k, v in re.findall(r"([A-Za-z_]+)=(\d+)", line)}
for key in sorted(counters):
    print("%s=%d" % (key, counters[key]))
buckets = sum(counters.get(k, 0) for k in ("rewritten", "current", "other"))
scanned = counters.get("scanned")
print("bucket_sum=%d" % buckets)
print("consistent=%d" % (1 if scanned is not None and buckets == scanned else 0))
')
if [ -z "$PARSED" ]; then
  fail "no key=value counters found on the last line; is this the rewrite job?"
else
  while IFS='=' read -r key value; do
    case "$key" in
      consistent) check_eq "rewritten + current + other == scanned" "$value" "1" ;;
      bucket_sum) note "rewritten + current + other = $value" ;;
      other) note "other (files the rewrite left alone) = $value"
             if [ -n "$EXPECT_OTHER" ]; then check_eq "other" "$value" "$EXPECT_OTHER"; fi ;;
      *) note "$key = $value" ;;
    esac
  done <<EOF
$(printf '%s' "$PARSED")
EOF
fi

summary
