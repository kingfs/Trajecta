# M05 检测与敏感信息

## 0. 头部
| 字段 | 值 |
| --- | --- |
| 模块 ID | M05 |
| 模块名 | 检测与敏感信息 |
| 归属 | `pkg/detect`（`pkg/secretmask` 是未来规则来源，尚未适配） |
| 状态 | Landed (P1) |
| 依赖模块 | 无（仅标准库） |
| 被依赖模块 | 当前无；计划由 M03 Hook、M10 Gateway 使用 |
| 目标版本 | 2.0 |
| 最后更新 | 2026-11-09 |

## 1. 目标与非目标
**目标**：为 hot/audit lane 提供统一只读检测框架——`Scanner` 扫描调用方字节，`Engine` 统一顺序、范围、置信度、去重和排序；具体检测能力作为独立包实现 `Scanner`；只依赖标准库，可被外部 Go 程序 import，且不修改字节、不保留 `Raw`。

**非目标**：不实现具体规则；不做 mask/替换/字节改写；不做 wire JSON 解析或 span 定位（`Input.Spans` 由调用方给）；不做 Hook 计划、lane 调度、超时预算或 panic 恢复（M03）；不读写 DB/cassette/配置，不访问网络。

## 2. 当前实现与约束
- 代码：[`pkg/detect/detect.go`](../../pkg/detect/detect.go)、[`pkg/detect/match.go`](../../pkg/detect/match.go)；测试 `detect_test.go`、`match_test.go`。随 `feat(pkg): add XIR and reusable capability packages` 落地。
- `Engine` 零值可用；`New` 静默跳过 nil、空名和重名 scanner，`Register` 才返回可分类错误。
- `Scan` 规范化：非法/越界 match 丢弃（不 clamp）；match 必须完整落在一个声明 span 内，否则丢弃；声明 span 本身 clamp 进 `Raw`；置信度 clamp 到 `[0,1]`；按 `(start, end, rule)` 稳定排序去重。
- 已知限制：`pkg/secretmask` 不是 `detect.Scanner`，`internal/proxy` 仍走自己的检测/遮蔽路径；本包没有 `internal/*` 调用方，无 benchmark/fuzz。

## 3. 对外契约（`pkg`）
### 3.1 包结构
```text
pkg/detect/
  detect.go   // Input、Scanner、Engine、Scan、规范化
  match.go    // Span、Match、ContainsSpan、MergeMatches
```

### 3.2 Go API
```go
package detect

type Input struct { Raw []byte; Spans []Span; Protocol string; Meta map[string]string }
type Scanner interface { Name() string; Scan(ctx context.Context, in Input) ([]Match, error) }
type Engine struct{ /* unexported */ }

func New(scanners ...Scanner) *Engine
func (e *Engine) Register(s Scanner) error
func (e *Engine) Scanners() []Scanner
func (e *Engine) Scan(ctx context.Context, in Input) ([]Match, error)

type Span struct { Start, End int; Path, Kind string }
func (s Span) Valid() bool
func (s Span) Len() int

type Match struct {
    Span
    Rule       string
    Confidence float64
    Value      string
    Metadata   map[string]string
}

func ContainsSpan(spans []Span, sp Span) bool
func MergeMatches(a, b []Match) []Match
```

`Span` 是半开区间；`Path`/`Kind` 只随 match 透传，`Engine` 不解释。`Match.Value` 不由 `Engine` 填充；`Metadata` 原样透传。

### 3.3 错误与诊断
| 错误/诊断 | 触发条件 | 调用方应如何处理 |
| --- | --- | --- |
| `ErrInvalidScanner` | `Register(nil)` 或空 `Name()` | 配置错误；修正后重建 |
| `ErrDuplicateScanner` | 同名 scanner 已注册 | 配置错误；检查注册表 |
| `detect: scanner %q: %w` | `Scanner.Scan` 返回错误 | 中止本次扫描；已收集 match 丢弃 |
| 被丢弃的 match | span 非法、越界、在 scope 外 | 不是错误；修正 scanner 或 span 来源 |

错误不用字符串匹配；`Register` 失败不改变 Engine；`Scan` 错误包 scanner 名，不包敏感值。

### 3.4 并发、生命周期、资源
`Scan`、`Scanners`、`Register` 可并发，内部 `RWMutex` 保护。`Scanner.Scan` 可能被并发调用，实现必须只读或自行同步，不得保留/修改 `in.Raw`。不持有 goroutine、文件或连接，无 `Close`；`nil ctx` 按 `context.Background()`。`Scan` 不预检 `ctx.Err()`，取消语义由 scanner 决定。

## 4. `internal` 集成
当前无集成点。计划路径：

```text
internal/gateway ──uses──▶ pkg/detect（经 Hook 计划）
pkg/secretmask    ──adapt──▶ detect.Scanner
```

显式构造 `detect.New(...)`/`Register`，不用全局 registry。规则应声明 lane，热 lane 只挂线性、有预算的 scanner。本包无配置、无 feature flag；scanner 错误使整次扫描失败，上层决定 fail-open（记录并继续）或 fail-closed（阻断）。

## 5. 数据模型与格式
### 5.1 类型
`Input.Raw` 是唯一事实源，所有 span 指向它；`Input.Spans` 为空表示整段 buffer。`Match.Span` 必须 `Valid()` 且 `End <= len(Raw)`；`Confidence` 是 `[0,1]` 分数；去重键是 `(start, end, rule)`，不同规则可对同一段字节各报一条。

### 5.2 序列化
不序列化：没有 JSON tag，不进入 cassette、DB 或 HTTP API，需要持久化时由调用方转换。

### 5.3 配置
无配置项，范围/协议/元数据全部通过 `Input` 显式传入。

### 5.4 数据放置
| 数据 | 文件 / DB / 内存 | 理由 | 丢失后果 |
| --- | --- | --- | --- |
| `Input.Raw` | 调用方内存 | 只读扫描，不复制 | 由调用方重新提供 |
| `Match` 结果 | 内存（本次调用） | 一次性诊断 | 可重新扫描 |
| scanner 注册表 | 内存 | 进程内能力装配 | 启动时重建 |
| cassette / DB | 不写 | 检测不是证据 | 不适用 |

不写 cassette、不写 DB；无保留期或清理；结果可重算。

## 6. 算法与不变量
### 6.1 算法
**算法**：取 scanner 快照（注册顺序）逐个 `Scan`，任一错误立即返回；对声明 scope clamp（空 scope 表示全 buffer，全部 clamp 掉则返回空）；逐条校验 span，丢弃非法/越界/越 scope 项并 clamp 置信度；稳定排序 `(start, end, rule)` 后相邻去重。复杂度 `O(M log M + M·K)`：`M` 为 match 数，`K` 为声明 span 数（`ContainsSpan` 线性查找），scope 很多时退化为 `M·K`。

### 6.2 不变量
不变量：返回的 span 非空、在 `Raw` 内、且在声明 scope 内；结果有序且三元组不重复；`Confidence` 始终在 `[0,1]`；`Scan` 不修改 `Raw`/`Spans`/scanner 状态；错误返回时 match 为 nil，部分结果不泄漏。

### 6.3 边界条件
边界：`Raw` 为空或 scope 全被 clamp 掉时返回空；重叠/乱序 scope 不合并，逐条判断；重叠但起止不同的 match 都保留；并发 `Register` 与 `Scan` 安全（看到调用时刻快照）；scanner panic 不恢复。

## 7. 失败语义
| 场景 | 行为 | 影响上游健康 | 客户端看到 |
| --- | --- | --- | --- |
| scanner 错误 | 整次失败，丢弃已收集结果 | 不适用（纯计算） | 上层决定记录并继续或阻断 |
| match 越界/越范围 | 静默丢弃该条 | 不适用 | 无影响 |
| 无 scanner | 返回 `nil, nil` | 不适用 | 无影响 |
| 非法注册 | sentinel error | 不适用 | 上层处理配置错误 |
| `nil` context | 按 Background | 不适用 | 无影响 |

本包不选 fail-open/fail-closed，只把错误交给上层。

## 8. 性能
纯内存操作，无网络/DB/文件 I/O。每次 `Scan` 分配一个结果切片，排序是主要开销。没有热路径预算数字和 benchmark；接入 M10 前需按真实 body 规模补测。

## 9. 安全
不写日志/文件、不发网络请求，结果只返回调用方。`Match.Value`/`Metadata` 可能含敏感值，本包不脱敏，调用方按数据分类决定是否持久化或展示。不执行外部输入（路径、命令、URL），不解析 JSON。`Raw` 不复制，调用方保证 `Scan` 期间有效。

## 10. 可观测性
无指标、事件或日志；错误通过返回值携带 scanner 名。排障入口在调用方审计/日志；本包不提供规则命中统计。

## 11. 测试与验收
### 11.1 单元测试
`GOCACHE=/tmp/trajecta-gocache go test ./pkg/detect` 通过（Go 1.26.9，`ok` 约 0.005s）。覆盖零值 Engine、注册顺序、非法/重复注册、越界与越 scope 丢弃、scope clamp、排序去重、置信度 clamp、scanner 错误、nil context、并发 Scan/Register、`MergeMatches` 不别名输入。无 fuzz/benchmark。

### 11.2 集成测试
无；尚未与 `pkg/secretmask`、`pkg/xir` 或 `internal/*` 联调，全部离线。

### 11.3 差分测试
不适用（新框架无旧实现），接入 `pkg/secretmask` 后应对比现有 DFA 的 match 集合。

### 11.4 验收标准
- [x] 公开 API 有 Go doc，离线测试通过；尚无外部 example test。
- [ ] benchmark 达到预算（未接入热路径）。
- [x] 失败语义有测试（scanner error、非法 match）。
- [x] 文件/DB/内存边界明确，不写 cassette/DB。
- [ ] 与 `pkg/secretmask` 的适配完成（未实现）。
- [x] 文档同步（本文）。

## 12. 迁移、兼容、回滚
新增包，无数据库迁移、无 cassette 格式变化、无 feature flag。回滚只需删除调用方 import；包本身可保留。旧检测路径（`internal/proxy` + `pkg/secretmask`）不受影响，继续作为运行时事实源。

## 13. 开放问题
| 问题 | 影响 | 建议 | 需要谁决定 |
| --- | --- | --- | --- |
| 谁提供 `secretmask` 适配器？ | 能否进入热路径 | 在 `pkg/secretmask` 加薄 `Scanner`，不搬 DFA | M03/M10 负责人 |
| 规则 ID 是否需要命名空间？ | 去重与审计 | 约定 `<package>/<rule>` | 架构 |
| 是否支持 scanner 超时预算？ | 热路径稳定性 | Hook 框架按 lane 执行，本包保持无状态 | M03 |
| `Value` 是否必须由 scanner 填？ | 审计证据完整性 | 保持可选，需要时从 `Raw` 切片 | 调用方 |

## 14. 实施拆分
1. 已完成：类型、Engine、规范化、单元测试。
2. 下一步：`pkg/secretmask` 适配 `detect.Scanner`，并做行为差分测试。
3. 下一步：M03 声明 lane/point/预算，M10 在 gateway 显式构造 Engine。
4. 清理：确认 `internal/proxy` 无重复检测后删除旧适配。

每步验证 `go test ./pkg/detect ./pkg/secretmask`，接入后跑对应 `internal/*` 与 `task check:quick`。

## 15. 参考
- [检测框架](../../pkg/detect/detect.go)
- [Match 与 Span](../../pkg/detect/match.go)
- [总体架构](../01-overall-architecture.md) §6.2、§7.5
- [数据放置](../02-data-placement.md)
- [敏感信息遮蔽](../../docs/SECRET_MASKING.md)
