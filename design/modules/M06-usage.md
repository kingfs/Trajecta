# M06 Usage 与成本

## 0. 头部
| 字段 | 值 |
| --- | --- |
| 模块 ID | M06 |
| 模块名 | Usage 与成本 |
| 归属 | `pkg/usage` |
| 状态 | Landed (P1) |
| 依赖模块 | 无（仅标准库） |
| 被依赖模块 | `pkg/xir`（类型、codec、hook） |
| 目标版本 | 2.0 |
| 最后更新 | 2026-11-09 |

## 1. 目标与非目标
**目标**：定义协议中立的 token usage 模型，供 XIR、codec、录制/审计消费方共用；保留 provider 分项（缓存读、缓存写、reasoning、Anthropic 5m/1h 缓存写），不折叠成单一总数；用非零覆盖规则合并流式快照，避免后到的零值抹掉已知计数。

**非目标**：不做价格表、成本计算或预算控制——本包目前**没有** `Cost`/计价 API，尽管总体架构包清单提到过；不做 tokenizer、估算或响应解析（分项来自 codec）；不读写 DB、cassette、配置，不访问网络。

## 2. 当前实现与约束
- 代码：[`pkg/usage/usage.go`](../../pkg/usage/usage.go)，测试 `usage_test.go`；只 import `strings`，随 `feat(pkg): add XIR and reusable capability packages` 落地。
- 零值表示“provider 未报告”，不是“0 token”；`Merge` 的非零规则依赖这个语义。
- 已被 `pkg/xir/types.go`、`pkg/xir/codec/*`、`pkg/xir/hook` 引用；`internal/*` 尚未直接使用。
- 录制/DB 现有 usage 字段仍是 `pkg/recordfile.UsageInfo`，未映射到 `usage.Usage`。
- 已知限制：无成本模型、无字段校验、无 `recordfile`/DB 适配器；未接入热路径。无 benchmark/fuzz。

## 3. 对外契约（`pkg`）
### 3.1 包结构
`pkg/usage/usage.go`（单文件，无 `internal` 子包）。

### 3.2 Go API
```go
package usage

type Usage struct {
    InputTokens  int `json:"input_tokens"`
    OutputTokens int `json:"output_tokens"`
    TotalTokens  int `json:"total_tokens"`

    ReasoningTokens int `json:"reasoning_tokens,omitempty"`

    CacheReadTokens       int `json:"cache_read_tokens,omitempty"`
    CacheCreationTokens   int `json:"cache_creation_tokens,omitempty"`
    CacheCreation5mTokens int `json:"cache_creation_5m_tokens,omitempty"`
    CacheCreation1hTokens int `json:"cache_creation_1h_tokens,omitempty"`

    Source    string `json:"source,omitempty"`
    Estimated bool   `json:"estimated,omitempty"`
}

func (u Usage) Normalize() Usage
func (u Usage) PromptTokens() int
func (u Usage) Empty() bool
func Merge(a, b Usage) Usage
func Add(a, b Usage) Usage // Merge 的别名
```

### 3.3 错误与诊断
没有 error 返回值或诊断类型。计数不合法（例如负数）不会被拒绝或修正，调用方只能在边界处自行校验。计数缺失按零值处理。

### 3.4 并发、生命周期、资源
`Usage` 是纯值类型，`Merge`/`Add`/`Normalize` 无共享状态，可并发调用。不持有 goroutine、连接或文件，无 `Close`，无超时/取消语义。

## 4. `internal` 集成
```text
pkg/xir/types.go  ──uses──▶ pkg/usage
pkg/xir/codec/*   ──uses──▶ pkg/usage
internal/recorder ──uses──▶ pkg/recordfile.UsageInfo（未接入 pkg/usage）
internal/store    ──reads──▶ logs 中的 recordfile usage 列
```

值类型直接传递，无接口、无 registry；codec 解析在审计/转换路径，尚未进入转发热路径。无配置、无 feature flag。无失败降级路径，缺失分项保留零值。

## 5. 数据模型与格式
### 5.1 类型
`InputTokens`/`OutputTokens` 是 provider 语义：OpenAI 的 input 通常已含缓存部分，Anthropic 的 input 不含缓存读写。`PromptTokens()` 把 input、cache read、cache creation 三者相加，调用方必须按 provider 语义解释，不能无条件当“账单 prompt 数”。`CacheCreation5mTokens`/`CacheCreation1hTokens` 只对 Anthropic 风格 provider 有意义，是 `CacheCreationTokens` 的细分，不自动相加。`Source` 命名来源 wire shape（chat/responses/messages/gemini），`Estimated` 标记本地推算而非 provider 事实。

### 5.2 序列化
字段有 JSON tag，供 IR/审计投影序列化；`omitempty` 只作用于分项和 Source/Estimated，三个主计数始终输出。未知 JSON 字段由标准库忽略；本包不定义版本号。它不直接进入 cassette prelude、DB 表或 HTTP API。

### 5.3 配置
无配置项，无默认值、校验规则或热更新。

### 5.4 数据放置
| 数据 | 文件 / DB / 内存 | 理由 | 丢失后果 |
| --- | --- | --- | --- |
| usage 快照 | 内存（调用方持有） | 从 provider 响应解析 | 可从原始响应重算 |
| 合并后的 usage | 内存 | 流式最终值 | 可重新合并 |
| cassette / DB | 本包不写 | usage 是可重算派生数据 | 不适用 |

不写 cassette、不写 DB；若后续写入 `logs` usage 列，属于 recorder/store 的职责。无保留期或清理逻辑。

## 6. 算法与不变量
### 6.1 算法
- `Normalize`：`TotalTokens == 0` 且 input/output 至少一个为正时，令 total = input + output；不伪造 input/output。
- `Merge`：逐字段非零覆盖（`b` 的非零字段胜出，零字段不擦除 `a`）；`Source` 取后到的非空值；`Estimated` 取逻辑或；最后若合并结果有 input 或 output，则 total 重算为两者之和，否则保留已报告的 total。
- `PromptTokens`：`InputTokens + CacheReadTokens + CacheCreationTokens`。
- `Empty`：所有计数（含三项缓存细分）为零。

复杂度均为 O(1)，无堆分配。

### 6.2 不变量
零值永远表示“未报告”，`Merge` 的零字段不会覆盖已有计数；`Normalize`/`Merge` 不会凭空空想 input/output；相同 `Usage` 反复 `Merge` 结果不变（幂等）；`Merge` 后有 input/output 明细时 total 一定等于二者之和，provider 原始 total 只在没有任何 input/output 明细时保留。因此“total 与分项不一致”的快照会被重算，这是当前实现的实际行为。

### 6.3 边界条件
只有 total、没有 input/output 时保留 provider total；只有 input/output、没有 total 时补/重算；后到快照带 total 和 input/output 且 total 不等于两者之和时以重算值为准；负数/超大数原样参与运算；同时含 `Estimated` 与 provider 计数时保留 `Estimated=true`，由调用方决定展示。

## 7. 失败语义
| 场景 | 行为 | 影响上游健康 | 客户端看到 |
| --- | --- | --- | --- |
| 分项缺失 | 零值，不报错 | 不适用 | 统计可能偏小 |
| 分项非法 | 原样保留，不校验 | 不适用 | 由调用方处理 |
| 合并冲突 | 非零覆盖，total 重算 | 不适用 | 无直接影响 |
| 内部 bug | 纯函数，无 panic 捕获 | 不适用 | 调用方 panic/错误传播 |

本包天然 fail-open：没有阻断路径。

## 8. 性能
O(1)、按值传递、无堆分配、无 I/O。未接热路径，无 benchmark；对每请求一次的 usage 合并而言开销可忽略。

## 9. 安全
只含计数和来源标签，不含 prompt/响应文本、凭据或 PII。`Source` 是调用方提供的任意字符串，本包不校验、不写日志；若上层把它拼进日志需自行限制字符集。不执行外部输入，无 SSRF/路径/注入面。

## 10. 可观测性
无指标、事件或日志。usage 的展示/落库由 recorder、store 或 Monitor 负责；本包只提供值。

## 11. 测试与验收
### 11.1 单元测试
`GOCACHE=/tmp/trajecta-gocache go test ./pkg/usage` 通过（Go 1.26.9，`ok` 约 0.004s）。覆盖 Normalize 补 total/保留 provider total、Merge 非零覆盖、零不擦除、无明细时保留 total、幂等、PromptTokens、Empty。无 fuzz/benchmark。

### 11.2 集成测试
无；尚未与 `pkg/xir` codec 的 golden fixture 做对照。

### 11.3 差分测试
尚未与 `pkg/recordfile.UsageInfo` 做对照。

### 11.4 验收标准
- [x] 公开 API 有 Go doc，离线测试通过。
- [ ] benchmark 达到预算（无热路径）。
- [x] 边界与合并语义有测试。
- [x] 不写 cassette/DB，边界明确。
- [ ] 成本 API 与 DB/recordfile 映射（未实现）。
- [x] 文档同步（本文）。

## 12. 迁移、兼容、回滚
新增包，无数据库迁移、无 cassette 变化、无 feature flag。`pkg/xir` 已依赖它；回滚需先移除 XIR 引用。旧 `pkg/recordfile.UsageInfo` 保持不变，二者尚未建立映射，迁移期允许并存。

## 13. 开放问题
| 问题 | 影响 | 建议 | 需要谁决定 |
| --- | --- | --- | --- |
| 成本 API 是否放入本包？ | M06 名称与总体架构清单 | 单独 `pkg/cost` 或本包 `Cost`，需价格表来源 | 架构 |
| 如何映射到 `recordfile.UsageInfo`/DB？ | 录制统计 | 在 recorder 边界做适配，保持本包中立 | M10 |
| provider total 与分项冲突时以谁为准？ | 统计一致性 | 当前以分项重算；若需保留 total 应改 Merge | M06 负责人 |
| OpenAI input 含缓存、Anthropic 不含，是否统一？ | 跨厂商比较 | 保持 provider 语义，展示层解释 | 审计/UI |

## 14. 实施拆分
1. 已完成：类型、Normalize、Merge、PromptTokens/Empty、单元测试。
2. 下一步：XIR codec 的 usage 输出覆盖四协议 golden fixture。
3. 下一步：recorder 增加 `usage.Usage` ↔ `recordfile.UsageInfo` 适配。
4. 可选：成本模型独立成包，不在本包塞价格表。

验证 `go test ./pkg/usage ./pkg/xir/...`，接入后跑 `task check:quick`。

## 15. 参考
- [Usage 实现](../../pkg/usage/usage.go)
- [XIR 类型](../../pkg/xir/types.go)
- [总体架构](../01-overall-architecture.md) §6.2、§7.4
- [数据放置](../02-data-placement.md)
