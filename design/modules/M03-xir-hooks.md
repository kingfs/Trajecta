# M03 Hook 框架

## 0. 头部

| 字段 | 值 |
| --- | --- |
| 模块 ID | M03 |
| 模块名 | Hook 框架 |
| 归属 | `pkg/xir/hook` |
| 状态 | Landed (P2) |
| 依赖模块 | M01（`pkg/xir`、`pkg/xir/wire`）、M02（Event 的语义视图） |
| 被依赖模块 | M04（流式点契约）；未来 `internal/gateway`、`internal/audit` |
| 目标版本 | Trajecta 2.0 |
| 最后更新 | 2026-11-09 |

## 1. 目标与非目标

### 1.1 目标

- 定义 `Hook` 契约：ID、Point、Lane、Needs、Priority、FailPolicy、`Handle`。
- 把配置编译成不可变 `Plan`，按 `(lane, point)` 预建有序索引；定义可合并的 `Result`。
- 提供线程安全的 `Registry`，让 hook 以 Go 包 + factory 的方式注册。

### 1.2 非目标

- 不提供内置 hook（secretmask/usage/tool-guard 等均未在此包实现）。
- 不强制热路径时间预算/超时；不实现语义 decode 或流式处理。
- 不接入转发/审计路径，不读写 DB/cassette，不持久化 findings；不支持动态插件/热更新。

## 2. 当前实现与约束

- [`hook.go`](../../pkg/xir/hook/hook.go)：`Hook`/`Event`/`Result`/`Finding`/`Block`/`Config`/`Factory`。
- [`points.go`](../../pkg/xir/hook/points.go)：`Lane`/`Point`/`Needs`/`FailPolicy`/`Severity` 常量与校验。
- [`plan.go`](../../pkg/xir/hook/plan.go)：`Compile`、不可变 `Plan`、`Hooks`、`Run`。
- [`registry.go`](../../pkg/xir/hook/registry.go)：并发安全 registry（`Register` 对重复/空 ID/nil 工厂 panic）。
- 测试：`compile_test.go`、`plan_test.go`、`points_test.go`、`registry_test.go`、`example_test.go`；无 fuzz/benchmark。
- 当前没有任何 `internal` 包 import 本包；`pkg/xir/convert` 也不 import hook。
- `Event` 携带 `Operation`；`Plan` 通过 map 查找 `(lane, point)`。

## 3. 对外契约（`pkg`）

### 3.1 包结构

```text
pkg/xir/hook/
  hook.go      Hook/Event/Result/Finding/Block/Config/Factory
  points.go    Lane/Point/Needs/FailPolicy/Severity 及校验
  plan.go      Compile/Plan/Hooks/Run
  registry.go  Registry
```

### 3.2 Go API

```go
type Lane uint8; const LaneHot Lane = 1 << iota; const LaneAudit; func (l Lane) Has(other Lane) bool; func (l Lane) Valid() bool
type Point string
const ( PointRequestEnvelope, PointRequestSystem, PointRequestMessage, PointRequestToolDecl, PointRequestToolCall,
 PointRequestToolResult, PointRequestMetadata, PointResponseOutput, PointResponseReasoning, PointResponseToolCall,
 PointResponseToolResult, PointResponseUsage, PointResponseError, PointStreamEvent, PointStreamTextDelta,
 PointStreamReasonDelta, PointStreamToolArgs, PointExchangeFinalize Point = ... )
func AllPoints() []Point; func (p Point) Valid() bool
type Needs uint8; const NeedsWire Needs = 1 << iota; const NeedsSemantic; const NeedsStream; func (n Needs) Has(other Needs) bool; func (n Needs) Valid() bool
type FailPolicy uint8; const FailOpen FailPolicy = iota; const FailClosed
type Severity string; const SeverityInfo, SeverityLow, SeverityMedium, SeverityHigh, SeverityCritical Severity = ...
type Hook interface { ID() string; Points() []Point; Lanes() Lane; Needs() Needs; Priority() int; FailPolicy() FailPolicy; Handle(ctx context.Context, ev *Event) (Result, error) }
type Event struct { Lane Lane; Point Point; Protocol xir.Protocol; Operation xir.Operation; Doc *xir.Document; Request *xir.Request; Response *xir.Response
    Part *xir.Part; Node *wire.Node; Path string; Span wire.Span; Text string; Meta map[string]string }
type Result struct { Findings []Finding; Edits []wire.Edit; Block *Block; Usage *usage.Usage; Tags map[string]string }
type Finding struct { Hook, Rule string; Severity Severity; Point Point; Path string; Span wire.Span; Message, Evidence string; Meta map[string]string }
type Block struct { Status int; Code, Message string }; const RuleHookError = "hook.error"
type Config struct { ID string; Enabled bool; Lanes Lane; Points []Point; Priority int; Settings json.RawMessage }; type Factory func(Config) (Hook, error)

type Registry struct{}; func (r *Registry) Register(id string, f Factory) // 空 ID/nil/重复 -> panic
func (r *Registry) Lookup(id string) (Factory, bool); func (r *Registry) IDs() []string // 排序副本
type Plan struct{}; func Compile(reg *Registry, configs []Config) (*Plan, error); func (p *Plan) Empty() bool
func (p *Plan) Hooks(lane Lane, point Point) []Hook // 副本; func (p *Plan) Run(ctx context.Context, ev *Event) (Result, error)
```

### 3.3 错误与诊断

`Compile` 返回带配置项名字的 `fmt.Errorf`：空/重复 ID、未知 hook、非法 lane/point、工厂错误/nil/ID 不匹配、无 lane/point。`Run` 对 nil context/event 返回 error；FailClosed 的 hook 错误用 `fmt.Errorf("hook %q: %w", id, err)` 包装（可用 `errors.Is` 匹配原错误），FailOpen 则丢弃该 hook 的局部结果并追加 `RuleHookError`/`SeverityMedium` finding。hook panic 被转成 error，再按同一策略处理。本包没有 sentinel error，也没有 typed diagnostic。

### 3.4 并发、生命周期、资源

`Plan` 编译后不可变，可被任意 goroutine 并发 `Run`/`Hooks`；`Registry` 用 `RWMutex`，`Register` 仅在启动期调用。hook 实现必须无状态、并发安全，因为同一值被所有请求共享。`Run` 不启动 goroutine、不持有资源；超时/取消由传入 `context` 传播，取消会在 hook 之间检查并立即返回已合并的部分结果。

## 4. `internal` 集成

当前无集成。设计接入点是 `internal/gateway`（M10 热 lane）和 `internal/audit`（M12 审计 lane）；二者需显式持有 `*hook.Plan` 并构造 `Event`，不要用全局 registry。没有配置来源、feature flag 或降级路径；是否 fail-open/fail-closed 由每个 hook 自己声明。

## 5. 数据模型与格式

### 5.1 类型

- `Lane` 是位标志；`Valid` 要求非零且只含 Hot/Audit 位。`Needs` 的零值合法（只看事件元数据）。
- `Point` 是稳定字符串；`Config.Points` 为空回退 `Hook.Points()`，`Lanes==0` 回退 `Hook.Lanes()`，`Priority==0` 回退 `Hook.Priority()`。
- `Event` 的 wire/semantic 指针仅在调用方 decode 后非 nil；hook 只读。`Result` 零值是空结果，`Edits` 由调用方 `wire.Apply`。

### 5.2 序列化

本包不定义持久化格式；`Config.Settings` 是调用方给的原始 JSON，`Compile` 复制后交给 factory。`Finding`/`Result` 没有 JSON tag，不进入 cassette/DB/API；`Point` 字符串可安全写入配置和日志。

### 5.3 配置

不适用：没有 YAML/env 配置加载。调用方自行从（未来的）`hook_configs` 构造 `[]Config`；当前无该表读写实现。

### 5.4 数据放置

| 数据 | 文件 / DB / 内存 | 理由 | 丢失后果 |
| --- | --- | --- | --- |
| `Registry`/`Plan`、`Config`/`Settings` | 内存 | 启动期编译/配置绑定 | 重新编译 |
| `Finding`/`Edits`/`Tags`/`Usage` | 内存 | 单次事件结果 | 不持久化，调用方决定 |

本模块不写 cassette/DB/文件；重启后无状态需要恢复（配置源未来另行实现）。

## 6. 算法与不变量

### 6.1 算法

- `Compile`：过滤 disabled → 校验 ID/重复/lane/point → `Registry.Lookup` → 复制 settings/points 并调用 factory → 校验 hook ID/lane/point → 按 `(priority asc, ID asc)` 稳定排序 → 为 Hot/Audit 各建 `point → []Hook` 索引。时间 O(n log n + 索引项数)。
- `Hooks`：单 lane 直接 map 查找 O(1) 后复制；组合 lane 按 entries 线性过滤。
- `Run`：查表后按顺序调用；每个结果 merge；遇 Block 立即返回。合并会复制 finding Meta、edit Bytes、Block 值并 `usage.Merge`；后续 hook 的结果覆盖同名 tag。

### 6.2 不变量

- Plan 不可变、并发安全；`Compile` 不修改输入 config，settings/points 均复制。
- 排序固定：Priority 越小越先执行，同 Priority 按 ID 字典序；不是“高优先级先跑”。
- 同一 config ID 只能出现一次；disabled config 不做 registry 查找。
- Point 去重保序；无 point/无 lane 是编译错误。
- FailOpen 丢弃失败 hook 的全部局部结果（findings/edits/tags/usage），只保留诊断 finding 后继续；FailClosed 立即停止并包装错误，之前的结果保留。
- Block 非 nil 即停止后续 hook，Block 被复制；hook panic 不会穿透 `Run`，context 取消优先于 fail policy。

### 6.3 边界条件

- nil `*Plan` 视为 empty；`Run` 返回零结果、nil error（但 nil ctx/ev 仍报错）。
- `Event.Lane==0` 或未知 `Point` 不会报错，只是没有 hook 匹配。
- `Plan.Run` 不校验 `Event.Lane`/`Point` 是否合法，也不强制 Event 的 Protocol 与 Point 一致。
- 同一 hook 同时声明 Hot/Audit 时出现在两个索引；Run 只查事件 lane，不会重复执行。

## 7. 失败语义

| 场景 | 行为 | 是否影响上游健康 | 客户端看到什么 |
| --- | --- | --- | --- |
| 配置非法 | `Compile` 返回 error | 否 | 启动/配置错误 |
| FailOpen hook 返回 error/panic | 丢弃局部结果，追加诊断 finding，继续 | 否 | 无直接影响 |
| FailClosed hook 返回 error/panic | 停止，返回包装 error | 否 | 由调用方映射 4xx/5xx |
| 返回 Block/context 取消 | 停止，结果带 Block 或返回 `ctx.Err()` | 否 | 按 Block 或取消/超时响应 |

本包不接触上游；fail policy 由 hook 自己声明。

## 8. 性能

当前不在热路径；设计目标是编译期建索引，运行期一次 `(lane, point)` 查找 + K 个 hook 调用。没有时间预算/超时/分配基准；`Run` 会复制结果容器和 edit 字节，K 大时线性增长。无 benchmark；命令：`go test ./pkg/xir/hook`。

## 9. 安全

`Event` 可能携带原始 body、语义文本和敏感值；本包不写日志、不落盘。`Finding.Evidence`/`Meta` 由 hook 填充，本包不脱敏；调用方必须避免把敏感值写入 DB/日志。不执行外部输入；`Block` 只表达策略决定，不直接写 HTTP 响应。

## 10. 可观测性

无指标、无日志；失败只通过返回 error 或 `RuleHookError` finding 暴露。排障入口是 finding 的 Hook/Rule/Point/Path/Span 和调用方记录；不新增 DB/cassette event。

## 11. 测试与验收

### 11.1 单元测试

`points_test.go` 覆盖 Lane/Needs bit、Point 集合/别名、Severity；`compile_test.go` 覆盖 disabled、默认/显式覆盖、排序、输入不别名、全部校验错误；`plan_test.go` 覆盖 lane/point 过滤、副本、顺序/merge、FailOpen/FailClosed、panic、Block、nil 输入、context 取消、空/nil plan；`registry_test.go` 覆盖查找/排序、nil 安全、重复/非法注册 panic；`example_test.go` 有可运行示例。无 fuzz/benchmark，race 由全包 `-race` 覆盖。

### 11.2 集成测试

无跨包集成；测试全部离线、无网络/DB/cassette。

### 11.3 差分测试

不适用：本包没有旧实现可直接对比；未来接入 M10 时再用同一 hook 对旧路径做差分。

### 11.4 验收标准

- [x] Hook/Plan/Registry 公开 API 有文档和离线测试；[x] 排序、失败策略、Block、panic、取消有测试。
- [x] Plan 不可变、可并发；[x] 不写 cassette/DB。
- [ ] 内置 hook、时间预算、热路径接入；[ ] benchmark。

## 12. 迁移、兼容、回滚

- 新增包，无 DB 迁移/文件格式/API 兼容问题；不影响现有转发路径。
- 无 feature flag；回滚删除使用方对 `Plan`/`Registry` 的构造即可。
- 未来接入 M10/M12 前，本包保持独立，不删除任何旧检测路径。

## 13. 开放问题

| 问题 | 影响 | 建议 | 需要谁决定 |
| --- | --- | --- | --- |

| 无超时/预算执行 | 热 lane 可能被慢 hook 拖住 | `Run` 增加 deadline 或每 hook 超时 | M03/M10 owner |
| Config 不能覆盖 FailPolicy | 同一 hook 无法按配置切换策略 | 增加 `Config.FailPolicy`（需区分零值） | M03 owner |
| `Priority==0` 回退 Hook.Priority | 无法用配置显式设 0 | 用指针或 `*int` | M03 owner |
| 无内置 hook | M03 本身不产生能力 | 在 M10/M12 分别实现并注册 | M10/M12 owner |

## 14. 实施拆分

1. 类型/常量/校验；2. Registry；3. Compile/Plan；4. Run/fail policy/merge；5. 测试与示例（均已完成）。6. M10/M12 内置 hook 与接入（未开始）。
- 验证：`GOCACHE=/tmp/gocache-trajecta go test ./pkg/xir/hook`。

## 15. 参考

- [M01](./M01-xir-foundation.md)、[M02](./M02-xir-codecs.md)；[总体架构](../01-overall-architecture.md) 第 7.5 节。
- 代码：[`pkg/xir/hook`](../../pkg/xir/hook)。
