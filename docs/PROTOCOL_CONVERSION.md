# 协议转换（pkg/protocolconvert）

Trajecta 默认走**模型直通**：客户端协议与上游原生协议一致时，请求字节原样转发，不做任何转换。只有当上游不支持客户端协议时，才进入**协议直接转换**，把请求/响应在四种协议之间翻译：

| 协议 | 入口 | 上游路径 |
| --- | --- | --- |
| `chat_completions` | `/v1/chat/completions` | `/v1/chat/completions` |
| `responses` | `/v1/responses` | `/v1/responses` |
| `messages` | `/v1/messages` | `/v1/messages` |
| `gemini` | `/v1beta/models/{model}:generateContent` | 同左（Vertex 时为 `/v1/publishers/...`） |

实现位于 `pkg/protocolconvert`，设计参考 magpie（直通优先、转换兜底）、`chaitin/ai-api-protocol-bridge`（统一 IR + 每协议一个 adapter）与 new-api relaykit（按渠道协议族分发）。转换本身不另起协议结构体，而是复用 `pkg/llm` 已有的 `LLMRequest`/`LLMResponse` 中间表示与各协议 adapter 的编解码。

## 决策：Plan

`protocolconvert.Plan(client, caps, mode)` 对一次请求给出三种结果：

- `passthrough`：上游原生支持客户端协议，字节直通。两种模式下都优先于转换。
- `convert`：上游不支持客户端协议，但支持某个可转换协议，按 `chat_completions > responses > messages > gemini` 的顺序选第一个。
- `reject`：上游一个可转换协议都不支持，或模式为 `passthrough_only`。

`mode` 只有两个取值：`passthrough_preferred`（默认）和 `passthrough_only`。

上游的能力集合由 `internal/upstream.ResolvedUpstream.ProtocolCapabilitiesForModel` 派生：Anthropic 渠道为 `messages`，Google/Vertex 渠道为 `gemini`，OpenAI 兼容渠道按 `SupportsChatCompletionsAPIForModel` / `SupportsResponsesAPIForModel` 声明（每模型 `supports_*` 覆盖优先）。通过本地 Responses runtime 服务 `/v1/responses` 不计入原生 `responses` 能力——那本身就是一次转换，由 `internal/responses` 负责，计入会让 Plan 把原始 Responses 请求直发给无法承接的上游。

## 转换 API

- `ConvertRequest` / `ConvertResponse`：非流式请求/响应转换。`model` 参数覆盖转换后 body 中的模型（上游 wire 名）；转到 Anthropic 时自动补 `max_tokens=4096`（Messages 必填）。
- `ConvertStreamResponse`：把上游 SSE 流聚合成 `LLMResponse` 后，按客户端协议重新编码为 SSE（文本 delta + 工具调用在最终帧）。它不是逐帧翻译，上游增量顺序不保留，但客户端拿到的是自己协议下格式完好的流。
- `Protocol.RequestPath(model, stream, vertex)`：转换后请求应发往的上游路径；Gemini 的模型在路径里，其余协议模型在 body 里。
- `DetectFromPath`：只从请求头（路径）判定客户端协议，不读 body。
- `Result.Warnings`：转换丢掉的信息（见下）。调用方不能容忍丢失时应检查 warnings 并拒绝，而不是静默降级。

## 已知边界（IR 限制）

转换经由 `pkg/llm` 的 IR，IR 之外的字段会丢：

- Gemini 编码只保留文本 parts，消息中的 `tool_use`/`tool_result` 在转到 Gemini 时丢失；反之亦然。
- `top_k` 转到 Chat Completions/Responses 时不携带（warnings 会报告）。
- reasoning effort、presence/frequency penalty、provider options 等协议特有字段不在 IR 中，转换时丢失；`UnsupportedFields` 可列出源 body 中 IR 不携带的顶层键。

## 热路径接入

转发热路径在配置 `router.selection.protocol_conversion: true` 时启用转换，默认关闭（关闭时行为与引入转换前完全一致）。启用后：

- 路由选路（`internal/router`）把 `PlanForRequest` 纳入候选判定：原生直通仍优先；只有上游不支持客户端协议、且请求非流式时，转换候选才入选。流式请求永远不转换（聚合式流转换会破坏增量交付），需要原生直通上游。
- 代理（`internal/proxy`）在选路后转换请求 body 并改写上游路径（`RequestPath`），响应回来先转回客户端协议再写出；转换失败按候选失败处理，试下一个候选（可能原生直通），不会把错误协议的字节发给客户端。
- 错误响应（非 2xx）不转换，原样透传：错误 body 是 provider 特有的诊断，经 IR 会变成空壳。
- 录制不变：cassette 记录客户端可见的原始请求与转换后的响应（即客户端实际看到的字节），V3 格式不变；转换在 cassette 中以 `protocol_conversion` 事件记录（含协议对与 warnings 字段）。
- 转换顺序：先转换、再做 secret masking（掩码作用于实际上游的字节）；响应侧先转换、再做 stand-in 还原。

`PlanForRequest` 的流式拦截是正确性护栏，不是功能缺失：流式转换需要逐帧翻译器（桥接包的 `StreamDecoder/StreamEncoder` 形态），聚合式重编码不能满足"增量交付"这一正确性要求，在实现逐帧翻译前不启用。

## 库调用顺序（非热路径调用方）

1. `DetectFromPath` 判定客户端协议；
2. `ProtocolCapabilitiesForModel` 取上游能力，`Plan`（或 `PlanForRequest`）决策；
3. `convert` 时用 `ConvertRequest` 得到上游 body + `RequestPath` 得到上游路径，响应回来用 `ConvertResponse`/`ConvertStreamResponse` 转回客户端协议；
4. 录制时仍记录客户端可见的原始请求/响应（`pkg/recordfile` 的 V3 格式不变），转换只影响上游字节。
