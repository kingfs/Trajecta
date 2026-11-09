# 数据放置与证据模型

> 状态：Draft v0.1  
> 更新时间：2026-11-09  
> 关联：[总体架构](./01-overall-architecture.md) 第 10、11 节

## 1. 结论

Trajecta 的数据分三类，边界必须清晰：

1. **文件（cassette）**：只存原始 HTTP 交换 + 最小解析元数据。它是 replay 和审计的事实源，不是运行日志，也不是路由决策流水账。
2. **数据库**：存结构化、可查询、可重建的索引和配置。路由决策、usage、session、findings、凭据密文都在这里。
3. **内存**：存运行态和缓存。重启丢失可以接受；不能接受丢失的，必须落 DB。

一句话：**文件回答"客户端和上游到底交换了什么字节"；DB 回答"为什么这样路由、结果如何"；内存回答"现在哪条链路健康、哪个会话粘在哪里"。**

因此：

- **不引入 Cassette V4 的双段格式**。继续使用 V3：magic + 一行 meta + 可选 event + 原始 request + 原始 response。
- **仓库默认开启 `trace.minimal_cassette`**：写最小 meta、不写 event，routing/usage/harness/摘要仍进 DB；未写该键的旧配置仍保留完整 prelude。
- **目标态默认不写 event**。路由、usage、harness、mask、conversion 摘要全部进 DB；只保留极少数"不写就无法解释原始字节"的异常标记。
- **默认只在最终 exchange 上创建 cassette**。失败重试只记 DB，不产生文件；成功/最终失败才写一个文件。
- **不把上游 wire 字节默认写进文件**。需要精确取证时，用 opt-in 的 wire capture（sidecar），默认关闭。
- **路由信息不进 cassette**。它属于 DB 的 `logs` / `route_attempts` / `route_decisions`。

## 2. 数据放置总表

| 数据 | 位置 | 理由 | 丢失后果 |
| --- | --- | --- | --- |
| 原始 HTTP request | 文件 | replay/审计事实源 | 无法回放/审计 |
| 原始 HTTP response | 文件 | 同上 | 无法回放/审计 |
| trace 时间、endpoint、model、protocol、stream | 文件 meta + DB 索引 | 解析和关联所需的最小元数据 | 可从 raw 重新推导，但成本高 |
| exchange 结构（entry/model、parent、sequence） | 文件 meta + DB | 本地 Responses 的子交换无法只从 raw path 推导 | 轨迹重建会丢失层级 |
| 路由候选、选中 target、tier、affinity | DB | 可查询、可聚合；不是字节事实 | 丢失"为什么这样路由"，raw 仍在 |
| 每次重试的状态/延迟/错误 | DB | 失败分析；不应产生多个文件 | 丢失失败尝试历史 |
| usage | DB | 可从 raw response 重算；列表/聚合需要索引 | 丢失统计，可重算 |
| harness 分类 | DB | 可从 request headers 重算 | 丢失筛选标签，可重算 |
| session 分组 | DB（fingerprint） | 可从 headers/body 重算；用于查询 | 丢失分组，可重算 |
| mask 真值/假值对 | DB（加密） | 安全审计；不能进 prelude | 丢失遮蔽审计 |
| mask 映射/抑制名单 | 本地 bbolt 文件（现有 `secret_mask_mappings.db`） | 安全策略状态；不是 trace 证据 | 假值不稳定/抑制失效 |
| mask 规则/偏移事件 | DB | 结构化查询；不是字节事实 | 可重新检测，但历史不同 |
| 协议转换摘要/loss | DB | 结构化查询；用于解释响应形状 | 丢失转换解释 |
| 上游 wire 字节（mask/convert 后） | 默认不存；opt-in sidecar | 体积大、可用 transform 证据近似；精确取证才需要 | 无法逐字节证明上游收到什么 |
| 渠道/模型/优先级/凭据密文 | DB | 配置事实 | 丢失配置 |
| 路由健康、EWMA、熔断、inflight | 内存 | 高频变化；重启重建 | 短暂失去健康判断，可接受 |
| 软亲和绑定 | 内存 | 缓存局部性优化；重启丢失只影响 cache hit | 可接受 |
| 硬亲和 `response_id → upstream` | DB | 客户端可能在重启后继续会话 | 会话可能无法继续 |
| 编译后的 route/hook plan、模型快照 | 内存 | 配置变更时重建 | 启动时重建，可接受 |
| 限流计数 | 内存（单进程） | 高频；多实例时另议 | 重启后短时放行，可接受 |
| 解密后的凭据 | 内存 | 不落盘 | 需重新解密，可接受 |
| Observation IR 节点树 | 不持久化 | 可从 raw 重算 | 无 |
| findings / resources / ATIF | DB 或派生缓存 | 可重算；查询需要索引 | 可重算 |
| audit 响应缓存 | 内存/磁盘缓存 | 纯派生 | 可删除 |

## 3. Cassette 设计

### 3.1 文件内容

V3 保持不变：

```text
# trajecta/v3
# meta: {"version":"LLM_PROXY_V3","trace_id":"...","time":"...","endpoint":"/v1/chat/completions","protocol":"chat_completions","model":"...","stream":true,"exchange_kind":"entry","exchange_role":"client_request"}
<空行>
<原始 HTTP request bytes>
<分隔换行>
<原始 HTTP response bytes>
```

注意：cassette 不是唯一的文件。`secret_mask_mappings.db` 是本地 bbolt 状态文件，存 mask 映射和抑制名单，不是 per-trace 原始证据；它不属于 cassette 格式。

默认（当前兼容行为）：

- 保留现有完整 meta 和 event；
- `trace.minimal_cassette: true` 时：0 条 `# event:`，meta 只含解析/关联必需字段；
- request/response 是原始字节，不做 JSON 美化、不截断（除非达到配置上限，并标记 truncated）。

### 3.2 最小 meta

| 字段 | 是否必需 | 说明 |
| --- | --- | --- |
| `version` | 是 | `LLM_PROXY_V3` |
| `trace_id` | 是 | 与 DB 主键一致；文件名的来源 |
| `time` | 是 | raw HTTP 本身没有可靠时间 |
| `endpoint` | 是 | 归一化 endpoint；raw path 可能是别名 |
| `protocol` | 是 | 解析器选择；可由 endpoint 推导，但显式更稳 |
| `model` | 是 | 列表/索引/重解析便利；可从 body 推导 |
| `stream` | 是 | 响应解析形状；可从 content-type/body 推导 |
| `exchange_kind` | 条件 | `entry` / `model`；本地 Responses 子交换需要 |
| `exchange_role` | 条件 | `client_request` / `primary_model_call` 等 |
| `parent_exchange_id` | 条件 | 子交换层级 |
| `sequence_index` | 条件 | 同一 trace 内顺序 |
| `truncated` | 条件 | 达到 body 上限时为 true |
| `synthetic` | 条件 | chaos/本地合成响应，raw response 不是上游返回 |

不进入 meta：

- 路由候选、选中 target、tier、affinity、sticky 状态；
- usage 数字、TTFT、duration；
- harness 名称/版本/置信度；
- client IP、user agent（raw request headers 已有，DB 索引另存）；
- mask 规则/偏移/真值；
- 转换 loss、wire model；
- 错误分类（raw response 或 DB 有）。

这些都可以从 raw + DB 配置重建，或本来就不该由文件回答。

### 3.3 event 政策

`# event:` 保留给"无法从 raw 字节解释"的极少数情况：

- `synthetic`：响应是本地合成的（chaos、本地错误包）；
- `truncated`：body 被上限截断；
- `wire_capture`：opt-in 的 wire 字节 sidecar 引用；
- `format_anomaly`：写入过程中发现的结构异常。

路由、usage、timeline、mask、conversion 不写 event。现有 `pkg/recordfile` 继续支持读取旧 event；新写入默认不产生。

### 3.4 写入路径与文件操作

目标：**每个最终 exchange 一次创建、一次顺序写、一次 rename；重试不产生文件。**

```text
请求开始
  ├─ 生成 trace_id
  ├─ 读取 request body（内存）
  ├─ 路由/重试（失败只写 DB route_attempts）
  └─ 最终 attempt:
       1. 创建 <trace_id>.http.tmp
       2. 写 magic + meta + 空行
       3. 写 raw request
       4. 写分隔换行
       5. 写 raw response（流式边读边写）
       6. close
       7. rename 为 <trace_id>.http
       8. 异步 finalize：DB 索引/usage/parse job
```

好处：

- 不再"先写 body，再读回文件、prepend prelude、重写"；
- 不再为每个失败 attempt 创建/删除文件；
- 流式响应只写一遍；
- 文件在 rename 前不可见，避免读到半成品；
- 崩溃留下 `.tmp`，由启动清理任务删除。

失败场景：

- 连接失败/超时，没有 response：不创建 cassette；DB 记录 attempt 的 target/status/error；
- 上游返回可重试状态：同上，继续下一个 attempt；最后一个失败仍不创建 cassette；
- 客户端取消：如果已有部分响应，按策略写 `.tmp` 并标记 `truncated`/`canceled`，或删除；默认删除，DB 记录 canceled；
- 进程崩溃：`.tmp` 清理；DB 可能没有 trace 行，接受。

### 3.5 上游 wire 字节

默认不存。原因：

- 体积可能翻倍；
- 大多数审计问题可以用 raw client 字节 + DB 的 mask audit/conversion 摘要回答；
- 文件操作和存储成本需要控制。

需要精确取证时，配置：

```yaml
cassette:
  capture_wire: false   # 默认
  wire_dir: ""          # 为空则与 cassette 同目录
```

开启后写 `<trace_id>.wire.http`，内容：

```text
# trajecta/wire/v1
# meta: {"trace_id":"...","direction":"request","stage":"egress",...}
<masked/converted 后的 request bytes>

# meta: {"trace_id":"...","direction":"response","stage":"ingress",...}
<upstream 原始 response bytes>
```

它是 sidecar，不是主 cassette；删除不影响 replay/审计。默认关闭，只在排障/合规需要时开启。

## 4. 数据库

### 4.1 配置类

- `channel_configs`：渠道、endpoint、协议、优先级、权重、启用、preset、plan。
- `channel_models`：模型、wire names、协议、启用、来源、metadata。
- `channel_credentials`：凭据密文、auth_type、expiry、account、plan、scopes、status。
- `route_policies`：全局/模型级 `priority_mode`、conversion 开关、affinity TTL。
- `hook_configs`：Hook 启用、scope、config。
- `model_aliases`、`model_catalog`、`app_settings`。

### 4.2 流量索引类

`logs`（或 `traces`）至少保留：

```text
trace_id, cassette_path, time, endpoint, protocol, model, stream,
status_code, duration_ms, ttft_ms, request_bytes, response_bytes,
client_harness, session_fingerprint,
selected_channel_id, selected_route_target_id, selected_credential_id,
route_mode, route_tier, sticky_status,
conversion_from, conversion_to, conversion_loss_count,
error_class, error_text
```

`route_attempts`：

```text
trace_id, attempt_index, channel_id, route_target_id, credential_id,
status_code, duration_ms, failure_class, retry_delay_ms, created_at
```

`route_decisions`（可选，明细）：

```text
trace_id, candidate_id, tier, mode, selectable, filter_reason, score
```

这样路由排障不依赖 cassette event。

### 4.3 审计类

- `trace_observations`：解析摘要；节点树不落库。
- `findings`：检测结果，带 evidence path。
- `resources` / `trace_resources`：资源索引。
- `credential_mask_audits`：真值/假值对，加密。
- `analysis_jobs`：重分析任务。
- `execution_events` / `request_audits`：本地 Responses 运行时的结构化审计。

### 4.4 会话与硬亲和

- `session_summaries`：会话聚合。
- `response_routes`（新增，或并入 Responses state）：

```text
response_id, model, channel_id, route_target_id, wire_protocol,
expires_at, created_at
```

`previous_response_id` / `conversation` 请求在重启后仍能找回上游；这是硬亲和必须落 DB 的原因。软亲和不需要。

## 5. 内存

只放"丢失可接受"或"可快速重建"的状态：

- 路由健康：连续失败、熔断窗口、EWMA TTFT/延迟/错误率；
- inflight / 并发计数；
- 软亲和绑定（session fingerprint + model → target）；
- 编译后的 route plan / hook plan；
- 渠道/模型快照；
- 模型广场/审计结果缓存；
- 限流计数（单进程）；
- 解密后的凭据；
- 请求上下文、XIR Document、流式缓冲。

规则：

- 任何内存状态必须能从 DB/文件重建，或明确接受丢失；
- 硬亲和、凭据、配置不能只放内存；
- 内存缓存必须有上限和过期。

## 6. 最小 cassette 的落地状态

`internal/recorder` 已支持 `trace.minimal_cassette`（默认 false）：

- prelude 使用 `minimalCassetteHeader`：保留 `version`、`layout`、`trace_id`/`exchange_*`/`time`/`model`/`provider`/`operation`/`endpoint`/`url`/`method`/`status_code`/`content_length`，去掉 routing/harness/client_ip/duration/usage；
- 不写 timeline/event；
- DB 索引仍用完整 `LogInfo.Header`，因此列表/统计不丢；
- 默认关闭，避免 Monitor 旧的 event 读取路径突然失去数据；
- 迁移完成后可把默认改为 true，并删除旧 event 读取。

## 7. 为什么不做 Cassette V4

最初提出 V4 的理由是：mask/convert 后，客户端看到的字节和上游收到的字节不同，想在一个文件里同时保存两份。

重新评估后，不采用：

1. **文件是原始证据，不是决策日志。** 双段会把"客户端交换"和"上游 wire"混在一个文件里，解析、replay、人工阅读都更复杂。
2. **路由/usage/转换摘要属于 DB。** 它们可查询、可聚合、可重建，放文件会重复且容易不一致。
3. **文件操作更多。** 双段需要更多写入/定位逻辑；当前 recorder 已经为每个 attempt 写文件、失败删除、finalize 时 prepend prelude 重写。再加双段只会放大问题。
4. **大多数审计不需要精确 wire 字节。** raw client 字节 + mask audit + conversion summary 足以回答"发生了什么"；需要逐字节取证时用 opt-in sidecar。
5. **replay 只依赖客户端可见交换。** V4 对 replay 没有收益。

因此：**V3 继续，meta 最小化，event 默认关闭，wire 用 opt-in sidecar。** 如果未来出现必须默认保存 wire 的合规需求，再单独评估，不现在引入。

## 8. 审计如何以文件为核心

```text
打开 trace
  → 读 DB 索引（路由、usage、harness、session、conversion 摘要）
  → 读 cassette raw request/response
  → XIR parse
  → audit hooks
  → findings（可选持久化）
```

- 文件提供字节事实；
- DB 提供上下文和索引；
- 两者通过 `trace_id`/`cassette_path` 关联；
- 没有 DB 时，仍可单独解析 raw 文件，只是丢失路由/session 上下文；
- 没有文件时，DB 索引不能替代原始证据。

## 9. 兼容与迁移

- 读取端继续支持 V3/V2 和旧 event；写入端只写 V3。
- 读取端必须在 meta 字段缺失时从 raw request 推导 `method`/`url`/`model`/`endpoint` 等，不能假设旧字段一定存在。
- 写入端可以先继续填现有 `MetaData` 字段，再逐步改为最小字段；改 `pkg/recordfile` 时先保证旧 reader/新 reader 都能读。
- 现有 `BuildEvents` 仍可用于测试/旧路径；新 writer 默认不调用。
- Monitor 当前从 cassette 读 timeline/usage；迁移期改为：
  - 列表/统计：DB；
  - trace 详情：DB 索引 + 按需 parse raw；
  - 旧 trace：继续读 event，保证兼容。
- 文件命名改为 `trace_id.http`；旧文件继续按路径读取。
- `.tmp` 清理任务在 serve 启动时运行。

## 10. 开放问题

| 问题 | 建议 |
| --- | --- |
| 失败 attempt 是否要可选保存 raw request？ | 默认不存；调试开关 `cassette.capture_failed_attempts` |
| `trace_id` 在请求开始时生成，还是在选中 target 后？ | 请求开始时生成，便于失败 attempt 关联 |
| `status_code` 是否进 meta？ | 可进 DB；meta 可不放，raw response 有 |
| `duration_ms` 是否进 meta？ | 不进；DB 有 |
| 大 body 截断阈值？ | 沿用请求体上限；截断时 meta `truncated=true` |
| sidecar 目录/清理？ | 与 cassette 同目录；按保留策略删除 |
| 多实例限流/亲和？ | 当前单进程；未来用 DB/Redis，另立决策 |
