# M01 Exchange IR 基础

## 0. 头部

| 字段 | 值 |
| --- | --- |
| 模块 ID | M01 |
| 模块名 | Exchange IR 基础 |
| 归属 | `pkg/xir`、`pkg/xir/wire` |
| 状态 | Landed (P1) |
| 依赖模块 | `pkg/usage`（M06，仅 `Usage` 类型）；无 XIR 内部依赖 |
| 被依赖模块 | M02、M03、M04；`pkg/upstream`、`pkg/route`；`internal/router` |
| 目标版本 | Trajecta 2.0 |
| 最后更新 | 2026-11-09 |

## 1. 目标与非目标

### 1.1 目标

- 无损 JSON wire 视图：原始字节、节点 span、成员顺序、重复 key、未知字段全部保留。
- 协议无关 semantic 类型：`Request`/`Response`/`Part`/`Tool`/`Params`/`StateRefs`/`Loss`。
- 单次扫描的 `Envelope` 和可被外部 Go 程序直接 import 的 `pkg` 边界（不依赖 `internal`）。

### 1.2 非目标

- 不解释协议语义、不做转换/loss policy/路由（M02、M04、`pkg/route` 负责）。
- 不实现流式解析/SSE 编解码；`StreamDecoder`/`StreamEncoder` 只是接口。
- 不读写 DB/cassette/网络/配置、不写日志、不做脱敏；不承诺 JSON pointer/lazy string/schema 校验。

## 2. 当前实现与约束

- [`pkg/xir/types.go`](../../pkg/xir/types.go)、[`envelope.go`](../../pkg/xir/envelope.go)、[`wire/wire.go`](../../pkg/xir/wire/wire.go)。
- 测试：`types_test.go`、`envelope_test.go`、`wire_test.go`，共 20 个测试函数；无 fixture/benchmark/fuzz。
- 已接入：`internal/router` 用 `xir.ParseEnvelope` 读取路由特征（[`router.go:2485`](../../internal/router/router.go#L2485)、[`router.go:2508`](../../internal/router/router.go#L2508)）；解析失败按流式 fail-closed。
- 兼容：不改变 HTTP、cassette、replay、DB 或 `pkg/protocolconvert`；无配置项/feature flag；`pkg/xir` 不 fail-open。

## 3. 对外契约（`pkg`）

### 3.1 包结构

```text
pkg/xir/       types.go（语义类型/Codec/Stream 接口）、envelope.go
pkg/xir/wire/  wire.go（无损 JSON tree 与 Edit/Apply）
```

### 3.2 Go API

```go
package xir
type Protocol string // chat_completions / responses / messages / gemini
func ParseProtocol(s string) (Protocol, error); func (p Protocol) Valid() bool; func (p Protocol) Endpoint() string
type Document struct { Protocol Protocol; Operation Operation; Raw []byte; Root *wire.Node }
func ParseDocument(protocol Protocol, operation Operation, raw []byte) (*Document, error)
type DecodeOptions struct { NeedParts, NeedTools, NeedUsage, NeedState bool }; type EncodeOptions struct { Model string; Stream, Vertex bool }
type Codec interface { Protocol() Protocol
    DecodeRequest(ctx context.Context, doc *Document, opts DecodeOptions) (*Request, error)
    EncodeRequest(ctx context.Context, req *Request, opts EncodeOptions) ([]byte, error)
    DecodeResponse(ctx context.Context, doc *Document, opts DecodeOptions) (*Response, error)
    EncodeResponse(ctx context.Context, resp *Response, opts EncodeOptions) ([]byte, error) }
type Request struct { /* Model/System/Messages/Tools/ToolChoice/Params/State/Stream/Extensions/Ref */ }
type Response struct { /* ID/Model/Status/Parts/Usage/StopReason/StopDetail/Error/Extensions/Ref */ }
type Part struct { /* Kind/Text/ToolCall/ToolResult/Media/Reasoning/Citation/Search/Sealed/Ref */ }
type Sealed struct { Protocol Protocol; Kind string; Raw json.RawMessage }
type Loss struct { Field string; Kind LossKind; Reason string; Fatal bool; From, To Protocol }; func (l Loss) Error() string; func (s StateRefs) HasServerState() bool
type Capabilities struct { ChatCompletions, Responses, Messages, Gemini bool }; func (c Capabilities) Has(p Protocol) bool; func (c Capabilities) Protocols() []Protocol
type Envelope struct { Model string; Stream, Tools, StructuredOutput bool; MaxTokens float64; PreviousResponseID, PromptCacheKey, ConversationID, SessionID string }
func ParseEnvelope(raw []byte) (Envelope, error)
func RewriteModel(raw []byte, model string) ([]byte, bool, error)
func EnsureStreamIncludeUsage(raw []byte) ([]byte, bool, error)
// StreamEvent、StreamDecoder、StreamEncoder 定义在 types.go，M04 描述其契约。

package wire
type Kind uint8; type Span struct { Start, End int }; type Member struct { Key string; KeySpan Span; Value *Node }
type Node struct { Kind Kind; Span Span; Bytes []byte; Members []Member; Items []*Node; Str string }
func Parse(raw []byte) (*Node, error); func MustParse(raw []byte) *Node
func (n *Node) Raw() []byte; func (n *Node) Get(key string) (*Node, bool) // 重复 key 取最后一个
func (n *Node) Index(i int) (*Node, bool); func (n *Node) String() (string, bool); // 另有 Int/Float/Bool/StringField/IntField/BoolField/ObjectElem/Len
type EditMode uint8; type Edit struct { Span Span; Bytes []byte; Mode EditMode; Reason string; PreserveLength bool }; func Apply(raw []byte, edits []Edit) ([]byte, error) // ReplaceRaw/ReplaceString/Delete
```

### 3.3 错误与诊断

`wire.Parse`/`wire.Apply`/`xir.ParseProtocol` 都返回 `fmt.Errorf`（无 sentinel，错误不含 body 内容）；`xir.Loss` 实现 `error`，调用方用 `errors.As` 或字段判断。非法 JSON、span 越界、edit 重叠、破坏 JSON 或长度不满足时必须拒绝，不能使用部分结果；未知字段和缺失可选字段不算错误。

### 3.4 并发、生命周期、资源

所有解析/改写函数无共享可变状态，可并发；`Node.Bytes`/`Raw()` 与 `Document.Raw` 共享底层数组，树使用期间 raw 必须存活。不启动 goroutine、不持有资源、不设超时，无 `Close`。

## 4. `internal` 集成

`internal/router` 用 `ParseEnvelope` 做一次 span 扫描；解析失败时把请求当流式，避免转换不可解析的 body。`internal/monitor`、`pkg/upstream`、`pkg/route` 只用 `Protocol`/`Operation`/`StateRefs` 等值类型；不读配置、不注入 registry，失败降级由调用方决定。

## 5. 数据模型与格式

### 5.1 类型

`Request`/`Response` 零值表示缺失，指针 nil 表示客户端未发送；`Extensions` 的 key 是协议名，value 是原始 JSON 对象；`Part` 的 payload 指针与 `Kind` 对应；`Sealed.Raw` 必须是可重发的合法 JSON。`Document.Raw` 是事实源，`Root`/`Node` 是派生索引。

### 5.2 序列化

XIR 结构没有 JSON tag，不能直接 marshal 成协议 body；`Document.Raw` 保留空白、转义、key 顺序和重复 key，未知字段不进入 semantic 类型。`Envelope` 只读，不进入 cassette/DB/API；本模块无持久化格式。

### 5.3 配置

不适用：无 YAML/env 配置、默认值或热更新。

### 5.4 数据放置

| 数据 | 文件 / DB / 内存 | 理由 | 丢失后果 |
| --- | --- | --- | --- |
| 原始 JSON 请求/响应 | 内存（调用方 body） | 运行时投影，不是证据文件 | 请求结束即释放；cassette 由录制层负责 |
| `Document`/`Node`/`Span`、`Envelope`、`Loss` | 内存 | 派生结构/路由视图/转换诊断 | 可从 raw 重建；Loss 未持久化 |

事实源是调用方 raw body；本模块不写 cassette/DB/文件。

## 6. 算法与不变量

### 6.1 算法

- `Parse`：递归下降单次扫描，O(n)/O(节点数)；string 立即用 `json.Unmarshal` 解码并复制，number 用 `json.Valid` 校验。
- `ParseEnvelope`：一次 `wire.Parse` + 固定字段查找；空 body 返回零值无错误，根非 object 报错。
- `Apply`：排序 O(m log m)、校验 O(m)、复制 raw O(n)，从后往前应用；逐 edit 拼接最坏 O(n·m)，最后验证 JSON。

### 6.2 不变量

- `Span` 是 `Raw` 的半开区间；`Node.Bytes`/`Raw()` 必须可在 `Raw` 中定位。
- 成员顺序、重复 key、未知字段、空白和转义不丢失；`Get` 对重复 key 取最后一个。
- `Apply` 不修改输入；无 edit 返回原 slice，有 edit 返回新 slice；输出必须是合法 JSON。
- `PreserveLength` 只对 `ReplaceRaw`/`ReplaceString` 生效，长度必须等于 span；`ParseDocument` 只绑定 Protocol/Operation。

### 6.3 边界条件

- 空 body：`ParseEnvelope` 返回零值；`wire.Parse` 报错，codec 自行决定是否接受。
- 非法/截断 JSON、尾随多余字节、未闭合字符串：报带偏移错误；重复 key 保留全部成员。
- 超大 body 无内置上限；不同 `Document` 可并发，同一文档只读可并发、改写需串行化。

## 7. 失败语义

| 场景 | 行为 | 是否影响上游健康 | 客户端看到什么 |
| --- | --- | --- | --- |
| 输入非法 JSON | `Parse` 返回带偏移 error | 否 | 由调用方决定（router 当流式；转换报错） |
| envelope 根非 object | 返回 error | 否 | 同调用方策略 |
| edit 越界/重叠/破坏 JSON | `Apply` 返回 error，无部分结果 | 否 | 调用方映射为内部错误 |
| 未知协议名 | `ParseProtocol` 返回 error | 否 | 配置错误 |

本模块不接触上游，也不决定客户端状态码；panic 由上层隔离。

## 8. 性能

热路径仅 `Envelope` 在 `internal/router` 使用；其余类型尚未进入转发热路径。`ParseEnvelope` 每次一次解析；完整 semantic decode 在 M02，默认路由不做。无 benchmark、无分配预算、无大 body 上限，不能声称达标。命令：`go test ./pkg/xir ./pkg/xir/wire`。

## 9. 安全

原始 body 可能含凭据/PII，只在调用方内存，不写日志/DB/cassette。`Span` 只暴露偏移；错误信息含偏移和 JSON kind，不含字段值。不执行外部输入；本模块不做脱敏，遮蔽必须在写出/记录前完成。

## 10. 可观测性

无指标、无日志、无事件；排障依赖错误偏移和调用方 decision trace。不新增 DB 事件或 cassette event。

## 11. 测试与验收

### 11.1 单元测试

`wire_test.go` 覆盖顺序/未知字段、转义、重复 key、非法输入、Apply 替换/删除/长度/重叠/空 edit、数字；`envelope_test.go` 覆盖基础字段、Gemini `maxOutputTokens`、非法根、stream 拼写；`types_test.go` 覆盖协议别名、Capabilities、Loss、Sealed/Extensions 往返。无 fuzz/benchmark，race 由全包 `-race` 覆盖。

### 11.2 集成测试

`internal/router` 请求特征测试覆盖 `ParseEnvelope` 接入；不需要网络/cassette/fixture。

### 11.3 差分测试

无专门新旧差分；router 现有测试保证 model/stream/tools/max-token 不回归，不等价于逐字节差分。

### 11.4 验收标准

- [x] 公开 API 有 Go doc 和离线测试；[x] span 保留、`Apply` 验证 JSON；[x] 文件/DB/内存边界明确，未写 cassette。
- [ ] benchmark 达到预算（未定义）；[x] 非法输入/越界/重叠有失败测试；[ ] 文档与代码完全同步（见第 13 节）。

## 12. 迁移、兼容、回滚

- 无数据库迁移、无文件格式变化；新增包，兼容 cassette V3、replay、HTTP 和 `pkg/protocolconvert`。
- 无 feature flag；回滚移除 router 对 `ParseEnvelope` 的调用并恢复旧解析。旧包待 M10/M12 接入后清理。

## 13. 开放问题

| 问题 | 影响 | 建议 | 需要谁决定 |
| --- | --- | --- | --- |
| `Node.Str` 在 parse 时立即解码，与“惰性/不复制字符串”目标不一致 | 大 body 分配偏高 | 改为按需 `DecodeString` 或修正设计 | M01 owner |
| `Apply` 逐 edit 复制尾部，最坏 O(n·m) | 大量 patch 变慢 | 预计算总长度、单次重建 | M01 owner |
| `DecodeOptions`/`EncodeOptions.Vertex` 已定义但 codec 未使用 | 调用方误以为可减少解码/切换 Vertex | M02 实现或标记 reserved | M02/M04 owner |

## 14. 实施拆分

1. 类型与接口；2. `wire.Parse`/`Edit`/`Apply` 与校验；3. wire/envelope/types 测试；4. router 接入（commit `c009b15`）；5. M10/M12 接入后删除旧解析（未开始）。
- 验证：`GOCACHE=/tmp/gocache-trajecta go test ./pkg/xir/... ./internal/router`。

## 15. 参考

- [总体架构](../01-overall-architecture.md) 第 7.2–7.4 节；[数据放置](../02-data-placement.md)。
- 代码：[`types.go`](../../pkg/xir/types.go)、[`envelope.go`](../../pkg/xir/envelope.go)、[`wire.go`](../../pkg/xir/wire/wire.go)。
- magpie（MIT，仅参考 IR/sealed 思路，未复制代码，也未引入 agent/bridge 状态）。
