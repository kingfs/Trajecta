# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) 1.1.0, and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html). The release dates below are the dates of the tagged commits; the earlier release history of this project is also visible in the repository's git tags.

## [Unreleased]

### Fixed

- The README badge now reads the repository's tags (`github/v/tag`). The project publishes release tags without GitHub Releases, so the previous `github/v/release` badge rendered "no releases or repo not found", and it now also links to the tag list.

<!-- Add entries here as changes land. -->

## [2.0.1] - 2026-09-28

Documentation-only release: no runtime, configuration, record-format or API
behaviour changed. The docs tree was re-verified against the code and the
READMEs were rewritten as project landing pages.

### Added

- `CHANGELOG.md`, `CONTRIBUTING.md` and `SECURITY.md` at the repository root, covering the release history, the contribution workflow with the verification commands to run before a pull request, and vulnerability reporting plus the operator notes for cassettes, secrets and exposed ports.
- `task ui:screenshots`, with a Playwright capture config and capture script that regenerate the README screenshots for the Chinese and English Monitor UIs from the offline fixture server.

### Changed

- `README.md` and `README_EN.md` were rewritten as landing pages and kept in sync: positioning, capability table, three quick-start paths (Docker Compose, source, upgrading from llm-tracelab), a five-minute record-and-replay walkthrough, the supported-upstream matrix, an architecture overview with a directory map, the documentation index, contributing and the license.
- The README screenshots were replaced with captures of the current UI (`images/` for the Chinese UI, `images/en/` for English), and four stale screenshots were removed.
- Every document under `docs/` was checked against the code and the claims that had drifted were corrected, including the scope of `debug.mask_key` on-disk redaction, the absence of timing simulation in the replay transport, the two-cassette shape of local Responses recording, the environment and table coverage of the Postgres baseline script, exchange-column nullability, the embeddings endpoint being classified but not routable, and the provider-preset matrix.
- `ent/migrate/README.md` no longer describes SQLite as the default generation workflow; Postgres is the production and tracked default.
- `docs/protocol-reference/README.md` links the four dated upstream schema snapshots directly, and `docs/DEVELOPMENT.md` links the ent migration workflow, the Codex fixture profile and the pull request template.

## [2.0.0] - 2026-09-28

### Added

- `scripts/migrate-to-trajecta.sh`, a migration helper that runs as a dry run by default and rewrites `.env` keys, renames the local SQLite database together with its `-wal`/`-shm` siblings, reports the cassette magic version of each recording, and lists the items that need a manual decision: Postgres database and role names, Docker image and volumes, CI secrets, and browser localStorage.
- Session trajectory export as ATIF JSONL.

### Changed

- The docs tree was consolidated from 43 files into 13 current-fact Chinese documents plus `docs/protocol-reference/`: `IMPLEMENTATION_STATUS.md`, `PROTOCOLS_AND_PROVIDERS.md`, `ROUTING_AND_CREDENTIALS.md`, `RESPONSES_RUNTIME.md`, `OBSERVATION_AND_AUDIT.md`, `STORAGE_AND_DEPLOYMENT.md`, `DEVELOPMENT.md` and `POSTGRES_OPERATIONS.md` were added, `ARCHITECTURE.md`, `MONITOR_GUIDE.md`, `MCP_GUIDE.md` and `PROXY_USAGE_EXAMPLES.md` were rewritten against the current UI routes, MCP tools and proxy entrypoints, and every roadmap, phase, milestone and progress claim was dropped in favour of current code facts.
- The embedded Monitor UI bundle was rebuilt from the renamed sources.
- Documentation corrected against the code: `db migrate down` and `auth migrate down` exit with usage code 3, `AutoMigrate: false` applies to Postgres only, the exact `analyze backfill-exchanges` column set, the written `exchange_kind`/`exchange_role` value sets, and the stable channel id rule.
- YAML keys that no config field reads are now warned about at startup, and unknown `limits.scope` values plus a header scope without `limits.channel_key_header` are rejected at load time.

### Fixed

- Compact ATIF v1.8 session trajectories are reconstructed correctly.
- Upstream and model routing switches are authoritative.
- Configuration-consistency gaps: every `Store` view of one database shares one state block, nested `ConfigurationTransaction` calls are rejected instead of deadlocking on the configuration lock, an upstream write lock stops a background refresh from writing rows for a deleted target, the routing snapshot is published only after the database transaction commits, and `GET`/`DELETE /api/settings/channels` expose and clear the `channels.initialized` bootstrap marker.
- `ensureBootstrapUpstream` wrote the rejected `routing_profile: openai`, so `LLM_TRACELAB_BOOTSTRAP_UPSTREAM_BASE_URL` and its API key produced a config that failed to resolve; it now writes `openai_default`.
- Trace detail read `prompt_token_details` while the record tag is `prompt_tokens_details`, so cached-token details were always empty.
- A channel model capability PATCH now distinguishes an absent key from an explicit `null`, so "inherit" clears a pinned value again.

### Removed

- Stale code and dead configuration found during an audit: zero-reference exports and constants across `internal/*` and `pkg/*`, unwritten struct fields, the dead `POST /api/router/reload` route and handler, a zero-reference frontend component, dead `apiPaths`, unused i18n keys, and the removed `startup_policy` and `responses_server.enabled` keys from configs, examples and docs.
- 40 obsolete documentation files, including the v1 documentation tree, the design notes and roadmaps, duplicate baselines, `.gitattributes` and a one-off audit report; `.codex/config.toml`, which held an internal MCP address, was untracked and deleted.

### Breaking

- The project was renamed from llm-tracelab to Trajecta. Forwarding, recording, replay, the Responses runtime, Monitor and audit behaviour are unchanged; naming is the only breaking change.
- Environment variables now use the `TRAJECTA_*` prefix. The `LLM_TRACELAB_*` prefix is no longer read, so existing deployments must rename their variables.
- The Go module path changed from `github.com/kingfs/llm-tracelab` to `github.com/kingfs/Trajecta`; importers must update their import paths.
- Binary, Docker image and Compose identifiers are now `trajecta`, `kingfs/trajecta`, and the `trajecta` service and volume.
- Cassette writers now emit the prelude magic `# trajecta/v3`. Readers still accept the legacy `# llm-tracelab/v3` and the older `LLM_PROXY_V2` format, and `LLM_PROXY_V3` remains the deliberate, stable format version identifier. Cassettes are never rewritten by default.
- The local SQLite database default is now `trajecta.sqlite3`; a lone legacy `llm_tracelab.sqlite3` is reused in place instead of starting an empty database.
- Monitor localStorage keys moved to `trajecta.monitor.*`.

## [1.2.5] - 2026-09-14

### Added

- UI-managed gateway routing: model alias persistence, generic application-settings JSON helpers, Monitor routing configuration APIs, a gateway route-planning core, route-plan events recorded in cassettes and shown in trace detail, and a per-model routing simulator on the Monitor Routing page.
- Postgres runtime store support with a Postgres operations runbook, baseline SQL and migration checks, a baseline collection script, and a non-transactional index optimizer command.
- Session summaries with a store cache, a Postgres migration and operations controls, plus overview metric buckets and cursor pagination for system events.
- Support for MCP protocol version 2026-07-28.
- Model alias validation in Monitor and alias rewriting of upstream model names.

### Changed

- Native-vs-local Responses resolution is now per model: an explicit `channel_models.supports_responses`/`supports_chat_completions` value (or `upstream.model_capabilities` in YAML) overrides the channel-level `api_type` and capabilities, while an unset value keeps channel-level behaviour.
- The `responses_server.enabled` switch and the `LLM_TRACELAB_RESPONSES_ENABLED` variable were removed: the local Responses execution mode is always available, and the runtime is built lazily on first use so an optional provider configuration failure surfaces as a 502 on that request instead of blocking startup.
- The default Responses routing strategy is `auto` (native pass-through preferred, local translation as fallback) instead of always preferring the local Responses server; each strategy is described on the Routing page.
- Store query hot paths were optimized.

### Fixed

- Serve keeps booting when no local Responses chat-completions backend is configured; the preflight check is now a warning instead of a fatal error, so the management UI stays reachable.
- Monitor renders the per-model capability columns as tri-state inherit/supported/unsupported selects and sends `null` for "inherit", so saving an unrelated field no longer pins the columns to `true` and overrides channel-level capabilities.
- The Postgres model-aliases migration and the native Responses routing fallback were fixed, and a DeepSeek root base URL is accepted again.

## [1.2.4] - 2026-06-26

### Changed

- The Monitor distinguishes providers explicitly in its model and channel views.

### Fixed

- Route selection for "all" in the analyze refresh, and analyze parallelism.
- Model catalog routing visibility.

## [1.2.3] - 2026-06-25

### Fixed

- Migration errors.
- The embedded Postgres auth migration is skipped on `serve`, so serving no longer runs it accidentally.
- A legacy configuration that lacks an LLM API key is accepted again.

## [1.2.2] - 2026-06-25

### Added

- Model detail metadata endpoints.

### Changed

- Monitor authentication uses JWT.
- The experimental PP/TG performance-rate columns, the aggregate rate hardening and the tiny-window suppression were reverted before release, so the request list keeps its previous behaviour.

## [1.2.1] - 2026-06-25

### Added

- Batch analysis as a CLI command, with a simplified analysis refresh workflow.
- Client traces are separated from internal model exchanges; upstream model calls are shown in Monitor and Responses server entries link to the model exchange they produced.

### Fixed

- Trace detail refresh and duplicated stream output.
- Monitor detail layout and performance tab; detail pages are full width and the event inbox and trace readability were improved.
- Observation parsing handles SSE responses, empty responses, non-LLM HTTP error responses and interrupted streams.
- `all` is treated as a wildcard for system event filters.
- Postgres log exchange column migration, and Codex session headers are preserved by the Responses server.

## [1.2.0] - 2026-06-24

### Added

- Exchange metadata: a storage foundation, recording of entry and model exchanges, classification of Responses model exchange roles, an audit exchange query model, exposure through Monitor and MCP, an exchange metadata backfill command and exchange-aware observation parsing scoped by exchange kind.
- Hosted tools for Codex: a hosted tool contract package, a mock MCP hosted executor, MCP hosted tool configuration and diagnostics, streamable HTTP MCP executor support, web search routed through the hosted registry, and Codex hosted tool request normalization with compatibility injection.
- A Codex model profile editor in Monitor and enriched `models codex-profile` output.
- Monitor i18n preferences and an MCP descriptor streaming path.

### Fixed

- Responses server upstream time-to-first-token is recorded.
- Trace-derived model channel management.
- Monitor login and search Compose stability, monitor provider UI and observation UTF-8 handling.

### Removed

- Obsolete Responses runtime helpers.

## [1.1.0] - 2026-06-23

### Added

- A local Responses runtime: semantic runtime skeleton, HTTP API handler and chat-completions client, incremental and deferred SSE streaming with a rich stream lifecycle, threshold-based auto compaction with compact v2 provenance metadata, and model-profile token budgeting.
- Server-side function executors: a registry, streamed function call arguments, external command execution with process isolation and sandbox constraints, safe executor configuration snapshots with persistent hot reload, and a hosted web search tool loop.
- Responses audit: audit schema, query service, CLI, Monitor API and trace views, tool call audit persistence with lifecycle queries, rejected hosted tool choices, and correlation of Responses upstream exchanges.
- Provider setup tooling: probe CLI, reports and batch apply (CLI and Monitor UI), a probe suggestion library, provider profile adoption diagnostics and dry-run reports, provider detection before Monitor creation, an opt-in startup probe fill, provider tokenize counters with auto-selection, and the upstream API surface in channel configuration.
- Doctor diagnostics: a provider probe, model catalog drift, Codex local-config drift, Responses store health, `config inspect` with a source summary, and application/auth migration status reporting.
- Postgres store support: versioned SQL migrations for the app and auth namespaces with status, rollback and generation paths, runtime store tables, an application store open separated from migrations, and raw SQL compatibility auditing.
- Docker Compose packaging for a Postgres-first gateway.

### Changed

- Upstream capabilities are centralized and enforced for chat routing, and the tool-calling capability is respected when routing.
- The Responses runtime applies the profile's upstream model, and rejected hosted tool audits are persisted.

### Fixed

- Unsupported hosted response tools are rejected instead of being forwarded.
- The Responses gateway Compose smoke path was hardened, the default gateway config is inspectable, manual provider setup no longer requires validation; a missing or wrong Responses chat backend fails startup validation while `responses_server.enabled` is on.
- Docker build proxy environment handling was normalized.

## [1.0.8] - 2026-06-16

### Fixed

- Tokenization requests are routed by model.
- vLLM tokenization is routed to the provider's root endpoints.

## [1.0.7] - 2026-06-16

### Added

- vLLM tokenizer endpoints are proxied.
- Aggregated model metadata is enriched.

### Fixed

- Tokenizer upstream path rewriting.

## [1.0.6] - 2026-06-12

### Changed

- Monitor UI and backend enhancements.

## [1.0.5] - 2026-06-05

### Added

- Provider entrypoints and build metadata.
- A synthetic Anthropic count-tokens fallback.
- Recognition of Claude Code sessions.
- Restructured current documentation and a current protocol reference.

### Fixed

- Router protocol family path matching.

## [1.0.4] - 2026-06-01

### Added

- Credential-aware routing: upstream credential config projection, route targets expanded by credential, routing reads grouped by credential fields, credential routing event fields, and Monitor display of credential routing.
- Scoped local limit keys with rejection events, and in-memory sticky routing with an MCP drilldown.
- Routing decision timeline recording, exposure through MCP and trace detail, Monitor routing aggregation, and failure clustering by routing event.

### Fixed

- Routing candidate URLs are redacted.
- Fallback routing selection is deterministic.
- Merged credential helper dead code was removed.

## [1.0.3] - 2026-05-19

### Added

- Unparsed traces are surfaced in Monitor, and traces can be filtered by observation status in Monitor and MCP.
- Documentation of the unparsed trace reanalysis workflow.

## [1.0.2] - 2026-05-19

### Added

- Model-scoped upstream health, upstream retry jitter and observability, and health checks tied to router recovery.
- Retry queue saturation is exposed in failure classification.

### Fixed

- The upstream retry wait queue is bounded, and upstream retry, refresh and probe concurrency and resilience were improved.

## [1.0.1] - 2026-05-15

### Added

- Reanalysis: a job foundation, batch reanalysis jobs, Monitor controls and API, usage repair from recorded cassettes, and workflow documentation.
- A system events centre: store, emission from failures, Monitor API and UI with streaming updates, and MCP tools.

### Fixed

- Usage is parsed from long stream events.
- Model event listing.
- Semantic nodes are deduplicated before persistence.

## [1.0.0] - 2026-05-14

### Added

- An observation pipeline: Observation IR, OpenAI, Claude and Gemini parsers, OpenAI streaming observation parsing, persisted trace observations with an async parser worker, a protocol observation API, and audit findings exposed through the API.
- A Monitor redesign: new shell navigation, redrawn overview, trace, session, audit and analysis pages, an overview API with p95 latency and observation health, breakdown drilldowns, a performance view and session analysis runs, plus an account menu and theme switcher.
- Channel management: configuration storage, a channel model probe service, Monitor APIs with a web editing UI (header keep semantics, modal edit form, advanced provider options), manual model management, batch model toggles, local secret key storage with rotation and backup, routing decision filters, and channel and model catalog analytics APIs.
- An auth token inventory page with list and revoke APIs, and encryption of local channel secrets.
- Quality gates: embedded-UI smoke tests, a browser smoke suite, a full quality gate and legacy V2 replay coverage.

### Changed

- Serve uses the channel store as the router configuration source and reloads the router when channels change.

### Fixed

- Non-stream provider errors are parsed correctly.
- Serve background workers are awaited, model marketplace analytics were refined, and token form overlap was fixed.
- The full quality gate passes.

## [0.10.0] - 2026-05-12

### Added

- Throughput and cache-hit rate computation.
- AI-native CLI contracts.

### Changed

- Monitor latency and session failure display.

### Fixed

- Model routing selection errors.

## [0.9.1] - 2026-04-28

### Changed

- The server CLI was split into separate command files and aligned with Cobra/Viper, using Cobra flags.
- golangci-lint checks are enforced.

### Removed

- Unused MCP server code.

## [0.9.0] - 2026-04-27

### Added

- A user-backed auth control plane, with user tokens used for all service authentication.

### Changed

- The structured database schema was unified and the store read and write paths were migrated to ent.
- Docker runtime storage configuration, local configuration for the multi-upstream runtime, and service defaults were aligned; legacy trace indexes are adopted during database migrations, relative SQLite database paths are handled, and monitor list page display was accelerated.

### Fixed

- Auth token and SQLite DSN handling were hardened.
- Duplicate trace detail tabs were removed.

## [0.8.0] - 2026-04-27

### Added

- A read-only MCP server over streamable HTTP with auth tokens and a narrowed tool surface: replay tools, dataset curation, persisted experiment runs with baselines, scores and comparison, versioned evaluator profiles, deterministic budget evaluators, a tool-call conformance evaluator, summaries and explanations, dataset creation from regressions, and trace failure clustering.
- Model list aggregation and routing across upstreams, and token access control.
- Core performance benchmarks and a development command matrix.

### Changed

- Monitor was split into dedicated routes with shared components and primitives, unified empty states and a unified visual hierarchy.
- Recorder and replay performance: prelude parse allocations reduced, replay cassette response offsets cached, and the request body reused across the proxy pipeline.
- SQLite store configuration was hardened, Docker build environment handling was unified, and session grouping is persisted and backfilled for stale rows.

### Fixed

- Monitor trace list spacing and tool definitions, and acceptance review findings.

## [0.7.0] - 2026-04-17

### Added

- Multi-upstream routing with cost-aware upstream selection, upstream monitoring and refresh, filtered upstream analytics views and drilldowns.
- Routing diagnostics: routing failure analytics, timeline and trend detail, upstream failure reason classification, recording of routing selection failures, per-trace routing decision explanations, and upstream health signals, thresholds and detail drilldowns.

### Fixed

- `allow_static` fallback routing is covered by tests.

## [0.6.0] - 2026-04-17

### Added

- Provider support: Anthropic upstream protocol support, Google GenAI upstream and stream support, and a Vertex native upstream with a `generateContent` adapter and a controlled preset.
- An expanded upstream preset registry with routing profiles, preset validation and end-to-end coverage, plus cassette-backed model listing.
- Startup upstream routing diagnostics.
- Session-aware Monitor views: a session timeline overview, failure summaries and context, deep links between trace tabs, sessions and focused timeline/response panels, and Monitor list filters.

### Changed

- The cassette fixture catalog was refactored and expanded with history, block, refusal, error, safety, stream-error, partial-completion, multi-turn and tool-result matrices.

### Fixed

- Responses non-stream and streamed refusal semantics, and preservation of custom tool call arguments.
- Google finish reason semantics and the Vertex connectivity diagnostics path.
- Legacy store schema upgrades for session columns, and OpenAI-compatible `base_url` prefixes are required.

## [0.5.0] - 2026-04-01

### Changed

- The front end was refactored, with reworked timeline and summary display.

### Fixed

- A build error and front-end display issues.
- Unnecessary environment variables were removed.

## [0.4.0] - 2026-03-30

### Fixed

- Display issues, including OpenClaw messages that could not be rendered.

## [0.3.0] - 2026-03-27

### Added

- `/v1/responses` support.
- Claude API content display, including cached tokens, and proxy support.

### Changed

- Tooling and the default configuration and ignore rules were adjusted.

### Fixed

- Claude cached-token accounting errors.
- Ordering/timing issues, statistics errors and unfriendly configuration.

## [0.2.0] - 2026-01-30

### Added

- Message format conversion between three providers, including mutual conversion.
- Reduced memory copying.
- A run screenshot in the documentation.

### Fixed

- Test loading.

## [0.1.0] - 2026-01-01

### Added

- The initial project commit.
- A GitHub Actions workflow and a Docker image build script.

### Changed

- The project was named llm-tracelab.

[Unreleased]: https://github.com/kingfs/Trajecta/compare/v2.0.1...HEAD
[2.0.1]: https://github.com/kingfs/Trajecta/compare/v2.0.0...v2.0.1
[2.0.0]: https://github.com/kingfs/Trajecta/compare/v1.2.5...v2.0.0
[1.2.5]: https://github.com/kingfs/Trajecta/compare/v1.2.4...v1.2.5
[1.2.4]: https://github.com/kingfs/Trajecta/compare/v1.2.3...v1.2.4
[1.2.3]: https://github.com/kingfs/Trajecta/compare/v1.2.2...v1.2.3
[1.2.2]: https://github.com/kingfs/Trajecta/compare/v1.2.1...v1.2.2
[1.2.1]: https://github.com/kingfs/Trajecta/compare/v1.2.0...v1.2.1
[1.2.0]: https://github.com/kingfs/Trajecta/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/kingfs/Trajecta/compare/v1.0.8...v1.1.0
[1.0.8]: https://github.com/kingfs/Trajecta/compare/v1.0.7...v1.0.8
[1.0.7]: https://github.com/kingfs/Trajecta/compare/v1.0.6...v1.0.7
[1.0.6]: https://github.com/kingfs/Trajecta/compare/v1.0.5...v1.0.6
[1.0.5]: https://github.com/kingfs/Trajecta/compare/v1.0.4...v1.0.5
[1.0.4]: https://github.com/kingfs/Trajecta/compare/v1.0.3...v1.0.4
[1.0.3]: https://github.com/kingfs/Trajecta/compare/v1.0.2...v1.0.3
[1.0.2]: https://github.com/kingfs/Trajecta/compare/v1.0.1...v1.0.2
[1.0.1]: https://github.com/kingfs/Trajecta/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/kingfs/Trajecta/compare/v0.10.0...v1.0.0
[0.10.0]: https://github.com/kingfs/Trajecta/compare/v0.9.1...v0.10.0
[0.9.1]: https://github.com/kingfs/Trajecta/compare/v0.9.0...v0.9.1
[0.9.0]: https://github.com/kingfs/Trajecta/compare/v0.8.0...v0.9.0
[0.8.0]: https://github.com/kingfs/Trajecta/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/kingfs/Trajecta/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/kingfs/Trajecta/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/kingfs/Trajecta/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/kingfs/Trajecta/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/kingfs/Trajecta/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/kingfs/Trajecta/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/kingfs/Trajecta/releases/tag/v0.1.0
