# 协议族与上游 Provider

Trajecta 不为每个上游写一套独立集成，而是把上游解析为协议族（`protocol_family`）、路由 profile（`routing_profile`），以及鉴权、版本、header 与 URL 构造规则。代理是协议感知的透传录制与路由层：它按协议族识别请求、录制 `.http` cassette、把 trace 解析为 Observation IR，但不在转发路径上把一个协议族的请求 schema 翻译成另一个。

协议 schema 快照与英文事实源见 [协议参考](./protocol-reference/README.md)、[已实现的协议](./protocol-reference/implemented-protocols.md) 与 [协议差异](./protocol-reference/protocol-differences.md)。

## 术语

- **Provider**：上游公司、公司网关或本地/自托管代理，即 provider preset 描述的对象；「一个上游连接 + 凭据」的正式称呼是渠道（channel），见下。
- **协议族（`protocol_family`）**：上游 API 请求/响应 schema、认证方式与 endpoint 形态的归类。当前有 4 个：`openai_compatible`、`anthropic_messages`、`google_genai`、`vertex_native`。
- **路由 profile（`routing_profile`）**：同一协议族内的 URL 构造与鉴权差异，例如 Azure、vLLM、Vertex express 与 project/location。
- **客户端入口（entrypoint）**：Trajecta 暴露给客户端的 URL 形态，只选择客户端 API 形态，默认不跨协议族翻译（开启 `router.selection.protocol_conversion` 后按 [协议转换](./PROTOCOL_CONVERSION.md) 的规则兜底）。
- **能力（capability）**：`capabilities.*` 布尔声明，用于按 endpoint 判断某个上游或模型能否服务某类请求。

内部实现与数据库仍沿用 `channel_*` 命名：`channel_configs`、`channel_models`、`/api/channels`。这些是实现细节。Monitor UI 的正式页面是「渠道」`/channels`（API 为 `/api/channels`），`/providers`、`/providers/:id` 只保留到 `/channels` 的旧地址重定向；`provider` 只指上游公司或 provider preset。存储细节见 [存储与部署](./STORAGE_AND_DEPLOYMENT.md)。

## 协议族与请求入口

客户端入口前缀在 routing、recording、forwarding 之前由 `internal/proxy/entrypoints.go` 归一化。原始请求 body 保持原样；`.http` 记录中的协议路径保持规范化，因此 parser 与 replay 稳定。

| 客户端入口 | 归一化 endpoint | 目标客户端 | 需要的 provider 能力 |
| --- | --- | --- | --- |
| `/v1/chat/completions` | `/v1/chat/completions` | OpenAI 兼容 SDK 与旧集成 | `openai_compatible` Chat Completions |
| `/responses`、`/v1/responses` | `/v1/responses` | Codex 与 OpenAI Responses 客户端 | 匹配的原生 Responses 上游，或可承接本地 Responses runtime 的 Chat Completions 上游 |
| `/anthropic/messages`、`/anthropic/v1/messages`、`/v1/messages` | `/v1/messages` | Claude Code 与 Anthropic SDK | `anthropic_messages` |
| `/anthropic/messages/count_tokens`、`/anthropic/v1/messages/count_tokens`、`/v1/messages/count_tokens` | `/v1/messages/count_tokens` | Anthropic token 计数客户端 | `anthropic_messages` |
| `/v1/tokenize`、`/v1/detokenize` | `/tokenize`、`/detokenize` | vLLM 分词客户端 | `openai_compatible`（整个协议族） |
| `/v1/models/{model}`、`/api/show` | 路径保持原样；由代理在路由前本地合成模型详情 | OpenAI 模型详情与 Ollama show 客户端 | 无（不选择上游） |

`/v1/models` 是跨上游聚合的 OpenAI 兼容模型列表 endpoint，不是单上游透传；`/v1/models/{model}` 对可路由模型或 registry 命中的模型返回详情，其它 id 返回 404（错误码 `model_not_found`），不再为任意 id 合成模型对象；`/api/show` 与 `/v1/models/{model}` 共用同一个「已知模型」判断与同一个条目构造器，因此同样对未知模型返回 404，而它的请求体按 Ollama 现行文档读 `model` 字段并兼容旧的 `name`；列表条目与 `/v1/models/{model}` 详情共用同一个构造器，因此都带 OpenAI 模型 schema 要求的 `created`（固定值，见 [代理使用示例](./PROXY_USAGE_EXAMPLES.md)）。

各协议族的当前 endpoint 覆盖与 parser 覆盖：

| 协议族 | provider 标签 | 路由 profile | 当前 endpoint 覆盖 | parser 覆盖 |
| --- | --- | --- | --- | --- |
| `openai_compatible` | `openai_compatible`、`azure_openai`、`vllm` | `openai_default`、`azure_openai_v1`、`azure_openai_deployment`、`vllm_openai` | `/v1/chat/completions`、`/v1/responses`、`/v1/embeddings`、`/v1/models`、vLLM `/tokenize`、`/detokenize` | Chat Completions、Responses、Models、Tokenization（Observation IR parser）；Embeddings 只被分类与追踪，没有 adapter 因此不可路由 |
| `anthropic_messages` | `anthropic` | `anthropic_default` | `/v1/messages`、`/v1/messages/count_tokens`；连通性与模型发现使用 `/v1/models` | Messages |
| `google_genai` | `google_genai` | `google_ai_studio` | `/v1beta/models/{model}:generateContent`、`/v1beta/models/{model}:streamGenerateContent`、`/v1beta/models` | GenerateContent、streamGenerateContent |
| `vertex_native` | `vertex_native` | `vertex_express`、`vertex_project_location` | Vertex Gemini `generateContent`、`streamGenerateContent`、模型列表路径 | GenerateContent、streamGenerateContent |

`openai_compatible` 要点：

- `upstream.base_url` 必须包含上游 API prefix，例如 `/v1`、`/api/v1`、`/openai`、`/openai/v1`；唯一例外是 `provider_preset: deepseek`，它以 origin 作为 base URL，endpoint 位于 `/responses`、`/models`。
- 客户端 `/tokenize`、`/v1/tokenize`、`/detokenize`、`/v1/detokenize` 的根路径映射在 `openai_compatible` 协议族层面完成（不区分 routing profile），统一路由到上游根路径的 tokenization endpoint；`vllm_openai` profile 下的同类分支代码不可达。
- Embeddings 请求只被分类与追踪。`pkg/llm.AdapterFor` 没有 embeddings 分支，因此该请求在路由选择阶段得到零个候选并失败，`capabilities.embeddings` 也不会被查询；它同样没有 Observation IR parser，不是深度解析目标。
- Responses 与 Chat Completions 是 OpenAI 的两个不同 surface；Codex 流量通常走 `/v1/responses`。

`anthropic_messages` 要点：

- 鉴权重写为 `x-api-key`；`anthropic-version` 缺失时按 `api_version` 补齐，默认 `2023-06-01`。
- 请求与响应 body 原样透传；请求、响应与 streaming 事件会解析进 Observation IR。

`google_genai` 与 `vertex_native` 是独立协议族，因为其 endpoint 形态、认证模型与请求/响应 schema 都不同于 OpenAI 与 Anthropic。两者都经 GenerateContent 语义适配路径解析；Vertex 使用资源路径（`model_resource`，可选的 `project`/`location`）与 `Authorization: Bearer` 鉴权。

`pkg/llm.NormalizeEndpoint` 会保留模型路径上的 `:action` 后缀，使 `:countTokens`、`:embedContent`、`:batchEmbedContents` 这类操作不会被当成模型目录；`pkg/llm.ClassifyPath` 因此也把这些带后缀的形态（`/v1beta/models:<action>`、`/v1/publishers/models:<action>`、`/v1/models:<action>`）归入 `google_genai` / `vertex_native` 协议族，而不是 `unknown`。归类只影响 provider 标签、provider 过滤、协议族路由判定与错误响应包络：这些 endpoint 没有 adapter，仍然不可路由，代理也不会改写它们的路径。

## 协议差异要点

各协议族在概念层的重叠（model、instructions、用户输入、工具定义、生成内容、tool calls、usage、streaming）与逐项差异（请求核心、响应核心、streaming 形态、tool 形态、usage 形态）、"识别不等于转换"的边界，以及由此产生的路由后果，见 [协议差异](./protocol-reference/protocol-differences.md)。

### 代理自身错误的响应形态

代理自己产生的错误（路由失败、上游传输失败、请求体超限、鉴权失败、限流）统一走 `internal/proxy/errors.go` 的包络，形态按入口 endpoint 的协议族选择，与 `pkg/llm.ClassifyPath` 的归类一致：

| 入口协议族 | 形态 | 类型字段取值 |
| --- | --- | --- |
| OpenAI 兼容 | `{"error":{"message","type","code","param"}}` | 400/401/403/404/413 → `invalid_request_error`；429 → `rate_limit_error`；5xx → `api_error` |
| Anthropic | `{"type":"error","error":{"type","message"}}` | 400 → `invalid_request_error`；401 → `authentication_error`；403 → `permission_error`；404 → `not_found_error`；413 → `request_too_large`；429 → `rate_limit_error`；503 → `overloaded_error`；500/502/504 → `api_error` |
| Google GenAI / Vertex | `{"error":{"code","message","status"}}` | 400/413 → `INVALID_ARGUMENT`；401 → `UNAUTHENTICATED`；403 → `PERMISSION_DENIED`；404 → `NOT_FOUND`；429 → `RESOURCE_EXHAUSTED`；502/503 → `UNAVAILABLE`；504 → `DEADLINE_EXCEEDED`；500 → `INTERNAL`；其余 4xx 按调用方错误归类为 `INVALID_ARGUMENT`，其余 5xx 为 `INTERNAL` |
| Ollama 兼容（`/api/show`） | `{"error":"<message>"}` | 该字段是字符串，Ollama 客户端按字符串反序列化 |

代理自身产生的错误不会使用 `http.Error` 之类的纯文本响应：HTTP SDK 只解析各自的错误形态，纯文本会把可读的失败变成客户端解析错误。本地应答的 endpoint（`/v1/messages/count_tokens`、`/v1/models`、`/v1/models/{id}`、`/api/show`）同样走这套包络。`internal/proxy` 的结构性门禁 `TestProxyErrorsNeverFallBackToPlainText` 会扫描源码，发现新的 `http.Error` 调用即失败。

未在表中列出的状态码按状态类别回退（4xx → 调用方错误、5xx → 服务端错误），不再一律回退到服务端错误。响应同时带 `X-Trajecta-Error-Source: proxy` 以区分代理自身错误与上游错误；上游返回的错误体原样透传，不被改写。

## 渠道插件与 Provider Preset 清单

目录的事实源是 `pkg/upstream/presets.go` 的编译期 preset 表，按**渠道插件**组织：一个可见 preset = 一个插件 = 一个厂商站点（厂商 + 区域）。计费套餐（Coding Plan、Token Plan、Kimi Code 会员）是 preset 的 `PlanVariant`，协议面（OpenAI 兼容、Anthropic Messages、Gemini）是 preset 的每协议 `Endpoints`——两者都不是独立 preset。一个渠道携带 preset 声明的全部协议端点，共用一个 API key。

当前有 21 个可见插件：`alibaba`（阿里百炼-中国）、`alibaba_intl`（阿里百炼-国际）、`anthropic`、`azure`、`cerebras`、`deepseek`、`fireworks`、`google`、`groq`、`minimax`、`mistral`、`moonshot`、`ollama`、`openai`、`openrouter`、`together`、`vertex`、`vllm`、`xai`、`zai`、`zhipu`。每个 preset 携带双语短名（`Name`/`NameZh`，如 "Alibaba Bailian-China" / "阿里百炼-中国"）、区域、基于官方文档撰写的双语描述、端点、鉴权、套餐、模型来源与文档链接；`Plugins()`/`PluginForPreset()`/`PresetIDsForPlugin()` 提供插件视图与归属推导。`github`（GitHub Models）已于 2026-07-30 被官方全面退役，preset 隐藏、不再进入目录，仅保留给存量渠道解析。

插件的启用状态保存在应用数据库 `app_settings` 键 `plugins.channels`（`{"enabled":[...]}`），**默认全部禁用**：`GET /api/catalog/providers` 只返回已启用插件的可见 preset，创建渠道弹窗的官方目录因此初始为空，由「插件」页启用。`GET /api/plugins`（admin）返回全量插件目录（厂商信息：名称/描述/文档/套餐/端点，不含渠道关联统计）；`PATCH /api/plugins/{id}` 切换开关，停用仍有启用渠道的插件时返回 409 与受影响渠道列表（仅此时查询渠道归属），需 `force:true` 确认。**插件开关只影响目录展示，从不改变路由**：已有渠道的流量由渠道自身的 `enabled` 决定。

11 个历史 plan/协议 preset 被标记 `Hidden` 并保留可解析（存量渠道与导入 bundle 不受影响），每条通过 `Replacement` 指向规范 preset+plan：`qwen_coding`、`qwen_coding_anthropic` → `alibaba`+`coding`；`qwen_coding_intl`、`qwen_coding_intl_anthropic` → `alibaba_intl`+`coding`；`qwen_token_plan` → `alibaba`+`token`；`qwen_token_plan_intl` → `alibaba_intl`+`token`；`zai_coding`、`zai_coding_anthropic` → `zai`+`coding`；`zhipu_coding`、`zhipu_coding_anthropic` → `zhipu`+`coding`；`kimi_coding` → `moonshot`+`kimi-code`。隐藏 preset 不出现在目录与插件页。

运行时校验矩阵仍是 `internal/upstream/resolved.go` 的 `providerPresetRegistry`（preset → 协议族 + routing profile）：显式条目优先，目录新增 preset 由 `catalogSpec` 自动注入。Monitor 通过 `GET /api/provider-presets` 暴露该矩阵（含隐藏 preset，供存量渠道编辑）。主要条目：

| `provider_preset` | 协议族 | 默认 `routing_profile` | 允许的 `routing_profile` | 支持级别 | 备注 |
| --- | --- | --- | --- | --- | --- |
| `alibaba` | `openai_compatible` | `openai_default` | `openai_default` | compatible | 中国站；套餐 coding/token |
| `alibaba_intl` | `openai_compatible` | `openai_default` | `openai_default` | verified | 国际站；套餐 coding/token |
| `azure` | `openai_compatible` | 自动推断 | `azure_openai_v1`、`azure_openai_deployment` | verified | `azure_openai` 别名 |
| `baseten` | `openai_compatible` | `openai_default` | `openai_default` | compatible | 仅 registry |
| `cerebras` | `openai_compatible` | `openai_default` | `openai_default` | compatible | |
| `deepseek` | `openai_compatible` | `openai_default` | `openai_default` | compatible | 允许以 origin 作为 base URL |
| `fireworks` | `openai_compatible` | `openai_default` | `openai_default` | verified | |
| `github` | `openai_compatible` | `openai_default` | `openai_default` | verified | `github_models` 别名；服务已于 2026-07-30 退役，preset 隐藏，仅存量渠道 |
| `groq` | `openai_compatible` | `openai_default` | `openai_default` | verified | |
| `hugging_face` | `openai_compatible` | `openai_default` | `openai_default` | compatible | 仅 registry |
| `minimax` | `openai_compatible` | `openai_default` | `openai_default` | compatible | |
| `mistral` | `openai_compatible` | `openai_default` | `openai_default` | compatible | |
| `moonshot` | `openai_compatible` | `openai_default` | `openai_default` | compatible | 套餐 kimi-code |
| `nvidia_nim` | `openai_compatible` | `openai_default` | `openai_default` | compatible | 仅 registry |
| `openai` | `openai_compatible` | `openai_default` | `openai_default` | verified | |
| `openrouter` | `openai_compatible` | `openai_default` | `openai_default` | verified | |
| `perplexity` | `openai_compatible` | `openai_default` | `openai_default` | compatible | 仅 registry |
| `together` | `openai_compatible` | `openai_default` | `openai_default` | verified | |
| `vllm` | `openai_compatible` | `vllm_openai` | `vllm_openai` | verified | |
| `xai` | `openai_compatible` | `openai_default` | `openai_default` | verified | |
| `zai` | `openai_compatible` | `openai_default` | `openai_default` | verified | 套餐 coding |
| `zhipu` | `openai_compatible` | `openai_default` | `openai_default` | verified | 套餐 coding |
| `anthropic` | `anthropic_messages` | `anthropic_default` | `anthropic_default` | verified | |
| `google` | `google_genai` | `google_ai_studio` | `google_ai_studio` | verified | `google_genai`/`google_ai_studio`/`gemini` 别名 |
| `vertex` | `vertex_native` | 自动推断 | `vertex_express`、`vertex_project_location` | verified | |

`provider_preset` 是可选项；省略时由 host、base path 与 `protocol_family` 推断协议族和 routing profile。提供 preset 时，若与显式 `protocol_family` 冲突，或使用了不在允许列表内的 routing profile，会在配置解析或启动时失败。例如 `provider_preset: anthropic` 搭配 `protocol_family: google_genai`、`provider_preset: openrouter` 搭配 `routing_profile: azure_openai_v1`、以及未知 preset 都会报错。

host 推断的当前规则（按顺序匹配）：`api.deepseek.com` 且未指定 preset（或指定为 `openai`）时自动使用 `deepseek`；host 含 `anthropic.com` 或 `claude` 推断 `anthropic_messages`；`aiplatform.googleapis.com` 推断 `vertex_native`，其中该 host 默认 `vertex_express`，其他情况默认 `vertex_project_location`；host 含 `generativelanguage.googleapis.com` 或 `googleapis.com` 推断 `google_genai`；host 含 `azure.com`/`azure.net` 或 path 含 `/openai/` 时，有 `deployment` 或 path 含 `/deployments/` 用 `azure_openai_deployment`，否则用 `azure_openai_v1`；host 含 `vllm` 用 `vllm_openai`；其余默认 `openai_default`。

## Provider 配置来源

渠道的数据库唯一来源、`channels.initialized` 标记、`store.ConfigurationTransaction` 的写入边界与 `routing.settings` 策略位置，见 [路由与凭据](./ROUTING_AND_CREDENTIALS.md) 与 [Responses 运行时](./RESPONSES_RUNTIME.md)。

## 能力声明与 protocol_family 映射

- 能力字段为 `responses`、`chat_completions`、`tool_calling`、`embeddings`、`models`、`tokenize`，数据库为 channel 的 capabilities JSON。每个字段都是可选布尔，未声明表示未知。
- 已声明的能力对对应 surface 是有约束力的：`chat_completions` / `responses` 决定候选能否服务该路径，`tool_calling` 决定带 tools 的请求能否选中该 target（`tools` 与非空 `functions` 同等看待：后者是 OpenAI 已废弃但仍被客户端与上游接受的同义字段），`tokenize` 决定 `/tokenize` 与 `/v1/detokenize` 能否选中该 target；显式 `false` 会把该 target 排除，未声明的字段保持宽松（tokenize 这类没有 `api_type` 可推导的 surface 因此仍然可被选中）。`embeddings` 与 `models` 目前是描述性的：`POST /v1/embeddings` 没有 adapter，因此始终不可路由，`/v1/models` 由代理本地按配置目录回答、不转发给上游。需要特别注意 `/v1/responses`：`capabilities.responses=true` 只表示该渠道的 Responses 入口可由本地 Responses runtime 用 Chat Completions 翻译来服务；**原生 Responses 直通必须显式声明**——模型的 `supports_responses=true`，或渠道 `api_type` 为 `responses`/`responses_native`（或渠道显式没有 Chat Completions backend，此时 responses 只能解释为原生）。
- `api_type` 的全局合法值为 `chat_completions`、`responses`、`responses_native`、`messages`、`gemini_generate_content`；协议族另有约束：`anthropic_messages` 只接受 `messages`，`google_genai` 与 `vertex_native` 只接受 `gemini_generate_content`，`openai_compatible` 没有额外的 `api_type` 约束。`api_type` 缺失时按协议族取默认值：`anthropic_messages` → `messages`，`google_genai`、`vertex_native` → `gemini_generate_content`，其余 → `chat_completions`。
- `mode` 的合法值为 `proxy`、`record_only`、`server`、`responses_server`；它只被解析、校验和记录日志，不参与路由，也不再作为渠道表单字段暴露。
- 按模型覆盖：数据库 `channel_models.supports_responses` / `supports_chat_completions`（Monitor 可编辑）对该模型覆盖 channel 级 `api_type`/capabilities；没有声明覆盖的模型回退到 channel 级行为。这使一个 channel 能对部分模型走原生 surface，对另一些模型走其他 surface。
- endpoint 级判定：`/v1/chat/completions` 要求该模型的 Chat Completions 支持；`/v1/responses` 要求原生 Responses 支持或 Chat Completions 支持（后者走本地 Responses runtime）；`/tokenize` 与 `/v1/detokenize` 要求该模型的 tokenize 能力没有显式关闭（`capabilities.tokenize: false` 或 `model_capabilities.<model>.tokenize: false` 时该 target 不参与选择，返回 `no_supporting_target`；客户端可见错误码的完整表见 [代理使用示例](./PROXY_USAGE_EXAMPLES.md#客户端可见的错误码)）。
- `internal/upstream` 的 capability registry 被 probe endpoint、provider setup/apply 写入与 routing API surface 判断共用，各控制面不维护各自事实源；probe 未探测到的能力不会被自动补出。registry 里没有 tokenize 的 probe spec，因此该能力只来自显式声明。

## 接口自动识别

渠道页面不再要求操作者手工勾选 Responses/Chat/Tool/Models 等能力。每个模型行有一个由配置投影出来的 `interfaces`（`chat_completions`、`native_responses`、`responses_via_chat`、`messages`、`generate_content`），并可通过 `POST /api/provider-model-detect` 用真实最小请求重新探测：

- 探测集合以渠道已声明的协议为准：声明了哪些协议就探测哪些，包括跨协议族声明（例如 OpenAI-compatible 自定义渠道勾选了 Messages，`POST /api/provider-model-detect` 也会探测 `/v1/messages`，勾选的支持的协议就是配置依据）；渠道未声明任何协议能力时回退为按协议族选择：OpenAI-compatible 探测 `chat_completions` 与 `responses`，Anthropic 探测 `messages`，Google/Vertex 探测 `generate_content`。「强制」指的是不依据模型级已有的 `supports_*` 结论跳过实测。
- 结论只写回 OpenAI-compatible 的两个模型级三态列：`ok` 写支持，`endpoint_not_supported`/`model_not_found` 写不支持，鉴权/超时/网络错误等不确定结果不写；`messages`/`generate_content` 是协议族级接口，只报告。
- 识别在配置锁外完成网络探测，再在一个配置事务里写能力并 reload 路由；它不写 `last_probe_at`、`channel_probe_runs`、cassette 或 logs，也不经过 router，因此不会污染流量与用量。
- 启用模型时前端会自动补全尚未声明的接口；模型卡的「识别接口」可以覆盖重新识别；渠道工具栏可以批量识别所有已启用模型。

## Provider Probe

`provider probe` 是手动诊断命令，用于检查应用数据库中渠道的 upstream endpoint 是否暴露常见 API surface 并给出保守建议：

```bash
trajecta --config config.yaml provider probe --id openai-local --format json
```

探测的 endpoint 固定为：OpenAI 兼容 `GET /v1/models`、`POST /v1/chat/completions`、`POST /v1/responses`；Anthropic `POST /v1/messages`、`GET /v1/models`；Gemini `GET /v1beta/models`。鉴权按 endpoint 选择：OpenAI 风格用 `Authorization: Bearer`，Anthropic 用 `x-api-key` 加 `anthropic-version: 2023-06-01`，Gemini 用 `x-goog-api-key`。

- 只要返回 2xx 或 400、401、403、405、415、422、429 之一，就认为 endpoint 存在。这个判断只看路由是否存在：`POST /v1/responses` 的空 body 会得到 `400 missing model`，它不能证明某个模型在该 endpoint 上可用（多个 provider 对未挂载模型返回纯文本 404）。因此 route-only probe 不再为 Chat Completions 渠道自动写入 `capabilities.responses`，该 capability 只会在 `api_type` 为 `responses`/`responses_native` 或 probe 未发现 Chat Completions surface 时应用；模型级可用性用 Monitor 的按模型连通性测试确认。
- 每个 endpoint 带权重：`POST /v1/responses`、`POST /v1/messages` 与 `GET /v1beta/models` 为 4，`POST /v1/chat/completions` 为 3，OpenAI 与 Anthropic 的 `GET /v1/models` 为 1。按协议族累计得分后取最高者作为建议，并汇总命中的 capability。
- 输出包含建议的 `protocol_family`、`api_type`、capabilities、confidence 与 warnings。confidence 按得分给出：≥5 为 0.9，≥4 为 0.8，≥3 为 0.7，≥1 为 0.45，否则 0。
- 当显式配置的 `api_type` 或 `protocol_family` 与探测建议不一致时，报告写入 warning，但不改写配置。

`provider probe-report` 与 `provider probe` 是只读批量报告，`provider probe-apply` 是写入口；三者都打开 application store，对已有数据库渠道运行 probe。probe-apply 只填补缺失字段：

```bash
trajecta --config config.yaml provider probe-apply --id openai-local --format json
```

`provider probe-apply` 只填补空白的 `api_type`、`protocol_family` 和未设置的能力布尔；不写入 API key 或 header secret，也不覆盖显式 `api_type`、`protocol_family` 或显式 `false` 能力。省略 `--id` 时处理所有启用且有 `base_url` 的 channel。

capability registry 中没有 `vertex_native` 的 probe spec，因此 provider probe 与 `provider probe-apply` 不会给出 `vertex_native` 建议，也不会为 Vertex 上游建议或填补 Vertex 相关字段。

默认启动不执行 provider probe，也不依赖网络。需要探测时使用 Monitor 的渠道页操作或 CLI：

```bash
trajecta provider probe --id <channel-id>
trajecta provider probe-report
```

两者都从应用数据库读取渠道，不再读取 YAML `upstreams`；serve 启动不会 probe，也不会回填任何 YAML 配置。

## 新增 preset 的原则

可以新增 preset 的条件：

- 上游在生态中常见。
- 能清晰映射到已有协议族。
- 不需要新的请求/响应语义。

只有当请求 schema、响应 schema、stream 事件、usage 或 replay 行为明显不同，才应新增协议族。新增 preset 时用 [开发](./DEVELOPMENT.md) 中的命令矩阵做校验；新增协议族还需补齐官方 schema 快照与流式/非流式 cassette fixture，并保证解析进 Observation IR 时不改变 replay 原始字节。

## 协议转换

协议转换默认关闭：上游必须原生支持客户端实际请求的 endpoint。`pkg/protocolconvert` 提供直通优先的转换库；开启 `router.selection.protocol_conversion` 后，只有在本轮没有可用原生候选、请求非流式、入口为 chat/messages/gemini 时才启用，`/v1/responses` 始终由本地 Responses runtime 负责。多模态/thinking 与 `response_format`、`reasoning_effort` 等 IR 不携带的字段会以 400 拒绝，详见 [协议转换](./PROTOCOL_CONVERSION.md)。

## 非目标与未实现

- 流式逐帧转换与多模态/thinking 转换：流式请求和 IR 无法承载的内容不会被转换，必须使用原生直通上游。
- 默认情况下 `/v1/messages` 不会转换成 OpenAI Chat Completions 或 Responses，`/v1/responses` 也不会转换成 Anthropic Messages；开启协议转换后，chat/messages/gemini 入口按 [协议转换](./PROTOCOL_CONVERSION.md) 的矩阵生效，`/v1/responses` 仍不转换。cache 控制、reasoning 字段与 provider 特有错误保持 provider 特有（IR 不携带的字段会拒绝或记 warning）。
- provider 特有 tools、reasoning、cache 控制、citations、safety block 与 streaming 事件生命周期没有完整语义等价。
- 唯一的 Responses 特例发生在同一协议族内部：没有匹配的原生 Responses 上游时，本地 Responses runtime 会把 `/v1/responses` 编排为一次内部 `/v1/chat/completions` 调用，而不是跨协议族翻译。录制、观测与审计细节见 [观测与审计](./OBSERVATION_AND_AUDIT.md)。

## 每协议 endpoint

渠道的 `capabilities.endpoints` 可选地按 wire protocol 覆盖 base URL：

```json
{"endpoints":{"messages":"https://api.deepseek.com/anthropic","gemini":"https://generativelanguage.googleapis.com"}}
```

未列出的协议继续使用渠道 `BaseURL`。转发路径在 native 请求上按客户端 path 选择 endpoint，在 conversion 后按目标协议显式选择（`ResolvedUpstream.BuildURLForProtocol`）。该字段通过应用数据库的 `channel_configs.capabilities_json` 持久化，不需要新增表；渠道 API 另有顶层 `endpoints`，Monitor 高级选项可逐协议编辑。从官方目录创建渠道时，`channelRecordFromRequest` 会按 preset+plan 自动填充全部协议端点与对应能力布尔（显式传入优先，编辑不重新推导），因此「一个 key、两个 URL」（如 `/v1` 与 `/apps/anthropic`）不需要手工配置。


## 套餐（Coding Plan / Token Plan）

套餐是插件 preset 的 `PlanVariant`：覆盖端点、声明专用 key 前缀（`KeyPrefix`）、标记 `Coding`、可覆盖模型来源（`Models`，coding 端点通常无模型列表接口）。渠道以 `provider_preset` + `plan`（`channel_configs.plan` 列）记录所选套餐；创建时 `ValidatePlanCredential` 按套餐校验 key 前缀，`IsCodingPlan`/`PlanNameFor` 推导渠道卡的套餐徽标。

阿里百炼两个插件的套餐端点（与官方文档一致，均为 Bearer 鉴权、双协议共用一个 key）：

| 插件 | 套餐 | Chat endpoint | Anthropic endpoint |
| --- | --- | --- | --- |
| `alibaba`（中国） | 默认（按量） | `https://dashscope.aliyuncs.com/compatible-mode/v1` | `https://dashscope.aliyuncs.com/apps/anthropic` |
| `alibaba`（中国） | `coding` | `https://coding.dashscope.aliyuncs.com/v1` | `https://coding.dashscope.aliyuncs.com/apps/anthropic` |
| `alibaba`（中国） | `token` | `https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1` | `https://token-plan.cn-beijing.maas.aliyuncs.com/apps/anthropic` |
| `alibaba_intl`（国际） | 默认（按量） | `https://dashscope-intl.aliyuncs.com/compatible-mode/v1` | `https://dashscope-intl.aliyuncs.com/apps/anthropic` |
| `alibaba_intl`（国际） | `coding` | `https://coding-intl.dashscope.aliyuncs.com/v1` | `https://coding-intl.dashscope.aliyuncs.com/apps/anthropic` |
| `alibaba_intl`（国际） | `token` | `https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1` | `https://token-plan.ap-southeast-1.maas.aliyuncs.com/apps/anthropic` |

Coding Plan 与 Token Plan 使用 `sk-sp-` 前缀的专用 key，和普通 DashScope key 不可混用；渠道创建/编辑时会拒绝非 `sk-sp-` key。Coding Plan 整合千问、GLM、Kimi、MiniMax 等模型，是阿里百炼的产品而非单一模型厂商的套餐。GLM Coding Plan（`zai`/`zhipu` 的 `coding` 套餐）使用对应平台的 API key；Kimi Code 会员（`moonshot` 的 `kimi-code` 套餐）使用会员 key 访问 `https://api.kimi.ai/coding`。coding endpoint 才会消耗套餐额度。

历史的独立 plan/协议 preset（`qwen_coding*`、`qwen_token_plan*`、`zai_coding*`、`zhipu_coding*`、`kimi_coding`）已隐藏，仅保留给存量渠道解析，其凭据校验由 `Replacement` 指向的规范套餐派生，行为不变。

**关于 OAuth**：Qwen 当前公开文档已把 Qwen OAuth 从 `/auth` 选项移除，官方 coding plan 走专用 API key；Z.ai 的 ZCode OAuth 是桌面客户端流程，BigModel 的 OAuth 也只接受桌面回调，均不是稳定的公开第三方 API。Trajecta 先接入官方 API key 路径，不实现逆向/客户端专用 OAuth。
