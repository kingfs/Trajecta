<div align="center">

# Trajecta

**The Agent Trajectory & Traffic Engine**

Turn real LLM API traffic into replayable, auditable, reviewable test assets.

[![Go CI](https://github.com/kingfs/Trajecta/actions/workflows/ci.yml/badge.svg)](https://github.com/kingfs/Trajecta/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/kingfs/Trajecta?color=blue)](https://github.com/kingfs/Trajecta/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](./LICENSE)
[![Go Version](https://img.shields.io/github/go-mod/go-version/kingfs/Trajecta)](./go.mod)

English | [简体中文](./README.md)

<img src="./images/en/monitor-overview.png" alt="Trajecta Monitor overview" width="900">

</div>

## Why Trajecta

Testing LLM applications is hard. Real calls are slow, cost money, and return different results every run. Hand-written mocks drift away from the real upstream and throw away exactly the parts that break in production: stream chunks, tool calls, token usage, status codes, and latency.

Trajecta is a **local-first record/replay proxy**. Point your SDK or CLI at it during development: requests are forwarded to the real upstream as usual while the complete HTTP exchange is written to disk byte-for-byte as a `.http` cassette. Attach `pkg/replay` to a unit test later and the same exchange replays offline — no network, no API key, no cost, deterministic results.

- **Records the real bytes** — request and response payloads, SSE chunks, status codes, and timings, stored verbatim
- **Replays for free** — `replay.NewTransport()` plugs into any SDK's `http.Client`
- **Readable and diffable** — cassettes are text: review them in a PR, edit them by hand, commit them with your tests
- **Trajectories you can see** — the monitor parses every trace into one timeline: messages, tool calls, tokens, routing decisions
- **Clear source of truth** — cassettes are authoritative; the application database (Postgres in production, SQLite locally) is only a derived index

## Highlights

| Capability | Description |
| --- | --- |
| Transparent proxy | Records and forwards OpenAI Chat Completions, OpenAI Responses, Anthropic Messages, Google GenAI, Vertex and other protocol families |
| Offline replay | `pkg/replay` implements `http.RoundTripper`; tests need no network and no API key |
| Stream fidelity | SSE data is stored chunk by chunk and replayed in its original order |
| Record format | `LLM_PROXY_V3` compact prelude plus raw HTTP bytes; legacy V2 recordings stay readable |
| Monitor UI | Go-embedded React interface: request list, session grouping, trace detail, routing decisions, audit, model and provider management |
| Trajectory parsing | Cassettes are parsed into an Observation IR that yields findings such as dangerous tool calls and sensitive data |
| Session export | Export a session's cumulative history as ATIF v1.8 JSONL |
| Agent interface | An MCP server exposes 21 query and reanalysis tools so agents read trajectories instead of scraping HTML |
| Multi-upstream routing | Channel, weight, p2c and sticky routing, with model aliases and per-model capability overrides |
| Storage tiers | Postgres in production (with checked-in SQL migrations), SQLite locally, cassettes always authoritative |

## Quick Start

### Option 1: Docker Compose (recommended)

Requires Docker and Docker Compose.

```bash
git clone https://github.com/kingfs/Trajecta.git && cd Trajecta
cp .env.example .env          # change POSTGRES_PASSWORD
docker compose up -d
docker compose exec trajecta /app/bin/trajecta \
  -c /app/config/config.yaml auth init-user --username admin --password 'change-me-123'
```

- Monitor: <http://localhost:8081> — log in with the credentials above
- Proxy: <http://localhost:8080/v1> — point your SDK's `base_url` here

After signing in, configure upstream URLs and API keys on the `Providers` page, then create a personal token on the `Tokens` page: use it as the SDK's `api_key` and your requests will be proxied and recorded. Host ports come from `TRAJECTA_HOST_SERVER_PORT` / `TRAJECTA_HOST_MONITOR_PORT` in `.env`.

### Option 2: From source

Requires Go 1.25+, [Task](https://taskfile.dev) optional but recommended.

```bash
task build && task run                  # Postgres-first default: config/config.yaml
CONFIG=config/examples/local-sqlite.yaml task run   # local SQLite only
```

Without Task:

```bash
export TRAJECTA_DATABASE_DSN='postgres://trajecta:trajecta@localhost:5432/trajecta?sslmode=disable'
go run ./cmd/server -c config/config.yaml
```

### Option 3: Upgrading from llm-tracelab

The project was renamed to Trajecta in `v2.0.0`. Existing deployments can run the migration script (a dry run by default):

```bash
scripts/migrate-to-trajecta.sh --env-file .env --output-dir ./data/traces
scripts/migrate-to-trajecta.sh --apply --env-file .env --output-dir ./data/traces
```

It rewrites the `LLM_TRACELAB_*` keys in `.env`, renames the local SQLite database with its `-wal`/`-shm` siblings, reports cassette magic versions, and lists the items that need a manual decision. Breaking changes and compatibility guarantees are in the [CHANGELOG](./CHANGELOG.md).

## Five Minutes: Record a Call, Replay It in a Test

**1. Send one real call through the proxy** (the token comes from the monitor's `Tokens` page):

```bash
export TRAJECTA_TOKEN=llmtl_xxx
curl -s http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer ${TRAJECTA_TOKEN}" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"Hello"}],"stream":true}'
```

**2. The proxy writes the exchange as a cassette** at `<output_dir>/<upstream host>/<model>/<yyyy>/<mm>/<dd>/<timestamp>.http`:

```text
# trajecta/v3
# meta: {"version":"LLM_PROXY_V3","meta":{"request_id":"…","time":"…","model":"gpt-4o-mini",
#        "provider":"openai","operation":"chat.completions","endpoint":"/v1/chat/completions",
#        "method":"POST","status_code":200,"duration_ms":842,"ttft_ms":210,"routing_policy":"p2c"},
#        "layout":{"req_header_len":231,"req_body_len":96,"res_header_len":120,
#        "res_body_len":512,"is_stream":true},
#        "usage":{"prompt_tokens":18,"completion_tokens":42,"total_tokens":60}}
# event: {"type":"request","time":"…","method":"POST","url":"/v1/chat/completions","body_bytes":96}
# event: {"type":"response","time":"…","status_code":200,"is_stream":true,"body_bytes":512}
# event: {"type":"llm.output_text.delta","time":"…","is_stream":true,"message":"Hello"}
# event: {"type":"llm.usage","time":"…","attributes":{"total_tokens":60}}

POST /v1/chat/completions HTTP/1.1
Content-Type: application/json
Authorization: Bearer sk-***

{"model":"gpt-4o-mini","messages":[{"role":"user","content":"Hello"}],"stream":true}

HTTP/1.1 200 OK
Content-Type: text/event-stream

data: {"choices":[{"delta":{"content":"Hello"}}]}
…
data: [DONE]
```

(The example is abridged; `…` marks omitted content. With the default `debug.mask_key: true`, the recorder replaces the value of `Authorization`, `api-key`, `x-api-key` and `x-goog-api-key` in the recorded request headers with `fake-key-logging`; every other header and both bodies are stored verbatim, which is what makes replay faithful.)

**3. Replay that file in a unit test**, with no network and no API key:

```go
import (
    "context"
    "net/http"
    "testing"

    "github.com/kingfs/Trajecta/pkg/replay"
    "github.com/sashabaranov/go-openai"
)

func TestChat(t *testing.T) {
    cfg := openai.DefaultConfig("fake-key")
    cfg.BaseURL = "http://localhost/v1" // the URL does not matter, the transport intercepts it
    cfg.HTTPClient = &http.Client{
        Transport: replay.NewTransport("testdata/chat.http"),
    }
    client := openai.NewClientWithConfig(cfg)

    resp, err := client.CreateChatCompletion(context.Background(), openai.ChatCompletionRequest{
        Model:    "gpt-4o-mini",
        Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "Hello"}},
    })
    if err != nil {
        t.Fatal(err)
    }
    _ = resp
}
```

Any SDK that goes through `http.Client` works the same way: swap the transport, change nothing else in your application code.

## Supported Upstreams

| Protocol family | Provider presets | Support |
| --- | --- | --- |
| OpenAI-compatible | `openai`, `openrouter`, `fireworks`, `together`, `deepseek`, `groq`, `moonshot`, `cerebras`, `perplexity` | verified / compatible |
| OpenAI Responses | native `/v1/responses` pass-through, or translated to chat completions by the local runtime | verified |
| Anthropic Messages | `anthropic` | verified |
| Google GenAI | `google_genai`, `google`, `gemini` | verified |
| Vertex AI | `vertex` (`vertex_express` / `vertex_project_location`) | verified |
| Azure OpenAI | `azure` (v1 and deployment routing) | verified |
| vLLM | `vllm` | verified |

The full preset list, capability rules, routing-profile combinations and protocol differences are in [docs/PROTOCOLS_AND_PROVIDERS.md](./docs/PROTOCOLS_AND_PROVIDERS.md). The proxy is **protocol-aware pass-through plus recording/parsing**; it does not translate between protocol families in the forwarding hot path. The single exception is `/v1/responses`, which the local runtime can serve.

## Architecture

```text
   SDK / CLI / Codex / Claude Code
                │  OpenAI · Anthropic · Google · Vertex
                ▼
┌──────────────────────────────────────────────────────────┐
│ trajecta serve                                           │
│                                                          │
│   proxy ──▶ router ──▶ upstream provider (channel/key/limit)
│     │         │                                          │
│     │         └──▶ routing decisions, sticky routes, failover
│     ▼                                                    │
│   recorder ──▶ .http cassette  ← source of truth for replay
│     │                                                    │
│     ├──▶ semantic parsing ──▶ Observation IR ──▶ findings │
│     └──▶ index ──▶ application DB (Postgres prod / SQLite) │
└──────────────────────────────────────────────────────────┘
       │                                   │
       ▼                                   ▼
  Monitor UI (go:embed)              MCP server
                                           │
                                           ▼
                              pkg/replay ──▶ offline unit tests
```

| Directory | Responsibility |
| --- | --- |
| `cmd/server` | CLI entry point and all subcommands (serve, db, auth, provider, models, audit, analyze, doctor…) |
| `internal/proxy` | Reverse proxy, protocol entry normalization, streaming interception |
| `internal/recorder` | Cassette recording and persistence |
| `pkg/recordfile` | Record format parsing and writing (writes V3, reads V2 and the legacy magic) |
| `pkg/replay` | Replay `http.RoundTripper` for unit tests |
| `pkg/llm` | Cross-provider request/response, stream transcript and usage normalization |
| `internal/store` | Application database and metadata index |
| `internal/upstream`, `internal/channel` | Upstream resolution, channel configuration, capabilities and probing |
| `internal/responses` | Local Responses runtime, hosted tools, audit queries |
| `internal/observe`, `internal/analyzer` | Semantic parsing pipeline and audit findings |
| `internal/monitor` | Monitor HTTP API and the embedded UI |
| `internal/mcpserver` | MCP tool surface |
| `internal/trajectory` | ATIF v1.8 session trajectory rebuild and export |

Data flow, concurrency guarantees and source-of-truth boundaries are documented in [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md) (Chinese).

## Documentation

| I want to… | Read |
| --- | --- |
| See what is implemented and what is not | [Implementation status](./docs/IMPLEMENTATION_STATUS.md) |
| Understand the code layout and data flow | [Architecture and code map](./docs/ARCHITECTURE.md) |
| Point an SDK or CLI at the proxy | [Proxy usage examples](./docs/PROXY_USAGE_EXAMPLES.md) |
| Add an upstream provider, see the protocol matrix | [Protocols and providers](./docs/PROTOCOLS_AND_PROVIDERS.md), [protocol reference](./docs/protocol-reference/README.md) |
| Manage channels, models, credentials and limits | [Routing, channels and credentials](./docs/ROUTING_AND_CREDENTIALS.md) |
| Use Codex or the local Responses runtime | [Local Responses runtime](./docs/RESPONSES_RUNTIME.md) |
| Use the monitor | [Monitor guide](./docs/MONITOR_GUIDE.md) |
| Let an agent query trajectories over MCP | [MCP guide](./docs/MCP_GUIDE.md) |
| Deploy, migrate and retain data | [Storage and deployment](./docs/STORAGE_AND_DEPLOYMENT.md) |
| Tune and troubleshoot long-running Postgres | [PostgreSQL operations](./docs/POSTGRES_OPERATIONS.md) |
| Contribute, run tests and CI | [Development](./docs/DEVELOPMENT.md), [CONTRIBUTING](./CONTRIBUTING.md) |
| See what changed in each release | [CHANGELOG](./CHANGELOG.md) |
| Read the AI-oriented project conventions | [AGENTS.md](./AGENTS.md) |

The complete documentation index is [docs/README.md](./docs/README.md). Detailed reference documents are written in Chinese.

## Screenshots

<div align="center">
<img src="./images/en/monitor-traces.png" alt="Request list" width="46%">
<img src="./images/en/monitor-trace-detail.png" alt="Trace detail: timeline, protocol, audit and performance" width="46%">
<br>
<img src="./images/en/monitor-providers.png" alt="Provider and channel management" width="70%">
</div>

Trace detail splits one exchange into Routing & Conversation, Protocol, Audit, Performance and Raw views, so audit findings, routing decisions and the raw protocol all live on one page.

## Project Status and Scope

Current release `v2.0.0`, MIT licensed. Deliberately out of scope:

- public multi-tenant relay, billing, top-up or subscription distribution
- cross-protocol translation in the forwarding hot path (the local `/v1/responses` runtime is the only exception)
- real execution lifecycles and container-level sandboxes for hosted `file_search` / `code_interpreter` / `computer_use`

The full comparison of scope and current implementation is in [docs/IMPLEMENTATION_STATUS.md](./docs/IMPLEMENTATION_STATUS.md).

## Contributing

Issues and pull requests are welcome. Before you start, read [CONTRIBUTING.md](./CONTRIBUTING.md): it covers the verification commands to run before submitting, the compatibility requirements for the record format, and the documentation rules (`docs/` describes current code facts only).

```bash
task check:quick     # formatting check, lint, short tests
task check:full      # adds full tests, e2e, race and a complete build
```

## License

[MIT](./LICENSE)
