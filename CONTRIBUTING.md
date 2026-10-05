# Contributing to Trajecta

Thanks for taking the time to contribute. This document covers how to set up the project, what to run before opening a pull request, and the rules that are not negotiable.

## Before you start

- **Bug reports and feature requests**: use the issue templates. Include the version/commit, your config (redact secrets), and reproduction steps.
- **Security issues**: do not open a public issue. See [SECURITY.md](./SECURITY.md).
- **Larger changes**: open an issue first so the approach can be agreed on before you write code.

## Development setup

| Requirement | Notes |
| --- | --- |
| Go 1.25+ | Version pinned in `go.mod` |
| [Task](https://taskfile.dev) | Optional; every task has a plain `go`/`bun` equivalent |
| Bun + Node | Only for the monitor UI in `web/monitor-ui` |
| Postgres | Optional. `task run` uses the Postgres-first `config/config.yaml`; `CONFIG=config/examples/local-sqlite.yaml task run` needs no Postgres at all |

```bash
task build          # backend + embedded UI
task run            # start with config/config.yaml
task migrate:db:up  # apply Postgres migrations (only when database.auto_migrate is off)
```

## Commands

| Command | What it does |
| --- | --- |
| `task fmt` / `task fmt:check` | Format, or check formatting without editing |
| `task lint` | `golangci-lint run ./...` |
| `task lint:vet` | `go vet ./...` |
| `task test` | `go test ./...` |
| `task test:short` | `go test -short ./...` |
| `task test:race` | `go test -race ./...` |
| `task test:e2e` | End-to-end tests |
| `task test:atif` | ATIF v1.8 session export plus the offline validator |
| `task atif:validate` | Validate ATIF JSONL with the pinned models (install `scripts/atif-requirements.txt` first) |
| `task bench:core` | Core benchmarks |
| `task build` / `task build:go` | Build everything, or the backend only |
| `task ui:build` | Install UI deps and rebuild `internal/monitor/ui/dist` |
| `task ui:test` / `task ui:test:real` | Playwright UI tests against mocks, or against a real fixture server |
| `task check:quick` | `fmt:check` + `lint` + `test:short` |
| `task check:full` | `check:quick` + `test` + `test:e2e` + `test:race` + `build:all` |

`docs/DEVELOPMENT.md` (Chinese) has the full command matrix and explains what each verification level covers.

## What to run before opening a pull request

- **Always**: `task check:quick`. Its formatting check covers the same directories as CI (`./cmd`, `./internal`, `./pkg`, `./unittest`, `./web/monitor-ui/test-fixtures`), so a green local run also passes the CI formatting step.
- **If you touched storage, the record format, the proxy, the Responses runtime, routing, or the UI**: `task check:full`.
- **If you changed the monitor UI**: run `task ui:build`. The bundle in `internal/monitor/ui/dist` is embedded with `go:embed`, so the rebuilt files belong in the same commit as the source change.

CI runs formatting checks, lint, `go test -v ./...`, the ATIF validation pipeline, and the Playwright UI suites. A pull request that fails CI will not be reviewed until it is green.

## Engineering rules

These are enforced by review, and several of them are asserted by tests:

- **Preserve replay compatibility.** `pkg/replay` is a hard requirement: an existing `.http` cassette must keep replaying.
- **Tests must not depend on network access.** Record a cassette instead of calling a real provider.
- **Record format changes start in `pkg/recordfile`.** Writers emit V3 only. Readers must keep supporting legacy `LLM_PROXY_V2` files (fixed 2KB JSON header block) and the pre-rename prelude magic `# llm-tracelab/v3`. `LLM_PROXY_V3` stays the stable format identifier and is deliberately not renamed.
- **Keep cassettes human-inspectable, and prefer additive evolution** over rewriting files that already exist on disk.
- **Do not commit real traffic or secrets.** `logs/`, `data/` and `docker-data/` are gitignored; keep it that way, and never paste a real API key into a config, test, or doc example.
- **Raw `.http` cassettes remain the source of truth for replay and detail views.** The application database is a derived index for trace lists and aggregates, and the authoritative store for channels, credentials, routing and Responses state.

## Documentation

- `docs/` is written in Chinese and describes **current code facts only**. Plans, roadmaps, phase designs and archives are not kept in this repository. If a doc and the code disagree, the code wins and the doc gets fixed in the same change.
- The only English files under `docs/` are the dated upstream schema snapshots in `docs/protocol-reference/upstream/*/schema-index-*.md`.
- `README.md` (Chinese) and `README_EN.md` (English) must stay in sync; a change to one belongs in the other.
- README screenshots are generated from the real UI fixture server, not hand-captured:

  ```bash
  cd web/monitor-ui && bunx playwright install chromium   # once, if you have not run the UI suites
  task ui:screenshots
  ```

  That writes `images/*.png` (Chinese UI) and `images/en/*.png` (English UI). Update `web/monitor-ui/tools/screenshots/readme.capture.js` when a page it captures changes shape.
- [AGENTS.md](./AGENTS.md) holds the AI-oriented project map and invariants. Keep it accurate when you move packages or change a documented rule.

## Commit and pull request conventions

- Commit subjects follow [Conventional Commits](https://www.conventionalcommits.org/): `feat:`, `fix:`, `docs:`, `refactor:`, `perf:`, `test:`, `chore:`. Use `!` and a `BREAKING CHANGE:` footer for incompatible changes.
- Keep a pull request to one logical change; fill in the [pull request template](./.github/PULL_REQUEST_TEMPLATE.md), including how you verified it.
- Reference the issue it closes.

## License

By contributing you agree that your contributions are licensed under the [MIT License](./LICENSE).
