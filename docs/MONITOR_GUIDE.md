# Monitor 使用指南

Monitor 是 Trajecta 的本地 Web 工作台，由 `internal/monitor` 提供 API 与前端静态资源，和代理运行在同一个进程里（`monitor.port`）。它面向三个场景：

- 查看真实 LLM HTTP 请求与原始协议。
- 分析 session、模型、模型服务商、路由和失败。
- 管理模型服务商、模型、路由设置、分析任务与个人 API token。

## 启动与登录

配置 `monitor.port` 后，`serve` 启动时会同时挂载管理端口，浏览器打开 `http://localhost:<monitor.port>` 即可进入 Monitor。

首次部署需要创建用户。tracked 默认配置 `config/config.yaml` 使用 Postgres 且 `database.dsn` 为空，运行前需导出 `TRAJECTA_DATABASE_DSN`；纯本地运行可改用 `config/examples/local-sqlite.yaml`：

```bash
export TRAJECTA_DATABASE_DSN='postgres://user:pass@host:5432/trajecta?sslmode=disable'
go run ./cmd/server auth init-user -c config/config.yaml --username admin --password 'change-me-123'
```

Monitor 使用用户名密码登录（`POST /api/auth/login`），成功后签发仅用于 Monitor 的 JWT：issuer 为 `trajecta-monitor`，audience 为 `trajecta-monitor-ui`，TTL 默认 24 小时，可用 `auth.session_ttl` 调整。前端把 JWT 存在浏览器 localStorage，并以 `Authorization: Bearer` 访问 Monitor API；该 JWT 不用于 SDK、proxy 或 MCP。

左侧导航底部的账号入口（`aria-label` 为「账户」）打开一个**向上弹出**的浮层面板（Radix Popover，封装在 `web/monitor-ui/src/components/ui/popover.tsx`），而不是居中对话框。面板提供主题（深色、浅色、跟随系统）、语言（`dark`/`light`/`system` 与 `zh-CN`/`en`，都保存在当前浏览器）、修改密码（`POST /api/auth/password`）和退出登录。主题与语言各自是一个 Radix RadioGroup（`role="radiogroup"` + `role="radio"`，方向键在组内移动并选中，整组只有一个 Tab 停靠点），选中后面板不关闭，两项可以在一次打开里都改完；改动语言会动态拉取另一个语言 chunk。Radix 把面板 portal 到 body 并按触发按钮定位，`side="top"` 让它开在按钮上方，`align="start"` 让它与按钮左边缘对齐，因此面板按视口而非按 248px 的侧边栏测量——它会横向盖出侧边栏，这正是之前被截断的原因；空间不足时 popper 会自动翻到下方而不是移出屏幕，`collisionPadding` 保证它不贴边。Esc 关闭、点击外部关闭、`role="dialog"` 与关闭后把焦点还给触发按钮都由 primitive 提供；焦点还原要求触发按钮与面板处在同一个 Popover root 内，所以 `PrimaryNav` 用一层 `Popover` 同时包住两者。

对话框仍用于真正需要打断的表单：修改密码（从面板进入，`DialogContent` 的 `onCloseAutoFocus` 显式把焦点还给账号按钮，因为这个表单没有自己的触发按钮）、模型服务商列表页的新建弹窗、模型服务商详情页的编辑弹窗（`ChannelDetailPage` 里的 `EditProviderDialog`，触发按钮与对话框同处一个 Dialog root）以及 trace 详情页的工具弹窗，都走 `web/monitor-ui/src/components/ui/dialog.tsx`（`@radix-ui/react-dialog`）。焦点陷阱、Esc 关闭、遮罩点击关闭、`role="dialog"` 与 `aria-modal` 语义、背景滚动锁定都由这个 primitive 提供，`src/` 里已没有自建的 portal。`serve` 总是挂载 auth store，所以 `/api/auth/status` 返回 `auth_required: true`；只有在没有挂载 auth store 的嵌入式/测试场景下才返回 `false`，此时前端以 local 用户直接进入。

## 个人 API token

「令牌」页面（`/api/auth/tokens`）管理当前用户的个人 API token：创建、撤销、删除。列表列为 name、prefix、scope、status、created、expires、last_used；TTL 只是创建表单的输入项（`ttl`），列表与 API 都不返回、也不显示 TTL。token 只在创建时显示一次，之后只保留 prefix。

个人 token 的用途：

- 代理 API：`Authorization: Bearer <token>`，proxy 的每个请求都要通过该 token 鉴权。
- MCP：`Authorization: Bearer <token>`。

Monitor API 本身只接受登录 JWT，不使用个人 token。

## 数据来源

Monitor 同时使用两类数据：

- application database（生产环境为 Postgres，本地 fallback 为 SQLite，默认文件 `{{output_dir}}/trajecta.sqlite3`）：trace 索引、session、列表/过滤/分页/聚合、模型服务商与模型配置、模型别名、路由设置、事件、analysis run/job、Responses 状态和 audit 表。
- raw `.http` cassette：trace 详情、raw protocol 与 replay-safe 检查的事实来源。路由事实在数据库里是 `logs` 上的派生列，原始 `routing.*` 事件仍记录在 cassette prelude 中。

因此列表页只读数据库索引、响应很快，详情页仍能回到原始 HTTP 证据。`GET /api/routing/summary` 也完全走数据库：`buildRoutingSummary` 调 `store.RoutingSummary(since, model)`，对 `logs` 上的路由列做一次 `GROUP BY`，不再打开任何 cassette。计数是**按 trace** 而不是按路由事件：每条 trace 只保留每个事实的最后一个非空值（`internal/recorder/recorder.go` 的 `setLastNonEmpty`），所以同一条 trace 里的多次 sticky 事件折叠成最后一次。只要 `selected_upstream_id`、`route_target_id`、`channel_id`、`credential_id`、`sticky_status`、`routing_failure_reason` 任一非空，这条 trace 就算「有事件」；`legacy_or_missing_events = total_traces - eventful_traces`。迁移前的行不一定算 legacy：`selected_upstream_id` 是迁移前就有的列，这类 trace 仍算有事件，只是 `route_target_id` / `channel_id` / `credential_id` 与 sticky 拆分为空，直到一次重索引从 prelude 重新推导这五列。

## 页面

左侧主导航分三组共 9 项：**监控**（概览、流量、事件、质量）、**配置**（模型服务商、模型、路由、接入）、**系统**（系统）。内容较多的页面用页内标签页（tab）而不是继续增加导航项——标签页状态保存在 URL 的 `tab` 查询参数里（例如 `/traces?tab=sessions`），因此可以收藏与分享。标签条由 Radix Tabs 实现：整条 strip 只占一个 tab stop，方向键与 Home/End 移动选中项，只有选中的 tab 是 `tabindex=0`，tab 与它所控制的 panel 用 `aria-controls` / `aria-labelledby` 互指。下面按导航顺序说明路由与用途。

侧边栏可以折叠成只有图标的窄栏，折叠状态保存在浏览器 localStorage（`trajecta.monitor.sidebar.collapsed`）。折叠后品牌区块整体让位给展开按钮，因此折叠始终可逆；宽度不足 1000px 时布局固定为窄栏，与折叠状态无关，此时展开按钮不显示。

trace 列表、trace 详情、会话详情与上游详情上的指标统一渲染为「图标 + 数值」的紧凑标签：可见部分只有图标和格式化后的数值，人类标签与未格式化的原始值放在 `title` 悬停提示里（以 ` · ` 连接）。数值按可读宽度自动换算——耗时超过 60 秒改用分钟/小时（`125000` ms → `2m 5s`，`3725000` ms → `1h 2m`），token 速率超过 1000 后按 `k`/`M` 缩写且百位以上取整（`112055` tok/s → `112k tok/s`），token 数量沿用 `k`/`M`/`B` 缩写（`112055` → `112.1K`）。

### 监控

#### 概览 `/overview`

时间窗口内的请求量、成功/失败数、Token、平均 TTFT、延迟、发现项和系统事件概览。趋势图按请求量、Token、延迟三个面板展示；主要拆分收敛为一个面板，用分段控件在模型、模型服务商、端点、上游、路由失败、发现类别之间切换，而不是六列并排。概览的数据由实时 socket 推送（见「实时推送」一节），并给出两条去向链接：质量页的发现项与事件收件箱。已配置模型服务商的卡片列表与派生数据健康度不再放在这里——前者的完整版本在「模型服务商」页，后者是解析管道的属性而不是最近一小时流量的属性，现已移到「质量 → 数据健康」标签页。

#### 流量 `/traces`

请求与会话两个标签页。

**请求**（默认）：逐请求 trace 列表，支持过滤与分页（`page_size` 上限 200，超出按 200 处理，非正数按默认 50），展示 endpoint、model、状态码、duration、TTFT、token。可以进入 trace detail，也可以跳到对应的模型、模型服务商或路由上下文。列表数据来自应用库索引。
- 过滤语义：`model`、`endpoint`、`upstream` 三个过滤项都按子串匹配且大小写不敏感，过滤值里的 `%`、`_` 与反斜杠按字面量处理，不作为 LIKE 通配符——因此按 `gpt_oss` 过滤只会命中包含 `gpt_oss` 的 trace。统计、trace id 导出与 session 列表共用同一套过滤条件，结论必须与列表一致。
- `status` 过滤只识别 `success`、`error` 与 `failed`（`failed` 是 `error` 的同义值，大小写不敏感；session 列表用的是同一套取值），其它取值不参与过滤、也不报错。trace 级别的 `error`/`failed` 还包含「HTTP 已返回 2xx、但记录里带 `error_text`」的 trace（例如流式响应中途断掉），而 session 列表的 `failed` 按状态码统计（与页面展示的 `success_request`/`failed_request` 同源），两者口径不同是刻意的。

**会话**：按 session 聚合最近 50 个会话，展示健康度、成功率、模型、模型服务商、流式标记与耗时。列表页顶部的时间窗口与统计只覆盖**当前这一页**的 50 条会话，页面上的「本页范围」提示即说明这一点；它不是全库聚合。

- 过滤语义：`provider` 匹配会话使用过的任一 provider；`model` 与搜索框 `q` 匹配会话中**任一**请求（因此一个「先 `gpt-alpha`、后 `claude-beta`」的会话按 `gpt-alpha` 也能查到）；`status` 是**会话级**判断——`success` 表示该会话没有任何失败请求（非 2xx），`failed` / `error` 表示至少有一个，与列表展示的 `success_request` / `failed_request` 计数同源。
- `database.use_session_summary_read`（`TRAJECTA_DATABASE_USE_SESSION_SUMMARY_READ`）打开后，会话列表改由派生读模型 `session_summaries` 提供；该模型只保存会话的最后一个模型、provider 列表与请求计数，因此无法表达「任一请求命中」的 `model` / `q` 过滤，这两类过滤始终走日志路径。两个读路径对同一过滤条件必须给出相同结果，门禁 `TestSessionFiltersAgreeAcrossReadPaths` 会同时跑两条路径并逐项比对。 代码默认关闭，仓库自带 `config/config.yaml` 已显式打开（参考部署已核对两条读路径一致）；未完成 `session_summaries` 回填的部署应先核对再打开，方法见 [PostgreSQL 运维](./POSTGRES_OPERATIONS.md)。

session ID 的抽取顺序为：

1. `Session-Id` / `Session_id`
2. `X-Claude-Code-Session-Id`
3. `X-Codex-Turn-Metadata` 中的 `session_id`
4. `X-Codex-Window-Id` 的前缀

适合分析 Codex、Claude Code 等 agent 在一轮任务中多次模型调用的整体行为。

##### 手动导出会话轨迹

会话详情页提供两个按钮：`导出轨迹 (ATIF)`（默认，带上限）与 `完整导出 (NDJSON 流)`（完整会话，流式）。两者都从该会话的客户端可见 cassette 重建轨迹并下载文件，不会调用模型、修改 cassette 或自动提交分析任务。

- 格式固定为 **ATIF-v1.8**；默认响应是单个 trajectory 对象的 JSONL（一行，以换行结束），可拼接成多会话数据集。
- 默认只重建最早的 500 条 trace（`trajectoryTraceCap`），以便默认视图在慢盘上仍有界；响应 `extra` 增加 `trace_count`（会话总 trace 数）、`included_traces`（本次导出条数）、`truncated`（是否被截断）与 `trace_cap`。被截断时页面提示"默认只导出最早的 N / M 条 trace"，并引导使用完整导出。
- `?full=1` 取消上限，导出完整会话（仍是单对象 JSON）。
- `?stream=1` 使用 NDJSON 流式导出，每行一个 JSON 对象并逐条 flush，不在服务端累积整个会话：第一行 `type=header`（`schema_version`、`session_id`、`agent`、`trace_count`、`included_traces`、`truncated`、`trace_cap`）；随后每个 trace 一行 `type=trace`（`trace_id`、`steps`、`results`、`warnings`、可选的 `error`），其中 `results` 是晚到、需要回挂到更早步骤的工具结果，带 `step_id`；从未收到结果的工具调用最后以 `type=missing_tool_result` 行报告；最后一行 `type=final`（`final_metrics` 与会话级 `extra`）。`?stream=1&full=1` 流式导出完整会话。服务端每读完一条 cassette 就 flush，请求 context 取消（客户端断开）时立即停止后续读取。
- 默认（带上限）导出的结果缓存到 `<trace.output_dir>/trajectory-cache/`，键为 `(session_id, last_trace_id, trace_count, limit)`，因此重看同一会话无需重建。会话只会追加 trace，且已写入的索引行不可变，所以只要轨迹会变，键就必然变化：命中即证明是同一条 trace 序列，无需失效逻辑。条目以临时文件加 rename 原子写入，最多保留 512 个条目 / 512 MiB，超出按写入时间淘汰；`?full=1` 与 `?stream=1` 不读写该缓存。容量上限只影响命中率，未命中一律回落到重建。
- 当前语义重建支持 Codex 使用的 OpenAI Responses generation（`/responses`、`/v1/responses`）及 SSE。其他 endpoint（包括 compact）保留源 trace 引用，并报告 `unsupported_endpoint`；不伪装成已解析的对话。
- 请求按 `recorded_at` 与 trace ID 排序，输出按 Responses `output_index` 排序。会话归组依据保存在 `extra.session_source`，无法从 HTTP 证明全部事件的因果关系或任务已完成。
- 相邻请求的历史上下文按有序重叠合并，不全局删除相同文本。`previous_response_id` 请求按增量输入处理。工具结果按调用 ID 回挂到发起调用的步骤。
- 每个录制响应合并为一个 agent 步骤，而不是每个 SSE 事件一个步骤。多模态内容保留文本与占位引用，不生成外部附件；未知内容报告警告。reasoning summary 放在 `extra.reasoning_summary`，加密内容仅标记为已省略，不当作完整可读思维链。
- 每步携带 `trace_id`、origin 与归一化 item 路径（流式输出先重建）。标准 `metrics` 每个响应记录一次 token 计数，`final_metrics` 汇总；不重复导出逐项 usage attribution、原生 item 或请求配置。内部 model child exchanges 不重复计入这份客户端视角导出；仅历史恢复的内容不推测 usage。
- `extra.warnings` 报告文件缺失、无法解析、上下文不连续、缺失或孤立工具结果、流式中断等问题，下载完成后页面显示警告数量。`completion=unknown` 不将 HTTP 成功解释为任务成功。
- 导出以一次查询得到的请求集合为快照，生成期间新增请求不进入本次文件。默认与 `?full=1` 仍在服务端组装完整响应；极大会话应使用 `?stream=1`。

接口为 `GET /api/sessions/:sessionID/trajectory`，可选参数 `full` 与 `stream`（`1`/`true`/`yes`/`on` 视为真），使用现有 Monitor JWT 登录鉴权，返回 `application/x-ndjson` 和附件下载头；默认文件名 `session-<id>.atif.jsonl`，流式为 `session-<id>.atif.ndjson`。无请求的会话返回 404。导出不依赖异步 Observation 是否已生成，而是从 V2/V3 cassette 重建。

#### 事件 `/events`

Trajecta 自身的事件收件箱：

- 来源：代码当前只发出 `parser`、`analyzer`、`router`、`upstream` 四种来源；UI 的来源过滤下拉额外提供 `proxy`、`recorder`、`monitor`、`store`、`auth`、`mcp`，即过滤项比实际发出的来源更宽。
- 类别：`parse_failure`、`analysis_failure`、`analysis_job_failure`、`routing_failure`、`transport_error`。
- 状态：`unread`、`read`、`resolved`、`ignored`。
- 级别过滤提供 `critical`、`error`、`warning`、`info`。

支持按状态、级别、来源过滤和搜索 fingerprint / trace / model / 消息，支持单条标记已读、解决、忽略，以及「全部标为已读」。重复事件按 fingerprint 合并。导航上的未读角标由 `GET /api/events/summary` 与 SSE `GET /api/events/stream` 驱动。

#### 质量 `/audit`

四个标签页：发现项、请求链路、分析任务、数据健康。带 `?response_id=` 或 `?request_audit_id=` 的深链直接打开「请求链路」，否则默认「发现项」。

**发现项**（`GET /api/findings`）：按类别和级别（`critical`、`high`、`medium`、`low`）过滤，可跳转到对应 trace 的审计或协议视图。`severity` 与 `category` 都是大小写不敏感的整值匹配，`all` 表示不过滤（与事件列表 `GET /api/events` 的 `severity`/`category`/`status` 同一套规则）；未识别的取值按字面量匹配、返回空列表，不会退化成"不过滤"。

**请求链路**：输入 `response_id` 或 `request_audit_id` 加载本地 Responses runtime 的 request audit、execution events 与 upstream exchanges（`GET /api/responses/audit/trace`）。

**分析任务**：展示已持久化的 analysis run 与 analysis job 队列，并提供两个批量操作，都以异步 job 提交（`POST /api/analysis/batch/reanalyze`），不调用上游模型：

- 「修复分析数据」：对失败或未解析的 observation 重新解析。
- 「修复 Token 统计」：对缺失 usage 的记录重新抽取 token 统计。

**数据健康**：派生数据管道的健康度——已解析/未解析 trace、解析队列深度与失败的分析任务数，未解析项可跳到按 `observation=unparsed` 过滤的流量列表。数据由实时 socket 推送。这一项原先挤在概览页，但它是解析管道的属性，与「最近一小时的流量」无关。

整个页面调用的接口只有 `GET /api/findings`、`GET /api/responses/audit/trace`、`GET /api/analysis/*` 和概览/事件摘要接口；`GET /api/responses/audit/tool-calls`（工具调用审计）虽已在 management server 注册，但 Monitor UI 从不调用它，属于 API-only 接口。server-side function executor 状态面板已移到「系统 → 服务端工具」。

### 配置

#### 模型 `/models`

按模型查看时间窗口内的流量：模型覆盖哪些模型服务商，以及请求数、错误数、Token 与趋势。详情路由为 `/models/:model`，详情页可编辑该模型的 `display_name`、`enabled`、`upstream_model`、`context_window`、`max_output_tokens`、`compact_history_item_threshold` 与 `profile_adoption_status`，以及三态的 `supports_responses` / `supports_chat_completions` / `supports_embeddings` 能力覆盖（模型级声明优先于 provider 级配置）。

#### 模型服务商 `/providers`

管理上游模型服务商。支持创建与编辑 provider preset、base URL、API key、headers、routing 字段，以及 API surface：`api_type`、`mode`、Responses/Chat Completions/tool calling/models 等 capability。模型级的 `supports_responses` / `supports_chat_completions` / `supports_embeddings` 覆盖不在这里编辑，见上面的模型页。

创建前可以先做探测：

- `POST /api/provider-probe` 返回只读的 Detect provider 建议，不落库。
- `POST /api/provider-setup/validate` 组合 base URL、API key、preset、模型发现与 capability 字段做一次探测，并把归一化配置写回表单，但不落库；响应不回显 API key，只返回 `api_key_hint`、secret storage mode 和脱敏 header 状态。表单字段变化会清空旧验证结果。
- `POST /api/provider-setup/apply` 才真正把配置写入 store。
- 列表页的 Batch probe and apply 先跑 `POST /api/provider-probe/report` 预览，再用 `POST /api/provider-probe/report/apply` 对 detected 且存在可补字段的 provider 批量应用；批量应用只补缺失的 `api_type`、`protocol_family` 和未设置的 capability，不覆盖显式配置，也不覆盖显式 `false`。

还可以启停模型服务商、启停单个模型、探测模型、查看用量/Token/失败与 probe 结果。

配置归属：模型服务商、模型与别名的长期配置保存在 application store；YAML 只作为首次 bootstrap 输入。由 YAML 显式 `credentials` 列表管理的配置下，Monitor 的这三类写操作返回 409。配置来源、bootstrap 标记与状态机见 [路由、渠道与凭据](./ROUTING_AND_CREDENTIALS.md) 与 [架构与代码地图](./ARCHITECTURE.md)。

#### 接入 `/connect`

两个标签页：客户端接入、API 令牌。

**客户端接入**展示当前部署的 base URL 与三种协议入口，并给出可复制的 curl 示例：

- `/v1/chat/completions`（OpenAI-compatible）
- `/responses`（OpenAI Responses / Codex；`/v1/responses` 仍然支持）
- `/anthropic/messages`（Anthropic Messages / Claude Code）

**API 令牌**管理当前用户的个人 API token，见上文「个人 API token」。

#### 路由 `/routing`

工作区包含四个标签页：

- Decisions：最近选中的路由（数据来自 `GET /api/routing/exchanges`），可按模型、通道/上游、状态、耗时、TTFT、Token 过滤。
- Settings：编辑路由设置（`PATCH /api/settings/routing`），包括 `responses_strategy`、`selection_policy`、`missing_model_policy`。
- Aliases：模型别名的增删改与校验（`/api/model-aliases`、`/api/model-aliases/validate`）。
- Inspector：`POST /api/routing/inspect` 预演某个请求/模型会如何被路由。候选的能力判定与转发热路径共用同一个谓词（`upstream.ResolvedUpstream.SupportsRawPath`：协议族 → API surface → adapter），因此「只写 provider preset、不写 `api_type`」的渠道（如 `config/examples/anthropic.yaml`、`google_genai.yaml`、`vertex.yaml`）会按解析后的协议族回答，而不是按空的 `api_type` 被当成 Chat Completions 渠道：Anthropic 渠道对 `/v1/messages` 报可服务、对 `/v1/chat/completions` 报 `requires_chat_completions`，Google/Vertex 渠道对这三个可预演 endpoint 都报不可服务。模型级能力覆盖同样按转发路径的投影读取（`channel.ChannelModelCapabilities`：别名键携带其目标模型行的覆盖），因此「别名名恰好也是该渠道已声明模型名」时，Inspector 与代理都会用别名目标行的覆盖，而不会用被遮蔽模型行自己的声明。未声明模型的可路由性也按同一条规则判断：只有渠道的 `allow_unknown_models` 为真时才会规划出路由，否则候选会以 `model_not_matched` 被排除——与转发热路径一致，不会因为该渠道的模型集合暂时为空就放行。

摘要面板来自 `GET /api/routing/summary`，按 failure reason、selected route target、credential 和 sticky 状态聚合，并区分有事件与旧数据/缺失事件的 trace。该端点不解析任何文件，所以响应里没有解析失败计数，页面也不再显示这一项。

### 系统

#### 实时推送

Monitor 不再有前端定时刷新。整个控制台开一条 WebSocket（`GET /api/events/ws`），页面告诉服务端自己正在显示哪些主题，服务端在某主题的数据变化时推一条只带主题名的消息，浏览器据此只重新获取自己注册在这个主题下的请求：

- `traffic`：有请求被索引、解析或重分析——trace / 会话列表、概览聚合、发现项、分析队列、路由与上游视图都归它；
- `events`：运行时事件收据变化（侧边栏未读角标与事件页）；
- `system`：主机、进程与数据库采样到期，这是唯一真正周期性的读数，由服务端定时器产生，且只在有标签页停在系统页时采样，多个标签页共用一份。

主题与请求的对应关系写在 `web/monitor-ui/src/lib/realtime.js`：每条读请求经 `useJSON` 按其 URL 前缀自动注册主题，因此页面不需要知道 socket 存在。`GET /api/events/stream` 这个 SSE 端点仍然提供，供已经在用 SSE 的外部集成使用，控制台本身不再使用它。浏览器无法在 `new WebSocket()` 上带 `Authorization` 头，所以这条路径（以及 SSE 端点）允许把访问令牌放在 `?access_token=` 查询串里，其余接口仍然只认请求头。

#### 系统 `/system`

管理员专属页面：导航项只对 `role=admin` 的登录用户显示，未启用认证时显示给 `local` 伪用户（此时 Monitor 本身不校验身份）；后端对未认证请求返回 401、对已认证的非管理员返回 403。页面分四个标签页，各标签页只读取自己需要的接口。这一页是唯一由服务端定时的数据：只要还有一个标签页停在系统页，服务端就每 30 秒推送一次 `system` 主题（见「实时推送」一节），四个标签页共用这一份采样：

- **运行时**：`GET /api/system/host` + `GET /api/system/runtime`。
- **数据库**：`GET /api/system/db`。
- **慢查询**：`GET /api/system/slow-queries`。
- **服务端工具**：`GET /api/responses/function-executors`。

`GET /api/system/host` 报告主机资源事实，全部来自 `/proc` 与 `syscall.Statfs` 等系统调用，不需要额外 agent：主机名、uptime、内核版本；CPU 核数、总使用率与 user/system/iowait/idle 分解、1/5/15 分钟负载、每核使用率；内存总量/已用/可用/页缓存与 swap；每个挂载点的文件系统、设备、总量/已用/可用/使用率；网络接口的累计收发字节数与瞬时速率；以及本进程的 PID、RSS、虚拟内存、CPU 占用、线程数与打开文件数。非 Linux 平台返回 `unsupported=true` 与 `reason`，页面显示说明而不是报错。瞬时速率在两个采样点之间计算，同一进程内两次请求间隔不足 500 ms 时复用上一次结果，间隔超过 5 分钟则重置基线（此时速率显示为 0 而不是把休眠期间的平均值当成当前速率）。页面展示时会折叠噪音：同一设备上的多个挂载点（容器 overlay 层与 bind mount）合并为一行并标注还有几个挂载点，从未传输过字节的网络接口不显示。

`GET /api/system/runtime` 报告 Go 进程事实：Go 版本、GOOS/GOARCH、CPU 核数、GOMAXPROCS、协程数、进程运行时长、堆的累计分配/在用/存活对象/进程总内存/栈内存、GC 次数与最近 256 次 GC 暂停的 min/p25/p50/p75/max，以及 `database/sql` 连接池计数（最大连接、已打开、使用中、空闲、等待次数、等待累计、因空闲或超时被关闭）。它不查询任何数据库行。

`GET /api/system/db` 报告 Postgres 统计视图；非 Postgres 驱动（如 SQLite）返回 HTTP 200、`supported=false`、`unsupported=true` 和 `reason`，页面显示说明而不是报错。采集全部来自 `pg_stat_*` 与系统视图，在一条只读事务内完成（`SET LOCAL statement_timeout = 2000`，外层 5 秒超时），每个列表最多 20 行，`pg_stat_activity.query` 先在 SQL 里截到 4000 字符，再在 Go 侧脱敏并截断到 500 字符。它从不执行 `EXPLAIN`、`EXPLAIN ANALYZE`、`VACUUM`、`ANALYZE` 或任何写语句；单个分节失败只进入响应的 `warnings`。页面展示：

- 数据库大小、缓存命中率（低于 95% 标黄、低于 90% 标红）、临时文件与临时写入字节、死锁、事务提交/回滚。
- 连接与活动：本库连接数、实例总连接数、按 `state` 与 `wait_event_type` 的分组计数、运行最久的非 idle 语句。
- `public` 下最大的关系（总大小/堆大小/估算行数）、未被使用的索引（`idx_scan = 0`，按大小高亮，附维护成本提示）、读放大最严重的索引（`idx_tup_read / idx_scan`）、写入最频繁的表。
- 检查点计数（Postgres 17 来自 `pg_stat_checkpointer` 的 `num_timed`/`num_requested`，响应中的 `source` 标明来源）与 `pg_settings` 的关键参数（`shared_buffers`、`work_mem`、`maintenance_work_mem`、`effective_cache_size`、`random_page_cost`、`max_wal_size`、`max_connections`、`statement_timeout`）。

`GET /api/system/slow-queries` 返回进程内慢语句环形缓冲区。采集默认关闭：`debug.slow_query_threshold`（`TRAJECTA_DEBUG_SLOW_QUERY_THRESHOLD`）为 `0` 时不记录任何语句，热路径只有一次原子读取，页面显示「collector disabled」空状态并提示设置该配置。设为正数（例如 `"200ms"`）并重启后，慢于阈值且经过数据库驱动的语句进入容量 50 的缓冲区，保存时间、耗时、操作类型（`query`/`exec`）以及脱敏并截断到 500 字符的语句。

pprof 只在 `debug.pprof_enabled`（`TRAJECTA_DEBUG_PPROF_ENABLED`，默认 false）为 true 时挂载到 Monitor 的 `/debug/pprof/`；为 false 时该前缀返回 404，不会落到前端 SPA。

## 兼容路由与详情路由

- `/` 重定向到 `/overview`。
- 合并掉的旧地址全部保留重定向，并且**保留原有查询参数**：`/requests` → `/traces`，`/sessions` → `/traces?tab=sessions`，`/analysis` → `/audit?tab=analysis`，`/tokens` → `/connect?tab=tokens`。
- `/channels` 重定向到 `/providers`；`/channels/:channelID` 仍渲染模型服务商详情页。
- 详情路由：`/traces/:traceID`、`/sessions/:sessionID`、`/models/:model`、`/providers/:providerID`、`/upstreams/:upstreamID`。
- 未匹配的路径重定向到 `/overview`。

## Trace 详情

Reading guide 提供五个视图：

- Routing & Conversation：路由选择、prompt 消息、最终输出和 timeline 事件。
- Protocol：Observation IR 的语义节点、归一化类型、JSON 路径与原始 payload。
- Audit：确定性 findings、证据路径。
- Performance：延迟、TTFT、Token 吞吐、缓存比例、状态与路由上下文。
- Raw：原始 HTTP 请求/响应字节与 headers。

可执行动作：

- `Refresh analysis`（`POST /api/traces/:id/reanalyze`）：从本地 cassette 重新生成 Observation 和 findings，仅在自动处理异常或结果明显不对时使用。
- `Repair stats`（`POST /api/traces/:id/repair-usage`）：从本地响应重新抽取 usage/token 统计，仅在 token/cost 统计缺失或错误时使用。

Deep link 支持 query 参数 `tab`、`from_session`、`view`（`sessions` / `requests`）和 `focus`（`failure`、`timeline`、`timeline_error`、`request`、`response`）。当 trace 携带 Responses audit id，或后端能通过 `upstream_exchanges.trace_id` 反查到 audit id 时，Reading guide 会显示 `Responses audit` 入口，跳转到 `/audit` 中同一条请求链路。

## Responses function executors

`GET /api/responses/function-executors` 返回当前进程的 server-side function executor 摘要，供「系统 → 服务端工具」标签页使用；响应不返回 `static_response` 的 output 或 `external_command` 的 command 等敏感内容。

`POST /api/responses/function-executors` 是 Monitor 的写入口：默认 `validate_only=true`，只返回归一化摘要和 warnings；`validate_only=false` 时把非敏感 overlay 持久化到应用库 `app_settings`，并热更新当前进程的 executor registry，后续新请求生效。字段、redaction 与进程约束语义见 [本地 Responses Runtime](./RESPONSES_RUNTIME.md)。

## 排障建议

请求失败时按顺序查看：

1. trace detail 的 Routing & Conversation 与 Raw。
2. Routing 页面的 Decisions，或 trace 中的 routing context。
3. Events 页面是否有 parser / analyzer / router / upstream 事件。
4. 模型与模型服务商页面确认模型启用状态和模型服务商健康。
5. 只有在派生结果明显不对时才运行 `Refresh analysis`；Token 统计异常时运行 `Repair stats`。

## 前端实现

Monitor 的前端是一个客户端 SPA，源码在 `web/monitor-ui`（React 19、`react-router-dom`、Vite 8、TypeScript）。`vite build` 输出到 `internal/monitor/ui/dist`，由 `internal/monitor/server.go` 的 `//go:embed ui/dist/*` 嵌进二进制，再由 `internal/monitor` 用 `http.FileServer` 提供，生产环境不需要额外的 Node 进程。`task ui:test` 依次跑 `bun run typecheck`（`tsc --noEmit`）、`node --test` 单元测试与 Playwright 浏览器测试；只跑类型检查用 `task ui:typecheck`。

样式是 Tailwind CSS v4（`@tailwindcss/vite` 插件）加一份 OKLCH 调色板。`src/styles/tokens.css` 里的颜色全部取自 Radix 的公开色阶——中性色用 slate，六个色相用 blue、green、amber、red、cyan、violet——`tests-unit/tokens.test.js` 读这份 CSS 校验对比度关系，因此在 token 被手工改动或色阶升级后会在测试里失败而不是在眼睛里失败。`tokens.css` 留在 cascade layer 之外：它只声明自定义属性，而它使用的主题属性选择器必须继续压过 `:root` 默认值。其余历史样式表由 `src/styles/tailwind.css` 导入到最低优先级的 `legacy` 层（层序 `legacy < theme < base < components < utilities`），所以迁移期间一个 utility 类总能盖过同名的旧页面规则，不需要考虑特异性或源码顺序。Tailwind 的 preflight 故意不引入：它会重置 `styles.css` 等历史样式表所依赖的元素默认值，引入等于重排每个页面，`base.css` 已经带着 Monitor 实际需要的 reset。

可复用的界面原语在 `src/components/ui/`：`dialog`、`tabs`、`switch`、`separator`、`skeleton`、`popover`、`radio-group`、`segmented-control`、`sonner` 已在页面上使用，类名合并走 `clsx` 加 `tailwind-merge`。`button` 也已有调用点：工具栏上的图标按钮全部是 `variant="default" size="icon"`，原语的变体按它替换掉的类命名并使用同一批 token，因此迁移过的按钮和旁边没迁移的按钮尺寸、配色一致。目录里另有 `badge`、`card`、`dropdown-menu`、`input`、`select`、`tooltip` 已经写好但还没有页面接入——它们是 shadcn/ui 式的拷贝进源码的原语，接入才算完成，未接入的按死代码看待。按钮与文本控件已经迁完：95 处 `.ghost-button` 是 `variant="ghost"`／`variant="primary"`（其中 26 处是 `<Link>`／`<a>`，用 `<Button asChild>` 包住，链接仍是链接），55 处受元素规则影响的 `<input>` 是 `input` 原语，三种筛选宽度（180/120/260px，原 `.filter-input`、`-small`、`-wide`）成为调用点的工具类。表单与设置面板里的 `<select>` 也已经换成 Radix 的 `select` 原语（触发器用与 `input` 相同的盒子，宽度留在调用点，因为 `.provider-form select` 原本把它撑满网格单元，而按钮不会继承那条规则；空字符串选项的翻译规则写在原语里）；页面区块也已经换成 `card` 原语：70 个 `<section className="panel">` 是 `<Card as="section">`（`as` 存在的理由正是它们原本是 `<section>`，换成卡片不该把文档大纲压平）。这次迁移用几何而不是眼睛验收——15 条路由上 87 个 panel 与 stat 卡的位置、尺寸、内边距、外边距、边框、圆角在迁移前后完全一致；也正是这次对比找出了唯一的真实差异。两个承重细节现在有测试钉住：区块之间的 16px 间距写在旧样式表里（`[data-slot="card"]`）而不是工具类，因为 `.page-body > .panel` 会为直接位于页面主体下的区块取消它，而工具类会盖过那条规则（第一版正是这样，让 11 条路由的内容下移了 16px）。仍留在旧实现上的是列表筛选用的 6 个原生 `<select>`，保留的理由是它们筛选列表、其中一个随外层表单提交、且手机上是系统选择器；`.stat-card` 也保留自己的类，它是 4px 间距的 flex 列，不是这个表面。服务端状态走 TanStack Query，客户端实例在 `src/lib/queryClient.ts`，其默认值刻意保持被替换掉的自写 `useJSON` 的行为（`retry: false`、`refetchOnWindowFocus: false`）。

写操作统一走 `src/lib/mutations.js` 的 `useWriteMutation`：它自己持有 pending 状态，成功后按需失效调用方声明的读查询，并把结果交给 `sonner` 的 toast 播出；页面不再各自维护 `busy`／`error`／`saved` 之类的状态。播报力度是有约定的——成功且行内已经能看到变化（事件操作、token 增删）保持静默，被截断的导出用 warning，失败一律 error 并带上服务端消息。toast 容器是一个 `aria-live` 区域，所以结果既看得见也读得到；登录表单是唯一例外，它的错误留在表单里，因为那条错误出现在 shell 之前。分段选择器（窗口、分布、trace 过滤、路由工作区）统一为 `src/components/ui/segmented-control.tsx`，底层是 Radix 的 radio group：单选语义、`aria-checked`、方向键移动并选中。Radix 的 radio group 只移动焦点不选中，原因是 React 19 从根元素派发事件而 Radix 在 `document` 上监听冒泡阶段的 keydown，它判断“方向键按下了吗”时按键还没被记下；原语补上了缺的那一半。日期与时间按当前语言格式化（`Intl.DateTimeFormat`／`Intl.NumberFormat`，`src/lib/monitor.js`），不再固定 `zh-CN`。Markdown 内容由 `react-markdown` 加 `remark-gfm` 渲染（`src/components/common/MarkdownBlock.jsx`，懒加载），它是 React 元素而不是 HTML 字符串，因此不再需要 `dangerouslySetInnerHTML` 和手写转义。系统事件流由 `@microsoft/fetch-event-source` 读取，替代手写的 SSE 解析。

构建产物按需拆包，入口之外只加载用到的部分。当前 `vite build` 输出（3342 个 module）：

- 语言词典是每种语言一个 chunk：`src/locales/<language>.js` 由 `loadMessages` 动态 `import()`，Vite 为每个匹配模块生成一个 chunk。`bootstrapI18n()` 在渲染前取到当前语言，取不到时回退英文而不是白屏。`zh-CN` chunk 55.97 kB（gzip 16.81）、`en` chunk 56.86 kB（gzip 15.32）。
- 图表库是独立 chunk，只在真正画图的页面加载：`src/components/common/Charts.jsx` 用 `React.lazy` 在渲染时才 `import("./ChartsImpl")`，该 chunk 350.58 kB（gzip 102.44）。
- `motion` 的特性集同样是独立 chunk（37.86 kB，gzip 14.53），首屏不等它；在它到达之前 `m` 元素就是普通元素，只是动画从起点瞬间到位。
- Markdown 渲染器是独立 chunk：`src/components/common/Display.jsx` 用 `React.lazy` 在真正渲染内容时才 `import("./MarkdownBlock")`，该 chunk 155.32 kB（gzip 46.44），因此只有带 Markdown 内容的 trace 详情页会付它的代价。
- 入口 `index` chunk 837.12 kB（gzip 234.18），样式 `index` 107.48 kB（gzip 18.93）。入口比引入 Popover 之前大 76.6 kB（gzip 24.9）：约 29.7 kB（gzip 11.1）是 popper/floating-ui——把一个浮层按视口定位、空间不足时翻转、并让它盖出侧边栏所要付的一次性成本，`radio-group` 6.2 kB（gzip 1.7），`sonner` 与 toast 约 14 kB（gzip 5），`fetch-event-source` 约 6 kB（gzip 2.3），其余是 Radix Collapsible 与新增的词典条目。把 Markdown 与图表从入口挪出去是这笔账的另一半：手写渲染器换成 `react-markdown` 时入口净减，代价转到按需加载的 chunk 上。

### 主题

账号面板里的主题单选组提供深色、浅色、跟随系统三个选项。保存的是偏好本身，键为 localStorage 的 `trajecta.monitor.theme`，取值可能是 `dark`、`light` 或 `system`；`applyTheme()`（`src/lib/theme.ts`）把 `system` 用 `matchMedia("(prefers-color-scheme: dark)")` 解析成实际主题，只把解析后的 `dark` 或 `light` 写进 `<html>` 的 `data-theme`，因此 `data-theme` 从不出现 `system`。`index.html` 里的内联脚本在样式表加载前做同一件事，这是页面加载时不再先闪出另一个主题的原因；`watchSystemTheme()` 监听 `matchMedia` 的 `change` 事件，让 `system` 偏好在页面打开期间继续跟随操作系统。

因为 `data-theme` 始终是实际主题而不是偏好，CSS 只需要两组规则——深色写在 `:root, :root[data-theme="dark"]`（属性缺失时就是深色），浅色写在 `:root[data-theme="light"]`——不需要第三份写在 `prefers-color-scheme` 媒体查询里的浅色主题；Tailwind v4 侧对应的是 `src/styles/tailwind.css` 里的 `@custom-variant dark`，所以 `dark:` 工具类也只有一个选择器。

## 非目标与未实现

- 代理不做协议族之间的转换，唯一例外是 `/v1/responses` 的本地 Responses runtime；边界见 [架构与代码地图](./ARCHITECTURE.md) 与 [实现状态](./IMPLEMENTATION_STATUS.md)。
- Monitor 没有用户管理界面：用户由 `auth init-user`、`auth reset-password`、`auth create-token` 等 CLI 命令创建和维护，界面只支持修改自己的密码。
- Monitor API 不接受个人 API token；个人 token 面向 proxy 与 MCP。
- Monitor 不能修改由 YAML 显式 `credentials` 管理的模型服务商、模型和别名，这类写操作返回 409。
- 写接口不接受 `output`、`command`、`args`、`env` 等敏感可执行字段，Monitor overlay 不能创建新的可执行 command；字段语义见 [本地 Responses Runtime](./RESPONSES_RUNTIME.md)。
- `external_command` 只有轻量进程约束（工作目录、绝对 command、允许目录、拒绝 root），不等同于容器或 namespace 沙箱。

## 相关文档

存储与部署 `./STORAGE_AND_DEPLOYMENT.md`、Postgres 运维 `./POSTGRES_OPERATIONS.md`、路由与凭据 `./ROUTING_AND_CREDENTIALS.md`、观测与审计 `./OBSERVATION_AND_AUDIT.md`、Responses runtime `./RESPONSES_RUNTIME.md`、MCP 使用 `./MCP_GUIDE.md`。

架构 `./ARCHITECTURE.md`、实现状态 `./IMPLEMENTATION_STATUS.md`、协议与模型服务商 `./PROTOCOLS_AND_PROVIDERS.md`、协议参考 `./protocol-reference/README.md`、代理接入示例 `./PROXY_USAGE_EXAMPLES.md`、开发命令 `./DEVELOPMENT.md`、文档总入口 `./README.md`。
