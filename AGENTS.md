# AGENTS

## Project Intent

Trajecta is a local-first LLM API record/replay proxy for OpenAI-compatible and other mainstream LLM APIs.
Its main use case is:

1. route SDK traffic through a proxy during development
2. persist the raw HTTP exchange as a `.http` cassette
3. replay the cassette in unit tests without hitting the upstream model provider

The project optimizes for reliable tests, lower API cost, and fast debugging.

## Current Architecture

- CLI split: `server` is the runtime binary and `trajecta` is the operations CLI. A command belongs to `server` only if it needs the proxy process; that is `serve` (also the bare invocation) plus the per-binary `version` and `completion`
- `cmd/server`: `main.go` only exits through `run`; `root.go` wires `serve`, `version`, `completion` and the `--config`/`--format` flags and nothing else. `management.go` holds the management HTTP/MCP wiring shared by serve and tests, `provider_startup_probe.go` the startup provider probe
- `cmd/trajecta`: the operations CLI (build artifact `trajecta`). Its own local, server-less work is the one-off rename migration (`.env` prefix mapping, legacy SQLite merge into Postgres, cassette magic rewrite and structural validation, SQLite archiving) and cassette vault reorganisation (`layout plan` reads the preludes and reports the move list, `layout apply` renames the files and repoints `logs.path` and `upstream_exchanges.cassette_path` in one transaction per move); that logic lives in `internal/legacymigrate`, every command is a dry run unless `--apply` is passed, and no legacy `LLM_TRACELAB_*` fallback exists in the binaries. It also exposes every server-management command group through `internal/cli`: `analyze`, `audit`, `auth`, `config`, `db`, `doctor`, `migrate`, `models`, `provider`, `schema`, `tools`. `root.go` binds `--config`/`--format` to a viper instance and hands it to `internal/cli.Register`
- `internal/cli`: the CLI contract plus those command groups. The contract is the exit-code table, the JSON envelope, the viper-backed `Runtime` (`--config`/`--format`), the `Execute` top-level error path and the ERROR-recording log handler; both binaries share it, `cmd/server` imports the contract only and registers none of the commands
- `internal/appdb`: the config-driven application-database entry points shared by serve startup and the trajecta `db`/`auth`/`audit`/`analyze` commands - driver normalization, migration mode, `MigrateUp`/`OptimizeIndexes`, `Open`/`Initialize` and the auth store opener
- Reverse proxy: `internal/proxy`
- Recording pipeline: `internal/recorder`
- Application store and metadata index: `internal/store`
- Upstream resolution and capability/protocol-family rules: `internal/upstream`
- Channel (provider) config and probe services: `internal/channel`
- Routing core: multi-upstream selection, health, retries, sticky routing and snapshot publication in `internal/router`; entry-endpoint and execution-mode decisions in `internal/routeplan`; request rate limiting in `internal/limit`
- Local Responses runtime, HTTP surface, chat client, and audit queries: `internal/responses` (subpackages `runtime`, `httpapi`, `chatclient`, `audit`, `functionexec`, `tools`, `protocol`, `codexfixtures`)
- Postgres application migrations: `internal/appdbmigrate`; legacy V2 cassette upgrade and index rebuild: `internal/migrate`
- Monitor UI: `internal/monitor`; MCP tool surface: `internal/mcpserver`
- Semantic parsing and findings: Observation IR types and protocol parsers in `pkg/observe`, the cassette-backed parse worker in `internal/observeworker`, deterministic detectors in `internal/analyzer`
- Reanalysis and session aggregation: `internal/reanalysis`, `internal/sessionanalysis`
- Session trajectory rebuild (ATIF-v1.8) from client-visible Responses cassettes, plus the offline official-model validator under `scripts/atif_*`: `internal/trajectory`
- Auth (users, tokens, JWT): `internal/auth`; YAML/env configuration loading: `internal/config`; secret redaction: `internal/redaction`; fault injection: `internal/chaos`; replay-based deterministic eval harness: `internal/evals`
- Replay transport for tests: `pkg/replay`
- Shared record format parser: `pkg/recordfile`
- Cross-provider request/response normalization helpers: `pkg/llm`
- Credential and personal-data shape detection and masking: the Ragel-compiled DFA, the rule table, the offline validators, the context scorer and the format-preserving stand-in vault in `pkg/secretmask`; wired into `internal/proxy` on both the pass-through and the local Responses paths, gated by `secret_masking.enabled`. Numeric shapes with no lexical anchor (national IDs, mobile numbers) do not get their own DFA pattern: `rules.rl` has one coarse `digit_run`, `scan.go`'s `digitClasses` picks the class after the match, and adding a class needs no regeneration. A mobile number has no check digit, so its rule uses a non-proving `CandidateCheck` (allocated `1[3-9]x` head) and needs surrounding phone vocabulary before it is rewritten; the bare eleven-digit run is reported only, and `ContextWindow` narrows its context window to 24 bytes so a distant mention of a phone cannot anchor an order number

## Storage

Structured state (trace index, sessions, channel/provider config, upstream targets, observations, findings, analysis jobs, Responses state, and audit tables) lives in the application database.

- Production and the tracked default config use Postgres; the checked-in SQL migrations live in `ent/postgres-migrations/`.
- Postgres is the only driver chosen implicitly: an unset `database.driver` means Postgres, and a missing DSN then fails loudly instead of creating a local database file. SQLite must be named explicitly (`database.driver: "sqlite"`) and serves the local dev/test harness plus legacy import and read paths, with default file `{{output_dir}}/trajecta.sqlite3`; its schema is applied at startup rather than by versioned migrations.
- Raw `.http` cassettes remain the source of truth for replay and detail views; the database is a derived index for lists, filters, and aggregates.
- YAML channel configuration is a first-bootstrap input only. The first database write stores the application-database `app_settings` key `channels.initialized`; afterwards the database owns routing configuration even when every channel was disabled or deleted, and `GET /api/settings/channels` reports `channel.Service.HasConfiguration()` — the marker or any stored channel row. A YAML config with an explicit `credentials` list stays YAML-managed and rejects Monitor channel/model/alias writes with 409.
- Management writes (channels, models, aliases, provider setup, probe apply) must go through one `store.ConfigurationTransaction`; runtime routing is published only after the commit succeeds, and background upstream refresh persists best-effort through the same upstream write lock.

Lock ordering, transaction scope, and marker reset semantics are owned by `docs/ARCHITECTURE.md:108-137` (sections `## 存储边界`, `## 并发与一致性`).

Current protocol families are documented in `docs/protocol-reference/implemented-protocols.md`.
The proxy is protocol-aware pass-through plus recording/parsing; it does not currently translate requests between OpenAI, Anthropic, Gemini, and Vertex protocol families in the forwarding hot path.
The single exception is `/v1/responses`: the proxy accepts `/v1/chat/completions`, `/v1/responses` and `/v1/messages` unconditionally, and per-request routing prefers a matching native Responses upstream (pass-through) before falling back to the local Responses runtime, which orchestrates the request as an internal upstream `/v1/chat/completions` call. That local execution mode is always available and has no configuration switch; the legacy `responses_server.enabled` field and `TRAJECTA_RESPONSES_ENABLED` variable were removed. To opt out of local translation, set the application-database `app_settings` key `routing.settings` to `{"responses_strategy":"native_only"}` from the Monitor Routing settings (`PATCH /api/settings/routing`); this is not a YAML key. The local Responses runtime is built lazily, so optional provider configuration must never block startup. A raw `/v1/responses` pass-through only ever selects a target that serves the Responses API itself; a target that could serve the path only through the local runtime's Chat Completions translation stays ineligible for the whole attempt, including the retry re-selection. The native-vs-local choice is resolved per model, not per channel: an explicit `channel_models.supports_responses` / `supports_chat_completions` value (editable in the monitor UI) overrides the channel-level `api_type`/`capabilities`, and models without a declared value fall back to the channel-level behaviour. `upstream.model_capabilities` expresses the same override in YAML.

## Record File Format

The byte layout is owned by `docs/ARCHITECTURE.md:96-106` (section `## 录制格式`). Binding invariants:

- writers emit V3 only, unless a migration task explicitly says otherwise
- readers must continue to support legacy `LLM_PROXY_V2` files with a fixed 2KB JSON header block
- readers must also treat the pre-rename prelude magic `# llm-tracelab/v3` as V3; writers only emit `# trajecta/v3`
- `LLM_PROXY_V3` stays the stable format identifier for the meta-header `version` value and is deliberately not renamed

## Engineering Constraints

- Preserve replay compatibility. `pkg/replay` is a hard requirement.
- Do not make tests depend on network access.
- Keep recorded `.http` payloads human-inspectable.
- Prefer additive evolution over destructive migration of existing cassettes.
- The application database is the source for monitor list/statistics; raw `.http` files remain the source of truth for replay and detail views.
- `pkg/secretmask` renders the stand-ins for masked credentials and personal data and reverses them. A national-ID stand-in is re-rendered as a well-formed number (real province code, calendar birthdate, recomputed check character) rather than byte-substituted; its birthdate is deliberately not the real one, so a downstream flow that cross-checks the ID against a separately entered birthdate will disagree. A mobile-number stand-in keeps the real allocated three-digit head and re-renders the eight subscriber digits, so it is itself a well-formed number but a downstream flow that shows or checks the last four digits will disagree. A stand-in MUST stay
  exactly as long as the value it replaces and MUST keep that value's prefix, punctuation and per-segment
  alphabet: the proxy copies the upstream `Content-Length` to the client verbatim and derives the outbound
  request length from the body slice it is handed, so a length-changing replacement desynchronises both
  silently, and a prefix change breaks every tool that validates on shape. The cassette records the original
  body by design while the upstream receives the stand-in, so the two must never be collapsed back into one
  variable: `internal/proxy/handler.go` keeps `upstreamBodyBytes` for the recorder and `sendBodyBytes` for the
  wire, and `internal/proxy/responses_server.go` passes the stand-in to `buildResponsesServerChatRequest` and
  the original as the third argument of `PrepareLogFileWithOptionsAndBody`. On the response side the restorer
  goes underneath `UsageSniffer`, never around it, because the sniffer writes the bytes it read into the
  cassette, and it restores a stand-in completely or not at all: a stream cuts one into runs with the JSON
  envelope between them, so a run that merely resembles a stand-in is held back while the chain is open and
  released byte for byte if the rest never arrives. Never rewrite a run before the whole stand-in is
  accounted for - that is what keeps a lookalike from corrupting the text it sits in. Adding or changing a rule means editing `rules.rl` and `rule.go`, including `Strength`,
  `Prefixes`, `PrefixSkip`, `Literals`, any `Validator` and any `CandidateCheck` (a non-proving filter such
  as the allocated mobile-number head), then running `task generate:secretmask`; a numeric class added to
  `scan.go`'s `digitClasses` needs neither a `rules.rl` change nor a regeneration.
- `pkg/secretmask/rules_ragel.go` is generated from `pkg/secretmask/rules.rl` by Ragel and is committed. Never open, read, or edit it: it is a ~340 KB state-table blob whose contents are not reviewable by a human or an agent, and any hand edit is silently destroyed by the next generation. Change `rules.rl` (patterns) and `rule.go` (rule metadata) instead, then run `task generate:secretmask`. Regeneration needs `ragel-go`; the generic `ragel` binary emits C for this machine, so `ragel-go -G2` is not interchangeable with it. Commit `rules.rl`, `rule.go` and the regenerated `rules_ragel.go` in the same change, and never commit a hand-written substitute for the generated file.

## Common Workflows

- Format: `task fmt`
- Formatting check without edits: `task fmt:check`
- Lint: `task lint`
- Test: `task test`
- Short check: `task check:quick`
- Full check: `task check:full`
- Race tests: `task test:race`
- ATIF-v1.8 export and offline official-model validator: `task test:atif`
- ATIF-v1.8 JSONL validation with the pinned Harbor models (install `scripts/atif-requirements.txt` first): `task atif:validate`
- Benchmarks: `task bench:core`
- Build backend only: `task build:go` (builds `server` from `cmd/server` and `trajecta` from `cmd/trajecta`)
- Build everything: `task build`
- Run locally: `task run`
- See `docs/DEVELOPMENT.md` for the command matrix humans and AI agents should use.

## When Changing Storage Or Format

- Update `pkg/recordfile` first, then adapt recorder, monitor, and replay together.
- Keep V2 read compatibility unless the task explicitly allows a breaking change.
- If schema changes in `internal/store`, ensure startup initialization still works on an existing database (Postgres via `internal/appdbmigrate`, SQLite via startup schema).
- Prefer indexing metadata in the application database rather than rescanning every file for aggregate stats.

## Documentation Targets

All files under `docs/` are written in Chinese and describe current code facts only; the sole exception is the upstream schema snapshots `docs/protocol-reference/upstream/*/schema-index-*.md`, which are English. The repository-level process documents `CHANGELOG.md`, `CONTRIBUTING.md` and `SECURITY.md` are English, and `README.md` (Chinese) must stay in sync with `README_EN.md`. Plans, roadmaps, phase designs, and archives are not kept in this repository.

- `README.md` and `README_EN.md`: human-facing overview, quick start and capability table; keep them short landing pages and link out to `docs/` for detail
- `AGENTS.md`: AI-oriented project map and invariants
- `CHANGELOG.md`: release history in Keep a Changelog format, newest release first
- `CONTRIBUTING.md`: contribution workflow, verification commands to run before a pull request, and the hard engineering constraints
- `SECURITY.md`: how to report a vulnerability, plus the operator notes about cassettes, secrets and exposed ports
- `docs/README.md`: 中文文档总入口，列出事实源文档、操作指南、开发文档与协议参考
- `docs/IMPLEMENTATION_STATUS.md`: 当前已实现与未实现能力的事实基线
- `docs/ARCHITECTURE.md`: 代码地图、数据流、存储边界、并发一致性与测试基线
- `docs/PROTOCOLS_AND_PROVIDERS.md`: 协议族、请求入口、provider preset 与能力声明
- `docs/ROUTING_AND_CREDENTIALS.md`: 配置来源与所有权、路由选择、凭据与 limit scope
- `docs/RESPONSES_RUNTIME.md`: 本地 Responses runtime、Codex 兼容面、hosted tools
- `docs/SECRET_MASKING.md`: 凭据与个人数据遮蔽的规则来源、置信度门限、保形假值与流式还原、接入点与已知边界
- `docs/OBSERVATION_AND_AUDIT.md`: 语义解析管道、Observation IR、findings 与重分析
- `docs/STORAGE_AND_DEPLOYMENT.md`: 存储分层、迁移命令、生产部署与派生数据重算
- `docs/MONITOR_GUIDE.md`: Monitor 当前用户可见能力与操作流程
- `docs/MCP_GUIDE.md`: 当前 MCP 工具面与使用方式
- `docs/PROXY_USAGE_EXAMPLES.md`: 把 SDK 与 CLI 接到代理上的示例
- `docs/POSTGRES_OPERATIONS.md`: Postgres 长期运行的基线采集、索引、调优与排障
- `docs/DEVELOPMENT.md`: 测试、lint、构建、基准与依赖的稳定命令入口
- `docs/LEGACY_MIGRATION.md`: 从 `llm-tracelab` 迁移的操作路径（`.env` 前缀、SQLite → Postgres 合并、cassette magic 重写与结构校验、旧库归档），也是 `trajecta upgrade` 的用户文档
- `docs/protocol-reference/README.md`: 协议参考入口、已实现协议矩阵、协议差异与带日期的上游 schema 快照
- add focused docs under `docs/` only when they clarify architecture or storage decisions
