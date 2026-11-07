# 路由、渠道与凭据

本文描述当前代码事实下的渠道（provider）配置、模型与别名管理、路由选择、凭据路由与 limit scope。
渠道配置只属于应用数据库；YAML 不再接受 `upstream` / `upstreams`，加载到这两个键会直接报错并提示去 Monitor 配置。
相关主题见 [ARCHITECTURE.md](./ARCHITECTURE.md)、[PROTOCOLS_AND_PROVIDERS.md](./PROTOCOLS_AND_PROVIDERS.md)、[RESPONSES_RUNTIME.md](./RESPONSES_RUNTIME.md)、[MONITOR_GUIDE.md](./MONITOR_GUIDE.md)、[MCP_GUIDE.md](./MCP_GUIDE.md) 与 [STORAGE_AND_DEPLOYMENT.md](./STORAGE_AND_DEPLOYMENT.md)。

## 配置来源与所有权

- 启动只为数据库中的渠道构建 `RuntimeTargets`；没有任何 YAML 导入步骤。
  所有渠道、渠道模型与别名都由 Monitor 的渠道管理 API 写入 `channel_configs` / `channel_models` / `model_aliases`；`channel.Service.RuntimeTargets` 是唯一的运行时投影。
- 渠道 ID 由 Monitor 创建时给定；同一数据库内唯一。
- 第一次管理写会在应用库 `app_settings` 写入 `channels.initialized`。
  此后数据库拥有路由配置，即使所有渠道都被禁用或删除。
- `GET /api/settings/channels` 返回 `{"initialized": <bool>}`；`DELETE /api/settings/channels` 在 `ConfigurationTransaction` 内清除该标记。
  该标记只用于 `GET /api/settings/channels` 报告“是否已有配置”；启动时不再导入 YAML，清掉标记也不会重新生成渠道。
- 不存在 YAML 只读模式：channel service 没有 `ReadOnly` 状态，Monitor 的渠道/模型/别名写路径始终走数据库事务。
- 环境变量只用于启动与本地部署覆盖，不是长期的渠道/模型管理面。

## 数据模型

- `channel_configs`（渠道主配置）：`id`、`name`、`description`、`source`、`base_url`、`provider_preset`、`api_type`、`mode`、`capabilities_json`、`protocol_family`、`routing_profile`、`api_version`、`deployment`、`project`、`location`、`model_resource`、`api_key_ciphertext`、`api_key_hint`、`headers_json`、`enabled`、`priority`、`weight`、`capacity_hint`、`model_discovery`、`created_at`、`updated_at`、`last_probe_at`、`last_probe_status`、`last_probe_error`。
- `channel_models`（渠道与模型的供应/启用关系）：唯一键 `(channel_id, model)`；字段包括 `display_name`、`source`（`static` / `discovered` / `manual` / `trace`）、`enabled`、`supports_responses` / `supports_chat_completions` / `supports_embeddings`（三态 `INTEGER NULL`，`NULL` 表示继承渠道级能力）、`context_window`、`max_output_tokens`、`compact_history_item_threshold`、`upstream_model`、`profile_source`、`profile_adoption_status`、输入/输出模态 JSON、`raw_model_json` 与 `first_seen_at` / `last_seen_at` / `last_probe_at`。
- `model_aliases`：`id`、`alias`、`target_model`、`channel_id`（空表示全局别名）、`enabled`、`description`、`source`、时间戳。
  `channel_id` 非空时表示该别名只允许在该渠道展开；写入时校验禁止直接别名循环、禁止同一 `alias + channel` 重复激活目标，冲突返回 `model alias conflict`。
- `model_catalog`：按模型保存展示元信息（`display_name`、`family`、`vendor`、`description`、标签、首末出现时间），它不是路由事实来源；可路由性由 `channel_models.enabled` 与 router 内存快照决定。
- `channel_probe_runs`：探测历史（状态、耗时、发现/启用数量、endpoint、`status_code`、错误文本、`request_meta_json`、`response_sample_json`）。
- `upstream_targets` / `upstream_models`：旧的运行时快照与兼容投影。配置事务与后台 refresh **不再写入**（router 只维护内存快照）；保留这两张表是为了兼容旧库、旧页面回退读与 `trajecta upgrade` 的漂移校验，新代码不再以它们作为路由或授权输入。
- 凭据：每个渠道的 API key 在数据库中以加密形式保存（`channel_configs.api_key_ciphertext`），Monitor 只显示 hint；没有 YAML credentials 列表。

SQLite 走启动期 schema 初始化，Postgres 走 `ent/postgres-migrations/` 中的版本化迁移（例如 `20260629192000_add_model_aliases.up.sql` 与配对的 `.down.sql`；`model_aliases` 没有 ent schema，只存在于 SQL 迁移里）。

## 管理写路径的一致性

所有管理写（渠道、渠道模型、模型别名、provider setup、probe apply）都经过 `store.ConfigurationTransaction`：

- 它在整个回调期间持有进程级配置锁 `configMu` 与 upstream 写锁 `upstreamMu`，并开启一个 SQL 事务；原始 SQL 与 ent 操作共享该事务。
- 回调拿到 `tx *store.Store` 与 `commit func() error`，必须先准备好运行时快照再显式 `commit`；其它退出路径回滚。回调结束仍未 commit 则报 `configuration transaction was not committed`。
- Monitor 侧由 `configurationAPIHandler` 统一包装：GET/HEAD 与 `/validate` 直接执行；其它方法先检查 `svc.ReadOnly()`，只读时返回 409；否则在事务内以 `txService`/`txStore` 执行原 handler，把响应缓冲到 `configurationResponse`。
- handler 返回错误状态时事务回滚；唯一例外是 `/probe` 返回 `502` 且结果为 `failed`，此时提交事务以保留诊断记录，但不会改动路由。
- 写成功后依次执行 `MarkConfigurationInitialized`、`txService.RuntimeTargets()`，再以 `rtr.ReloadWithCommit(targets, tx, commit)` 在提交前构建新快照。提交成功后才对外发布路由；失败则保留旧快照。
- `RuntimeTargets` 只包含 `enabled = true` 的渠道：`StaticModels` = 该渠道已启用模型加上可在该渠道展开的启用别名，`DisabledModels` 记录被禁用的模型，`ConfiguredModelsOnly` 为 `true`；headers、capabilities、`model_capabilities`、API key 与渠道级 proxy 策略只在内存中交给 router/proxy。渠道代理来自 `channel_configs.proxy_json`，在 `upstream.Resolve` 时编译为 `ResolvedUpstream.Proxy`；关闭时该渠道跟随系统策略。
- reload 按 target ID 继承既有运行时状态（EWMA/health），锁顺序固定为 `configMu → upstreamMu → reloadMu → r.mu`。
- 后台 upstream refresh 以 best-effort 方式持久化：`LockUpstreamWrites` 阻塞等待，`TryLockUpstreamWrites` 在配置变更持锁时直接跳过写库。

## 模型与渠道管理 API

以下路径由 `internal/monitor/server.go` 真实注册，全部经过 `monitorAuthRequired`。

渠道与渠道模型：

- `GET /api/channels`、`POST /api/channels`
- `GET /api/channels/{id}`、`PATCH /api/channels/{id}`、`DELETE /api/channels/{id}`
- `POST /api/channels/{id}/probe`
- `GET /api/channels/{id}/models`、`POST /api/channels/{id}/models`
- `PATCH /api/channels/{id}/models/batch`
- `PATCH /api/channels/{id}/models/{model}`、`DELETE /api/channels/{id}/models/{model}`

`GET /api/channels/{id}` 返回配置摘要、模型数量、用量摘要、趋势、模型用量、最近失败与最近探测记录。
渠道启停通过 `PATCH /api/channels/{id}` 的 `enabled` 字段完成；`PATCH /api/channels/{id}/models/{model}` 支持 `display_name`、`enabled`、三态能力覆盖（显式 `null` 表示清除回继承）、`context_window`、`max_output_tokens`、`upstream_model` 等。

渠道（Channel）与探测：

- `GET /api/provider-presets`
- `POST /api/provider-probe`
- `POST /api/provider-probe/report`
- `POST /api/provider-probe/report/apply`
- `POST /api/provider-setup/validate`、`POST /api/provider-setup/apply`

模型与别名：

- `GET /api/models`、`GET /api/models/{model}`
- `GET /api/models/{model}/spec-lookup`、`POST /api/models/{model}/spec-lookup`
- `GET /api/model-aliases`、`POST /api/model-aliases`
- `POST /api/model-aliases/validate`
- `GET /api/model-aliases/{id}`、`PATCH /api/model-aliases/{id}`、`DELETE /api/model-aliases/{id}`

`GET /api/models` 只返回当前窗口内 `request_count > 0` 的模型；模型详情的渠道覆盖来自 `channel_models`。

设置、路由解释与本地密钥：

- `GET /api/settings/routing`、`PATCH /api/settings/routing`
- `GET /api/settings/channels`、`DELETE /api/settings/channels`
- `POST /api/routing/inspect`
- `POST /api/provider-model-test`（按模型、按协议的最小请求连通性测试，只读，不写库、不录制）
- `POST /api/provider-model-detect`（按模型自动识别接口：chat/responses/messages/generateContent，写入该模型的 `supports_*`；不带 model 时识别所有已启用模型）
- `GET /api/routing/exchanges`、`GET /api/routing/summary`
- `GET /api/secrets/local-key`（`?export=1` 下载备份）、`POST /api/secrets/local-key?rotate=1`
- 旧运行时诊断：`GET /api/upstreams`、`GET /api/upstreams/{id}`

`PATCH /api/settings/routing` 的字段、校验与持久化位置（应用库 `app_settings` 键 `routing.settings`）见 [MONITOR_GUIDE.md](./MONITOR_GUIDE.md) 与 [RESPONSES_RUNTIME.md](./RESPONSES_RUNTIME.md)。
`POST /api/routing/inspect` 接受 `endpoint`、`model`、`stream`、`tools`，返回 route plan 与候选排除原因。

## 渠道探测与模型写回

探测入口是 `channel.Service.ProbeWithOptions(channelID, options)`，`options` 对应 `POST /api/channels/{id}/probe` 的 `enable_discovered` 与 `detect_provider`：

1. 从数据库读取渠道并解密 secret。
2. 可选执行 provider 自动识别（`providerprobe.Probe`）。
3. 用 `internal/upstream.Resolve` 解析出 `ResolvedUpstream`，并执行 startup diagnostics 得到连通性 endpoint。
4. 按 provider family 选择模型发现策略，得到模型 ID 列表。
5. 成功时 upsert `channel_models`、`model_catalog`、`upstream_models`，更新渠道 `last_probe_*`，并写一条 `channel_probe_runs`。

行为要点：

- 新发现模型的默认启用状态由 `enable_discovered` 决定，缺省为 `false`；重复探测只刷新 `last_seen_at` / `last_probe_at`，不会覆盖操作者的启用状态或已采纳的模型 profile。
- `upstream_models` 只在探测成功时整体替换；router 不再改写它。
- 探测结果会返回 `added_count` / `removed_count`：新增是本次首次出现的模型，移除是非操作者声明（`manual` 或 `static`）且本次未再返回的 `discovered` 行；失败探测不改变快照。
- 探测失败不会自动禁用渠道；失败会写入 `channel_probe_runs`，并在 `request_meta_json` 中记录分类与重试建议：`auth_error`、`not_found`、`invalid_json`、`network_error`、`rate_limited`、`upstream_error`、`configuration_error`、`probe_error`。
- `POST /api/provider-probe/report/apply` 只对 `detected` 的结果生效，且只填补缺失字段（`no missing fields to apply`）；应用了字段才触发 router reload。
- provider probe 只判断「路由是否存在」：它给 `/v1/responses` 发的是空 body，很多 provider 会回 `400 missing model`，这个 400 被算作 endpoint 存在，却**不能证明某个模型在该 endpoint 上可用**。因此 route-only probe 不再自动把 `capabilities.responses` 写给 Chat Completions 渠道：只有渠道 `api_type` 为 `responses` / `responses_native`，或 probe 根本没有发现 `chat_completions` surface（此时 responses 只能表示原生 API）时才应用该 capability；其余情况由模型级 `supports_responses` 覆盖或按模型连通性测试实测决定。
- `POST /api/provider-model-test` 是无写入的按模型连通性测试。请求为 `{"channel_id","model","protocol"}`（`protocol` 省略或 `auto` 时按声明探测）；它用渠道已解密的 key 和该模型的线上名，按 `BuildURL` + `ApplyAuthHeaders` 与转发热路径相同的规则发送最小请求（chat 16 token / responses 16 output token / messages / generateContent），返回每个协议的 `status`（`ok`、`auth_failed`、`model_not_found`、`endpoint_not_supported`、`invalid_request`、`rate_limited`、`timeout`、`network_error`、`upstream_5xx` …）、HTTP 状态、延迟与可执行建议。逻辑失败也是 HTTP 200，只有「测试没有跑起来」才是 4xx/5xx。它不写 `channel_models.last_probe_at`、不写 `channel_probe_runs`、不产生 cassette/logs，也不经过 router。当 chat 成功而 `/v1/responses` 返回 `endpoint_not_supported` 时，建议为模型级 `supports_responses=false`，使 `/v1/responses` 入口改走本地 Responses runtime。
- `POST /api/provider-model-detect` 是接口自动识别的写入入口。请求为 `{"channel_id","model":"可选","overwrite":false}`：带 model 时只识别该模型，不带 model 时识别该渠道所有已启用模型（上限 100）。它在**配置锁外**先并行探测（每协议 8s、最多 4 个模型并发、不重试），再在**一个** `ConfigurationTransaction` 里写入该模型的三态 `supports_chat_completions` / `supports_responses`（`overwrite=false` 只填 NULL，`overwrite=true` 覆盖已有值），提交成功后才 reload 路由。结论规则：`ok` → 支持；`endpoint_not_supported` / `model_not_found` → 不支持；鉴权、超时、网络错误等不确定结果不写。Anthropic 的 messages 和 Gemini 的 generateContent 是协议族级接口，只报告、不写列。它不写 `last_probe_at`/`channel_probe_runs`，不产生 cassette/logs。Monitor 的模型卡「识别接口」、启用模型后的自动补全、渠道工具栏「识别已启用模型的接口」都调用它。
- `POST /api/provider-probe` 是无渠道的临时探测（`provider_id`、`base_url`、`api_key`、`headers`、`api_type`、`protocol_family`），不需要已有渠道。

## 路由选择与 responses_strategy

入口与执行模式定义在 `internal/routeplan`：客户端入口有 `chat_completions`（`/v1/chat/completions`）、`responses`（`/v1/responses`）、`anthropic_messages`（`/v1/messages`）；执行模式有 `proxy_pass` 与 `responses_server`。`internal/routeplan` 现在只保留路由词汇/DTO 与唯一的 `DecideResponses` 决策函数，**不再有第二套 planner**；候选过滤、健康与选择全部由 `internal/router` 提供，代理热路径（`SelectWithBody`）和 Monitor Inspector（`POST /api/routing/inspect` → `router.Explain` dry-run）调用同一套逻辑。dry-run 不读写 sticky、不增加 inflight，也不录制；它只回答“如果现在发这个请求会怎么选”。

- Chat Completions 与 Anthropic Messages 只生成 `proxy_pass` 计划；候选过滤顺序为渠道启用 → 模型匹配 → endpoint 能力（`requires_chat_completions` / `requires_anthropic_messages`）→ `HasTools` 时的 tool calling 能力。
- 模型匹配基于渠道的启用模型集合与别名；`UpstreamCandidate.ModelCapabilities` 支持按模型覆盖能力，键大小写不敏感。
- `/v1/responses` 由 `responses_strategy` 决定 native 直通与本地 runtime 的选择顺序：`auto`（默认）与 `prefer_native` 优先 native、native 不可用时才用本地 runtime；`prefer_local_server` 优先本地 runtime、本地不可用时回退 native；`native_only` 只保留 native；`local_server_only` 只保留本地 runtime；空值或未知取值按 `auto` 处理（`routeplan` 的 `strategy_disallows_*` / `unsupported_responses_strategy` 等常量已删除，inspect 的 `plan.strategy` 会归一为 `auto`）。候选上的 `rank` 不再表示策略排位：它是候选在**选择成本顺序**中的 1-based 位置（最低预期成本在前，同分按 priority、再按候选 id），被过滤的候选为 0；`first_available` 策略下选中的候选就是 rank 1，`p2c` 下选中项可能不是 rank 1。`resolved_model_candidates` 由真实路由的别名展开得到：请求名一条（`source=request`），加上每个 target 把该名解析出的 wire model（`source=alias`，带 `alias` 与 `channel_id`）。
- 各档策略的完整语义与本地 runtime 执行细节见 [RESPONSES_RUNTIME.md](./RESPONSES_RUNTIME.md)；本节只记录路由决策侧的事实。
- `/v1/responses` 的 native/local 判定由 `routeplan.DecideResponses` 统一实现，代理与 Inspector 都调用它：native 可选中则直通；native 存在但本请求不可选时直接拒绝并说明原因；否则在有本地 runtime 且存在 Chat Completions backend 时走 `responses_server`。Inspector 的候选来自同一次 dry-run selection，因此不会再出现「routeplan 列出本地 fallback、热路径却拒绝」的候选序列差异。
- `POST /api/routing/inspect` 的每个 candidate 都带过滤原因（`selected`、`model_not_matched`、`requires_chat_completions`、`requires_responses`、`requires_anthropic_messages`、`requires_tool_calling`、`no_route_candidate` 等），由 router 的真实 candidate 决策映射而来；候选的可服务性由 `upstream.ResolvedUpstream.SupportsRawPath` 判定（协议族、API surface 与 adapter 三者同时满足），与转发热路径共用同一个实现，因此 `api_type` 留空、由 provider preset 决定协议族的渠道不会被误判成 Chat Completions 渠道。V3 cassette 的 `routing.route_plan` 事件由 router 生成，其 `candidate_summary[].filter_reason` 取值为 `unsupported_path`、`unsupported_model`、`unsupported_tools`、`native_responses_required`（标记为 pass-through 的 `/v1/responses` 只接受原生 Responses target：该 target 能通过本地 runtime 服务同一路径，但无法不改写地接收请求）、`target_open`、`target_probation_full`、`model_open`、`model_probation_full`、`excluded`（选中项为空；空字符串表示未被过滤），失败原因写在 `failure_reason`。
- native-vs-local 按模型解析：`channel_models.supports_responses` / `supports_chat_completions` 的显式值覆盖渠道级 `api_type` / `capabilities`，未声明则回退渠道级；数据库对应表为 `channel_models.supports_*`。**native 直通需要显式声明**：模型的 `supports_responses` 显式为 true，或渠道 `api_type` 为 `responses` / `responses_native`；渠道级 `capabilities.responses=true` 本身只表示 `/v1/responses` 可由本地 runtime 翻译，不再被当成原生 Responses 的证据（只有渠道显式 `chat_completions: false`、没有本地 runtime backend 时才把 responses capability 解释为原生）。
- 负载均衡策略取自 router 配置 `router.selection.policy`：`p2c`（默认）或 `first_available`；缺失模型没有回退开关（见下一节的严格授权）；上游模型发现由每个 target 的 `model_discovery` 控制：`list_models`（默认）、`static_only`、`disabled`。`router.selection.policy` 在 `config.Load` 时校验，无法识别的取值会直接失败并列出可选值（与 `limits.scope` 相同）；数值字段同样在加载时校验有限性——YAML 的 `.nan`/`.inf`/`-.inf` 会被 `validateFiniteNumbers` 直接拒绝并报出字段路径（如 `router.selection.epsilon`）。数据库渠道的 `weight`/`capacity_hint` 由 router 的 `defaultFloat` 把非有限值按默认值处理，防止存储行进入热路径。空值仍然合法，表示使用默认。候选在打分前先按健康/退避过滤（`router.selection.open_window` 默认 15s、`selection.failure_threshold` 默认 3，以及 EWMA 驱动的 degraded/probation/open 状态；对应 `filter_reason` 为 `target_open`、`target_probation_full`、`model_open`、`model_probation_full`），再按 EWMA 成本与 priority/weight/capacity 打分。成本打分读取的请求特征是：`EstPromptTokens`（按请求体字节数估算）、输出上限 `MaxTokens`（依次取 `max_output_tokens`、`max_completion_tokens`、`max_tokens`，Google generateContent 族取 `generationConfig.maxOutputTokens`，都缺省时用 512）、以及流式标志 `Stream`（OpenAI/Anthropic 取请求体的 `stream`，Google 取请求行里的 `:streamGenerateContent`——与 `upstream.BuildURL` 添加 `alt=sse` 用的是同一个信号）。
- 上游失败的重试与健康判定按状态码分类：`404`（该渠道没有这个模型）、`402`（渠道余额/额度不足）、`429` 与 `5xx` 会在本轮剩余候选上继续尝试，其中 `429`/`5xx` 还会在 30s 重试预算内退避重试，`404`/`402` 只尝试本轮候选就结束（模型是否存在、账户是否有余额都不会在退避窗口内恢复）。`402` 同时计入模型级健康失败（`router.countsAsUpstreamHealthFailure`），所以「渠道 × 模型」会按 `selection.failure_threshold` / `selection.open_window` 熔断，sticky 绑定在该目标不可选后记录 `break` 并改绑到仍可用的渠道；否则一个已欠费的渠道会一直显示 healthy，每次请求都先打它一次。全部候选都以可重试状态失败时，客户端收到 `502 upstream_error`，错误文本里保留最后一次上游状态、渠道与 provider 的原文片段。
- `400`/`401`/`403` 不重试、也不计健康失败：它们按原样透传给客户端。`401`/`403` 通常是该渠道的凭据、权限或请求级拒绝（后者与渠道无关），把它们改写成 502 会掩盖真正的错误；这类问题需要操作者修复渠道配置。
- 模型授权是严格的：路由只使用 `channel_models` 中**已启用**且**当前可路由**的行，加上该行的 `upstream_model` 线上名。没有任何「允许未知模型」开关。
  - 模型状态（`internal/channel/service.go` 的 `ModelAvailability`）：`enabled`（已启用且最近一次成功同步仍返回它）、`disabled`（操作者关闭）、`removed`（最近一次成功同步不再返回它，操作者的开关保留但不参与路由）、`unverified`（操作者声明的模型——手动添加的 `static` 行——或渠道从未成功同步过；可路由但标记未验证）。`channel.last_probe_at` 只在成功探测时推进，失败的同步不会把任何模型标成 `removed`，也不会把已判定的模型翻成 `unverified`，上一次成功快照继续生效。升级注意：旧版本把失败探测的时间也写进 `channel_configs.last_probe_at`，这类库里的 `discovered` 模型可能暂时被判成 `removed`，直到一次成功同步重新锚定（重启不会修复，Monitor 的 探测/同步模型 可以）；`manual`/`static` 模型不受影响。
  - 未开启或未声明的模型由代理直接回答 **404**：`model_not_enabled`（在某个渠道声明过但不可路由，提示去供应商页开启/重新同步）或 `model_unknown`（没有渠道声明过，提示同步模型）；不会发往任何上游，也不会被包装成 502 `upstream_error`。
  - `/v1/models` 聚合列表只包含 `enabled` 与 `unverified` 的模型；`upstream_model` 线上名可路由但不出现在列表里。
  - 数据库渠道一律严格：只服务 `channel_models` 中启用的模型及其 `upstream_model` wire name。仅 `internal/router` 的内存单 upstream 投影（`Config.Upstream`，不再来自 YAML）保留未声明模型透传，供本地测试与旧调用方使用；生产启动始终由 `RuntimeTargets` 生成带 `ConfiguredModelsOnly` 的数据库目标。
- 别名在 proxy 侧改写上游请求体 `model`：`Target.ResolveModelAlias` 命中时把下游模型名替换为 `target_model`，route plan event 同时记录 `requested_model` 与 `upstream_model`。

## Sticky Route Target

- route target 身份是 `<channel_id>:<credential_id>`，没有显式凭据时为 `<channel_id>:default`。
- sticky key 依次从请求头 `Session_id`、`X-Session-Id`、`Session-Id`、`X-Conversation-Id`、`X-Claude-Code-Session-Id`、`X-Codex-Window-Id`（取 `:` 前前缀）、`X-Codex-Turn-Metadata` 的 `session_id` 提取；都没有时回退到请求体的 `previous_response_id`、`prompt_cache_key`、`metadata.session_id`、`conversation_id`、`session_id`。body 只解析一次；格式错误的 body 视为没有会话键。
- binding key 是 **`session + 模型名`** 的组合（模型名小写）：粘住的不是“这个会话”，而是“这个会话里的这个模型”。这样同一会话切到另一个模型时不会覆盖前一个模型的绑定，两个模型各自的供应商 prompt cache 都能继续命中。没有模型名时保持历史上的 session-only key；没有 session key 时不绑定。
- binding 记录在进程内的 `StickyBindingStore`，TTL 由 `router.selection.sticky_ttl` 控制（默认 1h，`0` 也按默认值处理）；`router.selection.sticky: false` 关闭整个绑定：不读、不写，也不产生任何 `routing.sticky.*` 事件，进程重启会清空全部绑定。
- 命中且 target 仍可用时复用同一 route target（因此也复用了上游的 prompt cache）；命中但 target 不可用（熔断打开、被删除）时记录一次 `sticky break` 并重新绑定；未命中则正常选择后绑定。
- 命中时如果绑定目标正在执行请求（inflight > 0）且还有其他可用目标，本次请求交给正常的成本选择（记录 `sticky spill`），不排队等待绑定目标、也不改写绑定；绑定目标空闲后，后续顺序请求仍回到它。这正是“快机器空闲、慢机器仍在执行”时避免把新请求继续压到慢机器的机制。
- 命中后还会做一次延迟复核：当绑定目标的已测延迟成本（TTFT + 总耗时的 EWMA，按 capacity 缩放；不含 inflight 与熔断惩罚）超过最便宜候选的 `router.selection.sticky_max_cost_ratio` 倍（默认 `2.0`，`0` 关闭该检查）时，改绑到延迟最低的候选并记录 `sticky rebalance`。这是避免会话被钉在明显更慢的硬件上的机制；代价是失去原目标的 prompt cache，因此阈值刻意取 2 倍而不是“只要更便宜就换”，等速双机不会来回抖动。瞬时排队不算“更慢”，不会仅因为绑定目标正忙就换绑。
- 重试时的排除（`SelectWithExclusion` 的 `excludeIDs`）只对当前请求生效。被排除的恰好是当前绑定时，记录 `sticky hold` 但**保留**绑定，本次请求走备用目标，下一次请求仍先试原目标；只有原目标真的不可用（健康/模型熔断、被移除）才走 `break` + rebind。这样一次瞬时 500 不会把整个会话改绑到备用目标并钉住一个 TTL。
- Monitor、MCP 与 cassette event 只暴露 `sticky_key_fingerprint`（sha256 前缀），不暴露原始 session 值。`sticky_status` 的取值为 `hit`、`miss`、`bind`、`break`、`hold`、`spill`、`rebalance`。

## 凭据与 route target

- 渠道负责 base URL、provider preset、routing profile、priority/weight/capacity 与模型覆盖；API key 与自定义 headers 加密保存在渠道行内，router/proxy 只在内存中使用明文。
- 每个数据库渠道展开为一个 route target（`route_target_id = <channel_id>:default`），凭据 id 为 `default`。
- API key 由 Monitor 的渠道表单写入 `channel_configs.api_key_ciphertext`，不来自 YAML，也不进入 route target ID。
- `concurrency_limit` 不是渠道字段；并发限制由下方 limit scope 统一处理。

## Limit 与 scope

`limit` 由 `LimitConfig`（`enabled`、`scope`、`max_concurrent`、`max_queued`、`channel_key_header`）控制，作用在代理热路径上。

- scope 取值只有 `global`、`header`、`channel`、`route_target`、`credential`。
- 生效值由 `ScopeOrDefault` 决定：显式 `scope` 优先；未写 `scope` 但写了 `channel_key_header` 时按 `header`；否则按 `global`。
- 当 `limits.enabled = true` 时，未知 scope 在配置加载期直接报错 `limits.scope %q is not supported; use one of global, header, channel, route_target, credential`；`header` scope 缺少 `limits.channel_key_header` 也会报错。因此不会出现“限流被静默忽略”。
- pre-selection 判定 `global` 与 `header`（按 `channel_key_header` 取值分桶，取不到时归入 `missing`）。
- post-selection 判定 `channel`（键为 channel ID）、`route_target`（键为 route target ID）、`credential`（键为 channel + credential）——三者都依赖 router 选择结果中的凭据身份。
- 拒绝语义：并发拒绝返回 `429` 与 `limit.concurrency_rejected`；队列饱和返回 `503` 与 `limit.queue_saturated`。当前没有速率（时间窗口）限制。
- 拒绝会写入带 `limit_key_fingerprint` 的事件，不写原始 limit key。
- tracked 的 `config/config.yaml` 目前没有 `limits` 示例块，实际字段以 `internal/config` 结构体为准。

## 安全 metadata 与脱敏

V3 cassette routing event 实际会写入的安全字段包括：`route_target_id`、`channel_id`、`credential_id`、`credential_hint`、`sticky_key_fingerprint`、`candidate` 列表中的 `base_url`（经过 `redaction.DisplayURL`）、`excluded` 与 `filter_reason`。

- `credential_selectable`、`credential_health_state` 与 `credential_filter_reason` 在 `router.CandidateDecision` 结构与 `candidateEventAttributes` 读取路径中存在，但生产代码没有任何赋值点（只有测试会赋值），因此事件中不会出现；它们当前是只读字段。
- 禁止写入 cassette、日志、Monitor JSON 或 MCP 输出：API key、bearer token、OAuth access/refresh token、service-account JSON、自定义 auth header 值、完整 raw sticky key。
- `redaction.DisplayURL` 会把 URL userinfo 中的密码替换为 `REDACTED`，名字本身像密钥的 userinfo（`https://<key>@host`，或形如 `apikey123` 的用户名）也替换为 `REDACTED`，并把名字含 `key`、`token`、`secret`、`password`、`passwd`、`credential`、`signature`、`sig`、`access_token`、`api_key`、`authorization`、`auth`、`dsn` 的 query 参数值替换为 `REDACTED`。
- `redaction.IsSensitiveURLParam` 是上面这份 marker 列表的唯一所有者：recorder/proxy、CLI `config inspect` 与 `legacymigrate.MaskSecret` 都走它。`MaskSecret` 对含 `://` 的值直接返回 `redaction.DisplayURL` 的结果，所以 CLI 诊断输出里的 DSN 掩码与运行期脱敏同为 `REDACTED`（旧实现自己判断 URL 并且只改密码段，会把 `?api_key=...` 原样打印出来）。
- `redaction.SafeCredentialHint` 先做 metadata 脱敏再截断到 32 字符，值等于 `REDACTED` 时按空处理。
- 渠道 API 返回值中的 `headers` 会对敏感 header 名（如 `Authorization`、`api-key`、`token` 类）掩码为 `***`；写路径接受 `{ "keep": true }` 表示保留原值，避免 UI 回传掩码覆盖真实密钥。
- 渠道 API 始终不返回明文 API key，只返回 `api_key_hint` 与 `secret_storage_mode`；`base_url` 按配置原样返回，路由事件里的候选 base URL 才会做 URL 脱敏。
- 本地 secret：API key 与敏感 header 以 `tlsec:v1:` envelope 保存，主密钥文件是 `{{output_dir}}/trace_index.secret`（权限 `0600`，本地 AES-GCM）；历史明文保持可读，下次写入时转为加密 envelope。
- 运维入口：`trajecta db secret status`（检查 key 文件、可读性与 fingerprint）、`db secret export --out backup.key`、`db secret rotate --yes`（备份旧 key、生成新 key、全量重加密）；Monitor 侧对应 `/api/secrets/local-key` 的状态、导出与轮换。
- 没有加密能力时 `secret_storage_mode` 为 `plaintext-local`，UI 据此提示本地风险。

## Monitor 与 MCP 的管理入口

Monitor 侧是渠道/模型/别名配置的主入口：导航中的「渠道」页（canonical `/channels`，API 仍为 `/api/channels`；`/providers` 只保留重定向）负责渠道创建、编辑、启停、探测、模型同步与接口自动识别；「模型」页负责模型广场、模型详情、模型启停、接口覆盖与别名管理；原来的「路由」页已折叠：路由设置与 dry-run 检查进入渠道页底部的高级卡片，`/routing` 只保留重定向；「接入」页（客户端接入 tab）展示协议入口。selected route 记录仍可在流量页与 trace 详情里查，不再单独成页。

MCP 侧提供查询工具与受控重分析动作工具，但没有渠道/模型/别名写入口：

- `list_upstreams`：上游分析列表。
- `query_routing_decisions`：单条 trace 的路由决策事件、候选、选中上游与失败原因。
- `query_sticky_routing`：按状态、上游、前一上游与 fingerprint 过滤 `routing.sticky.*` 事件。
- `query_failures`、`summarize_failure_clusters`：失败 trace 与失败聚类。
- `reanalyze_trace`、`reanalyze_session`：对单条 trace / 单个 session 运行或入队受控重分析（动作工具，不是只读查询）。

CLI 侧的管理入口包括 `trajecta provider probe` / `probe-report` / `probe-apply`、`models codex-config`、`db secret status/export/rotate`。

## Replay 兼容性

凭据路由不改变 replay 事实：

- 原始 HTTP 请求与响应字节仍然完整保留。
- V3 routing event 是附加 metadata；V2 cassette 仍可读。
- `pkg/replay` 不依赖渠道或凭据存储。
- replay 不会刷新凭据、查询账号、重新执行 sticky binding 或修改健康状态。
- 旧 cassette 只有 `upstream_id` 时，Monitor 与 MCP 退回 upstream 级展示。

## 非目标与未实现

- 不做多租户计费、团队权限、集中式云管理与订阅转售。
- 转发热路径默认不做跨协议翻译；`router.selection.protocol_conversion: true` 时只在没有可用原生候选时对非流式 chat/messages/gemini 请求做转换（原生候选始终优先），`/v1/responses` 仍由本地 Responses runtime 负责，流式不转换。
- 没有 `POST /api/router/reload`，也无需手动 reload：管理写成功即自动发布新快照。
- 没有 `POST /api/channels/{id}/enable`、`/disable` 与 `models:bulk-update`；启停走 `PATCH`，批量启停走 `PATCH /api/channels/{id}/models/batch`。
- 没有 `GET /api/models/{model}/channels`、`GET /api/models/{model}/trends`、`GET /api/channels/{id}/trends`；覆盖与趋势在模型详情与渠道详情响应内返回。
- `selection_policy`、`route_plan_log_level` 会被持久化与校验，但当前没有作用于运行中的 router；运行时策略来自 `app_settings` 的 `routing.settings`。`missing_model_policy` 已随「允许未知模型」一起移除。
- `credentials[].concurrency_limit` 没有运行时消费者。
- 没有 `unsupported_entrypoint`、`unsupported_responses_strategy`、`channel_not_enabled`、`requires_local_responses_runtime`、`strategy_disallows_native_responses`、`strategy_disallows_chat_fallback` 这些候选原因：`internal/routeplan` 已删除这六个没有生产者的常量（并有包测试防止再声明死值），inspect 只输出 `router.Explain` 真实会产出的 reason；未知 `responses_strategy` 按 `auto` 处理。
- 没有独立的 `route_decisions` 表；路由决策以 V3 cassette event 与日志字段形式保存。
- 没有渠道导入/导出能力。
- `model_catalog` 只由探测与人工写回填充，没有独立的目录管理页面或 API。
- `POST /api/settings/channels` 不存在；该端点只有 `GET` 与 `DELETE`。
