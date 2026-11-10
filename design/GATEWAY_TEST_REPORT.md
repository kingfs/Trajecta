# `/data/gateway-test` 环境功能测试报告（Trajecta 2.0 / a26655b）

> 日期：2026-11-10（Asia/Shanghai）
> 第一轮被测代码：`main` @ `a26655b7bb21a6c0113781d5210f487fc0ae3ee6`
> 修复分支：`fix/gateway-test-round2` @ `d9797bc` + `a637fe1`
> 第一轮镜像：`trajecta:test-a26655b`；修复后镜像：`trajecta:fix-round2`
> 环境：独立 compose project `gateway-test`；proxy `:28080`、monitor `:28081`、Postgres 17 `:25432`
> 上游：`gateway-test` 网络内 `mock-upstream:39099`（本轮重建的可控 mock，仅测试用，不属于产品代码）
> 上一轮（`09affac`）记录：[GATEWAY_TEST_REPORT_round1_09affac.md](./GATEWAY_TEST_REPORT_round1_09affac.md)

## 0. 结论

**第一轮在 `a26655b` 上发现的问题已完成根因分析与修复；修复后在同一环境复测，核心能力达到 2.0 预期，仅保留 1 个已说明的边界行为。**

- 修复分支：`fix/gateway-test-round2`，提交 `d9797bc`（UI 阻断）与 `a637fe1`（后端语义/协议）；
- 修复后镜像：`trajecta:fix-round2`，仍部署在 `/data/gateway-test`（`:28080/28081`），未触碰 `/data/gateway`；
- 构建、`go vet`、全量单测、全量 `-race` 全部通过；
- Playwright mock 套件从 **194 passed / 20 failed** 变为 **214 passed / 0 failed / 4 skipped**；
- 第一轮 API 矩阵（64 项）从 **58/64** 变为 **64/64**；第二批（35 项）从 **34/35** 变为 **35/35**；
- 真实浏览器复测：登录、渠道列表、路由设置面板、新建渠道弹窗、渠道详情、协议编辑保存/刷新、模型广场均通过；
- 3.10（压缩体转换返回 502 而非 400）保留为已知边界，原因见第 5.3 节；其余问题全部修复。

### 0.1 根因与修复一览

| 问题 | 根因 | 修复 | 复测 |
| --- | --- | --- | --- |
| 3.1 `/channels` 崩溃 `setForm is not defined` | effect 写在 `ProvidersPage`，`setForm` 属于 `CreateProviderDialog` | 把 effect 移入弹窗并使用 `presetData` | Playwright 214/0；真实浏览器列表页正常 |
| 3.2 新建弹窗崩溃 `catalog is not defined` | 弹窗读取 `ProvidersPage` 的 `catalog`，但签名没有该 prop | `catalog` 作为 prop 传入 | Playwright 新建用例通过；真实浏览器弹窗与内置目录正常 |
| 3.3 Gemini `/v1beta` 重复 + wire 不生效 | 原生路径没有使用 catalog 的版本折叠；alias 只改 body，模型在 path | `joinGoogleRequestPath` 折叠版本；新增 `gateway.RewriteModelPath` 并在转发前重写 path | 环境 mock 收到 `/v1beta/models/wire-*:generateContent`，无重复 |
| 3.4 Vertex Express 认证错误 | `ApplyAuthHeaders` 没有把 Vertex Express 放入 `x-goog-api-key` 分支 | 加入 Vertex Express（Project 保留 OAuth Bearer） | 环境 mock 收到 `x-goog-api-key`，无 `Authorization` |
| 3.5 `upstream_model` 不生效 / 缺省不回落 | `channelModelAliases` 不含 `upstream_model`；resolver 只有 protocol alias | 新增 `Target.ModelWireNames` 与两阶段 resolver（alias → protocol wire → upstream_model） | 环境测试两条均通过 |
| 3.6 alias + wire 不能组合 | 别名先解析成目标名后，没有再查目标模型的 protocol wire | 两阶段解析：别名 → 规范模型 → 该协议 wire | 环境测试上游收到目标 wire name |
| 3.7 `PATCH protocols` 整表覆盖 | handler 把请求 map 整体写入 | 新增 `mergeChannelModelProtocols`，按协议 key 合并 | 单协议 PATCH 后其他协议保留 |
| 3.8 decision trace 缺 `priority_mode`/`selected_tier` | `DecisionTrace` 有字段，事件/plan 没有投影 | 事件与 inspect plan 增加字段 | inspect 返回两字段 |
| 3.9 全量 race 偶发选择失败 | `Epsilon=0` 被 `New` 当成“未设置”，2% 随机探索恢复默认 0.02 | `Epsilon` 改为 `*float64`，显式 0 生效 | 全量 `-race` 69 包通过，重跑稳定 |
| 3.10 压缩体转换 502 | 不可解析 body 被 fail-closed 当成 streaming，选择阶段先失败，转换检查不可达 | **未改**，保留为已知边界（见 5.3） | 仍为 502；非压缩路径不受影响 |

## 1. 测试环境与隔离

- 未触碰 `/data/gateway`（`38080/38081`）的运行实例；只使用独立 project `gateway-test`。
- 第一轮部署 `trajecta:test-a26655b`；修复后部署 `trajecta:fix-round2`（同一数据库、同一 mock），复测完成后测试渠道/别名/转换容器已清理。
- 部署方式：宿主机 `CGO_ENABLED=0` 构建静态二进制，覆盖到运行时基础镜像（Docker 内无法直接访问 Go module 代理，属环境限制，不是代码问题）。
- 启动日志确认：`Applying application database migrations...` → `Server listening addr=:8080`；现有数据库 `schema_migrations = 20261110120000 / dirty=false`。
- 上游 mock 在 `gateway-test` 网络内 `mock-upstream:39099`，可记录每个请求的 method/path/query/headers/body，并按 chat/messages/responses/gemini 返回合法响应；用于断言“上游实际收到的字节”。
- 测试用渠道、模型、API token、别名在测试结束后清理；`gateway-test-multi` 的 `protocols_json` 已恢复为原始的 `chat_completions + messages`。

## 2. 测试矩阵与结果（第一轮基线 `a26655b`）

> 本节保留第一轮发现问题的现场结果；修复后的复测结果见 [5.2](#52-复测结果)。

### A. 基础检查

| 检查 | 命令 | 结果 |
| --- | --- | --- |
| 后端构建 | `go build ./cmd/server ./cmd/trajecta` | **通过**（server 44 MB / trajecta 36 MB） |
| 静态检查 | `go vet ./...` | **通过**（exit 0） |
| 格式 | `gofmt -l cmd internal pkg ent` | **无输出** |
| 全量单测 | `go test -count=1 ./...` | **通过**：69 个包 `ok`，0 `FAIL` |
| 关键包 race | `go test -race`（gateway/proxy/monitor/router/upstream/xir/audit/channel） | **通过**：19 个包 `ok` |
| 全量 race（第 1 次） | `go test -race ./...` | 1 个失败：`TestRouterCostAwareSelectionPrefersLowerObservedCost`（见 3.9） |
| 全量 race（第 2 次） | `go test -race ./...` | **通过**：69 个包 `ok`，0 `FAIL`，无 `DATA RACE` |
| 前端构建 | `vite build` | **通过**；产物与仓库内 `internal/monitor/ui/dist` **逐字节一致**（`git status` 为空） |
| 前端单测 | `node --test "tests-unit/**/*.test.js"` | **243 passed / 0 failed** |
| 前端类型检查 | `tsc --noEmit` | 通过；但 `tsconfig` 只包含 `.ts/.tsx`，**约 88 个 `.jsx/.js` 不在检查范围**（含出问题的 `ChannelsPage.jsx`） |
| Playwright（HEAD） | `playwright test`（默认 mock 配置） | **194 passed / 20 failed / 4 skipped**；20 个失败全部来自 `/channels` 崩溃 |
| Playwright（应用第 5 节补丁后） | 同上 | **214 passed / 0 failed / 4 skipped**（`exit 0`） |

### B. 数据库

| 项 | 结果 | 关键证据 |
| --- | --- | --- |
| 全新 Postgres 执行 `db migrate up` | **通过** | `schema_migrations = 20261110120000 / dirty=false` |
| `channel_models.protocols_json` 形状 | **通过** | `text`、`NOT NULL`、`DEFAULT '{}'::text` |
| 旧库升级（26 条旧迁移 + 已有 channel/model 行） | **通过** | 只应用新迁移；模型行保留，`protocols_json='{}'`；升级后 schema 与全新库一致 |
| down 迁移 | **通过** | down 删列；重新 up 后列恢复，老行得到 `{}` |
| SQLite 启动 schema | **通过** | `TEXT NOT NULL DEFAULT '{}'`；旧 SQLite 库启动时自动补列 |
| `atlas.sum` | **通过** | 重新生成的 hash 与仓库一致 |
| 迁移幂等 | **通过** | 第二次 `db migrate up` 成功且版本不变 |

细节与完整输出：`.scratch/gateway-test/db/REPORT.md`。

### C. 渠道 / preset

| 项 | 结果 |
| --- | --- |
| `GET /api/provider-presets` 返回 31 个 provider，2.0 新增 11 个 plan preset 全部存在 | **通过** |
| 11 个新 preset 的默认 base URL（qwen_coding/intl、qwen_coding_*_anthropic、qwen_token_plan*、zai_coding*、zhipu_coding*、kimi_coding） | **通过**（逐项与 `pkg/upstream` 目录一致） |
| `no_list` / `name` / `notes` | **通过**（qwen/kimi 为 `no_list=true`；zai/zhipu 有 `/models` 发现面，按预期不设 `no_list`） |
| Qwen Coding 创建时拒绝 `sk-`、接受 `sk-sp-` | **通过**（400 / 200） |
| PATCH 已有 Qwen key 复用旧 key 时再次校验 | **通过**（`sk-ordinary` → 400，`sk-sp-fixed` → 200） |
| Coding Plan 与 Token Plan badge 区分 | **通过**（token plan `coding_plan` 为空，`plan_name` 为 Token Plan） |
| `GET /api/catalog/providers` 暴露 endpoints/auth/protocols | **通过**；Anthropic `x-api-key`、Google `x-goog-api-key`、Vertex `x-goog-api-key` 均正确声明 |
| Anthropic preset 创建后发 Messages 请求 | **通过**；上游收到 `x-api-key` 与 `anthropic-version` |

### D. per-model 多协议

| 项 | 结果 |
| --- | --- |
| `PATCH /api/channels/{id}/models/{model}` 写入 `protocols`，响应回显 | **通过** |
| 渠道详情 `models_usage[].protocols` 回显（a26655b 修复） | **通过** |
| 模型广场 `protocols` badge | **通过**（`chat_completions/responses/messages` 正确） |
| chat 的 `wire_model` 到达上游 | **通过** |
| OpenAI-compatible 渠道上 `messages=true` 后走 `/v1/messages` | **通过**，wire name 正确 |
| `responses=true` 时原生 `/v1/responses` | **通过**，wire name 正确 |
| 无 `protocols` 时回退渠道 `protocol_family` | **通过**（chat 可用、messages 按预期 502） |
| `chat=false` 后 chat 不可路由 | **通过**（502 `no_supporting_target`；messages 仍可用） |
| 每协议 `wire_model` 到达上游 | **部分失败**：chat/messages/responses 通过；**gemini 不生效**（见 3.3） |
| `PATCH` 单协议不丢其他协议 | **失败**：只发一个协议条目时，服务端用新 map 覆盖整个 `protocols_json`（见 3.7） |
| 缺省 `wire_model` 回落 `upstream_model` | **失败**：上游收到的是客户端模型名（见 3.5） |
| 模型别名与 protocol wire 组合 | **失败**：别名解析后不再应用目标模型的 wire name（见 3.6） |
| UI 协议编辑器（真实浏览器） | **通过**：展开、修改、保存产生 PATCH、保留其他协议、刷新后可见、新填 wire 自动 enabled |

### E. Proxy / 协议

| 项 | 结果 |
| --- | --- |
| `/v1/chat/completions` 非流式 / 流式（SSE） | **通过**；usage 嗅探 total=5，trace usage 正确 |
| `/v1/messages` 非流式 / 流式 | **通过** |
| `/v1/responses` 原生 / 流式 / 本地 runtime（chat-only 上游） | **通过** |
| Gemini `generateContent` / `streamGenerateContent?alt=sse`（base 为 origin 时） | **通过**（非流式 JSON；流式 SSE 200；上游 query `alt=sse`） |
| API token 认证 | **通过**：缺失/非法 token 均 401 `invalid_api_key` |
| per-protocol `endpoints` 覆盖（messages → `/alt`） | **通过**，上游收到 `/alt/v1/messages` |
| Anthropic / Azure / Vertex 上游认证 | Anthropic、Azure **通过**；**Vertex 失败**（Bearer 而非 `x-goog-api-key`，见 3.4） |
| Azure `api-version` 查询参数 | **通过** |
| 屏蔽 + cassette 字节分离 | **通过**：上游收到保形 stand-in，cassette 保留真实 secret，`/findings` 不回显明文 |
| alias 与 wire name | **失败**（见 3.6） |
| Google 默认 base URL | **失败**：`/v1beta` 与客户端路径重复（见 3.3） |

### F. 路由

| 项 | 结果 |
| --- | --- |
| `GET/PATCH /api/settings/routing` | **通过**；`priority_mode` / `plan_engine` 热切换生效 |
| `strict` 优先选择高 priority 渠道 | **通过**（`/api/routing/inspect` 的 `selected_channel_id` 正确） |
| `compat` 仍可解释候选 | **通过** |
| `plan_engine=route` 热切换 | **通过**（inspect 仍返回可执行 plan） |
| sticky（同一 `Session_id` 连续请求） | **通过**：4 次请求固定在同一个 upstream |
| 禁用协议 / 未知模型错误 | **通过**：分别 502 `no_supporting_target` 与 404 `model_unknown`，均未打上游 |
| decision trace 记录 `priority_mode` / `selected_tier` | **失败**：字段存在于 `router.DecisionTrace`，但没有写入事件属性，inspect 也不返回（见 3.8） |

### G. 协议转换

在独立转换实例上验证（同 DB、独立输出目录、`protocol_conversion=true`，legacy 与 xir 各一套）：

| 项 | legacy | xir |
| --- | --- | --- |
| messages → chat（非流式）请求转换、响应转回 Messages | **通过** | **通过** |
| gemini → chat（非流式）请求转换、响应转回 Gemini | 未单独跑 | **通过** |
| 有原生 messages 候选时优先原生、不转换 | **通过**（上游收到 `/v1/messages`，wire name 正确） | — |
| `/v1/responses` 永不走转换（本地 runtime 接管） | **通过** | — |
| 流式跨协议必须拒绝 | **通过**（messages stream → 502，无上游调用） | **通过**（Gemini `?alt=sse` → 400） |
| IR 无法承载的内容（image block）必须 4xx | **通过**（400 `protocol_conversion_unsupported` 语义） | — |
| 压缩请求体转换必须显式 4xx | **未达标**：routing 先失败，返回 502 而不是转换错误 400（见 3.10） |
| 12 个非流式协议对 / 12 个流式方向拒绝 | 单测覆盖：`pkg/xir/convert` 与 `pkg/protocolconvert` 的矩阵测试随 `go test ./...` 通过；本轮环境只抽查了 messages↔chat、gemini→chat | 同左 |

### H. 审计 / findings / cassette / ATIF

| 项 | 结果 |
| --- | --- |
| `GET /api/traces/{id}/xir`（chat 非流式） | **通过**；`protocol=chat_completions` |
| Chat SSE 装配为完整 response | **通过**（`response` 非空） |
| Messages 流式只解码 request | **通过**（`response={}` + `warnings=["streaming response is not decoded for messages"]`） |
| `GET /api/traces/{id}/findings` 合并 XIR detections | **通过**；`source=xir`，无明文 |
| minimal cassette prelude 无 events | **通过** |
| 最小化 cassette 的 raw/detail 时间线兜底 | **通过**（request/response 合成，raw 视图可用） |
| `GET /api/traces/{id}/trajectory` 对 `/responses` | **通过**（2 steps，无 unsupported warning） |
| `GET /api/traces/{id}/trajectory` 对其他 endpoint | **通过**（`extra.warnings[].code=unsupported_endpoint`） |

### I. UI（真实浏览器 + Playwright）

| 项 | 结果 |
| --- | --- |
| 登录 | **通过**（JWT，进入 `/overview`） |
| `/channels` 渠道列表 | **阻断**：`Unable to render this page / setForm is not defined` |
| `/channels` 上的路由设置面板 | **不可达**（同页崩溃） |
| 新建渠道弹窗 | **阻断**（修掉 `setForm` 后暴露 `catalog is not defined`） |
| `/channels/{id}` 渠道详情 | **通过**（真实浏览器；模型卡正常） |
| 模型卡 →「协议」展开 / 修改 / 保存 / 刷新 | **通过**（PATCH 200；请求体保留其他协议；刷新后值仍在） |
| 新填 wire model 自动 enabled | **通过**（PATCH body 中 `enabled=true`） |
| `/models` 模型广场 | **通过**（模型与协议 badge 正常） |
| Playwright mock 套件 | HEAD **20 failed**；修复补丁后 **0 failed** |

## 3. 关键问题详情（根因留存）

> 修复状态：3.1–3.8 已修复并复测；3.9 已修复；3.10 保留为已知边界。修复提交见第 5 节。

### 3.1 [阻断，已修复] `/channels` 一进入就崩溃：`setForm is not defined`

- 复现：登录后打开 `/channels`，页面主体显示 `Unable to render this page`，控制台 `ReferenceError: setForm is not defined`；截图 `.scratch/gateway-test/ui/02-channels.png`、`10-channels.png`。
- 根因：`web/monitor-ui/src/routes/ChannelsPage.jsx:66-68` 的 `useEffect` 写在 `ProvidersPage` 中，却调用只在 `CreateProviderDialog`（`:177`）里定义的 `setForm`。该 effect 来自 `3f3a0ec fix(channels): prefill the default preset URL on load`。
- 影响：渠道列表、渠道新建入口、路由设置面板全部不可用；Playwright 20 个失败全部由此产生。
- 修复：把 effect 移入 `CreateProviderDialog`（使用 `presetData`），见第 5 节补丁。

### 3.2 [阻断，已修复] 新建渠道弹窗崩溃：`catalog is not defined`

- 复现：应用 3.1 的修复后打开「新建渠道」，页面显示 `catalog is not defined`；Playwright `Create channel` 用例失败。
- 根因：`ChannelsPage.jsx:345` 在 `CreateProviderDialog` 内使用 `catalog.data`，但 `catalog` 只在 `ProvidersPage:62` 定义，弹窗签名（`:175`）没有该参数。该代码来自 `5f16ee1 feat(channels): add the built-in provider catalog picker`。
- 影响：即使修好列表页，仍无法创建渠道，内置 provider catalog 也打不开。
- 修复：把 `catalog` 作为 prop 传入弹窗，见第 5 节补丁。

### 3.3 [高，已修复] Gemini 默认 base URL 重复 `/v1beta`，且 per-model `wire_model` 不生效

- 复现 A：创建 Google preset 渠道（默认 base `https://generativelanguage.googleapis.com/v1beta`），客户端请求 `/v1beta/models/<model>:generateContent`；mock 收到
  `/v1beta/v1beta/models/<model>:generateContent`（截图/日志见 `.scratch/gateway-test/functional-run3.log`）。
- 复现 B：给模型配置 `gemini.wire_model=wire-gemini-<tag>`，上游路径仍是 `/v1beta/models/<client-model>:generateContent`；mock 响应回显的也是客户端模型名。
- 根因：
  - 默认 base URL 由 `internal/monitor/server_configuration.go:1672-1699` 从 preset 取 `.../v1beta`；原生请求走 `internal/upstream/resolved.go:1147` 的 `joinRequestPath`，它只对 `/v1` 做版本折叠，不处理 `/v1beta`，于是重复；`pkg/upstream/url.go` 里已有的 Gemini version-folding 没有被原生路径使用。
  - alias 改写只改 body：`internal/gateway/transform.go:74` → `xir.RewriteModel`；Gemini 的模型在 URL path 中，`internal/proxy/handler.go:2116-2133` 原样使用 `original.URL.Path`，所以 `ResolveModelAliasForProtocol(...,"gemini")` 的结果无人消费。
- 影响：默认创建的 Google 渠道无法访问真实 Gemini；Gemini per-model wire name 形同虚设。Vertex 的 `/v1beta` 路径也会同样重复。
- 建议：原生 Gemini/Vertex 路径使用与 catalog 相同的版本折叠逻辑（或统一委托 `BuildURL`）；在 path 中按 `ResolveModelAliasForProtocol` 重写 `{model}`。

### 3.4 [高，已修复] Vertex Express 认证 header 错误：Bearer 而非 `x-goog-api-key`

- 复现：`provider_preset=vertex`、`routing_profile=vertex_express` 的渠道，请求
  `/v1/publishers/google/models/<model>:generateContent`；mock 收到 `Authorization: Bearer <key>`，没有 `x-goog-api-key`。
- 根因：`internal/upstream/resolved.go:499-509` 的 `ApplyAuthHeaders` 只对 `RoutingProfileGoogleAIStudio` 设置 `x-goog-api-key`；`RoutingProfileVertexExpress` 落入 default 的 `Authorization: Bearer`。而 `/api/catalog/providers` 对 `vertex` 声明的是 `{"mode":"api_key","header":"x-goog-api-key"}`。`VertexProject` 使用 OAuth access token 时 Bearer 是合理的，本问题只针对 Express（API key）模式。
- 影响：Vertex Express 会被 Google 拒绝；与 2.0 “Google 使用 x-goog-api-key”的约定不符。
- 建议：把 `RoutingProfileVertexExpress` 加入 `x-goog-api-key` 分支（Project 模式保留 OAuth Bearer），并补 URL/Auth 单测。

### 3.5 [中，已修复] `upstream_model` 不生效；缺省 `wire_model` 不回落

- 复现：
  - 模型 `ft-legacy-<tag>` 设置 `upstream_model=wire-legacy-<tag>`、无 `protocols`，上游仍收到 `ft-legacy-<tag>`；
  - 模型设置 `protocols={"chat_completions":{"enabled":true}}`（无 wire）且 `upstream_model=wire-upstream-default-<tag>`，上游仍收到客户端模型名。
- 根因：`internal/channel/service.go:1010` 的 `channelModelAliases` 只处理 `model_aliases` 表，未把每个 `channel_models.upstream_model` 放进 `Target.ModelAliases`；`internal/router/router.go:574` 的 `ResolveModelAliasForProtocol` 在 protocol alias 未命中时只回退 `ModelAliases`，不会再回退 `upstream_model`。
- 影响：交接说明第 5 节“缺省 `upstream_model` 仍保留为旧字段”在转发路径上没有兑现；老数据/老 UI 的 upstream model 配置会被忽略。
- 建议：把 `UpstreamModel` 纳入 alias 解析（protocol wire → upstream_model → 原名），或在一个统一的 resolver 中完成。

### 3.6 [中，已修复] 模型别名与 per-protocol wire name 不能组合

- 复现：目标模型 `ft-alias-target-<tag>` 配置 chat wire `wire-alias-target-<tag>`；别名 `ft-alias-client-<tag>` 指向该目标；请求别名时上游收到 `ft-alias-target-<tag>`，不是 `wire-alias-target-<tag>`。
- 根因：`ResolveModelAliasForProtocol` 只以“客户端请求名”查 protocol alias；别名先解析成目标模型名后，没有再对目标模型执行一次 protocol alias 解析。
- 影响：使用 model alias 的用户会绕过 per-model wire name，上游可能收到它不认识的模型名。
- 建议：两阶段解析：alias → 规范模型名 → 该模型在该协议下的 wire name。

### 3.7 [中，已修复] `PATCH protocols` 是整表覆盖，单协议 PATCH 会丢其他协议

- 复现：模型已有 `chat/messages/responses` 三条协议，PATCH `{"protocols":{"messages":{...}}}` 后，`GET` 只剩 `messages`。
- 根因：`internal/monitor/server_channels.go:250` 直接把请求里的 map 序列化后交给 `UpdateChannelModelProfile`；`internal/store/channel_store.go:759` 整体 `SetProtocolsJSON`，没有与现有 JSON 合并。
- 影响：UI 每次都回传完整 map，所以真实 UI 不受影响；但交接矩阵明确要求“PATCH 单协议不丢其他协议”，API 客户端/脚本会静默丢配置。
- 建议：服务端按协议 key 合并（或要求显式 `replace:true`），并补 API 单测。

### 3.8 [低，已修复] decision trace / inspect 缺少 `priority_mode` / `selected_tier`

- 复现：`/api/routing/inspect` 的 `plan` 只有 `client_entrypoint`、`selected_channel_id` 等，没有 `priority_mode`/`selected_tier`；最小化 cassette 下事件也不可见。
- 根因：`internal/router/router.go:1289` 设置了 `DecisionTrace.SelectedTier` / `PriorityMode`，但 `internal/proxy/handler.go:1707` 的 `routingDecisionEvents` 没有把它们写入事件属性；`routePlanEvent` 也没有。
- 影响：交接第 6 节声称“decision trace 记录 priority_mode、selected_tier”，实际无法观测，排障时无法从 trace 判断 compat/strict 与 tier。
- 建议：在 `routing.selected` 或 `routing.route_plan` 属性中加入这两个字段，并在 inspect 的 plan 中回显。

### 3.9 [观察，已修复] 全量 `-race` 的一次非确定性失败

- 第 1 次 `go test -race ./...`：`internal/router` 的 `TestRouterCostAwareSelectionPrefersLowerObservedCost` 失败（`selected target = "slow", want fast`），无 `DATA RACE` 报告。
- 根因：`New` 只在 `cfg.Router.Selection.Epsilon > 0` 时覆盖默认值，测试写 `Epsilon = 0` 被当成“未设置”，仍保留 0.02 的随机探索；单次选择因此有约 2% 概率选中 slow，与负载/并发无关。
- 修复：`Epsilon` 改为 `*float64`（nil = 默认 0.02，显式 0 = 关闭探索），并补 `TestNewHonorsExplicitZeroEpsilon`；全量 `-race` 69 包通过，重跑稳定。

### 3.10 [低，已知边界，未改] 压缩请求体的转换错误被路由 502 掩盖

- 复现：`protocol_conversion=true` 时，对 chat-only 目标发 Gemini `Content-Encoding: gzip` 请求；返回
  `502 no upstream target supports ...`，而不是 `protocol_conversion.go` 里预期的 400 “compressed body cannot be converted”。
- 根因：`internal/proxy/protocol_conversion.go:93` 的压缩检查发生在路由选择之后；gzip body 让 envelope 解析不到模型/特征，选择阶段先失败。
- 影响：可诊断性差；与交接说明“压缩体必须显式失败 400”不一致。正常非压缩请求不受影响。
- 处理：**未改代码**。原因与取舍见 5.3：fail-closed 的 streaming 推断是刻意行为，路由前统一 400 会误伤原生压缩透传；若要改，需要单独设计“压缩体 + 是否必须转换”的请求前判定。

## 4. 与 2.0 改动说明的逐项对照

| 2.0 交付项 | 判定 | 说明 |
| --- | --- | --- |
| `pkg/xir` 四协议 codec / wire / hook / stream / audit | **达成** | 单测全绿；chat/messages/responses/gemini 编解码与 SSE 装配可用 |
| 非流式跨协议转换、12 方向流式拒绝 | **达成** | legacy/xir 环境抽查通过；矩阵单测通过；流式显式拒绝 |
| `internal/gateway` 请求/响应字节契约 | **达成** | 四协议代理、masking、cassette、usage 嗅探均符合 |
| `internal/proxy` 去别名 wrapper | **达成** | 代理路径正常，未见旧双轨 |
| `internal/upstream` preset/auth/URL 委托 | **达成** | 普通 URL、Anthropic/Azure/Vertex Express 均通过；Gemini/Vertex path 折叠 |
| per-model 多协议存储/API/路由/UI | **达成** | 四协议 wire（含 gemini path）、PATCH 合并、upstream_model、alias+wire 均通过 |
| Router `compat/strict`、`plan_engine`、sticky | **达成** | 选择/热切换/sticky 通过；decision trace 与 inspect 带 priority_mode/selected_tier |
| 审计 `/xir`、findings、minimal cassette、ATIF | **达成** | 本轮全部通过 |
| 上游 preset / coding plan | **达成** | 11 个 plan preset、key 校验、badge、默认 URL 全部通过 |
| 配置默认值（compat/off/legacy） | **达成** | 默认值符合；`plan_engine=route` 仍 opt-in |
| UI 渠道详情协议编辑（a26655b 修复） | **达成** | 真实浏览器保存/刷新/保留其他协议通过 |
| UI 渠道列表/新建渠道/路由设置 | **达成** | 两个 ReferenceError 已修；真实浏览器与 Playwright 均通过 |

## 5. 修复提交与复测

### 5.1 提交

| 提交 | 内容 |
| --- | --- |
| `d9797bc` | `fix(monitor): repair the channels page and its create dialog`：把 preset 预填 effect 移入 `CreateProviderDialog`，把 `catalog` 作为 prop 传入，重建 dist |
| `a637fe1` | `fix(gateway): make per-model wire resolution consistent`：`ModelWireNames` + 两阶段 resolver、原生 Gemini/Vertex path 重写、`/v1beta` 折叠、Vertex Express auth、protocols 合并、priority trace 字段、epsilon 显式 0 |

分支：`fix/gateway-test-round2`。部署镜像：`trajecta:fix-round2`。

### 5.2 复测结果

| 检查 | 修复前 | 修复后 |
| --- | --- | --- |
| `go build ./cmd/server ./cmd/trajecta` | 通过 | 通过 |
| `go vet ./...` | 通过 | 通过 |
| `go test -count=1 ./...` | 69 包 ok | 69 包 ok |
| `go test -race -count=1 ./...` | 第 1 次 1 个非确定性失败 | **69 包 ok，无 DATA RACE**；重跑稳定 |
| Playwright（mock，218 用例） | 194 passed / **20 failed** / 4 skipped | **214 passed / 0 failed / 4 skipped** |
| 第一轮 API 矩阵（64 项） | 58 passed / 6 failed | **64 passed / 0 failed** |
| 第二批（35 项） | 34 passed / 1 failed | **35 passed / 0 failed** |
| 真实浏览器 UI | `/channels` 崩溃 | 登录、列表、路由设置、新建弹窗、详情、协议编辑/刷新、模型广场全部通过 |
| 转换（legacy/xir） | messages/gemini→chat、原生优先、流式拒绝、fatal 400 通过 | 同左，且 alias+wire 通过 |

关键实测：

- Gemini：mock 收到 `/v1beta/models/wire-gemini-*:generateContent`（不再有 `/v1beta/v1beta`），响应正常；
- Vertex Express：mock 收到 `x-goog-api-key: vertex-key-test`，`Authorization` 为空，路径为 `/v1/publishers/google/models/wire-vertex-*:generateContent`；
- `upstream_model` 与 `wire_model` 缺省：上游分别收到 `wire-legacy-*` / `wire-upstream-default-*`；
- alias + protocol wire：上游收到 `wire-alias-target-*`；
- 单协议 PATCH：其他协议条目仍在；
- `inspect`：返回 `priority_mode` 与 `selected_tier`；
- `go test -race ./...` 连续通过，epsilon 显式 0 的确定性选择测试不再抖动。

### 5.3 保留的已知边界：压缩体转换返回 502

3.10 未改代码，根因是设计上的 fail-closed：

- `extractRequestFeatures` 对无法解析的 body 设置 `Stream=true`，避免把不透明 body 当 JSON 或把 SSE 缓冲成 JSON；
- 路由在 `protocol_conversion` 检查之前失败，因此到不了 `conversionForSelection` 的压缩体 400 分支；
- 若在路由前对所有压缩请求返回 400，会误伤原生压缩透传（Gemini path 带模型时原生目标本可直通）；若只在选择失败后改写，又会掩盖真实的模型/候选错误。

因此保留 502（明确的“无可服务目标”），并在报告与代码注释中记录；这是低危可诊断性问题，不影响非压缩请求、原生透传、转换成功路径或健康度。若产品要求统一 400，应单独设计“压缩体 + 是否必须转换”的请求前判定。

## 6. 未覆盖 / 限制

- 未使用真实上游凭据：preset 只验证默认 URL/认证 header；Anthropic/Azure/Vertex 用 mock 验证 header 与 path，没有真实计费请求；真实 Claude Code/Codex/Gemini SDK 的联调仍需发布前单独做。
- OAuth 未实现（2.0 非目标）；`plan_engine=route` 未默认化；跨协议流式转换未实现（非目标）。
- 转换矩阵的 12 个非流式方向、12 个流式拒绝由 `go test` 覆盖；环境抽查 messages↔chat、gemini→chat。
- `tokenize`/`detokenize` 未做环境级端到端（Azure/Vertex 内部 URL 逻辑仍由单测覆盖）。
- “转换失败不污染健康度”未做环境级统计断言；代码路径与单测支持该语义。
- “DB 保留完整 header 事件”未直接查询（API 只暴露合成时间线）；已确认 minimal prelude 无 events、raw/detail 兜底正常。
- UI typecheck 仍只覆盖 `.ts/.tsx`，约 88 个 `.jsx/.js` 不在 `tsc` 范围；本轮两个 ReferenceError 只能靠 Playwright 发现。建议把 Playwright mock 套件纳入 CI，或补 ESLint `no-undef`。
- 测试镜像的 `server`/`trajecta` 版本为 `2.0-fix-round2`（非发布版本号）；Docker 内 module 代理不可达是环境限制。

## 7. 后续建议

1. 合并 `fix/gateway-test-round2` 前，在 CI 中固定运行 Playwright mock 套件（它直接覆盖本次两个 UI 阻断）。
2. 在发布前用真实 key 做一次 Anthropic/OpenAI/Gemini/Vertex 冒烟，重点看 Gemini 默认 base URL 与 Vertex Express header。
3. 若要统一 3.10 的 400 语义，单独设计压缩体请求的前置判定，不修改 fail-closed 的 streaming 推断。
4. 评估是否把 `plan_engine=route` 默认化（当前仍 opt-in；本轮未改）。
5. 长期看，给 `ChannelsPage.jsx` 这类大文件补 `.jsx` 类型检查或 lint，避免“只在真实浏览器才暴露”的引用错误再次进入主干。

## 8. 证据索引

| 内容 | 路径 |
| --- | --- |
| 第一轮 API 结果（64 项，修复前） | `.scratch/gateway-test/functional-results.json`、`functional-run3.log` |
| 第二批（35 项，修复前） | `.scratch/gateway-test/extra-results.json`、`extra-run4.log` |
| 修复后 API 结果（64/64） | `.scratch/gateway-test/functional-fix-run.log` |
| 修复后第二批（35/35） | `.scratch/gateway-test/extra-fix-run.log` |
| Go 全量测试 / vet / race（修复后） | `.scratch/gateway-test/gotest-fix.log`、`govet-fix.log`、`race-fix.log` |
| 第一轮 Go/race | `.scratch/gateway-test/gotest-all.log`、`race-key.log`、`race-all.log`、`race-all-2.log` |
| 数据库迁移 | `.scratch/gateway-test/db/REPORT.md` |
| 前端构建/单测/typecheck/Playwright（HEAD） | `.scratch/gateway-test/frontend/REPORT.md` 及其日志 |
| Playwright（修复后全量） | `.scratch/gateway-test/ui/playwright-fix-round2.log` |
| 真实浏览器 UI 证据 | `.scratch/gateway-test/ui/02-channels.png`、`03-channel-detail.png`、`12-protocol-editor.png`、`20-create-dialog.png` |
| UI 修复补丁（保留作历史 diff） | `.scratch/gateway-test/patches/channels-page-fix.patch` |
