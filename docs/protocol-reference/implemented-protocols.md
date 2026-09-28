# 已实现的协议

本文档记录当前代码库实际实现的协议行为。它描述现状，不描述目标。

## 实现形态

Trajecta 目前做三件协议感知的事情：

1. 在 `pkg/llm` 中把请求路径与上游归类为 provider／协议语义
2. 在 `internal/upstream` 中把配置的上游解析为协议族、路由 profile、认证 header 与 URL 重写行为
3. 透传原始 HTTP 字节，同时录制 cassette、抽取 usage／timeline 事件，并把 trace 解析为 Observation IR

代理热路径**不做**跨协议的请求 schema 翻译。例如 Anthropic Messages 请求不会在转发前被转换成 OpenAI Chat Completions 或 OpenAI Responses 请求。

## 协议族

协议族、provider 标签、routing profile 与客户端入口归一化的完整矩阵由 [协议族与上游 Provider](../PROTOCOLS_AND_PROVIDERS.md) 的"协议族与请求入口"一节维护，本文不重复；各协议族的官方上游 schema 快照索引见 [协议参考](./README.md) 的"上游快照"一节。下文只记录各协议族当前实现的请求/响应处理面。

## OpenAI-Compatible

OpenAI-compatible 用于 API 形态遵循 OpenAI 风格请求／响应语义的 provider。

当前归一化后的 endpoint：

- `/v1/chat/completions`
- `/v1/responses`
- `/v1/embeddings`
- `/v1/models`
- `/tokenize`
- `/detokenize`

要点：

- `/tokenize`、`/v1/tokenize`、`/detokenize`、`/v1/detokenize` 的根路径映射对整个 `openai_compatible` 协议族生效，不区分 routing profile，统一映射到上游根路径的 tokenization endpoint；`vllm_openai` profile 的分支因此不可达
- `upstream.base_url` 应包含 provider 的 API 前缀，例如 `/v1`、`/api/v1`、`/openai`、`/openai/v1`
- 代理会录制并解析 Chat Completions、Responses 与 Models。Embeddings 请求只被分类与追踪，`pkg/llm.AdapterFor` 没有 embeddings 分支，因此它不参与路由（选择阶段得到零个候选并失败），也不是当前 Observation IR 的深度解析目标；vLLM 分词请求会被分类、路由与录制，但不是 Observation IR 的深度解析目标
- Responses 与 Chat Completions 是 OpenAI 的两个不同接口面。Codex 流量通常使用 `/v1/responses`

## Anthropic Messages

Anthropic Messages 用于 Claude 风格的 `/v1/messages` 流量。客户端入口（`/anthropic/messages`、`/anthropic/v1/messages`、`/v1/messages` 以及 `/v1/messages/count_tokens` 及其别名）由 [协议族与上游 Provider](../PROTOCOLS_AND_PROVIDERS.md) 的"协议族与请求入口"一节维护。

当前行为：

- 请求与响应 body 原样透传
- 认证 header 被重写为 Anthropic 风格的 `x-api-key`
- `api_version` 默认为 `2023-06-01`：只要上游存在 API key，请求缺失 `anthropic-version` 时就会被注入该默认值，不要求显式配置
- 请求／响应 body 与流式事件会被解析为 Observation IR
- `/v1/messages/count_tokens` 与 `/v1/messages` 共用同一 operation 与 parser，因此同样被追踪与解析

因此，把 Claude Code 指向一个不重复 `/v1` 的 Trajecta base URL，并配置好兼容 Anthropic Messages 的上游（或所选网关确实支持该 endpoint）时，它就能正常工作。

## Google Gemini 与 Vertex Native

Google Gemini 与 Vertex native 是独立的协议族，因为它们的 endpoint 形态、认证模型和请求／响应 schema 都与 OpenAI、Anthropic 不同。

当前行为：

- Google AI Studio 使用 `/v1beta/models/{model}:generateContent` 及其流式变体
- Vertex native 支持 express 与 project/location 两种路由 profile
- 两者都经 GenerateContent 语义适配器路径解析

## 当前代码的非目标

以下能力当前未实现：

- Anthropic Messages → OpenAI-compatible 的请求翻译
- OpenAI Responses → Anthropic Messages 的请求翻译
- Gemini GenerateContent → OpenAI-compatible 的请求翻译
- provider 专有工具、reasoning、cache 控制、citations、safety 拦截、流式事件生命周期的完整语义等价

`pkg/llm.Adapter` 为常见内部结构提供了 marshal 方法，但代理转发路径并不把它们当作跨协议转换层使用。
