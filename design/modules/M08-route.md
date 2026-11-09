# M08 路由引擎

## 0. 头部
| 字段 | 值 |
| --- | --- |
| 模块 ID | M08 |
| 模块名 | 路由引擎 |
| 归属 | `pkg/route` |
| 状态 | Landed (P2) |
| 依赖模块 | `pkg/xir` |
| 被依赖模块 | 当前无；计划由 M10 Gateway、Inspector 使用 |
| 目标版本 | 2.0 |
| 最后更新 | 2026-11-09 |

## 1. 目标与非目标
**目标**：提供纯决策、确定性的选路器——候选过滤、mode 选择、`compat`/`strict` 优先级解释、软/硬亲和、诊断候选和失败分类；同一份 `Snapshot`/`Request`/`Policy` 必须得到相同结果，供热路径与 Inspector 共用。

**非目标**：不拥有健康探测、熔断、inflight 计数、绑定写入或请求执行；不做 HTTP/DB/配置读取；不执行 `StickyTTL` 过期；不按 `Features` 过滤（`Target` 没有对应字段）；不做协议转换本身。

## 2. 当前实现与约束
- 文件：[`route.go`](../../pkg/route/route.go)、[`score.go`](../../pkg/route/score.go)、[`failure.go`](../../pkg/route/failure.go)；测试 `route_test.go`、`failure_test.go`。只 import 标准库和 `pkg/xir`。
- 模型匹配：`Target.Model` 或 `Target.WireModel` 与 `Request.Model` 忽略大小写相等；`Healthy=false` 的零值 Target 不可选；`CooldownUntil` 严格晚于 `Now` 时冷却。
- mode 顺序默认 native → local_responses → convert。local 只用于 Responses 请求、`AllowLocal` 且目标服务 Chat Completions；convert 只用于非流式、`AllowConversion` 且目标服务另一协议；转换目标偏好 chat → responses → messages → gemini（排除请求协议）。
- `compat`：同 mode 内按 score 比较，`Priority` 只作同分 tiebreak；`strict`：先按 mode，再只保留最高 `Priority` tier。
- 亲和：有 session 且 `Snapshot.Bindings` 命中时标记软亲和；绑定目标在选定 mode/tier 内则优先，否则按 score spill（`Affinity.TargetID` 仍记录绑定目标）。`Request.State.HasServerState()`（`previous_response_id`/`conversation`）且有绑定才是硬亲和，绑定目标不可用直接 `session_upstream_unavailable`，不会回退 local 或换 target。
- 已知限制：`Policy.StickyTTL` 仅保留字段，**不执行**（`Bindings` 值没有时间戳）；`Features` 不参与过滤；不写健康/绑定/inflight。`internal/router` 仍拥有线上选路，本包当前没有生产调用方。

## 3. 对外契约（`pkg`）
### 3.1 包结构
`pkg/route/{route,score,failure}.go`；无 `internal` 子包。

### 3.2 Go API
```go
type Mode string        // native | local_responses | convert
type PriorityMode string // compat | strict

type Target struct {
    ID, ChannelID, CredentialID, Model, WireModel string
    Protocols []xir.Protocol
    Priority int; Weight float64; Healthy bool
    CooldownUntil time.Time; Inflight int; LatencyMs float64
}
type Snapshot struct { Targets []Target; Bindings map[string]string; Now func() time.Time }
type Request struct { Model string; Protocol xir.Protocol; Operation xir.Operation; Stream bool; Features Features; State xir.StateRefs; Session string }
type Features struct { Tools, Images, StructuredOutput bool; ContextTokens int }
type Policy struct { ModeOrder []Mode; PriorityMode PriorityMode; AllowConversion, AllowLocal bool; StickyTTL time.Duration }

type PlanResult struct {
    Target Target; Mode Mode; Wire xir.Protocol; WireModel string
    Tier int; Affinity Affinity; Candidates []Candidate; Reason string
}
type Candidate struct { Target Target; Mode Mode; Selectable bool; FilterReason string; Score float64; Tier int }
type Affinity struct { Status string; Failed bool; KeyFingerprint, TargetID string }

func BindingKey(session, model string) string
func Plan(ctx context.Context, req Request, snap Snapshot, policy Policy) (*PlanResult, error)
func Explain(ctx context.Context, req Request, snapshot Snapshot, policy Policy) (*PlanResult, error)
func ReasonOf(err error) string

type PlanError struct { Reason, Detail string }
func (e *PlanError) Error() string
func (e *PlanError) Is(target error) bool
// ErrModelUnknown, ErrNoCandidate, ErrAllUnavailable, ErrSessionUpstreamUnavailable, ErrConversionUnsupported

type FailureClass string // quota | rate | auth | model_missing | shape | context | upstream | refusal | conversion | unknown
type Failure struct { Class FailureClass; Retryable, TargetScoped, ModelScoped, CredentialScoped bool; Cooldown time.Duration; Reason string; StatusCode int }
func ClassifyFailure(status int, header http.Header, body []byte) Failure
```
`BindingKey` 是 `sha256(trim(session) + "\x00" + lower(trim(model)))` 的十六进制；`PlanResult` 是结果类型，因 Go 不允许函数与类型同名，入口函数仍叫 `Plan`，结果类型叫 `PlanResult`。

### 3.3 错误与诊断
`Plan` 失败返回非 nil 的 `*PlanResult`（含 Candidates、Affinity、Reason）和 `*PlanError`；`errors.Is(err, route.ErrNoCandidate)` 等 sentinel 可用，`ReasonOf` 取稳定 reason。context 取消原样返回，plan 为 nil。

| Reason / FilterReason | 触发条件 | 调用方处理 |
| --- | --- | --- |
| `model_unknown` | 没有 Target 匹配 model | 返回 404/模型未启用 |
| `all_unavailable` | 匹配目标全部 unhealthy/cooldown | 冷却或报错 |
| `conversion_unsupported` | 只能转换但被禁用或请求流式 | 返回不支持 |
| `no_candidate` | 有目标但协议/mode 不可用 | 返回无可用上游 |
| `session_upstream_unavailable` | 硬亲和目标缺失/不可用 | 不静默换上游 |
| `unhealthy` / `cooldown` / `no_conversion_protocol` / `conversion_disabled` / `streaming_conversion_unsupported` / `hard_affinity_bound_elsewhere` / `mode_disabled` | 单个候选被过滤 | 解释“为什么没选它” |

### 3.4 并发、生命周期、资源
纯函数，无共享状态、无 goroutine/文件/连接；`Snapshot`、`Policy`、`Request` 按值传入且 `Plan` 不修改。`Snapshot.Now` 由调用方保证并发安全；每次 `Plan` 只读一次时钟。`Target.Protocols` 切片与 `Candidate.Target` 共享底层数组，调用方不得在并发选路时修改快照。

## 4. `internal` 集成
```text
internal/router   ──(仍拥有)──▶ 健康、熔断、sticky、live selection
internal/routeplan──▶ 独立 DTO/vocabulary（未接 pkg/route）
pkg/route         ──(standalone)──▶ 无 internal 调用方
```

本包不读配置、没有 feature flag；`Policy` 由调用方从 `router.selection` 映射。失败时没有降级逻辑，返回 typed reason 由调用方决定 HTTP 状态。接入 M10 前，Inspector 与热路径仍使用 `internal/router`/`internal/routeplan`。

## 5. 数据模型与格式
### 5.1 类型
`Target` 是调用方填充的只读运行态：健康、冷却、inflight、延迟、优先级、权重、协议。`Snapshot.Bindings` 是 `BindingKey → target ID` 的映射，值不带时间戳，因此过期必须由调用方在构建快照前清理。`Affinity.KeyFingerprint` 永远是哈希，`TargetID` 可暴露给 Explain。

### 5.2 序列化
无 JSON tag、无序列化；不进入 cassette/DB/HTTP API。路由决策若需落库，由调用方映射到 `logs`/`route_attempts`，本包不写。

### 5.3 配置
无配置；`Policy`/`Snapshot` 全部由调用方构造，本包不读配置。

### 5.4 数据放置
| 数据 | 文件 / DB / 内存 | 理由 | 丢失后果 |
| --- | --- | --- | --- |
| Targets/health/cooldown | 内存（调用方快照） | 高频运行态 | 重启重建 |
| Bindings | 内存（调用方维护） | 软/硬亲和缓存 | 重新绑定 |
| Plan 结果 | 内存 | 单次决策诊断 | 可重算 |
| cassette / DB | 不写 | 纯决策 | 不适用 |

## 6. 算法与不变量
### 6.1 算法
**Plan 步骤**：校验 context → 取一次 `Now` → 归一化 mode 顺序/priority mode → 计算绑定 → 过滤 model 不符的 Target → 逐个计算 best mode、score、过滤原因 → 有 server state 且有绑定时走硬亲和 → 否则按 mode 顺序选第一个有可选项的 mode → strict 保留最高 tier → 软亲和命中则选绑定目标，否则选最低 score（score→priority→ID→ChannelID→CredentialID→WireModel→Wire 稳定 tiebreak）。

**score**：`clamp(LatencyMs) + 25*max(Inflight,0) + 100/weight`，weight 非正/非有限按 1；unhealthy/cooldown 加 `1e9` 惩罚仅用于解释，实际先被过滤。`Priority` 不进 score。

### 6.2 不变量
不变量：任何可选 native 目标都先于 convert；strict 的 tier 只在选定 mode 内生效；硬亲和绝不 spill；结果确定（无随机、无 map 遍历、单次时钟）；`Plan` 不修改 Snapshot。

### 6.3 边界条件
边界：空 Targets、无 session、绑定到不存在的 target、`ModeOrder` 含未知/重复值（归一化为默认或去重）、流式 convert、目标无第二协议、取消的 context、`Now=nil`（用 `time.Now`）。

## 7. 失败语义
| 场景 | 行为 | 影响上游健康 | 客户端看到 |
| --- | --- | --- | --- |
| 模型无目标 | `model_unknown` + 诊断 plan | 不适用 | 404/模型未启用 |
| 全部不可用 | `all_unavailable` | 不适用 | 503/无可用上游 |
| 只能转换但禁用/流式 | `conversion_unsupported` | 不适用 | 400/不支持 |
| 硬亲和目标不可用 | `session_upstream_unavailable`，`Affinity.Failed=true` | 不适用 | 明确错误，不换上游 |
| context 取消 | 原样返回，plan=nil | 不适用 | 取消 |
| 内部 bug | panic 传播 | 不适用 | 调用方错误 |

路由失败不修改任何健康状态；是否熔断/重试由调用方决定。

## 8. 性能
每次 `Plan` 对匹配 Target 做一次线性评估，候选分配与目标数线性，无排序、无网络/DB。没有 benchmark 或 p50/p99；热路径接入前需按候选数量补测。`score` 是常数时间。

## 9. 安全
不读 body、不写日志、不访问网络。`Affinity.KeyFingerprint` 是 SHA-256 指纹，原始 session 不会出现在结果中；调用方仍须避免把 `Request.Session` 写日志。`Target.CredentialID` 是标识符而非秘密；不要把 key/token 放进 ID、Model 或 WireModel。Explain 可暴露目标 ID 和过滤原因，需按管理端权限保护。

## 10. 可观测性
无指标、事件或日志；可观测性由 `PlanResult.Reason`、`Candidate.FilterReason`、`Affinity.Status/Failed` 和 `PlanError` 提供。Inspector 可原样渲染候选列表；本包不写 DB。

## 11. 测试与验收
### 11.1 单元测试
`GOCACHE=/tmp/trajecta-gocache go test ./pkg/route` 通过（Go 1.26.9，`ok` 约 0.007s）。覆盖 mode 顺序、compat vs strict、软亲和 spill、硬亲和成功/缺失/不可用/跨 model、failover/cooldown、convert 最后、Explain 与 Plan 一致且不改 Snapshot、BindingKey 指纹、WireModel、取消 context、候选分数稳定、自定义 mode 顺序、PlanError 格式、failure 分类（含 Retry-After 日期/中文 body）。无 fuzz/benchmark。
### 11.2 集成测试
无；未接入 `internal/router`。
### 11.3 差分测试
未与线上选择结果做差分。
### 11.4 验收标准
- [x] 公开 API 有 doc，离线测试通过。
- [x] 失败语义（typed reason、硬亲和 fail-closed）有测试。
- [x] 不写 cassette/DB，纯内存。
- [x] 文档同步（本文）。
## 12. 迁移、兼容、回滚
新增包，无 DB/格式变化、无 feature flag。`internal/router` 继续作为运行时事实源；回滚只需删除未来调用方 import。旧路由逻辑删除条件：M10 用 `pkg/route.Plan` 替换线上选择并通过差分/性能门禁。

## 13. 开放问题
| 问题 | 影响 | 建议 | 需要谁决定 |
| --- | --- | --- | --- |
| 谁执行 `StickyTTL`？ | 绑定过期 | 调用方在构建 Snapshot 前清理；或扩展 `Bindings` 带时间戳 | M10 |
| 硬亲和不可用时是否允许 local 重建？ | 设计 §9.3 与实现差异 | 当前实现直接报错；若要 local 回退需扩展 Plan | 架构 |
| `Features` 是否参与过滤？ | 能力路由 | 给 Target 增加能力字段后再实现 | M10/M11 |
| 失败分类后的重试/冷却谁负责？ | 选路闭环 | `internal/router` 消费 `Failure` | M10 |

## 14. 实施拆分
1. 已完成：类型、Plan/Explain、score、失败分类、单元测试。
2. 下一步：`internal/router` 构建 Snapshot/Policy 并对比现有选择，补差分测试；M10 用同一个 Plan 驱动热路径与 Inspector。
3. 清理：删除 `internal/routeplan` 的重复 DTO/决策，保留稳定的审计词表。

验证 `go test ./pkg/route`，接入后跑 `go test ./internal/router ./internal/monitor` 与 `task check:quick`。

## 15. 参考
- [路由入口](../../pkg/route/route.go)、[评分](../../pkg/route/score.go)、[失败分类](../../pkg/route/failure.go)
- [总体架构](../01-overall-architecture.md) §9
- [路由与凭据](../../docs/ROUTING_AND_CREDENTIALS.md)
- [数据放置](../02-data-placement.md)
