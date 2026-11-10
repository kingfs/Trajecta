# M12 审计与证据

## 0. 头部

| 字段 | 值 |
| --- | --- |
| 模块 ID | M12 |
| 模块名 | 审计与证据 |
| 归属 | `pkg/xir/audit`（投影）、`internal/audit`（cassette inspector）；cassette 最小化、ATIF 待做 |
| 状态 | 部分落地 (P2) |
| 依赖模块 | M01–M06 |
| 被依赖模块 | 计划由 M10/M13 使用 |
| 目标版本 | 2.0 |
| 最后更新 | 2026-11-09 |

## 1. 目标与非目标

**目标**：从 Exchange IR 投影出审计友好的结构，供 trace 详情、findings、资源索引和 ATIF 导出消费；投影是只读、可重算的，节点带稳定 ID、逻辑路径和原始字节 span。

**非目标（当前）**：不解析 cassette、不运行 detector、不写 findings/DB、不生成 ATIF、不做 cassette 最小化。这些仍由 `pkg/observe`、`internal/observeworker`、`internal/trajectory` 和现有 recorder 负责。

## 2. 当前实现与约束

- 代码：[`pkg/xir/audit/audit.go`](../../pkg/xir/audit/audit.go)、[`internal/audit/inspect.go`](../../internal/audit/inspect.go)；测试 `audit_test.go`、`inspect_test.go`。
- `internal/audit` 已把 cassette（V2/V3）解析为 XIR 并生成投影；`GET /api/traces/<id>/xir` 暴露该投影。Chat Completions 的 SSE 响应会按 chunk 装配为完整 `xir.Response`（文本、reasoning、tool calls、usage、finish reason）；其他协议的流式响应仍只解码 request 侧并返回 warning。
- 投影节点文本再经 `pkg/detect` 的 `secretmask` scanner 扫描，返回 `detections`（section/path/rule/start/end/confidence，不含匹配值）。这是 advisory 视图，不写 findings 表。
- `GET /api/traces/<id>/trajectory` 把单条 trace 导出为一行 ATIF JSONL；session 级导出仍是主路径。
- minimal prelude 无 event 时，trace 详情与 raw 视图共用 `timelineEvents`，从 raw request/response 合成 request/response 两段时间线。
- 输入是 `*xir.Request` / `*xir.Response`；不重新解析协议，不读 cassette。
- 节点 ID：`sha1(section|path|protocolType|index)[:16]`，前缀 `node_`；同一 payload 独立解析结果稳定。
- `SpanStart`/`SpanEnd` 来自语义值携带的 `*wire.Node`；没有 ref 时为 0。
- 媒体只保留 kind/URL/media type/filename；`Media.Data` 和 data URI 的字节不进入节点。
- sealed 块保留 raw JSON 引用，不解码为文本。
- `Nodes` 始终非 nil；nil request/response 生成空 section。

## 3. 对外契约（`pkg`）

```go
package audit

const SectionRequest = "request"
const SectionResponse = "response"

type Observation struct {
    Protocol xir.Protocol
    Operation xir.Operation
    Model string
    Status string
    Request RequestView
    Response ResponseView
    Usage *usage.Usage
    Error *ErrorView
    Nodes []Node
}

type RequestView struct { Model string; Stream bool; Instructions []Node; Messages []MessageView; Tools []Node }
type MessageView struct { Node Node; Parts []Node }
type ResponseView struct {
    ID, Status string
    Parts, ToolCalls, ToolResults, Reasoning, Refusals []Node
    Usage *usage.Usage
    StopReason xir.FinishReason
    StopDetail string
}
type Node struct {
    ID, Section, ProtocolType, Kind, Role, Name, ToolCallID, Path, Text string
    Raw json.RawMessage
    Media *MediaView
    SpanStart, SpanEnd int
    ParentID string
}

func Build(protocol xir.Protocol, operation xir.Operation, req *xir.Request, resp *xir.Response) *Observation
func BuildObservation(...) *Observation
func StableNodeID(section, path, protocolType string, index int) string
func EvidencePath(traceID string, node Node) string
```

`Path` 是审计 section 内的逻辑路径（如 `$.messages[0].parts[1]`），不是 provider payload 的 JSON Pointer；字节定位用 span。

## 4. `internal` 集成

已有一个 runtime 调用方（Monitor 的 trace XIR 子资源）。计划：

```text
internal/audit ──uses──▶ pkg/xir/audit
internal/observeworker / trajectory ──uses──▶ pkg/xir/audit
```

接入前需要：
- 从 cassette 构造 XIR（已有 codec）；
- 把现有 `pkg/observe` 的 findings/资源逻辑迁移或做差分；
- 决定是否保留 `pkg/observe` 的节点 ID 算法。

## 5. 数据放置

| 数据 | 位置 | 理由 | 丢失后果 |
| --- | --- | --- | --- |
| `Observation` | 内存 | 请求/按需重算 | 可重算 |
| 节点/ID/span | 内存 | 派生 | 可重算 |
| findings | DB（计划） | 查询/审计 | 可重算，但历史操作状态会丢 |
| cassette | 文件 | 事实源 | 不可丢 |

本包不写文件/DB/cassette，不持久化。

## 6. 不变量

1. 同一 `(protocol, request, response)` 的节点 ID 稳定。
2. 每个节点要么有 `Path`，要么有明确的根路径；`EvidencePath` 可直接定位。
3. 媒体 inline 字节不进入节点。
4. sealed 不被解码/改写。
5. `Nodes` 去重且非 nil。
6. 投影不修改输入。

## 7. 失败语义

- 输入 nil：返回空 Observation，不 panic。
- 语义值缺 ref：span 为 0，仍生成节点。
- 媒体 URL/data URI 解析失败：保留原 URL/空 media type，不丢节点。
- 不返回 error；投影没有需要调用方处理的失败。

## 8. 性能

- 单次线性遍历 request/response parts；节点 ID 每个节点一次 sha1。
- 无网络、无 JSON 重编码（raw 只 clone 已有字节）。
- 大工具结果会同时保留 text 和 raw，可能翻倍；后续可按上限裁剪。

## 9. 安全

- 节点文本原样复制；凭据遮蔽必须在投影前完成（proxy 已如此）。
- 媒体 inline 字节不复制。
- sealed 不解码。
- 本包不读 headers/credentials。

## 10. 测试与验收

- 7 个测试：chat 请求/响应、工具调用/结果、reasoning/sealed、usage/error/nil、确定性 ID/evidence、Responses instructions、媒体不泄漏 inline bytes。
- `go test ./pkg/xir/audit` 通过，覆盖率约 82.9%。

## 11. 待做

- [x] cassette → XIR → audit 的 `internal/audit` 服务（非流式；Chat SSE 装配；其他协议流式仅 request）。
- [ ] 与 `pkg/observe` 的差分（节点、工具调用、usage）。
- [ ] findings 迁移与 evidence path 持久化。
- [ ] ATIF 从 audit/XIR 导出。
- [x] cassette 最小 meta 写入（`trace.minimal_cassette`）；仓库默认配置已开启，旧配置/未写键仍为完整 prelude。旧 event 读取路径清理待做。
- [ ] 大 payload 裁剪策略。

## 12. 回滚

新包无 runtime 调用方；回滚只需删除计划中的接入，不影响现有 `pkg/observe`/`internal/observeworker`。
