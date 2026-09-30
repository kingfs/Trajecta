#!/usr/bin/env bash
#
# Shared helpers for the operational scripts in scripts/postgres.
#
# Source it from a script; never execute it directly:
#
#     . "$(cd "$(dirname "$0")" && pwd)/common.sh"
#
# The scripts drive one deployment through `docker compose`. They deliberately
# build no DSN of their own: the service environment already carries
# TRAJECTA_DATABASE_DRIVER / TRAJECTA_DATABASE_DSN / TRAJECTA_CONFIG, so `docker
# compose run` reaches the same database the service uses and no credential is
# copied into a script or a process list. Postgres is reached with `docker
# compose exec` inside the database container, which authenticates locally.
#
# Configuration, all optional, all overridable from the environment:
#
#   TRAJECTA_OPS_DEPLOY_DIR        directory holding docker-compose.yml (default: $PWD)
#   TRAJECTA_OPS_COMPOSE_FILE      compose file (default: $DEPLOY_DIR/docker-compose.yml)
#   TRAJECTA_OPS_SERVICE           application service (default: auto-detect trajecta / llm-tracelab)
#   TRAJECTA_OPS_POSTGRES_SERVICE  database service (default: auto-detect postgres)
#   TRAJECTA_OPS_CLI               in-image CLI path (default: /app/bin/trajecta)
#   TRAJECTA_OPS_SERVER            in-image server path (default: /app/bin/server)
#   TRAJECTA_OPS_CONFIG            in-container config path (default: /app/config/config.yaml)
#   TRAJECTA_OPS_DATA_ROOT         in-container cassette root (default: /app/data/traces)
#   TRAJECTA_OPS_DATA_ROOT_HOST    host cassette root (default: $DEPLOY_DIR/data/traces)
#   TRAJECTA_OPS_LEGACY_ROOT       pre-rename container prefix (default: /opt/llm_proxy/logs)
#   TRAJECTA_OPS_MONITOR_PORT      monitor port on 127.0.0.1 (default: .env
#                                  TRAJECTA_HOST_MONITOR_PORT, else 8081)
#   TRAJECTA_OPS_BASE_URL          monitor base URL (default: http://127.0.0.1:$MONITOR_PORT)
#   TRAJECTA_OPS_PG_USER           database role (default: .env POSTGRES_USER, else the postgres
#                                  container environment, else trajecta)
#   TRAJECTA_OPS_PG_DB             database name (default: .env POSTGRES_DB, else the postgres
#                                  container environment, else trajecta)
#   AUTH_USER / AUTH_PASSWORD      monitor credentials (default: from the deployment .env)
#   DOCKER_CONFIG                  honoured as usual; set it when the default docker config
#                                  directory is not writable (sandboxes, CI).
#
# Every script under scripts/postgres sources this file, so `set -Eeuo pipefail`
# and the deployment contract are identical everywhere.

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  printf 'common.sh is a library: source it from a script under scripts/postgres\n' >&2
  exit 2
fi

set -Eeuo pipefail

if [ -t 1 ]; then
  C_HEAD=$'\033[1m'; C_OK=$'\033[32m'; C_WARN=$'\033[33m'; C_FAIL=$'\033[31m'; C_OFF=$'\033[0m'
else
  C_HEAD=; C_OK=; C_WARN=; C_FAIL=; C_OFF=
fi

# ---------------------------------------------------------------- output ----

section() { printf '\n%s=== %s ===%s\n' "$C_HEAD" "$*" "$C_OFF"; }
note()    { printf '  %s\n' "$*"; }
warn()    { printf '  %s%s%s\n' "$C_WARN" "$*" "$C_OFF" >&2; }
die()     { printf '%serror%s: %s\n' "$C_FAIL" "$C_OFF" "$*" >&2; exit 1; }

# Checks accumulate instead of aborting, so one run reports every problem.
FAILURES=0
ok()        { printf '  %sok%s   %s\n' "$C_OK" "$C_OFF" "$*"; }
fail()      { FAILURES=$((FAILURES + 1)); printf '  %sFAIL%s %s\n' "$C_FAIL" "$C_OFF" "$*"; }
check_eq()  { if [ "$2" = "$3" ]; then ok "$1 = $2"; else fail "$1 = $2 (expected $3)"; fi; }
check_zero(){ if [ "${2:-0}" = "0" ]; then ok "$1 = 0"; else fail "$1 = $2 (expected 0)"; fi; }

summary() {
  if [ "$FAILURES" -eq 0 ]; then
    printf '\n%sall checks passed%s\n' "$C_OK" "$C_OFF"
    return 0
  fi
  printf '\n%s%d check(s) failed%s\n' "$C_FAIL" "$FAILURES" "$C_OFF" >&2
  return 1
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

# usage -- print the header comment of the script that sourced this library
usage() {
  sed -n '2,/^set -Eeuo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'
}

# --help must work without a deployment, a compose file or docker: every script
# under scripts/postgres documents itself in its header and shares this parse.
case "${1:-}" in
  -h|--help) usage; exit 0 ;;
esac

# ------------------------------------------------------- deployment facts ----

TRAJECTA_OPS_DEPLOY_DIR=${TRAJECTA_OPS_DEPLOY_DIR:-$PWD}
[ -d "$TRAJECTA_OPS_DEPLOY_DIR" ] || die "deploy dir does not exist: $TRAJECTA_OPS_DEPLOY_DIR"
TRAJECTA_OPS_DEPLOY_DIR=$(cd "$TRAJECTA_OPS_DEPLOY_DIR" && pwd)
TRAJECTA_OPS_COMPOSE_FILE=${TRAJECTA_OPS_COMPOSE_FILE:-$TRAJECTA_OPS_DEPLOY_DIR/docker-compose.yml}
[ -f "$TRAJECTA_OPS_COMPOSE_FILE" ] || die "compose file does not exist: $TRAJECTA_OPS_COMPOSE_FILE (set TRAJECTA_OPS_COMPOSE_FILE)"

require_cmd docker
docker compose version >/dev/null 2>&1 || die "the docker compose plugin is required"

dc() {
  docker compose --project-directory "$TRAJECTA_OPS_DEPLOY_DIR" -f "$TRAJECTA_OPS_COMPOSE_FILE" "$@"
}

env_file_value() { # env_file_value KEY -- read KEY from the deployment .env, if present
  local file=$TRAJECTA_OPS_DEPLOY_DIR/.env line
  [ -f "$file" ] || return 1
  line=$(grep -E "^[[:space:]]*$1=" "$file" | tail -1) || return 1
  [ -n "$line" ] || return 1
  printf '%s' "${line#*=}"
}

dc_service_exists() { dc config --services 2>/dev/null | grep -qx "$1"; }

detect_service() { # detect_service NAME... -- first name that the compose file defines
  local s
  for s in "$@"; do
    if dc_service_exists "$s"; then printf '%s' "$s"; return 0; fi
  done
  return 1
}

TRAJECTA_OPS_SERVICE=${TRAJECTA_OPS_SERVICE:-$(detect_service trajecta llm-tracelab llm_tracelab || true)}
TRAJECTA_OPS_SERVICE=${TRAJECTA_OPS_SERVICE:-trajecta}
TRAJECTA_OPS_POSTGRES_SERVICE=${TRAJECTA_OPS_POSTGRES_SERVICE:-$(detect_service postgres || true)}
TRAJECTA_OPS_POSTGRES_SERVICE=${TRAJECTA_OPS_POSTGRES_SERVICE:-postgres}

TRAJECTA_OPS_CLI=${TRAJECTA_OPS_CLI:-/app/bin/trajecta}
TRAJECTA_OPS_SERVER=${TRAJECTA_OPS_SERVER:-/app/bin/server}
TRAJECTA_OPS_CONFIG=${TRAJECTA_OPS_CONFIG:-/app/config/config.yaml}
TRAJECTA_OPS_DATA_ROOT=${TRAJECTA_OPS_DATA_ROOT:-/app/data/traces}
TRAJECTA_OPS_DATA_ROOT_HOST=${TRAJECTA_OPS_DATA_ROOT_HOST:-$TRAJECTA_OPS_DEPLOY_DIR/data/traces}
TRAJECTA_OPS_LEGACY_ROOT=${TRAJECTA_OPS_LEGACY_ROOT:-/opt/llm_proxy/logs}

TRAJECTA_OPS_MONITOR_PORT=${TRAJECTA_OPS_MONITOR_PORT:-$(env_file_value TRAJECTA_HOST_MONITOR_PORT || true)}
TRAJECTA_OPS_MONITOR_PORT=${TRAJECTA_OPS_MONITOR_PORT:-8081}
TRAJECTA_OPS_BASE_URL=${TRAJECTA_OPS_BASE_URL:-http://127.0.0.1:$TRAJECTA_OPS_MONITOR_PORT}

AUTH_USER=${AUTH_USER:-$(env_file_value AUTH_USER || true)}
AUTH_PASSWORD=${AUTH_PASSWORD:-$(env_file_value AUTH_PASSWORD || true)}

pg_container_env() { # pg_container_env KEY -- read KEY from the running postgres container
  dc exec -T "$TRAJECTA_OPS_POSTGRES_SERVICE" printenv "$1" 2>/dev/null | tr -d '\r'
}

TRAJECTA_OPS_PG_USER=${TRAJECTA_OPS_PG_USER:-$(env_file_value POSTGRES_USER || true)}
TRAJECTA_OPS_PG_USER=${TRAJECTA_OPS_PG_USER:-$(pg_container_env POSTGRES_USER || true)}
TRAJECTA_OPS_PG_USER=${TRAJECTA_OPS_PG_USER:-trajecta}
TRAJECTA_OPS_PG_DB=${TRAJECTA_OPS_PG_DB:-$(env_file_value POSTGRES_DB || true)}
TRAJECTA_OPS_PG_DB=${TRAJECTA_OPS_PG_DB:-$(pg_container_env POSTGRES_DB || true)}
TRAJECTA_OPS_PG_DB=${TRAJECTA_OPS_PG_DB:-trajecta}

# The acceptance and evidence queries are correctness probes, not benchmarks:
# run them serially so a small container /dev/shm cannot fail the whole
# statement (docs/POSTGRES_OPERATIONS.md, "大表聚合的内存与并行度").
#
# They reach the server through libpq, which forwards connection `options` in
# the startup packet, so PGOPTIONS is honoured here. The Go driver
# (github.com/lib/pq) does not forward the same value, which is why a
# `?options=-c ...` URL is silently ignored by the service but works in psql --
# see "这些参数不能通过 DSN 的 options 传递" in the operations doc.
PSQL_SHM_OPTIONS="-c max_parallel_workers_per_gather=0"

# --------------------------------------------------------------- postgres ----

psql_lines() { # psql_lines SQL -- raw -At rows, one per line
  dc exec -T -e "PGOPTIONS=$PSQL_SHM_OPTIONS" "$TRAJECTA_OPS_POSTGRES_SERVICE" psql \
    -U "$TRAJECTA_OPS_PG_USER" -d "$TRAJECTA_OPS_PG_DB" -At -c "$1" 2>&1 | tr -d '\r'
}

psql_scalar() { # psql_scalar SQL -- first row of the result, or empty
  psql_lines "$1" | head -1
}

psql_columns() { # psql_columns SQL -- rows joined with '|' (for label|value loops)
  dc exec -T -e "PGOPTIONS=$PSQL_SHM_OPTIONS" "$TRAJECTA_OPS_POSTGRES_SERVICE" psql \
    -U "$TRAJECTA_OPS_PG_USER" -d "$TRAJECTA_OPS_PG_DB" -At -F'|' -c "$1" 2>&1 | tr -d '\r'
}

table_exists() { # table_exists NAME
  [ "$(psql_scalar "SELECT to_regclass('public.$1') IS NOT NULL")" = "t" ]
}

# ------------------------------------------------------------ deployment -----

# cli ARGS... -- run the in-image CLI inside the service environment
cli() {
  dc run --rm -T --no-deps --entrypoint "$TRAJECTA_OPS_CLI" "$TRAJECTA_OPS_SERVICE" "$@"
}

# server ARGS... -- run the in-image server binary inside the service environment
server() {
  dc run --rm -T --no-deps --entrypoint "$TRAJECTA_OPS_SERVER" "$TRAJECTA_OPS_SERVICE" "$@"
}

service_running() {
  [ -n "$(dc ps -q "$TRAJECTA_OPS_SERVICE" 2>/dev/null)" ]
}

require_service_running() {
  service_running || die "service '$TRAJECTA_OPS_SERVICE' is not running in $TRAJECTA_OPS_DEPLOY_DIR"
}

# container_to_host PATH -- map an in-container cassette path to its host path
container_to_host() {
  printf '%s' "$1" | sed "s#^$TRAJECTA_OPS_DATA_ROOT#$TRAJECTA_OPS_DATA_ROOT_HOST#"
}

# host_to_container PATH -- map a host cassette path back to its container path
host_to_container() {
  printf '%s' "$1" | sed "s#^$TRAJECTA_OPS_DATA_ROOT_HOST#$TRAJECTA_OPS_DATA_ROOT#"
}

# api_login -- print a bearer token, or return 1 when credentials are missing
api_login() {
  [ -n "$AUTH_USER" ] || return 1
  curl -s -m 15 -X POST "$TRAJECTA_OPS_BASE_URL/api/auth/login" \
    -H 'Content-Type: application/json' \
    -d "$(printf '{"username":"%s","password":"%s"}' "$AUTH_USER" "$AUTH_PASSWORD")" \
    | sed -n 's/.*"token":"\([^"]*\)".*/\1/p'
}

api_get() { # api_get TOKEN PATH [curl args...]
  local token=$1 path=$2; shift 2
  curl -s -m 90 -H "Authorization: Bearer $token" "$@" "$TRAJECTA_OPS_BASE_URL$path"
}

api_code() { # api_code TOKEN PATH
  curl -s -o /dev/null -w '%{http_code}' -m 90 -H "Authorization: Bearer $1" "$TRAJECTA_OPS_BASE_URL$2"
}

# wait_for_monitor [ATTEMPTS] [SLEEP] -- poll the monitor UI until it answers 200
wait_for_monitor() {
  local attempts=${1:-30} sleep_for=${2:-5} code=000 i=0
  for i in $(seq 1 "$attempts"); do
    code=$(curl -s -o /dev/null -w '%{http_code}' -m 5 "$TRAJECTA_OPS_BASE_URL/" || true)
    [ "$code" = "200" ] && break
    sleep "$sleep_for"
  done
  printf '%s' "$code"
}
