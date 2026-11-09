# M10 Gateway 流水线

## 0. 头部

| 字段 | 值 |
| --- | --- |
| 模块 ID | M10 |
| 模块名 | Gateway 流水线 |
| 归属 | `internal/gateway`（Body、RequestTransform 已落地）；`internal/proxy` 响应侧/阶段拆分待迁移 |
| 状态 | 部分落地 (P2) |
| 依赖模块 | M02–M09 |
| 被依赖模块 | 计划由 `internal/proxy` 使用 |
| 目标版本 | 2.0 |
| 最后更新 | 2026-11-09 |

## 1. 目标与非目标

**目标**：为热路径建立协议无关的固定阶段——edge → identity → plan → request transform → egress → response transform → record/finalize——让 XIR、Hook、转换和录制通过明确接口接入，而不是继续堆在 `handler.go`。

**当前非目标**：尚未替换 `ServeHTTP`，尚未迁移 conversion/mask/recording 的完整逻辑。本轮只落地 `Body{Client,Wire}` 并在 proxy 主循环中使用，先消除“四个 body 变量靠约定一致”的风险。

## 2. 当前实现

- [`internal/gateway/body.go`](../../internal/gateway/body.go)：`Body{Client []byte, wire []byte}`。
  - `Client` 是客户端原始字节，不复制、不替换；
  - `wire` 为 nil 表示直通；
  - `SetWire` 在 wire 等于 Client 时恢复直通；
  - `WireBytes` 返回实际发往上游的字节；
  - `Changed` 表示是否发生改写。
- [`internal/gateway/transform.go`](../../internal/gateway/transform.go)：`RequestTransform` 固定顺序
  `alias -> record body -> conversion -> masking -> wire body`，返回 `Body`、`RecordBody`、`WireBody`、converter、path override、warnings、mask events。
- `internal/proxy/handler.go` 的重试循环现在只构造 `RequestTransform`（`ConverterFor` 闭包调用 `conversionForSelection`），然后使用结果：
  - recorder 用 `RecordBody`；
  - send 用 `Body.WireBytes()`；
  - conversion event 用 `Converter`（type assert 回 `*protocolConversion`）。
- 行为保持：cassette 仍记录 alias 后、conversion/mask 前的 body；mask 仍只作用于上游 wire。

## 3. 数据放置

| 数据 | 位置 | 说明 |
| --- | --- | --- |
| `Body.Client` | 内存 | 客户端原始字节，请求结束释放 |
| `Body.wire` | 内存 | 实际上游字节，可选 |
| cassette | 文件 | 由 recorder 用 `Client`/`recordBody` 写 |
| 转换/mask 摘要 | DB | 现有事件/审计，不在本包 |

## 4. 不变量

1. `Body.Client` 在构造后不可变；调用方不得原地改写。
2. `WireBytes` 在未改写时返回 `Client` 同一 slice；改写后返回 wire。
3. `SetWire` 传入 nil/同 slice/等值 slice 时恢复直通，`Changed` 为 false。
4. recorder 永远不接收 `WireBytes()`，只接收 `Client`/`recordBody`。
5. mask 后长度变化由 send 路径自行计算 `Content-Length`，不依赖上游头。

## 5. 测试

- `internal/gateway/body_test.go`：passthrough、set wire、等值恢复、Client 保持、空 body。
- `internal/proxy` 全量测试通过（含 alias/stream-options/conversion/secretmask e2e）。

## 6. 待做

- [x] 把 alias/conversion/mask 的调用顺序抽成 `RequestTransform`，由 gateway 单测覆盖。
- [ ] 把 `recordBody`/wire 的语义进一步固化为 `Body{Client,Record,Wire}` 或 `RecordBody()`。
- [ ] 响应侧 `Upstream`/`Client` Body，接入 restore/usage/convert/record 顺序。
- [ ] 把本地 Responses runtime 作为 `ModeLocal` 接入同一请求阶段。
- [ ] 删除 `handler.go` 中重复的 body 变量与转换上下文。
