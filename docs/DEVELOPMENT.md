# 开发与测试

本文是 Trajecta 的开发、验证、构建与集成测试入口。所有命令以仓库根目录的 `Taskfile.yml`、`.github/workflows/ci.yml`、`cmd/server/**`、`cmd/trajecta/**` 与 `internal/cli/**` 为准；本文只记录当前实际存在、可执行的命令。

相关阅读：[文档入口](./README.md)、[架构说明](./ARCHITECTURE.md)、[协议参考](./protocol-reference/README.md)、[Monitor 使用指南](./MONITOR_GUIDE.md)、[MCP 使用指南](./MCP_GUIDE.md)、[代理使用示例](./PROXY_USAGE_EXAMPLES.md)。

## 命令入口（Taskfile 与直接命令的对应关系）

`task` 是稳定入口，避免手写 Go / 前端命令。常用 task 与底层命令的对应关系：

| task | 实际执行 |
| --- | --- |
| `task fmt` | `gofmt -w ./cmd ./internal ./pkg ./unittest ./tests ./web/monitor-ui/test-fixtures` |
| `task fmt:check` | `gofmt -l` 检查同样的目录，不改文件，有输出即失败 |
| `task lint` | `golangci-lint run ./...` |
| `task lint:vet` | `go vet ./...` |
| `task test:short` | `go test -short ./...`；`-short` 会跳过所有 Postgres 门控集成用例（`TRAJECTA_TEST_POSTGRES_DSN` 已设置时同样跳过），用于不接数据库的快速验证 |
| `task test` | `go test ./...` |
| `task test:race` | `go test -race ./...` |
| `task test:cover` | `go test -coverprofile=coverage.out ./...` |
| `task test:e2e` | `go test ./internal/proxy ./internal/monitor ./cmd/server ./cmd/trajecta ./unittest` |
| `task atif:validate` | `python3 scripts/validate_atif.py {{.CLI_ARGS}}`（需先安装 `scripts/atif-requirements.txt`） |
| `task test:atif` | `go test ./internal/trajectory ./internal/monitor` + `python3 -m unittest discover -s scripts/atif_tests` |
| `task test:codex-fixtures` | `env -u GOROOT go test ./internal/responses/httpapi ./internal/responses/runtime -count=1` |
| `task bench` | `go test -bench=. -benchmem ./...` |
| `task bench:core` | `go test -bench=. -benchmem ./internal/proxy ./internal/router ./internal/store ./internal/gateway ./pkg/llm ./pkg/recordfile ./pkg/replay ./pkg/secretmask ./pkg/session ./pkg/xir` |
| `task bench:secretmask` | `go test -count=1 -run '^$' -bench=. -benchmem ./pkg/secretmask` |
| `task bench:gate` | `go test -run 'AllocGate' ./pkg/... ./internal/gateway ./internal/router -count=1` |
| `task bench:smoke` | `go test -run '^$' -bench . -benchtime 1x ./pkg/xir ./pkg/session ./pkg/recordfile ./pkg/secretmask ./internal/gateway ./internal/router` |
| `task deps:verify` | `go mod verify` + `go list -mod=readonly ./...` |
| `task deps:tidy` | `go mod tidy` |
| `task build:go` | `go build -trimpath -ldflags=... -o server ./cmd/server` 以及 `-o trajecta ./cmd/trajecta` |
| `task build:all` | `task ui:build` 然后 `task build:go`（`task build`、`default` 同此） |
| `task run` | `go run ./cmd/server -c {{.CONFIG}}`，不带子命令即启动 serve |
| `task ui:build` | 在 `web/monitor-ui` 下 `bun install --frozen-lockfile` + `bun run build` |
| `task ui:test` | 在 `web/monitor-ui` 下依次跑 `bun run typecheck`（`tsc --noEmit`）、`test:unit`（`node --test`）与 `test:ui`（Playwright 浏览器测试） |
| `task ui:test:real` | 在 `web/monitor-ui` 下 `bun install --frozen-lockfile` + `bun run test:ui:real` |
| `task ui:screenshots` | 在 `web/monitor-ui` 下 `bunx playwright test --config playwright.screenshots.config.js`，重新生成 README 截图 |
| `task migrate` | `go run ./cmd/trajecta migrate -c {{.CONFIG}}`（V2 cassette 重写为 V3 并重建索引） |
| `task migrate:db:up` | `go run ./cmd/trajecta db migrate up -c {{.CONFIG}}` |
| `task migrate:legacy` | `go run ./cmd/trajecta upgrade -c {{.CONFIG}} {{.ARGS}}`（默认 dry-run，`ARGS="--apply"` 才写盘） |
| `task auth:init-user` | `go run ./cmd/trajecta auth init-user -c {{.CONFIG}} --username "$USER" --password "$PASSWORD"` |
| `task auth:create-token` | `go run ./cmd/trajecta auth create-token -c {{.CONFIG}} --username "$USER" --name "$NAME"` |
| `task generate:ent` | `go generate ./ent/...` |
| `task generate:secretmask` | `go generate ./pkg/secretmask/...`，即 `ragel-go -G2 -o rules_ragel.go rules.rl` + `gofmt`；需要 `ragel-go` 二进制 |
| `task migrate:ent:sqlite NAME=x` | `ent/migrate/main.go --dialect sqlite --dir ent/migrations` + `update_hash.go`；Postgres 版为 `migrate:ent:postgres NAME=x DEV_URL=...`，写 `ent/postgres-migrations`；两个方向的完整生成流程与提交要求见 [Ent Migration Workflow](../ent/migrate/README.md) |
| `task docker:build` / `docker:up` | `scripts/docker-build-env.sh` + `docker build -t trajecta:local .`；`docker compose -f docker-compose.yml -f docker-compose.dev.yml up --build -d` |
| `task clean` | `rm -f {{.SERVER_BIN}} {{.CLI_BIN}}`，即删除 `server` 与 `trajecta` 两个产物 |

`CONFIG` 默认是 `config/config.yaml`，可用 `CONFIG=path/to/config.yaml task <task>` 覆盖，所有读取配置的 task 都支持。

## 日常开发流程

```bash
task fmt
task check:quick
task build:go
task run
```

- `task fmt`：格式化 Go 代码后再提交。
- `task check:quick`：格式检查 + lint + 短测试，不改写任何文件，是提交前的最小质量门。
- `task build:go`：只重编译后端，适合纯后端改动。
- `task run`：用 `CONFIG` 指定的配置启动代理与 Monitor。

`config/config.yaml` 是 Postgres-first 配置且 `database.dsn` 为空，直接 `task run` 前需要导出 `TRAJECTA_DATABASE_DSN`，或改用 `CONFIG=config/examples/local-sqlite.yaml`。其它示例配置见 `config/examples/`（openai、anthropic、google_genai、vertex、azure_openai、openai-compatible-vllm-postgres）。

## 验证等级（quick / full / race / bench 各自跑什么）

- `task check:quick` = `fmt:check` + `lint` + `test:short`：小范围代码改动的默认验证；`task check` 与它完全相同。`test:short` 的 `-short` 只影响 Postgres 门控集成用例，其余单测与全量一致。
- `task check:full` = `check:quick` + `test` + `test:e2e` + `test:race` + `build:all`：发布前或大范围改动。
- `task test:race`：全套单元测试加 Go race detector；代理、路由、录制、存储或 streaming 改动必须补跑。
- `task test:e2e`：本地端到端测试，覆盖 `internal/proxy`、`internal/monitor`、`cmd/server`、`cmd/trajecta`、`unittest`，不依赖外部网络。
- `task test:cover`：写 `coverage.out`。
- `task bench`：全部 benchmark；`task bench:core` 只跑热路径包（`internal/proxy`、`internal/router`、`internal/store`、`internal/gateway`、`pkg/llm`、`pkg/recordfile`、`pkg/replay`、`pkg/secretmask`、`pkg/session`、`pkg/xir`）。`task bench:secretmask` 只跑检测、遮蔽与还原，覆盖 4 KiB–512 KiB 请求、凭据密度、流式分片与一次完整往返，用来量化代理开启 `secret_masking` 后每个请求增加的耗时。`task bench:gate` 与 `task bench:smoke` 是分配门禁与基准冒烟，见下文[性能与分配门禁](#性能与分配门禁)。
- `task lint:vet`：单独跑 `go vet ./...`，与 golangci-lint 互补。
- `task test:codex-fixtures`：离线 Codex Responses fixture runner；fixture 清单、覆盖范围与 runner 契约见 [Codex Responses fixtures](../tests/fixtures/codex/README.md)。

所有测试都不应依赖真实 provider 网络或真实 API key。

### 出网护栏（`internal/testnet`）

生产代码有多处 `if client == nil { client = http.DefaultClient }` 兜底（Responses HTTP client、Responses chat client、tokenize counter、MCP tool executor）。为防止「忘记注入 HTTP client 的测试真的发起外网请求」，这些包以及 `unittest` 用 `TestMain` 调 `testnet.InstallLoopbackGuard()`：它把 `http.DefaultTransport` 换成拒绝非 loopback 目标的 `testnet.GuardTransport`，命中时立刻返回 `testnet: outbound network access to "<host>" ... is blocked in tests`，而不是产生费用、状态污染或数分钟挂起。

- 需要真实出网的少数套件（`tests/live`）有自己的 `TestMain` 范围之外，未安装该护栏；若某个默认套件确实要出网，用 `TRAJECTA_TEST_ALLOW_NETWORK=1 go test ...` 显式放开，不要把它写进默认测试环境。
- 断言该护栏自身行为的用例在 `internal/testnet/guard_test.go`。
- 新增「生产代码默认走 `http.DefaultClient`」的包时，应同时补上这个 `TestMain`。

## 性能与分配门禁

热路径函数的质量门禁用**分配数（allocs/op）**而不是时间：allocs/op 跨机器稳定，时间受硬件与负载影响，只做参考、不做硬门禁。

**机制**：

- 分配门禁是普通 Go 测试，命名为 `TestAllocGate*`，用 `testing.AllocsPerRun` 实测每次调用的堆分配次数，超过上限即失败。它们随 CI 的 `go test ./...` 自动执行，不需要额外步骤。
- 每个门禁的注释写明：被测函数、基线测量值（测量机器与日期）、上限（基线 + 约 25% 余量取整）。零分配路径（如 `secretmask` prefilter 早退）上限为 0，不留余量。
- 每个门禁配套同名基准（`Benchmark*`），fixture 与门禁完全一致，便于复测基线。
- 已知低效点也先锁住现状防恶化：如 `internal/router` 的 `extractRequestFeatures` 目前 `ParseEnvelope` 两遍，门禁注释标注了这一点，P4 修复后应下调上限。
- `-race` 构建下门禁自动跳过（`allocgate_race_test.go`/`allocgate_plain_test.go` 的构建标签常量 `allocGateInstrumented`）：race 插桩本身增加运行时分配，上限只在无插桩时有意义；`task test:race` 因此不会误报，常规 CI 测试步仍强制执行门禁。

**当前门禁覆盖**（文件均为 `*_gate_test.go`）：

| 包 | 被测函数 |
| --- | --- |
| `pkg/xir` | `ParseEnvelope`（典型 chat body，含 tools/stream_options） |
| `internal/router` | `extractRequestFeatures`（含双解析现状） |
| `internal/gateway` | `InjectStreamOptions` / `xir.EnsureStreamIncludeUsage`（注入与已含两条路径） |
| `pkg/secretmask` | `Detector.Scan`（清洁 body 零分配早退 / 含凭据 body） |
| `pkg/recordfile` | `ParsePrelude`（V3 与 V2 各一） |
| `pkg/session` | `Resolve`（头快路径 / body 回退） |

**如何本地跑**：

```bash
task bench:gate    # 只跑全部分配门禁（go test -run 'AllocGate' ./pkg/... ./internal/gateway ./internal/router -count=1）
task bench:smoke   # 热路径基准各跑一次（-benchtime 1x），保证基准可编译可运行
task bench:core    # 热路径包完整基准（含 -benchmem）
```

CI 在 `go test -v ./...` 之后另有一步 benchmark smoke（`-benchtime 1x`），保证基准不腐烂；AllocGate 已随 `go test ./...` 执行，不重复跑。

**如何更新上限**：

1. 先复测基线：`go test ./<包> -run '^$' -bench <对应基准> -benchtime=100x -benchmem -count=1`，记录 allocs/op 与机器/日期。
2. 上限 = 新基线 + 约 25% 余量向上取整；零分配路径保持 0。
3. 同步更新门禁注释里的基线、上限与测量日期。
4. **提高上限必须在 PR 说明理由**（为什么新增分配是必要的、考虑过哪些替代方案）；优化降低了分配时则应主动下调上限，而不是留着旧余量。

## 前端开发与 UI 测试

Monitor UI 位于 `web/monitor-ui`，使用 Bun 作为包管理器（`packageManager: bun@1.3.11`）。

- `task ui:build`：构建 UI 并写入 embed 目标目录。
- `cd web/monitor-ui && bun run build`：等价于 `vite build`，`vite.config.js` 把产物写到 `internal/monitor/ui/dist`；`task` 未包装。
- `cd web/monitor-ui && bun run dev`：启动 Vite dev server；Taskfile 中没有对应的 dev task，只有直接命令。
- `task ui:typecheck`：对 `src/` 下已迁移到 `.ts`/`.tsx` 的模块跑 `tsc --noEmit`。`tsconfig.json` 打开 `strict`，`checkJs` 保持关闭，因此只有显式改名的模块参与检查，未迁移的 `.jsx` 仍照常构建。
- `task ui:test:unit`：`node --test tests-unit/**/*.test.js`。Node 直接执行 `.ts` 模块（原生类型剥离），所以单元测试可以 import 前端源码；`tests-unit/color-math.js` 是这些测试共用的 OKLCH/sRGB 与 WCAG 对比度换算。
- `task ui:test`：依次跑 `tsc --noEmit`、单元测试与 mock 套件，类型错误会让该 target 失败。
- `task ui:screenshots`：用真实 fixture server 重新生成 README 引用的截图（`images/` 与 `images/en/`）。

Playwright 有两套套件：

- mock 套件：`bun run test:ui`（`playwright test`，配置 `playwright.config.js`），测试目录 `tests/`，`baseURL=http://127.0.0.1:4173`，由 `bun run build && bunx vite preview --host 127.0.0.1 --port 4173` 提供页面；`reuseExistingServer` 在非 CI 环境为真。项目为 `desktop`（Desktop Chrome）和 `mobile`（Pixel 7）。
- 真实服务套件：`bun run test:ui:real`（`playwright test --config playwright.real.config.js`），测试目录 `tests-real/`，`baseURL` 取自 `MONITOR_REAL_BASE_URL`（默认 `http://127.0.0.1:4183`），`webServer` 用 `MONITOR_REAL_ADDR=<host:port> go run ./test-fixtures/monitor_real_server.go` 启动本地 Go Monitor fixture 与 fake upstream；只有 `desktop-real` 一个项目，且 `reuseExistingServer` 为假。

Chromium 解析方式：两套配置都读取环境变量 `PLAYWRIGHT_CHROMIUM_EXECUTABLE`（`trim()` 后判断）。**只有**该变量非空时才把它设为 `launchOptions.executablePath`，否则不传该字段，使用 Playwright 自己托管的浏览器。CI 通过 `bunx playwright install --with-deps chromium` 安装托管 Chromium，本地无需设置该变量。

UI 或 embed 产物改动后建议执行：

```bash
task ui:build
task ui:test
task ui:test:real
go test ./internal/monitor
task build:go
```

`task ui:test` 覆盖类型检查、单元测试（含 `src/styles/tokens.css` 的 WCAG 对比度断言）与 mock 套件；改动样式、主题或共享组件后如果画面确实变化，用 `task ui:screenshots` 重新生成 README 截图，并在提交信息里说明。

## 构建产物（Go 二进制、UI dist 与 go:embed 的关系）

- Go 二进制：`task build:go` 在仓库根目录生成服务端 `server`（`./cmd/server`）与 CLI `trajecta`（`./cmd/trajecta`），都使用 `-trimpath`，并通过 ldflags 注入 `main.Version`、`main.Commit`、`main.Date`、`main.Branch`（分别来自 `git describe`、`git rev-parse --short=12 HEAD`、UTC 构建时间、当前分支）。两个 `package main` 各自声明这些变量，`task clean` 同时删除两个产物。命令分工：`server` 只保留 `serve`（以及 `version`、`completion`），其余不需要代理进程的运维命令组（`analyze`、`audit`、`auth`、`config`、`db`、`doctor`、`migrate`、`models`、`provider`、`schema`、`tools`）都挂在 `trajecta` 下，实现在 `internal/cli`；application database 的打开与迁移入口在 `internal/appdb`。
- UI 产物：`internal/monitor/ui/dist/`（`index.html` 与 `assets/`），由 `vite build` 生成，并已提交到仓库。
- 二者关系：`internal/monitor/server.go` 用 `//go:embed ui/dist/*` 把该目录编译进二进制，因此 `go build ./cmd/server` 读取的是编译时刻 `internal/monitor/ui/dist` 的内容。前端改动后必须先 `task ui:build`（或直接用 `task build` / `task build:all`，它们会先跑 `ui:build`）再编译，否则二进制里仍是旧 UI。
- 容器镜像：`task docker:build` 构建本地镜像 `trajecta:local`；`task docker:up` 用 compose 起本地栈。镜像内同时包含 `/app/bin/server`（entrypoint）与 `/app/bin/trajecta`（CLI），容器只有一个 entrypoint，用 `--entrypoint /app/bin/trajecta` 调用 CLI。

## 依赖管理

- Go：`task deps:verify` 只校验不改文件（`go mod verify` + `go list -mod=readonly ./...`）；只有依赖确实变化时才用 `task deps:tidy`（会改写 `go.mod` / `go.sum`）。`go.mod` 声明 `go 1.27.0`。Go 1.27 起标准库 `encoding/json`（v1 API）默认由 `encoding/json/v2` 实现支撑：v1 的 Marshal/Unmarshal 语义保持不变（错误消息文本可能略有差异），Unmarshal 明显更快；仓库全部 JSON 调用仍走 v1 API，不做 v2 默认行为迁移（v2 默认拒绝重复成员名/非法 UTF-8、nil slice 编码为 `[]`，与录制的字节保真约束冲突）。如需回退旧实现，用 `GOEXPERIMENT=nojsonv2` 构建。
- 前端：依赖锁定在 `web/monitor-ui/bun.lock`，task 与 CI 都使用 `bun install --frozen-lockfile`。
- ent 代码生成：`task generate:ent` 重新生成 `ent` 控制面 DAO；新增 SQLite / Postgres migration 分别用 `task migrate:ent:sqlite NAME=...` 和 `task migrate:ent:postgres NAME=... DEV_URL=...`。
- 凭据 DFA 代码生成：`task generate:secretmask` 从 `pkg/secretmask/rules.rl` 重新生成 `pkg/secretmask/rules_ragel.go`。**需要 `ragel-go` 二进制**：通用 `ragel` 对这台机器输出 C，两者不可互换。生成文件已提交，因此常规构建、测试与 `task check:full` 都不需要它；只有改规则时才要装，并须同时改 `rules.rl`（模式）与 `rule.go`（元数据），把生成文件一起提交，不要手工编辑生成文件。

## 数据库与认证初始化

首次启动或重置本地应用库：

```bash
task migrate:db:up
task auth:init-user USER=admin PASSWORD=<password>
task auth:create-token USER=admin NAME=local
```

- `task migrate:db:up`：对 `CONFIG` 指向的应用库执行 `db migrate up`。
- `task auth:init-user`：用 `USER`、`PASSWORD` 创建登录用户（底层 `auth init-user --username --password`）。
- `task auth:create-token`：为 `USER` 创建名为 `NAME` 的 API token（底层 `auth create-token --username --name`，还支持 `--scope`、`--ttl`）。
- Postgres 使用 `ent/postgres-migrations` 的版本化 SQL migration；在 `database.auto_migrate: false` 的生产路径下必须先执行 `db migrate up`，否则启动会报出缺失 migration / table 的错误。Postgres 下 auth 表由应用 migration 集合一并创建。
- 驱动默认是 Postgres（未配置 `database.driver` 时不会隐式落到 SQLite）。SQLite 需显式配置，用于本地 / 开发 / 测试，schema 在启动时应用而非版本化迁移，默认文件为 `{{output_dir}}/trajecta.sqlite3`。

本地开发只需要上面的 `task migrate:db:up` / `task auth:init-user` / `task auth:create-token`。Postgres 迁移状态核对、并发索引优化、exchange backfill、`session_summaries` 灰度读开关（`database.use_session_summary_read` / `TRAJECTA_DATABASE_USE_SESSION_SUMMARY_READ`）与基线采集脚本等只读运维入口见 [PostgreSQL 运维](./POSTGRES_OPERATIONS.md)；迁移命令归属、SQLite/Postgres schema 策略与派生数据重算入口见 [存储与部署](./STORAGE_AND_DEPLOYMENT.md)。

## 本地密钥命令

渠道密钥的本地加密 key 通过 `db secret` 子命令管理：

```bash
trajecta -c config/config.yaml db secret status
trajecta -c config/config.yaml db secret export --out trace_index.secret.backup
trajecta -c config/config.yaml db secret rotate --yes
trajecta -c config/config.yaml --format json db secret status
```

- `status` 只输出路径、可读性与 fingerprint，不打印密钥；`export --out` 以 `0600` 权限写备份文件，不带 `--out` 时把 key 写到 stdout。
- `rotate --yes` 缺少 `--yes` 会拒绝执行；确认后备份旧 key、写入新 key，并重加密渠道 API key 与敏感 header。

## 集成测试分层

| 层 | 资产 | 触发 | 依赖 |
| --- | --- | --- | --- |
| 离线单元/e2e | `go test ./...`（72 包，含 proxy 全链路 e2e、分配门禁、parity 常驻断言） | 每次 CI / 本地 | 无网络、无外部服务 |
| Postgres 集成 | `TRAJECTA_TEST_POSTGRES_DSN` 门控测试（版本化迁移与幂等、read pool/语句超时、Overview GROUPING SETS 奇偶、rpm/tpm 读模型、secret-mapping 管理、Responses runtime store、SQLite→Postgres 合并与 layout apply 的 PG 路径） | CI `postgres-integration` job（postgres:17-alpine 服务容器，`-p 1` 串行——多个套件对同一 DSN 应用迁移） | 一次性 PG 库；本地导出 DSN 即可跑 |
| 转换接线级 e2e | `internal/proxy/protocol_conversion_matrix_e2e_test.go`：9 个跨协议转换对 + 同协议直通对照 + native 优先 + 流式拒绝 + responses 入口不转换（stub 上游，离线） | 随 `go test ./...` | 无 |
| 转换引擎级 parity | `pkg/xir/convert/parity_test.go`（672 cell，见 [协议转换](./PROTOCOL_CONVERSION.md)） | 随 `go test ./...` | 无 |
| 浏览器 mock/real | `task ui:test` / `ui:test:real` | 每次 CI | Chromium |
| Live 黑盒（真实上游） | `tests/live`（录制→回放保真、Monitor/MCP、故障切换/断供契约） | `.github/workflows/live-smoke.yml`：手动 dispatch + 每晚 03:00（Asia/Shanghai）；**未配置密钥时优雅跳过** | 仓库 secrets：`TRAJECTA_LIVE_API_KEY`、`TRAJECTA_LIVE_CHAT_BASE`、`TRAJECTA_LIVE_MESSAGES_BASE`；可选 variable `TRAJECTA_LIVE_MODEL`。花费真实 token；cassette 含原始请求体，**不上传任何工作区 artifact** |

**升级演练（llm-tracelab → trajecta）**：Go 级路径已由 PG 集成 job 覆盖（`internal/legacymigrate` 的合并/布局 PG 测试 + `internal/appdbmigrate` 迁移链）。整栈演练按 [从 llm-tracelab 迁移](./LEGACY_MIGRATION.md) 手工执行：起 `tests/live/run-live.sh` 栈或生产 compose → 用旧品牌 `.env`/SQLite/cassette 布置旧部署形态 → `trajecta upgrade env/db/cassettes`（默认 dry-run，`--apply` 写入）→ `scripts/postgres/acceptance.sh` 做 PG 侧验收。仓库不含旧部署的整栈 fixture，演练是运维步骤而非 CI job。

## 集成 smoke test（Codex + hosted tools）

用于在目标实例（例如 `http://<your-host>:<port>`）上验证 Codex + 本地 Responses runtime + hosted tools 链路。

**前置配置**：按“默认全开”处理，需要 `responses_server.codex_compat.enabled=true`、`responses_server.codex_compat.auto_inject_hosted_tools=["web_search"]`、`tools.web_search.enabled=true`、`tools.mcp.enabled=true`、`mcp.enabled=true`、`database.driver=postgres`，并设置 `responses_server.default_model`。`tools.web_search` 当前示例 provider 为 `searxng`，需要 `base_url`。MCP server 的 `bearer_token_env` 只应指向环境变量（例如 `DGX_API_KEY`），不能把 token 写进 YAML。

高风险工具不在集成默认路径：`file_search` 仍是 unsupported / rejected，`code_interpreter` 与 `computer_use_preview` 不启用。

**预检命令**（按顺序）：

```bash
trajecta -c config/config.yaml --format json config inspect
trajecta -c config/config.yaml --format json doctor
trajecta -c config/config.yaml --format json tools status
trajecta -c config/config.yaml --format json models codex-config <model>
```

判断标准：

- `doctor` 不应出现 blocking failure（可用 `--fail-on-warn` / `--fail-on-fail=false` 调整退出行为）。
- `tools status` 的 `ready_for_codex_web_search=true`，且 `tools.web_search.readiness=ready`。
- 需要 hosted MCP 时 `tools.mcp.readiness=ready` 且至少一个 enabled server。
- `models codex-config` 应输出 `model_context_window`、`model_auto_compact_token_limit`、`tool_output_token_limit`、`model_reasoning_effort`。

**Codex Web Search smoke test**：

```bash
codex -p dgx "搜索一下今日新闻"
```

期望：即使 Codex 请求没有显式 tools，Trajecta 也会按 `responses_server.codex_compat` 注入 `web_search`；`execution_events` 中出现 `response.tool_call` 的 `web_search` started/completed；`tool_call_audits` 中出现 `hosted:web_search` completed；内部 `upstream_exchanges` 带 TTFT，便于 prefill 速率分析。

**失败时收集**：优先用 `client_request_id`、`response_id`、`request_audit_id` 或最新请求查询。

```bash
trajecta -c config/config.yaml --format json audit query --list --status failed --limit 20

trajecta -c config/config.yaml --format json audit query \
  --client-request-id <client_request_id> \
  --include-events --include-exchanges --include-tools --limit 200

trajecta -c config/config.yaml --format json audit query \
  --response-id <response_id> \
  --include-events --include-exchanges --include-tools --limit 200

trajecta -c config/config.yaml --format json audit query \
  --request-audit-id <request_audit_id> \
  --include-events --include-exchanges --include-tools --limit 200

trajecta -c config/config.yaml --format json audit tool-calls --latest-by-call --limit 100
```

**日志定位字段**：服务日志会输出用于关联数据库与请求链路的结构化字段——`request_audit_id`、`response_id`、`conversation_id`、`client_request_id`、`trace_id`、`cassette_path`、`upstream_id`、`model`、`endpoint`、`status`、`status_code`、`duration_ms`、`ttft_ms`、`tool_type`、`tool_name`、`executor`、`call_id`、`stream`、`server_id`、`server_label`。

日志不会输出 bearer token、raw authorization 或完整 tool payload。需要 raw payload 时只能用显式高权限的 `audit tool-calls --include-payloads`（会输出 `input_json` / `output_json` / `metadata_json`，可能含敏感数据），不建议在共享测试环境默认开启。审计数据的整体形态见 [观测与审计](./OBSERVATION_AND_AUDIT.md)，Responses 运行时细节见 [Responses 运行时](./RESPONSES_RUNTIME.md)。

**常见失败判断**：

- `ready_for_codex_web_search=false`：先看 `tools status` 的 reason 与 warnings。
- `web_search` 未触发：确认 `response.request` accepted 事件是否包含 `codex_compat.injected_hosted_tools`；failed 时查 `tool_call_audits` 的 `error_text` 与 `metadata.result_count` / `query`。
- MCP unknown server：确认 descriptor 的 `server_label` / `server_url` 与 `tools.mcp.servers` 完全匹配；auth failure 时确认 `bearer_token_env` 指向的环境变量存在（日志与 audit 只显示 env 名，不显示值）。
- TTFT 为 0：确认内部 upstream exchange 是否有响应 body；请求在连接阶段失败时 TTFT 为空是预期行为。

## CI 跑什么

`.github/workflows/ci.yml` 的 `build` job（push 到 `main`、`v*` tag、以及指向 `main` 的 PR）在 `ubuntu-latest` 上依次执行：

1. `actions/checkout@v4`。
2. `oven-sh/setup-bun@v2`（bun `1.3.11`）。
3. `actions/setup-go@v5`（Go `1.27.x`，开启 module cache）。
4. `go mod download`。
5. 在 `web/monitor-ui` 执行 `bun install --frozen-lockfile`。
6. 在 `web/monitor-ui` 执行 `bun run build` 构建 Monitor UI。
7. `go build -v -o server ./cmd/server` 与 `go build -v -o trajecta ./cmd/trajecta`（同时产出服务端与 CLI，供第 18 步上传）。
8. gofmt 检查：对 `./cmd ./internal ./pkg ./tests ./unittest ./web/monitor-ui/test-fixtures` 跑 `gofmt -l`，有未格式化文件即失败。
9. `go vet ./...`。
10. golangci-lint：`golangci/golangci-lint-action@v8`，版本 `v2.15.0`（首个支持 Go 1.27 的系列为 v2.13+，此处与本地 `task lint` 使用同一版本）。
11. `go test -v ./...`（`TestAllocGate*` 分配门禁是普通测试，随这一步执行）。
12. Benchmark smoke：`go test -run '^$' -bench . -benchtime 1x ./pkg/xir ./pkg/session ./pkg/recordfile ./pkg/secretmask ./internal/gateway ./internal/router`，保证热路径基准可编译可运行、不腐烂；时间不做硬门禁。
13. `actions/setup-python@v5`（Python `3.12`）。
14. ATIF v1.8 校验：`python -m pip install -r scripts/atif-requirements.txt`、`python -m unittest discover -s scripts/atif_tests`、`ATIF_TEST_OUTPUT_DIR="$RUNNER_TEMP" go test ./internal/trajectory -run TestBuildCumulativeHistoryAndHumanCorrection -count=1`、`python scripts/validate_atif.py "$RUNNER_TEMP/session.jsonl"`。
15. 在 `web/monitor-ui` 执行 `bunx playwright install --with-deps chromium`。
16. 在 `web/monitor-ui` 执行 `bun run test:ui`（mock 套件）。
17. 在 `web/monitor-ui` 执行 `bun run test:ui:real`（真实 Go Monitor fixture 套件）。
18. `actions/upload-artifact@v4` 上传构建产物 `trajecta-linux-amd64`。

另有 `postgres-integration` job（与 `build` 并行）：postgres:17-alpine 服务容器 + `TRAJECTA_TEST_POSTGRES_DSN`，以 `-p 1` 串行跑 DSN 门控的集成测试（见[集成测试分层](#集成测试分层)）。

另有 `docker` job，仅在 push 事件运行、依赖 `build` job：登录 Docker Hub 后构建并推送 `linux/amd64` 与 `linux/arm64` 多架构镜像，tag 由 `docker/metadata-action` 生成（`latest`、`sha`、版本 tag），并启用 gha 构建缓存。

`.github/workflows/live-smoke.yml` 独立于 ci.yml：手动 dispatch 与每晚定时触发，跑 `tests/live/run-live.sh` 整栈黑盒套件；未配置 `TRAJECTA_LIVE_API_KEY` 等 secrets 时打印 notice 并跳过，不产生失败。

CI 与本地 task 的差异：CI 的 gofmt 范围额外包含 `./web/monitor-ui/test-fixtures`，与 `task fmt:check` 的覆盖范围相同；CI 跑 `go test -v ./...`（含 `TestAllocGate*` 分配门禁）加一步 `-benchtime 1x` 的 benchmark smoke（与 `task bench:smoke` 同范围），而不跑 `test:race`、`test:e2e` 和完整 `bench`；CI 也做 ATIF v1.8 校验，覆盖 `task test:atif` 的两半（`python -m unittest discover -s scripts/atif_tests` 与 `go test ./internal/trajectory`），但 Go 那半带 `ATIF_TEST_OUTPUT_DIR` 且用 `-run TestBuildCumulativeHistoryAndHumanCorrection` 只跑该用例、不跑 `./internal/monitor`，随后直接调用 `python scripts/validate_atif.py "$RUNNER_TEMP/session.jsonl"` 校验生成的 JSONL。

## 给 AI Agent 的默认选择

按改动类型选择最小足够验证，避免无谓的全量运行：

- 文档改动：`git diff --check`，必要时补链接检查；小代码改动：`task check:quick`。
- record / replay / 协议解析改动：`go test ./pkg/recordfile ./pkg/replay ./pkg/llm ./pkg/observe ./internal/monitor`。
- Monitor UI 改动：`task ui:build && task ui:test && go test ./internal/monitor`。
- proxy / router / store 改动：`go test ./internal/proxy ./internal/router ./internal/store`，必要时补 `task test:race`；热路径性能改动补 `task bench:core`。
- 数据库、迁移或生产查询路径改动：`task check:quick` + `go test ./internal/store ./internal/monitor`。
- 大范围交付前：`task check:full`。

约束：不要让测试依赖真实 provider 网络或真实 API key；不要绕过 `task` 入口手写等价命令，除非本文明确说明 Taskfile 未包装（例如 `bun run dev`）。存储与录制格式的改动约束见 [存储与部署](./STORAGE_AND_DEPLOYMENT.md) 和 [架构说明](./ARCHITECTURE.md)。

## ATIF v1.8 校验

会话导出使用 ATIF-v1.8。仓库提供固定版本 Harbor 官方模型及离线校验入口，先安装依赖：

```sh
python3 -m venv .venv-atif
.venv-atif/bin/python -m pip install -r scripts/atif-requirements.txt
```

依赖安装后校验完全离线，两个 task 覆盖入口：

- `task atif:validate -- <session.jsonl>`：调用 `python3 scripts/validate_atif.py`；支持多个 JSONL 文件，任一记录不符合模型约束或不是 v1.8 时返回非零状态。
- `task test:atif`：`go test ./internal/trajectory ./internal/monitor` 加 `python3 -m unittest discover -s scripts/atif_tests`，覆盖 Go 侧会话重建与官方模型校验器两半。

也可以直接调用虚拟环境里的脚本：

```sh
.venv-atif/bin/python scripts/validate_atif.py /tmp/session.jsonl
.venv-atif/bin/python -m unittest discover -s scripts/atif_tests
```

模型来源、schema 生成与真实 cassette 回归方式见 [trajectory 包说明](../internal/trajectory/README.md)。校验只证明格式与模型约束，不证明 HTTP 录制涵盖全部执行过程。
