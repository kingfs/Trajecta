# 跨协议流式转换立项评估

> 状态：评估完成，2.0 保持 non-goal  
> 更新：2026-11-10  
> 相关：`pkg/xir/stream`、`pkg/xir/convert`、`internal/gateway`、`internal/proxy`

## 1. 结论

跨协议流式转换**不进入 2.0 热路径**。当前只保留显式拒绝：

- `pkg/xir/convert` 对 `Stream:true`、请求体 `stream:true`、SSE 响应返回 `ErrStreamingConversion`；
- proxy 的 legacy/xir 转换都只处理非流式；
- 原生候选永远优先，流式请求只走原生 passthrough。

原因不是“不能做”，而是它需要一套独立的逐帧状态机、协议事件映射、工具/推理/usage 终止语义和回退策略。把它塞进现有非流式 `ConvertRequest/ConvertResponse` 会引入静默截断和首字节延迟，风险高于收益。

## 2. 已有基础

- `pkg/xir/stream`：SSE `Frame`、`Decoder`、`Encoder`、`Assembler`；
- `pkg/xir`：`StreamEvent` / `StreamEventKind` / `StreamDecoder` / `StreamEncoder` 接口；
- `pkg/xir/codec`：四协议完整请求/响应 codec，可复用字段语义；
- `pkg/usage`：流式 usage 合并；
- `pkg/xir/hook`：已定义 stream delta 的 Point。

缺口：没有四个协议的 `StreamDecoder`/`StreamEncoder` 实现，也没有逐帧转换状态机。

## 3. 目标范围（如果立项）

第一阶段只做：

- Chat Completions ↔ Anthropic Messages；
- 文本 delta；
- tool call 开始/参数增量/结束；
- finish reason；
- usage；
- error / stop / ping。

第二阶段：

- Chat ↔ Gemini SSE；
- reasoning/thinking 块；
- 多模态增量。

Responses SSE 不进入转发路径：`/v1/responses` 由本地 runtime 编排，已有自己的 native/local 决策。

## 4. 设计草案

```text
client SSE
  -> xir/stream.Decoder (Frame)
  -> protocol StreamDecoder (StreamEvent)
  -> stream converter state machine
  -> target StreamEncoder (StreamEvent -> Frame)
  -> xir/stream.Encoder
```

关键约束：

1. **逐帧，不聚合**：不能先读完整响应再重编码；
2. **Finalize 必须发终止事件**：`message_stop` / `[DONE]` / finish reason / usage；
3. **工具调用按 index/id 关联**：参数增量不能跨工具串线；
4. **native 优先**：有原生候选时永不转换；
5. **loss 显式**：不能携带的字段拒绝，不静默丢弃；
6. **首字节延迟**：转换器不得等待完整 block；
7. **usage**：优先上游最终 usage，缺失时明确标记 estimated；
8. **错误**：上游 4xx/5xx 不转换，原样透传。

## 5. 建议接口

不修改现有非流式 API；新增独立入口：

```go
type StreamConverter interface {
    Convert(ctx context.Context, src io.Reader, dst io.Writer, opts StreamOptions) (StreamResult, error)
}
```

`StreamOptions` 至少包含 `From`、`To`、`Model`、`Vertex`、`Policy`、`Registry`。

配置建议独立：

```yaml
router:
  selection:
    protocol_conversion: true
    protocol_conversion_stream: false   # 默认 false
```

不能复用 `protocol_conversion` 直接打开流式转换。

## 6. 验收标准

- 12 个协议对的非流式行为不变；
- 流式转换只对显式开启的新开关生效；
- 原生候选存在时不触发转换；
- 文本、tool call、finish reason、usage 的逐帧 golden test；
- 不聚合、TTFT 不因转换显著变差；
- 不可表达内容返回 400，不产生半截流；
- 失败可回退到原生候选，且不污染健康度。

## 7. 工作量与风险

- 四个协议的 StreamDecoder/Encoder：中高；
- Chat↔Messages 状态机：中；
- Chat↔Gemini 状态机：中高；
- Responses SSE：不建议进入该路径；
- 风险：tool call 参数串线、usage 丢失、finalize 缺失、客户端 SDK 对事件顺序敏感。

建议作为独立 milestone（例如 2.1），不与本次 2.0 收尾混做。

## 8. 本轮锁定

- 新增全协议对测试，确保任何 `Stream:true` 跨协议转换都返回 `ErrStreamingConversion`；
- 不新增流式转换代码路径；
- 文档和配置保持“显式关闭”。
