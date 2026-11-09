# M04 协议转换与流式

## 0. 头部

| 字段 | 值 |
| --- | --- |
| 模块 ID | M04 |
| 模块名 | 协议转换与流式 |
| 归属 | `pkg/xir/convert`（非流式转换）、`pkg/xir/stream`（SSE 帧与事件装配） |
| 状态 | Landed (P2)，范围仅非流式 hub 转换 |
| 依赖模块 | M01、M02；M03（stream Point 定义，convert 本身不 import hook） |
| 被依赖模块 | 未来 `internal/gateway`（M10） |
| 目标版本 | Trajecta 2.0 |
| 最后更新 | 2026-11-09 |

## 1. 目标与非目标

### 1.1 目标

- 实现 semantic hub：源协议 decode → XIR → 目标协议 encode，不为协议对写专用转换器。
- 同协议转换永远字节直通：不解析、不 decode、不重新编码。
- 用 `xir.Loss` 记录语义损失，并用 `Strict`/`Warn`/`Drop` 决定是否拒绝。
- 通过 `registry` 选择 codec，未注册 codec 返回结构化错误。

### 1.2 非目标

- **不实现跨协议流式转换**。`pkg/xir/stream` 提供 SSE 帧编解码与 `xir.StreamEvent` 装配，但 convert 对跨协议流式一律拒绝。
- 不做上游/渠道选择、健康检查、重试或路由策略（`pkg/route`/M08）。
- 不持久化 loss、不写 cassette/DB，不实现 adaptive unfit memory。
- 不承诺携带所有 stateful/opaque 字段；不能表达时按 fatal loss 处理。

## 2. 当前实现与约束

- [`convert.go`](../../pkg/xir/convert/convert.go)：`ConvertRequest`/`ConvertResponse`、`Policy`、`Options`、`Result`。
- [`errors.go`](../../pkg/xir/convert/errors.go)：三个 sentinel 与三个结构化错误；[`losses.go`](../../pkg/xir/convert/losses.go)：损失判定；[`normalize.go`](../../pkg/xir/convert/normalize.go)：目标协议清理/角色重排。
- 测试：`convert_test.go` 覆盖同协议直通（含非 JSON、`Stream:true`）、多条跨协议路径、stateful 拒绝、流式拒绝、三种 policy、缺 codec。
- `pkg/xir/stream` 只有 SSE 帧/装配原语，没有协议事件映射或跨协议转换；convert 不 import 它。
- 当前没有 `internal` 包 import `pkg/xir/convert`；转发路径仍使用 `pkg/protocolconvert`（`router.selection.protocol_conversion` 默认关闭）。
- 约束：同协议直通必须保持原 slice；跨协议只接受非流式 JSON；未接入前不改变现有转发行为。

## 3. 对外契约（`pkg`）

### 3.1 包结构

```text
pkg/xir/convert/
  convert.go    ConvertRequest/ConvertResponse/Policy/Options/Result
  errors.go     sentinel + StreamingConversionError/CodecMissingError/FatalLossError
  losses.go     loss ledger 与各字段损失判定
  normalize.go  目标协议清理、tool_result 拆分、system 提升
```

### 3.2 Go API

```go
type Policy string; const Strict Policy = "strict"; const Warn Policy = "warn"; const Drop Policy = "drop"
type Options struct { Model string; FromOperation, ToOperation xir.Operation; Stream, Vertex bool; Policy Policy; Registry *registry.Registry }
type Result struct { Bytes []byte; Loss []xir.Loss; From, To xir.Protocol; Steps []xir.Protocol }
func ConvertRequest(ctx context.Context, raw []byte, from, to xir.Protocol, opts Options) (Result, error)
func ConvertResponse(ctx context.Context, raw []byte, from, to xir.Protocol, opts Options) (Result, error)

var ErrStreamingConversion, ErrFatalLoss, ErrCodecMissing error
type StreamingConversionError struct { From, To xir.Protocol } // Unwrap -> ErrStreamingConversion
type CodecMissingError struct { Protocol xir.Protocol; Direction string } // "source"/"target"
type FatalLossError struct { Losses []xir.Loss } // Unwrap -> ErrFatalLoss
```

### 3.3 错误与诊断

| 错误 | 触发条件 | 调用方应如何处理 |
| --- | --- | --- |
| `ErrStreamingConversion` | `Options.Stream`、请求体 `stream:true`、响应体像 SSE | 不转换，按流式路径处理 |
| `ErrFatalLoss` | Strict/Drop 下存在 fatal loss；错误携带 `FatalLossError.Losses` | 映射 400/策略拒绝，不能使用 `Result.Bytes` |
| `ErrCodecMissing` | 源/目标协议不在 registry | 配置错误；`Direction` 指明哪一侧 |
| 未知 policy | `Options.Policy` 不是空/Strict/Warn/Drop | 调用方 bug/配置错误 |
| parse/decode/encode error | 非法 JSON、结构不匹配、编码失败 | 映射客户端/内部错误 |

用 `errors.Is` 判断 sentinel，`errors.As` 取 `StreamingConversionError`/`FatalLossError`/`CodecMissingError`；不要字符串匹配。`Warn` 是唯一允许在 fatal loss 下返回 body 的 policy。

### 3.4 并发、生命周期、资源

两个入口都是纯函数：不持有状态、不启动 goroutine、不打开资源；`registry.Registry` 并发安全，`registry.Default()` 可共享。输入 `raw` 不被修改（同协议直接返回它），输出 `Bytes` 归调用方所有；`context` 只在入口检查取消。

## 4. `internal` 集成

当前无集成。未来由 `internal/gateway` 在 native 候选不可选时调用；codec 通过 `Options.Registry` 显式注入，loss 写入将来的路由/审计记录。没有 feature flag 或配置项；`router.selection.protocol_conversion` 仍控制旧 `pkg/protocolconvert` 路径，与本包无关。转换失败时不接触上游，也不改变上游健康。

## 5. 数据模型与格式

### 5.1 类型

- `Policy` 零值是 `Strict`；`Result.Steps` 在直通时为 `[from]`，hub 转换时为 `[from,to]`。
- `Result.Loss` 每个字段最多一条；fatal 条目优先于同字段的非 fatal 条目。
- `Options.Model` 覆盖源/目标 model；`FromOperation` 只影响源 `Document.Operation`，`ToOperation` 目前未被读取；四个 codec 的 encode 不按 operation 分支。
- `Options.Vertex` 当前无任何 codec 读取；`Options.Stream` 只用于拒绝/直通判断，不触发流式编码。

### 5.2 序列化

`Result.Bytes` 是目标协议 JSON（同协议时是原输入 slice）；`Loss`/`Steps` 只存在内存，没有 JSON tag，不写 cassette、DB 或 API。转换不产生 event，也不改变 cassette 的原始字节。

### 5.3 配置

不适用：无 YAML/env 配置；`Options` 由调用方以编程方式传入，loss policy 默认 `Strict`。

### 5.4 数据放置

| 数据 | 文件 / DB / 内存 | 理由 | 丢失后果 |
| --- | --- | --- | --- |
| 输入 raw body | 内存（调用方） | 转换输入 | 请求结束释放 |
| `Result.Bytes` | 内存 | 目标协议 body | 由调用方发送/记录 |
| `Loss`/`Steps` | 内存 | 转换诊断 | 不持久化，无法事后审计 |

本模块不写 cassette、不写 DB、不写文件；重启后无需重建任何转换状态。

## 6. 算法与不变量

### 6.1 算法

1. 检查 `ctx`；`from==to` 立即返回原 slice（`Steps=[from]`，无 loss），不检查 policy/stream。
2. 校验 policy；`opts.Stream` 或响应 `looksStreaming` 命中 `data:`/`event:`/`id:` 前缀则返回 `StreamingConversionError`。
3. 查找源/目标 codec；parse 源 body；decode 为 `Request`/`Response`；请求体 `stream:true` 也拒绝。
4. 计算 loss ledger；按 policy 过滤/拒绝；`prepareRequest`/`prepareResponse` 清理目标不支持的值；应用 `Model`。
5. 目标 codec encode；写 `Result.Bytes` 与 `Steps=[from,to]`。复杂度 O(输入+输出大小)，无流式缓冲。

### 6.2 不变量

- **同协议永远字节直通**：返回与输入相同的 slice，不 parse/decode/encode，不产生 loss；即使输入不是 JSON、`Stream:true` 也一样。
- **跨协议流式一律拒绝**：`Options.Stream`、请求体 `stream:true`、或响应像 SSE 时返回 `ErrStreamingConversion`；没有降级为聚合转换。
- loss ledger 按字段去重，fatal 覆盖非 fatal；Strict/Drop 拒绝 fatal，Warn 返回 body 并保留全部 loss。
- fatal 拒绝时 `Result.Bytes == nil`，调用方不得发送部分结果。
- `previous_response_id`/`conversation` 转到非 Responses 是 fatal；外来协议 Extensions、sealed/encrypted/signature、无法表达的媒体/工具/参数也是 fatal。
- 外来 sealed 在 `prepare*` 中剥离；只有 Warn 能走到 encode，因此不会伪造跨协议签名。
- 同协议不查 registry、不依赖 codec；`Result` 不包含路由/健康/DB 状态。

### 6.3 边界条件

- 空/非法 JSON：同协议直通不报错；跨协议在 parse 阶段报错。
- nil/未知 codec：`CodecMissingError`；未知 policy：error；nil context 会 panic（上游应传有效 ctx）。
- `looksStreaming` 只看前导 `data:`/`event:`/`id:`，带注释或空行的 SSE 可能漏判；请求体 `stream` 字段是主要判据。
- Gemini 目标不把 model 写进 body；跨协议 Chat→Gemini 需调用方用 URL path 传 model，`Options.Model` 不会出现在 Gemini body。
- 响应转换只处理非流式 body；多 choice/candidate 由源 codec 只取第一个。

## 7. 失败语义

| 场景 | 行为 | 是否影响上游健康 | 客户端看到什么 |
| --- | --- | --- | --- |
| 同协议 | 原样返回，无错误 | 否 | 原始字节 |
| 跨协议流式 | `ErrStreamingConversion`，无 body | 否 | 调用方走流式/拒绝 |
| fatal loss（Strict/Drop） | `ErrFatalLoss`，无 body | 否 | 400/策略错误 |
| fatal loss（Warn） | 记录 loss，返回转换 body | 否 | 目标协议响应 |
| 缺 codec/非法 policy | 结构化/文本 error | 否 | 配置/内部错误 |
| 目标 encode 失败 | 包装 error，无 body | 否 | 内部错误 |

转换不接触上游；是否 fail-open 由调用方 policy 决定，默认 fail-closed。

## 8. 性能

- 同协议直通 O(1)、零解析/零编码，是热路径应优先走的路径。
- 跨协议一次 parse + 一次 semantic decode + 一次 encode；loss 检查与 normalize 线性扫描。
- 无 benchmark、无分配预算；流式转换未实现，因此没有逐帧内存/延迟数据。命令：`go test ./pkg/xir/convert`。

## 9. 安全

- 输入/输出 body 可能含凭据/PII，只在内存；本包不写日志/DB/cassette。
- loss 的 `Reason` 只描述字段/能力，不写字段值；codec 错误含 JSON 路径/kind，不含值。
- 不执行外部输入；`Options.Model` 只作为字符串写入支持 model 字段的目标 body（Gemini 除外）。
- 转换不负责脱敏；需要遮蔽时应在转换前后由 Hook/secretmask 处理。

## 10. 可观测性

- 无指标、无日志、无事件；唯一诊断是返回的 `Loss`/错误。
- loss 目前不持久化；生产接入时需由 gateway 写入 DB（尚未实现）。不新增 cassette event。

## 11. 测试与验收

### 11.1 单元测试

`TestSameProtocolBytePassthrough` 对四协议验证请求/响应字节和 slice 身份；`TestSameProtocolSkipsDecode` 用非 JSON + `Stream:true` 证明不解析；`TestStreamingConversionRejected` 覆盖 Options、请求体 stream、SSE 三种入口；`TestLossPolicyStrictWarnDrop`/`TestNonFatalLossDropSuppressesLedger` 覆盖 policy；`TestStatefulResponsesToChatRejectedUnderStrict` 覆盖 fatal loss；`TestCodecMissing` 覆盖缺 codec；另有多条 Chat/Responses/Messages/Gemini 文本与工具转换用例。

### 11.2 集成测试

无跨包/网络/cassette 集成；`registry.Default()` 在测试中隐式使用。

### 11.3 差分测试

无与 `pkg/protocolconvert` 的差分测试；后者仍是转发路径实现，接入 M10 前必须补。

### 11.4 验收标准

- [x] 同协议字节直通；[x] 跨协议流式拒绝；[x] loss policy/结构化错误；[x] 离线测试通过。
- [x] 不写 cassette/DB；[ ] 流式转换（未实现）；[ ] 与旧实现差分；[ ] benchmark；[ ] loss 持久化。

## 12. 迁移、兼容、回滚

- 新增包，无 DB 迁移/文件格式变化；不改变 `pkg/protocolconvert` 的现有行为。
- 无 feature flag；回滚删除调用方 import 即可。
- 旧 `pkg/protocolconvert` 在 M10 差分通过前保留；`pkg/xir/stream` 的 SSE 原语可供未来的逐帧转换使用，但不复用旧聚合式转换器。

## 13. 开放问题

| 问题 | 影响 | 建议 | 需要谁决定 |
| --- | --- | --- | --- |
| 跨协议流式转换未实现 | 跨协议 SSE 无法转换；`pkg/xir/stream` 只提供帧/装配原语 | 先定协议状态机/终止帧/usage 契约，再实现 M04 扩展 | M04/M10 owner |
| loss 不持久化 | 无法解释历史转换 | gateway 接入时写入 DB | M10/M12 owner |
| `Options.Vertex`/`ToOperation` 无效果、Gemini model 不在 body | 目标形状/模型可能错 | 实现或删除；Gemini 用 URL 参数 | M02/M04 owner |
| 无 adaptive unfit memory | 上游拒绝字段后仍会重试 | 在渠道/模型维度记录并规避 | M08/M10 owner |
| SSE 检测只看前缀 | 特殊 SSE 可能漏判 | 用 content-type/流式标志为主 | M04 owner |

## 14. 实施拆分

1. `Policy`/`Options`/`Result`/错误；2. 同协议直通；3. loss ledger 与 normalize；4. 两入口与 policy；5. 测试（均已完成）。6. 流式接口实现（未开始，且不属于当前 Landed 范围）。
- 验证：`GOCACHE=/tmp/gocache-trajecta go test ./pkg/xir/convert`。

## 15. 参考

- [M01](./M01-xir-foundation.md)、[M02](./M02-xir-codecs.md)、[M03](./M03-xir-hooks.md)。
- [总体架构](../01-overall-architecture.md) 第 7.6–7.7 节；[数据放置](../02-data-placement.md)。
- 代码：[`pkg/xir/convert`](../../pkg/xir/convert)、[`pkg/xir/types.go`](../../pkg/xir/types.go)。
