# 协议转换（pkg/protocolconvert）

Trajecta 默认走**模型直通**：客户端协议与上游原生协议一致时，请求字节原样转发，不做任何转换。协议转换是**可选兜底**（`router.selection.protocol_conversion: true`，默认关闭）：只有上游无法原生承接客户端协议时，才把请求/响应在四种协议之间翻译。

| 协议 | 客户端入口 | 上游路径 |
| --- | --- | --- |
| `chat_completions` | `/v1/chat/completions` | `/v1/chat/completions` |
| `responses` | `/v1/responses`（**不参与热路径转换**） | `/v1/responses` |
| `messages` | `/v1/messages` | `/v1/messages` |
| `gemini` | `/v1beta/models/{model}:generateContent` | 同左；Vertex 由上游解析器展开为 publishers/projects 资源路径 |

## 热路径支持矩阵

转换在**非流式**请求上生效，且只发生在客户端入口是 chat / messages / gemini 时：

| 客户端入口 | 可转换到的目标 |
| --- | --- |
| `/v1/chat/completions` | responses、messages、gemini |
| `/v1/messages` | chat_completions、responses、gemini |
| Gemini `generateContent` | chat_completions、responses、messages |
| `/v1/responses` | **不转换**：交给本地 Responses runtime（native 直通或其自身的 Chat Completions 编排），转发路径不会把 Responses 原始请求发给不承接它的上游 |

`gemini` 覆盖 Google AI Studio 与 Vertex（`publishers/google/models` 等资源前缀由 `internal/upstream` 的 `BuildURL` 展开；转换只产出紧凑的 surface path，不自己拼 project/location/publisher）。

## 决策：Plan 与直通优先

`protocolconvert.Plan(client, caps, mode)` 给出三种结果：

- `passthrough`：上游原生支持客户端协议，字节直通。两种模式下都优先于转换。
- `convert`：上游不支持客户端协议，但支持某个可转换协议，按 `chat_completions > responses > messages > gemini` 选第一个。
- `reject`：上游一个可转换协议都不支持，或 `mode=passthrough_only`。

`PlanForRequest` 额外把**流式请求**变成 `reject`：流式转换必须逐帧翻译，聚合式重编码会破坏增量交付，属于明确的非目标（见下）。

热路径的选择规则是**硬优先**，不是成本启发式：`internal/router` 先按入口/模型/能力/健康/排除过滤出候选，只要其中还有一个**原生**候选，转换候选就完全退出本轮比较；只有原生候选全部不可用（熔断、并发、排除、协议不支持）时，才用转换候选兜底。因此原生通道健康时请求字节始终不变，同一请求不会因为延迟波动在“直通”和“转换”之间摆动。sticky binding 也遵守同一规则：绑定的转换目标在存在可用原生候选时会被移出候选集并重新绑定。

上游能力由 `internal/upstream.ResolvedUpstream.ProtocolCapabilitiesForModel` 派生：Anthropic 渠道为 `messages`，Google/Vertex 渠道为 `gemini`，OpenAI 兼容渠道按 `SupportsChatCompletionsAPIForModel` / `SupportsResponsesAPIForModel` 声明（每模型 `supports_*` 覆盖优先）。通过本地 Responses runtime 服务 `/v1/responses` **不计入**原生 `responses` 能力——那本身就是一次转换。

## 转换 API

- `ConvertRequest` / `ConvertResponse`：非流式请求/响应转换，经由 `pkg/llm` 的 `LLMRequest` / `LLMResponse`。`model` 参数覆盖转换后 body 中的模型（上游 wire 名）；转到 Anthropic 时自动补 `max_tokens=4096`（Messages 必填）；Chat 的 `max_completion_tokens` 映射为 `max_tokens`；源 body 的 `stream:true` 直接拒绝。
- `Protocol.RequestPath(model, stream, vertex) (string, error)`：转换后请求应发往的上游路径。Gemini 的模型在路径里，`ValidateModelName` 只接受字母/数字/`-_.@/`，拒绝 `:`、`..`、空段和前后空白，避免 action 后缀被改写或路径逃逸；其余协议模型在 body 里。
- `DetectFromPath`：只从请求头（路径）判定客户端协议，不读 body。`/v1/messages/count_tokens` 明确不可转换（语义/响应形状不同）。
- `Result.Warnings`：转换丢掉的信息。`Fatal` 表示“丢弃会改变答案”，热路径据此拒绝请求；`Result.FatalWarnings()` 可直接取出。
- **没有 `ConvertStreamResponse`**：流式转换是明确的非目标，已从库中移除；调用方不得用聚合式重编码代替逐帧翻译。流式请求需要原生直通上游。

## 内容与字段映射

转换保证以下内容在四种协议之间语义等价：

- 文本消息，以及 `system`（Anthropic 的 string 和 block 数组都保留；Gemini 的 `systemInstruction` 无 role）。
- 工具调用与工具结果：
  - Chat：assistant `tool_calls`，每个结果一条 `role:"tool"` 消息；
  - Messages：`tool_use` / `tool_result` block，结果在 `user` 消息里且排在正文之前；
  - Responses：`function_call` / `function_call_output` item，assistant 历史用 `output_text`，user/system 用 `input_text`；
  - Gemini：`functionCall` / `functionResponse` part，role 为 `model` / `user`，`functionResponse.name` 从对应的 `functionCall` 解析。
- `tool_choice`：IR 统一为 `auto` / `none` / `required` / `tool(name)`，再按目标协议编码（Chat 字符串或嵌套对象、Messages 对象、Responses 扁平对象、Gemini `toolConfig.functionCallingConfig`）。
- 工具声明：Chat 使用嵌套 `function`，Responses 使用扁平 `name`/`description`/`parameters`，Messages 使用 `input_schema`，Gemini 使用 `functionDeclarations`；`strict` 只在 Chat/Responses 间携带，转到 Messages/Gemini 时 `strict=true` 会拒绝（不静默放宽 schema）。
- `stop_reason` / `finish_reason`：先归类为 `stop` / `length` / `tool_use` / `content_filter` / `refusal`，再按目标协议拼写（Chat `stop`/`length`/`tool_calls`/`content_filter`；Messages `end_turn`/`max_tokens`/`tool_use`/`refusal`；Gemini `STOP`/`MAX_TOKENS`/`SAFETY`）。存在工具调用时，`STOP`/`stop` 一律升级为工具结束原因。
- role：`model` ↔ `assistant`，`tool`/`function` 在 Messages/Gemini 中落到 user 侧，Gemini 目标合并连续同 role 的 turns。
- refusal：Chat 写回 assistant `refusal` 字段并给出 `content_filter`；Messages 以 `stop_reason:"refusal"` 返回拒绝文本；Gemini 以 `SAFETY` + 文本返回；Responses 输出 `refusal` content part。
- usage（含 reasoning/cache token 字段可用的部分）与 Anthropic `metadata.user_id`。

## IR 边界与硬失败

`pkg/llm` 的 IR 只承载上述字段。以下内容**不能**忠实转换，默认**拒绝**而不是静默降级：

- 多模态/不透明内容（按结构检查，不按字符串猜测）：Chat 的 `image_url` / `input_audio` / 非 `text` content part、Messages 的 `image` / `document` / `thinking` / `redacted_thinking`、Gemini 的 `inlineData` / `fileData` / `thought` / `thoughtSignature` / `functionCall` / `functionResponse` 源请求、Responses 的 `reasoning` / 托管工具 item / `previous_response_id`。
- 改变答案的语义字段：`response_format`、`reasoning_effort`、`reasoning`、`seed`、非 0 的 `presence_penalty` / `frequency_penalty`、`logprobs=true`、`top_logprobs>0`、`parallel_tool_calls=false`、`functions` / `function_call`（旧字段）、非 text 的 `modalities`、`audio`、`web_search_options`、`prediction`、非空 `logit_bias`、`n>1` / `best_of>1`、Responses 的 `text.format`（非默认 `text`）/ `background=true` / `truncation`（非 `disabled`）/ `conversation` / `prompt` / `max_tool_calls>0`，以及非 Gemini 目标上的 `safety_settings`。
- 压缩请求体（`Content-Encoding: gzip` 等）、Gemini `?alt=sse`、流式请求、`/v1/messages/count_tokens`。

非致命 warning 仍会写进 cassette：`top_k` 转到 Chat/Responses 时不携带；响应里的 reasoning 内容不会带到目标协议。

## 热路径接入

- 请求：按 `Plan` 判定后转换 body 与上游路径，并按目标协议修正头（Anthropic `anthropic-version` 默认与 `anthropic-beta` 隔离、Gemini `?key=` 转 `x-goog-api-key`、`Content-Type: application/json`）。客户端 query 不带到上游（协议作用域的 query 由客户端协议拥有）。
- 失败分类：请求侧转换失败是**客户端可确定的**失败，代理返回 `400 protocol_conversion_unsupported`；它只尝试本轮剩余候选（可能有原生直通），不消耗退避重试预算，不更新目标健康/熔断（只释放并发名额）。响应侧转换失败发生在一次成功的上游调用之后，按上游成功计健康，但客户端收到错误 envelope（502）。
- 错误响应（非 2xx）与空 body 的成功响应不转换，原样透传：错误 body 是 provider 特有的诊断，经 IR 会变成空壳；空 body 没有可翻译的内容。
- 响应转换全量缓冲，上限与请求体上限一致（默认 64MiB），超限按转换失败处理；读失败会关闭上游 body。
- 录制：cassette 记录**客户端可见的请求**与**转换后的响应**（即客户端实际看到的字节），V3 格式不变；转换在 cassette 中以 `protocol_conversion` 事件记录（协议对、wire model、请求侧 warning 字段），响应侧 warning 以 `protocol_conversion_response` 事件记录。因此 cassette 记录的是“客户端视角的合成交换”，不是实际发往上游的字节；排障时上游路径由 `protocol_conversion` 的协议对推导。
- 顺序：请求先转换、再做 secret masking（掩码作用于实际上游字节）；响应先转换、再做 stand-in 还原。
- `/v1/responses` 入口不进入转换（见矩阵）；本地 Responses runtime 的 chat 后端检查保持 native-only。
- Monitor 的 `POST /api/routing/inspect` dry run 与热路径共用同一套候选过滤：转换候选的 `upstream_endpoint` 报告实际 wire 协议，`RoutePlan.protocol_conversion` 给出 `client_protocol` / `upstream_protocol`；流式请求不会报告转换。

## 验证

`pkg/protocolconvert` 的测试覆盖：四种协议请求/响应全矩阵（文本、模型、finish reason）、工具调用与工具结果的双向映射、Anthropic system block 数组、Chat→Responses 的 item 形状、tool_choice 四种拼写、Chat→Gemini / Gemini→Chat 工具流、结构化多模态拒绝、语义字段 fatal、流式拒绝、模型名校验；`internal/proxy` 覆盖端到端 Messages→Chat 文本与工具循环、400 且不污染健康、Responses 入口不转换；`internal/upstream` 覆盖 Vertex express/project 的转换短路径展开。
