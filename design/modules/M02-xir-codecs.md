# M02 协议 Codec

## 0. 头部

| 字段 | 值 |
| --- | --- |
| 模块 ID | M02 |
| 模块名 | 协议 Codec |
| 归属 | `pkg/xir/codec/{chat,responses,messages,gemini,registry}` |
| 状态 | Landed (P2) |
| 依赖模块 | M01（`pkg/xir`、`pkg/xir/wire`）、M06（`pkg/usage`） |
| 被依赖模块 | M03、M04；未来 `internal/gateway` |
| 目标版本 | Trajecta 2.0 |
| 最后更新 | 2026-11-09 |

## 1. 目标与非目标

### 1.1 目标

- 为 Chat Completions、Responses、Messages、Gemini generateContent 提供 `xir.Codec` 实现。
- 把协议 JSON 投影到 `xir.Request`/`xir.Response`，覆盖文本、工具、推理、多模态、usage、stop reason、state refs。
- 同协议重编码用 `Extensions` 带回未建模字段，opaque 块用 `Sealed` 保留；用 `registry` 按协议名查找。
- 保持无状态、可并发、可被外部 Go 程序直接使用。

### 1.2 非目标

- 不做流式 codec；四协议均只处理非流式 JSON body。
- 不承诺同协议 encode 与输入字节一致；字节直通由 M04 负责。
- 不做路由、转换决策、loss policy、DB/cassette 写入或脱敏。
- 不覆盖厂商私有字段的完整语义；chat 没有 `Extensions` 保留机制。

## 2. 当前实现与约束

- `chat`：decode.go/encode.go；`responses`：responses.go/decode.go/encode.go；`messages`：codec.go/request.go/response.go/content.go；`gemini`：gemini.go/decode_request.go/decode_response.go/encode_request.go/encode_response.go/extensions.go/wirefields.go。
- `registry`：唯一 import 四个内置 codec 的包；`Default()` 返回进程级四协议 registry。
- 测试：每个 codec 有表驱动 round-trip、错误路径和并发用例；`registry_test.go` 覆盖内置注册/查找/重复注册。无外部 fixture、fuzz、benchmark。
- 兼容：只新增包，不改变 cassette/replay/DB；当前未接入转发路径（`pkg/protocolconvert` 仍是运行时转换实现）。
- 已知债务：`DecodeOptions` 被所有 codec 忽略；chat 不保存未知字段；`EncodeOptions.Vertex` 无任何效果；Gemini 的 model/stream 需由调用方从 URL 传入。

## 3. 对外契约（`pkg`）

### 3.1 包结构

```text
pkg/xir/codec/
  chat/        decode.go encode.go
  responses/   responses.go decode.go encode.go
  messages/    codec.go request.go response.go content.go
  gemini/      gemini.go decode_request.go decode_response.go encode_request.go encode_response.go extensions.go wirefields.go
  registry/    registry.go
```

### 3.2 Go API

```go
// chat、responses、messages、gemini 各有一个 Codec，均实现 xir.Codec：
//   Protocol() xir.Protocol
//   DecodeRequest(ctx, *xir.Document, xir.DecodeOptions) (*xir.Request, error)
//   EncodeRequest(ctx, *xir.Request, xir.EncodeOptions) ([]byte, error)
//   DecodeResponse(ctx, *xir.Document, xir.DecodeOptions) (*xir.Response, error)
//   EncodeResponse(ctx, *xir.Response, xir.EncodeOptions) ([]byte, error)
// chat 用指针接收者，responses/messages/gemini 用值接收者。
type Codec struct{}; func New() *Codec
package responses; const ExtensionKey = "responses"; const ParamTextVerbosity = "text.verbosity"; const ParamReasoningSummary = "reasoning.summary"
package messages; const ExtensionKey = "messages"
package gemini; const ExtensionKey = "gemini"; const ParamResponseMimeType = "responseMimeType"; const ParamResponseSchema = "responseSchema"
type RequestMeta struct { Path, Model string; Stream bool }
func (c Codec) DecodeRequestWithPath(ctx context.Context, doc *xir.Document, opts xir.DecodeOptions, path string) (*xir.Request, error)
func (c Codec) DecodeRequestWithMeta(ctx context.Context, doc *xir.Document, opts xir.DecodeOptions, meta RequestMeta) (*xir.Request, error)
func ModelFromPath(path string) string
package registry; type Registry struct{}; func NewRegistry() *Registry; func Default() *Registry
func (r *Registry) Register(codec xir.Codec) error; func (r *Registry) Lookup(protocol xir.Protocol) (xir.Codec, bool); func (r *Registry) Protocols() []xir.Protocol
```

### 3.3 错误与诊断

所有 codec 返回 `fmt.Errorf` 文本错误，没有 sentinel/typed error。结构性错误包括：nil document、根不是 object、`messages`/`tools`/`content` 类型不符、缺少必需 block 字段、Gemini `functionCall.args` 不是 object、Responses 遇到外来 sealed 块。未知/缺失可选字段通常被忽略；Responses 和 Messages 的未知顶层字段进 `Extensions`，Gemini 的未知字段/`safetySettings` 进 `Extensions`，chat 直接丢弃。调用方不应字符串匹配错误；需要分类时应在本层补 typed error（当前未实现）。

### 3.4 并发、生命周期、资源

四个 codec 都是无状态值/空结构体，可被任意多个请求并发使用；`registry` 用 `RWMutex`，`Register` 通常只在启动时调用。codec 不启动 goroutine、不持有资源，`context` 只做取消检查；没有 `Close`。

## 4. `internal` 集成

当前没有 `internal` 包 import `pkg/xir/codec/*`；`pkg/upstream` 和 `pkg/route` 只使用 `pkg/xir` 类型。未来接入点是 `internal/gateway`（M10）与转换 hub（M04），建议显式注入 `*codec/registry.Registry`，不新增全局可变状态。没有 feature flag、配置或降级路径；codec 失败由调用方映射为 HTTP 错误。

## 5. 数据模型与格式

### 5.1 类型与映射

| 协议 | system | tool 声明/调用/结果 | reasoning | 备注 |
| --- | --- | --- | --- | --- |
| Chat | 留在 `Messages`（system/developer） | `tools[].function` / `tool_calls` / role=tool | `reasoning_content`/`reasoning` 文本 | 首个 choice；legacy `functions` |
| Responses | `instructions` → `Request.System` | 扁平 `tools[]` / `function_call` / `function_call_output` | reasoning item，`encrypted_content` 进 Sealed | item 顺序=消息顺序 |
| Messages | `system` → `Request.System` | `tools[].input_schema` / `tool_use` / `tool_result` | `thinking` 文本+signature；`redacted_thinking` 进 Sealed | 只有 user/assistant 角色 |
| Gemini | `systemInstruction` → `Request.System` | `functionDeclarations` / `functionCall` / `functionResponse` | thought + thoughtSignature 进 Sealed | model/stream 在 URL |

`Params.Extra` 约定：chat `reasoning`；responses `text.verbosity`/`reasoning.summary`；messages `thinking`（原始对象，另有 typed `Thinking`/`ThinkingBudget`）；gemini `responseMimeType`/`responseSchema`/`thinkingConfig.*`。

### 5.2 序列化

每个 codec 用私有 wire struct 生成目标协议 JSON，只输出 IR 携带的字段，不发明默认值（例外：chat/messages 的 tool 参数缺失时输出 `{}`，chat `messages` 为空时输出 `[]`）。未建模顶层字段：responses/messages/gemini 存 `Extensions[协议名]` 并在同协议 encode 时合并，编码器拥有的 key 优先；chat 无 `Extensions`，重编码会丢未知字段。`Sealed` 按协议作用域：responses 遇到外来 sealed 返回 error，messages 只重发 messages 所有且合法的 sealed（否则回退为构造 thinking/redacted 块），gemini 只重发 Gemini 块。codec 输出由调用方记录或发送，不进入 cassette/DB/API。

### 5.3 配置

不适用：codec 无 YAML/env 配置；registry 由调用方构造或使用 `Default()`。

### 5.4 数据放置

| 数据 | 文件 / DB / 内存 | 理由 | 丢失后果 |
| --- | --- | --- | --- |
| decoded `Request`/`Response`、encoded body | 内存 | 语义投影/目标协议 JSON | 可从 raw 重建；body 由调用方发送 |
| `Extensions`/`Sealed` raw | 内存 | 同协议字段保真 | 重编码丢字段 |

本模块不写 cassette/DB/文件，重启后无需重建状态。

## 6. 算法与不变量

### 6.1 算法

- Decode：`wire.Parse` 一次，按协议字段名查找节点并填 semantic 值；复杂度 O(输入节点数)，不构造 `map[string]any`（Extensions 收集时用一次 `json.Unmarshal`）。
- Encode：构造协议 wire struct，`json.Marshal` 一次；复杂度 O(IR 大小 + 输出大小）。
- `registry.Lookup` 是 O(1)；`Protocols` 按固定顺序扫描四个 key。

### 6.2 不变量

- codec 无状态、并发安全；相同输入可重复 decode。
- 同协议 encode 不是字节保真：只保证语义等价和已保留的扩展字段；字节直通由 M04 完成。
- Responses 的 item 顺序、Messages 的 block 顺序、Chat 的 message 顺序在 decode 后保持。
- Chat 的 system/developer 留在 `Messages`；Responses/Messages/Gemini 的 system 进入 `Request.System`；外协议 sealed 不重发。
- 四个 codec 都只取第一个 choice/candidate；usage 统一为 `pkg/usage.Usage` 并 `Normalize`；`DecodeOptions` 目前不影响结果。

### 6.3 边界条件

- 空 body/空对象：chat 要求 response 有非空 choices；responses 容忍空 output；messages 要求 response 有 content 或 error；gemini 可接受无 candidates。
- 非法 JSON 在 `ParseDocument` 阶段拒绝；字段类型不符由各 codec 报错或按可选字段忽略。
- 重复 key 由 wire 的“最后 key 生效”决定；未知 block 在 messages/gemini 中转为 `PartUnknown`/`PartSealed`。
- 无流式输入；SSE body 会被当作非法 JSON 拒绝。

## 7. 失败语义

| 场景 | 行为 | 是否影响上游健康 | 客户端看到什么 |
| --- | --- | --- | --- |
| 非法 JSON/根类型错 | decode 返回 error | 否 | 调用方映射为 400 |
| 结构字段类型错 | 按协议返回 error（chat 较宽容） | 否 | 调用方映射为 400 |
| 外来 sealed 块 | responses encode 返回 error；messages/gemini 降级/跳过 | 否 | 调用方决定 |
| 不支持字段/panic | 进 Extensions（chat 丢弃）或由上层隔离 | 否 | 可能静默丢失 |

codec 不接触上游，不改变上游健康；是否 fail-open 由调用方决定。

## 8. 性能

当前不在转发热路径；每次 decode 一次 wire 解析，每次 encode 一次 `json.Marshal`。无 benchmark、无分配预算；`Extensions` 收集对大 body 会额外 `Unmarshal` 一次。无流式内存上限；不聚合 SSE。命令：`go test ./pkg/xir/codec/...`。

## 9. 安全

body 可能含凭据/PII，只存在于调用方内存；codec 不写日志/DB/cassette。错误文本只含字段路径/JSON kind/偏移，不含字段值；`Extensions`/`Sealed` 会原样保留敏感字节，调用方负责脱敏。不执行外部输入，无网络/文件访问。

## 10. 可观测性

无指标、无日志、无事件；错误通过返回值上抛，排障依赖错误文本中的 `$.path` 和调用方日志。不新增 DB/cassette event。

## 11. 测试与验收

### 11.1 单元测试

四协议 round-trip 覆盖文本、system、工具声明/调用/结果、reasoning、媒体、usage、stop reason、error envelope；错误路径覆盖 nil/非法根、类型不符、错误 envelope、外来 sealed（Responses）、畸形 content。`TestCodecConcurrentUse` 覆盖 chat 并发；无 fuzz/benchmark。

### 11.2 集成测试

无跨包集成；测试全部离线、无网络、无 cassette/fixture。`registry_test.go` 覆盖内置注册和查找。

### 11.3 差分测试

无与 `pkg/protocolconvert`/旧 adapter 的差分测试；同协议字节直通由 M04 测试，不在 codec 层。

### 11.4 验收标准

- [x] 四协议 decode/encode + registry 可用；[x] 离线测试通过；[x] Responses/Messages/Gemini 保留扩展字段；[x] sealed 协议作用域有测试。
- [ ] chat 未知字段保真；[ ] `DecodeOptions`/`Vertex` 生效；[ ] benchmark/fuzz；[ ] 与旧实现差分；[x] 不写 cassette/DB。

## 12. 迁移、兼容、回滚

- 新增包，无 DB 迁移/文件格式变化；不改变现有 `pkg/llm`/`pkg/protocolconvert` 行为。
- 无 feature flag；回滚删除 import 即可。旧 adapter 在 M10 接入并差分通过前保留。

## 13. 开放问题

| 问题 | 影响 | 建议 | 需要谁决定 |
| --- | --- | --- | --- |
| `DecodeOptions` 未生效 | 无法按需 decode | 实现部分 decode 或标 reserved | M02 owner |
| chat 不保存未知字段 | 同协议重编码丢字段 | 增加 chat Extensions 或只用于转换 | M02 owner |
| `EncodeOptions.Vertex` 无效果 | 调用方误判 | 实现 Vertex 形状或删除字段 | M02/M04 owner |
| Codec 接口无法传 Gemini path/stream | model/stream 需额外 API | 在 M10 用 `DecodeRequestWithMeta` | M02/M10 owner |
| 只取首个 choice/candidate | 多候选响应丢数据 | 明确不支持或扩 IR | M02 owner |

## 14. 实施拆分

1. 四协议类型映射与 decode；2. encode 与 Extensions/Sealed；3. registry；4. round-trip/错误/并发测试（均已完成）。5. internal 接入与差分（未开始）。
- 验证：`GOCACHE=/tmp/gocache-trajecta go test ./pkg/xir/codec/...`。

## 15. 参考

- [M01](./M01-xir-foundation.md)；[总体架构](../01-overall-architecture.md) 第 7.4 节。
- 代码：[`chat`](../../pkg/xir/codec/chat)、[`responses`](../../pkg/xir/codec/responses)、[`messages`](../../pkg/xir/codec/messages)、[`gemini`](../../pkg/xir/codec/gemini)、[`registry`](../../pkg/xir/codec/registry)。
