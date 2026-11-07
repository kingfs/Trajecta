#!/usr/bin/env bash
# Seed the live stack's channels through the Monitor API. Channels are stored in
# the application database only; the live YAML configs intentionally contain no
# upstream/upstreams block.
#
# Usage:
#   TRAJECTA_LIVE_MONITOR_URL=http://127.0.0.1:18081 \
#   TRAJECTA_LIVE_MONITOR_USER=admin \
#   TRAJECTA_LIVE_MONITOR_PASSWORD=change-me-live-pass \
#   TRAJECTA_LIVE_CHAT_BASE=https://... \
#   TRAJECTA_LIVE_MESSAGES_BASE=https://... \
#   TRAJECTA_LIVE_API_KEY=sk-... \
#   tests/live/seed-channels.sh [main|failover|outage|chaos]
set -euo pipefail

MONITOR_URL="${TRAJECTA_LIVE_MONITOR_URL:-http://127.0.0.1:18081}"
USER="${TRAJECTA_LIVE_MONITOR_USER:-admin}"
PASS="${TRAJECTA_LIVE_MONITOR_PASSWORD:-change-me-live-pass}"
SCENARIO="${1:-main}"
MODEL="${TRAJECTA_LIVE_MODEL:-deepseek-flash}"
CHAT_BASE="${TRAJECTA_LIVE_CHAT_BASE:-https://ai-api-gateway.app.baizhi.cloud/api/openai}"
MESSAGES_BASE="${TRAJECTA_LIVE_MESSAGES_BASE:-https://ai-api-gateway.app.baizhi.cloud/api/anthropic}"
API_KEY="${TRAJECTA_LIVE_API_KEY:?set TRAJECTA_LIVE_API_KEY}"

command -v jq >/dev/null 2>&1 || { echo "jq is required" >&2; exit 2; }

TOKEN="$(
  curl -fsS -X POST "$MONITOR_URL/api/auth/login" \
    -H 'Content-Type: application/json' \
    -d "$(jq -n --arg u "$USER" --arg p "$PASS" '{username:$u,password:$p}')" |
  jq -r '.token // empty'
)"
if [[ -z "$TOKEN" ]]; then
  echo "monitor login failed: $MONITOR_URL" >&2
  exit 1
fi

api() {
  local method="$1" path="$2" body="${3:-}"
  local args=(-fsS -X "$method" "$MONITOR_URL$path" -H "Authorization: Bearer $TOKEN")
  if [[ -n "$body" ]]; then
    args+=(-H 'Content-Type: application/json' -d "$body")
  fi
  curl "${args[@]}"
}

seed_channel() {
  local id="$1" base="$2" preset="$3" api_type="$4" protocol="$5" priority="$6" caps="$7"
  api POST /api/channels "$(jq -n \
    --arg id "$id" --arg name "$id" --arg base "$base" --arg preset "$preset" \
    --arg api_type "$api_type" --arg protocol "$protocol" --argjson priority "$priority" \
    --arg api_key "$API_KEY" --argjson caps "$caps" \
    '{id:$id,name:$name,base_url:$base,provider_preset:$preset,api_type:$api_type,protocol_family:$protocol,api_key:$api_key,enabled:true,priority:$priority,weight:1,capacity_hint:1,capabilities:$caps}')" >/dev/null
}

seed_openai_model() {
  local id="$1" base="$2" priority="$3"
  seed_channel "$id" "$base" openai chat_completions openai_compatible "$priority" \
    '{"chat_completions":true,"responses":true,"models":true,"tool_calling":true}'
  api POST "/api/channels/$id/models" \
    "$(jq -n --arg m "$MODEL" '{model:$m,display_name:$m,enabled:true}')" >/dev/null
  api PATCH "/api/channels/$id/models/$MODEL" \
    '{"supports_chat_completions":true,"supports_responses":true}' >/dev/null
}

seed_messages_model() {
  local id="$1" base="$2" priority="$3"
  seed_channel "$id" "$base" anthropic messages anthropic_messages "$priority" \
    '{"chat_completions":false,"models":true,"tool_calling":true}'
  api POST "/api/channels/$id/models" \
    "$(jq -n --arg m "$MODEL" '{model:$m,display_name:$m,enabled:true}')" >/dev/null
}

case "$SCENARIO" in
  main|chaos)
    seed_openai_model baizhi-openai "$CHAT_BASE" 100
    seed_messages_model baizhi-anthropic "$MESSAGES_BASE" 100
    ;;
  failover)
    seed_openai_model baizhi-openai "$CHAT_BASE" 100
    seed_openai_model broken-openai "http://127.0.0.1:1/v1" 200
    ;;
  outage)
    seed_openai_model baizhi-openai "http://127.0.0.1:2/v1" 200
    seed_openai_model broken-openai "http://127.0.0.1:1/v1" 50
    ;;
  *)
    echo "unknown scenario: $SCENARIO (want main, failover, outage or chaos)" >&2
    exit 2
    ;;
esac

echo "seeded $SCENARIO channels on $MONITOR_URL"
