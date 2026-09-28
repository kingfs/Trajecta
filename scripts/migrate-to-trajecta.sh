#!/usr/bin/env bash
#
# Migrate an existing llm-tracelab deployment to Trajecta.
#
# The rename is mostly cosmetic, but three things carry over from the old name:
#   1. environment variables were renamed LLM_TRACELAB_* -> TRAJECTA_*
#   2. the default local SQLite file became trajecta.sqlite3
#   3. the Compose service/image/volume and the Postgres database name changed
#
# This script rewrites (1) and (2) when asked to --apply, and prints the exact
# manual steps for (3). Cassettes are never rewritten by a rename: readers keep
# accepting the pre-rename `# llm-tracelab/v3` magic, and existing .http files
# stay the untouched source of truth for replay. Pass --rewrite-cassette-magic
# only if you explicitly want the new magic on disk as well.
#
# Default mode is a dry run: nothing is written until you pass --apply.
#
# For a full migration prefer the dedicated binary `cmd/trajecta-migrate`
# (`task build:go`), which maps the environment variables the same way but also
# merges the legacy SQLite rows into Postgres, rewrites the cassette magic with
# a worker pool, validates cassette structure and archives the SQLite files:
#
#     ./trajecta-migrate env            # read-only report, including this check
#     ./trajecta-migrate run --apply
#
# This script stays useful for the .env-only path and for deployments that only
# need the file rename. Its cassette stage starts one `head` process per file,
# which is slow on vaults with hundreds of thousands of recordings.
set -Eeuo pipefail

LEGACY_ENV_PREFIX="LLM_TRACELAB_"
ENV_PREFIX="TRAJECTA_"
LEGACY_SQLITE="llm_tracelab.sqlite3"
SQLITE="trajecta.sqlite3"
LEGACY_MAGIC="# llm-tracelab/v3"
MAGIC="# trajecta/v3"

APPLY=0
ENV_FILE=""
OUTPUT_DIR=""
SQLITE_PATH=""
CASSETTE_ROOT=""
REWRITE_MAGIC=0
BINARY=""
CONFIG=""
SCAN_DIR="."
DO_SCAN=1

usage() {
  cat <<'EOF'
Usage:
  scripts/migrate-to-trajecta.sh [options]

Renames the llm-tracelab-era deployment surface to Trajecta. Dry-run by default.

Options:
  --apply                    Write changes. Without it the script only reports.
  --env-file PATH            Environment file to rewrite. Default: ./.env if present.
  --output-dir PATH          Local data directory holding the SQLite DB and cassettes.
                             Default: TRAJECTA_OUTPUT_DIR / TRAJECTA_TRACE_OUTPUT_DIR,
                             then the --env-file value, then ./data/traces.
  --sqlite PATH              Explicit legacy SQLite file instead of <output-dir>/llm_tracelab.sqlite3.
  --cassette-root PATH       Root scanned for .http cassettes. Default: --output-dir.
  --rewrite-cassette-magic   Also rewrite `# llm-tracelab/v3` -> `# trajecta/v3` in cassettes.
                             Not required: readers accept both magics.
  --binary PATH              trajecta binary used for the post-migration smoke check.
  --config PATH              Config file used for the post-migration smoke check.
  --scan PATH                Directory scanned for leftover old identifiers. Default: .
  --no-scan                  Skip the leftover-identifier scan.
  -h, --help                 Show this help.

Examples:
  scripts/migrate-to-trajecta.sh
  scripts/migrate-to-trajecta.sh --apply --env-file .env --output-dir ./data/traces
  scripts/migrate-to-trajecta.sh --apply --binary ./trajecta --config config/config.yaml
EOF
}

log()  { printf '%s\n' "$*"; }
head2() { printf '\n== %s ==\n' "$*"; }
plan() { printf '  [plan] %s\n' "$*"; }
done_() { printf '  [done] %s\n' "$*"; }
skip() { printf '  [skip] %s\n' "$*"; }
warn() { printf '  [warn] %s\n' "$*" >&2; }

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --apply) APPLY=1 ;;
      --env-file) ENV_FILE="${2:?--env-file requires a path}"; shift ;;
      --output-dir) OUTPUT_DIR="${2:?--output-dir requires a path}"; shift ;;
      --sqlite) SQLITE_PATH="${2:?--sqlite requires a path}"; shift ;;
      --cassette-root) CASSETTE_ROOT="${2:?--cassette-root requires a path}"; shift ;;
      --rewrite-cassette-magic) REWRITE_MAGIC=1 ;;
      --binary) BINARY="${2:?--binary requires a path}"; shift ;;
      --config) CONFIG="${2:?--config requires a path}"; shift ;;
      --scan) SCAN_DIR="${2:?--scan requires a path}"; shift ;;
      --no-scan) DO_SCAN=0 ;;
      -h|--help) usage; exit 0 ;;
      *) log "unknown argument: $1" >&2; usage >&2; exit 2 ;;
    esac
    shift
  done
}

# Print the contents of an env file, or nothing when it is absent.
env_text() {
  local file="$1"
  [[ -f "$file" ]] || return 0
  cat "$file"
}

# Print the value of KEY from env text. The text is passed in so callers that
# are rewriting a file never re-read it while writing.
value_in_text() {
  local text="$1" key="$2" line
  while IFS= read -r line || [[ -n "$line" ]]; do
    case "$line" in
      "$key"=*) printf '%s' "${line#"$key"=}" ; return 0 ;;
      "export $key"=*) printf '%s' "${line#export "$key"=}" ; return 0 ;;
    esac
  done <<< "$text"
}

env_file_value() {
  value_in_text "$(env_text "$1")" "$2"
}

resolve_output_dir() {
  if [[ -n "$OUTPUT_DIR" ]]; then return 0; fi
  if [[ -n "${TRAJECTA_OUTPUT_DIR:-}" ]]; then OUTPUT_DIR="$TRAJECTA_OUTPUT_DIR"; return 0; fi
  if [[ -n "${TRAJECTA_TRACE_OUTPUT_DIR:-}" ]]; then OUTPUT_DIR="$TRAJECTA_TRACE_OUTPUT_DIR"; return 0; fi
  local from_env
  from_env="$(env_file_value "$ENV_FILE" "${ENV_PREFIX}OUTPUT_DIR")"
  [[ -z "$from_env" ]] && from_env="$(env_file_value "$ENV_FILE" "${ENV_PREFIX}TRACE_OUTPUT_DIR")"
  OUTPUT_DIR="${from_env:-./data/traces}"
}

# ---------------------------------------------------------------- step 1: env

rewrite_env_file() {
  head2 "Environment file"
  if [[ -z "$ENV_FILE" ]]; then
    skip "no --env-file given and ./.env not found; nothing to rewrite"
    return 0
  fi
  if [[ ! -f "$ENV_FILE" ]]; then
    warn "--env-file $ENV_FILE does not exist"
    return 0
  fi

  local tmp backup line key target value prefix changed=0 collisions=0 original
  original="$(env_text "$ENV_FILE")"
  tmp="$(mktemp "${TMPDIR:-/tmp}/trajecta-env.XXXXXX")"
  while IFS= read -r line || [[ -n "$line" ]]; do
    key=""
    value=""
    prefix=""
    case "$line" in
      "${LEGACY_ENV_PREFIX}"*=*)
        key="${line%%=*}"
        value="${line#*=}"
        ;;
      "export ${LEGACY_ENV_PREFIX}"*=*)
        prefix="export "
        key="${line#export }"
        key="${key%%=*}"
        value="${line#*=}"
        ;;
      *)
        printf '%s\n' "$line" >> "$tmp"
        continue
        ;;
    esac

    target="${key/${LEGACY_ENV_PREFIX}/${ENV_PREFIX}}"
    if printf '%s\n' "$original" | grep -qE "^[[:space:]]*(export[[:space:]]+)?${target}="; then
      collisions=$((collisions + 1))
      if [[ "$(value_in_text "$original" "$target")" == "$value" ]]; then
        printf '# [migrate-to-trajecta] duplicate of %s\n' "$target" >> "$tmp"
        plan "$key: already set as $target with the same value, old line commented out"
      else
        printf '# [migrate-to-trajecta] %s kept; this old line conflicted and was disabled\n' "$target" >> "$tmp"
        warn "$key conflicts with the existing $target value; old line commented out, please review"
      fi
      changed=$((changed + 1))
      continue
    fi
    printf '%s%s=%s\n' "$prefix" "$target" "$value" >> "$tmp"
    plan "$key -> $target"
    changed=$((changed + 1))
  done < "$ENV_FILE"

  if [[ "$changed" -eq 0 ]]; then
    rm -f "$tmp"
    skip "no ${LEGACY_ENV_PREFIX}* keys in $ENV_FILE"
    return 0
  fi

  if [[ "$APPLY" -eq 1 ]]; then
    backup="${ENV_FILE}.trajecta-migration.bak"
    cp -p "$ENV_FILE" "$backup"
    cat "$tmp" > "$ENV_FILE"
    rm -f "$tmp"
    done_ "rewrote $changed key(s) in $ENV_FILE (backup: $backup)"
    [[ "$collisions" -gt 0 ]] && warn "$collisions conflicting key(s) were commented out; review $ENV_FILE"
  else
    rm -f "$tmp"
    skip "$changed key(s) would be rewritten in $ENV_FILE (dry run; pass --apply)"
  fi
}

# ------------------------------------------------------------- step 2: sqlite

migrate_sqlite() {
  head2 "Local SQLite application database"
  local dir="$1"
  local old="${SQLITE_PATH:-$dir/$LEGACY_SQLITE}"
  local new="$dir/$SQLITE"
  [[ -n "$SQLITE_PATH" ]] && new="$(dirname "$SQLITE_PATH")/$SQLITE"

  if [[ -f "$new" ]]; then
    skip "$new already exists; the app prefers it and keeps using it"
    return 0
  fi
  if [[ ! -f "$old" ]]; then
    skip "no legacy SQLite database at $old"
    return 0
  fi

  # The app would keep using the legacy file in place, so this rename is
  # optional. It simply finishes the rename on disk.
  local suffix
  for suffix in "" "-wal" "-shm"; do
    [[ -f "${old}${suffix}" ]] || continue
    if [[ "$APPLY" -eq 1 ]]; then
      mv "${old}${suffix}" "${new}${suffix}"
      done_ "renamed $(basename "${old}${suffix}") -> $(basename "${new}${suffix}")"
    else
      plan "$(basename "${old}${suffix}") -> $(basename "${new}${suffix}")"
    fi
  done
  if [[ "$APPLY" -eq 0 ]]; then
    skip "dry run; pass --apply to rename (the app works without this step too)"
  fi
}

# ----------------------------------------------------------- step 3: cassettes

scan_cassettes() {
  head2 "Cassettes"
  local root="$1"
  if [[ ! -d "$root" ]]; then
    skip "cassette root $root does not exist"
    return 0
  fi

  local legacy=0 current=0 other=0 file first
  while IFS= read -r file; do
    first="$(head -n 1 "$file" | tr -d '\r\n')"
    case "$first" in
      "$LEGACY_MAGIC") legacy=$((legacy + 1)) ;;
      "$MAGIC") current=$((current + 1)) ;;
      *) other=$((other + 1)) ;;
    esac
  done < <(find "$root" -type f -name '*.http' 2>/dev/null)

  log "  legacy magic ($LEGACY_MAGIC): $legacy"
  log "  current magic ($MAGIC): $current"
  [[ "$other" -gt 0 ]] && log "  legacy V2 / other headers: $other"

  if [[ "$legacy" -eq 0 ]]; then
    skip "no rewrite needed"
    return 0
  fi
  if [[ "$REWRITE_MAGIC" -eq 0 ]]; then
    skip "$legacy cassette(s) keep the pre-rename magic and stay fully readable; no action required"
    log "         (readers accept both magics; pass --rewrite-cassette-magic to also normalize the bytes)"
    return 0
  fi
  if [[ "$APPLY" -eq 0 ]]; then
    plan "would rewrite the magic in $legacy cassette(s) (dry run; pass --apply)"
    return 0
  fi

  # Byte-exact splice: replace only the first line, keep the payload untouched.
  local rewritten=0 tmp skip_bytes first_line cr
  while IFS= read -r file; do
    first_line="$(head -n 1 "$file" | tr -d '\n')"
    cr=0
    case "$first_line" in
      *$'\r') cr=1; first_line="${first_line%$'\r'}" ;;
    esac
    [[ "$first_line" == "$LEGACY_MAGIC" ]] || continue
    tmp="$(mktemp "${TMPDIR:-/tmp}/trajecta-cassette.XXXXXX")"
    skip_bytes=$(( ${#LEGACY_MAGIC} + 1 + cr ))
    {
      printf '%s' "$MAGIC"
      [[ "$cr" -eq 1 ]] && printf '\r'
      printf '\n'
      tail -c "+$((skip_bytes + 1))" "$file"
    } > "$tmp"
    mv "$tmp" "$file"
    rewritten=$((rewritten + 1))
  done < <(find "$root" -type f -name '*.http' 2>/dev/null)
  done_ "rewrote the magic in $rewritten cassette(s)"
}

# --------------------------------------------- step 4: things only you can do

manual_checklist() {
  head2 "Manual steps (not performed by this script)"
  cat <<'EOF'
  Postgres database/role name (optional; schema contents carry no old name):
    -- connect to another database, then:
    ALTER DATABASE llm_tracelab RENAME TO trajecta;
    ALTER ROLE     llm_tracelab RENAME TO trajecta;
    Keeping the old database name and only updating TRAJECTA_DATABASE_DSN is
    equally valid; nothing inside the schema references the old brand.

  Docker / Compose:
    image  kingfs/llm-tracelab:latest  ->  kingfs/trajecta:latest
    service llm-tracelab               ->  trajecta
    volume  llm-tracelab-data          ->  trajecta-data
    Existing volume data can stay put by keeping the old volume name in your
    compose override; to rename it:
      docker volume create trajecta-data
      docker run --rm -v llm-tracelab-data:/from -v trajecta-data:/to alpine \
        sh -c 'cp -a /from/. /to/'

  Docker Hub:
    rename or recreate the kingfs/llm-tracelab repository as kingfs/trajecta
    before the next push, otherwise CI image pushing fails.

  CI secrets and orchestrator config:
    any LLM_TRACELAB_* secret/variable must be re-created as TRAJECTA_*.

  Browser (Monitor UI):
    localStorage keys moved from llm-tracelab.monitor.* to trajecta.monitor.*,
    so saved language, theme, and monitor token must be set again.
EOF
}

# ------------------------------------------------------------- step 5: verify

smoke_check() {
  head2 "Smoke check"
  if [[ -z "$BINARY" ]]; then
    BINARY="$(command -v trajecta || true)"
  fi
  if [[ -z "$BINARY" || ! -x "$BINARY" ]]; then
    skip "no trajecta binary found; pass --binary PATH to verify the result"
    return 0
  fi
  if "$BINARY" version >/dev/null 2>&1; then
    done_ "$("$BINARY" version 2>&1 | head -n 1)"
  else
    warn "$BINARY version failed; check the binary"
  fi
  if [[ -n "$CONFIG" ]]; then
    if "$BINARY" -c "$CONFIG" --format json config inspect >/dev/null 2>&1; then
      done_ "config inspect succeeded for $CONFIG"
    else
      warn "config inspect failed for $CONFIG; run it manually for details"
    fi
  else
    skip "pass --config PATH to also check the effective configuration"
  fi
}

# -------------------------------------------------- step 6: leftover scan

scan_leftovers() {
  head2 "Leftover old identifiers"
  if [[ "$DO_SCAN" -eq 0 || ! -d "$SCAN_DIR" ]]; then
    skip "scan disabled"
    return 0
  fi
  local hits
  hits="$(grep -rIlE 'LLM_TRACELAB_|llm-tracelab|llm_tracelab|TraceLab' "$SCAN_DIR" \
    --exclude-dir=.git --exclude-dir=node_modules --exclude-dir=logs \
    --exclude-dir=docker-data --exclude-dir=data 2>/dev/null || true)"
  if [[ -z "$hits" ]]; then
    done_ "no old identifiers found under $SCAN_DIR"
    return 0
  fi
  warn "old identifiers still present under $SCAN_DIR:"
  printf '%s\n' "$hits" | sed 's/^/         /'
  log "         (expected in docs and in the legacy-compat code constants)"
}

main() {
  parse_args "$@"

  if [[ -z "$ENV_FILE" && -f .env ]]; then
    ENV_FILE=".env"
  fi
  resolve_output_dir
  [[ -z "$CASSETTE_ROOT" ]] && CASSETTE_ROOT="$OUTPUT_DIR"

  log "Trajecta migration (llm-tracelab -> trajecta)"
  log "mode:      $([[ "$APPLY" -eq 1 ]] && echo 'apply' || echo 'dry run (pass --apply to write)')"
  log "env file:  ${ENV_FILE:-<none>}"
  log "output:    $OUTPUT_DIR"

  rewrite_env_file
  migrate_sqlite "$OUTPUT_DIR"
  scan_cassettes "$CASSETTE_ROOT"
  manual_checklist
  smoke_check
  scan_leftovers

  head2 "Result"
  if [[ "$APPLY" -eq 1 ]]; then
    log "  Migration applied. Review the manual steps above, then start trajecta."
  else
    log "  Dry run only. Re-run with --apply to rewrite the env file and rename SQLite."
  fi
}

main "$@"
