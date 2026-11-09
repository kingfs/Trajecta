# Trajecta 2.0 总体架构设计

> 状态：Draft v0.1  
> 更新时间：2026-11-09  
> 模块模板：[TEMPLATE.md](./TEMPLATE.md)  
> 模块索引：[modules/README.md](./modules/README.md)

## 1. 摘要

Trajecta 从"LLM API record/replay 调试代理"升级为"本地优先的 LLM 接入、路由与审计平面"。重构围绕三件事展开：

1. **一个 Exchange IR（XIR）**：同时提供无损 wire 视图、类型化 semantic 视图和 audit 投影，支撑解析、转换、检测、改写、审计、ATIF 导出。
2. **一个精简的 Gateway Core**：固定阶段为 identity → plan → transform → egress → ingress → finalize；热路径只做声明过的工作，算法能力通过 `pkg` 注入。
3. **一套能力包**：IR、协议 codec、检测、敏感信息、usage、上游目录、路由、harness/session 全部放到 `pkg/`，可被外部 Go 程序引用；`internal/` 只保留 Trajecta 的业务编排、存储、HTTP 和进程生命周期。

目标不是增加更多分支，而是让"新增一种协议、一种检测、一种渠道、一种路由策略"都变成**实现一个包 + 注册一个能力**，而不是修改核心转发循环。

## 2. 当前状态与问题

### 2.1 必须保留的资产

| 资产 | 现状 | 处理 |
| --- | --- | --- |
| 热链路 / 旁路分析分离 | 转发、录制与解析已有分工 | 升级为显式 `hot` / `audit` 双通道 |
| 协议识别 + 敏感信息替换 | [`pkg/secretmask`](../pkg/secretmask) 的 Ragel DFA、保形假值、opaque 字段跳过 | 保留算法，包成 hot lane Hook |
| raw cassette 事实源 | [`pkg/recordfile`](../pkg/recordfile)、[`pkg/replay`](../pkg/replay) | 保留 V3；只写最小 meta，不引入 V4 |
| 渠道/模型数据库配置 | `ConfigurationTransaction` + reload | 保留事务和发布顺序，扩展 schema |
| 路由健康/熔断/sticky | [`internal/router`](../internal/router) | 保留运行时状态，选择逻辑交给 `pkg/route` |
| 模型广场/probe/detect | [`internal/monitor`](../internal/monitor)、[`internal/modelprobe`](../internal/modelprobe) | 保留并扩展多协议 |
| Inspector dry-run | `router.Explain` 与热路径共用候选逻辑 | 升级为同一 `pkg/route.Plan` |

### 2.2 结构性问题

**（1）没有稳定的中间表示，同一个 body 被反复解析。**

[`internal/proxy/handler.go`](../internal/proxy/handler.go) 的 `ServeHTTP` 约 397 行，一次请求可能经历：

- `injectStreamOptions` 解析并重新序列化；
- router 的 `extractRequestFeatures` 解析；
- protocol conversion 再解析；
- recorder 再解析模型名。

`/v1/responses` 的原生路径还会多次调用特征提取。每次解析的容错策略、字段覆盖和 `UseNumber` 设置都不同，导致"录制端、路由端、转换端、审计端对同一份字节给出不同答案"。

**（2）渠道优先级实际上是失效的。**

[`internal/router/router.go`](../internal/router/router.go) 的 `Target.Priority` 只用于排序和同分 tiebreak；`expectedCost` 根本不读 `Priority`。真实流量下，只要 EWMA 有一点差异，高优先级渠道就会被绕过。"主渠道/备用渠道"无法表达。

**（3）渠道配置模型无法表达多协议和 coding plan。**

[`internal/upstream/resolved.go`](../internal/upstream/resolved.go) 的 `ProviderPresetSpec` 只有 `ProtocolFamily`、`RoutingProfile`、`SupportLevel`、`AllowedProfiles`：

- 没有 base URL；
- 没有 auth 方式；
- 没有默认 header；
- 没有模型列表来源；
- 没有 plan/region 变体。

所以新建渠道必须手填 URL，coding plan 只能靠用户自己拼 endpoint，OpenRouter 这类同时支持 chat/messages 的中转也无法表达为"一个渠道多协议"。

**（4）协议转换面窄且不可组合。**

[`pkg/protocolconvert`](../pkg/protocolconvert) 只承载文本、工具、基础参数；多模态、reasoning、structured output、stateful 字段一律拒绝；流式转换明确不支持。转换失败只有 warning，没有统一的 loss ledger，也无法按渠道/模型做差异化策略。

**（5）会话识别有两套。**

router 的 [`sticky.go`](../internal/router/sticky.go) 读 headers + body 的 `previous_response_id` / `prompt_cache_key` / `metadata.session_id` 等；store 的分组只读 headers。两套来源、两套隐私规则、两套测试。`previous_response_id` 目前只是粘性 key，不是硬亲和，换上游可能静默破坏服务端状态。

**（6）本地 Responses runtime 是第二个 proxy。**

[`internal/proxy/responses_server.go`](../internal/proxy/responses_server.go) 有自己的选择、mask、录制、usage、审计写入，和主路径重复。任何"热路径/审计路径"改造都必须同时改两套，否则行为漂移。

**（7）字节保真靠约定，不靠类型。**

`bodyBytes`、`upstreamBodyBytes`、`wireBodyBytes`、`sendBodyBytes` 四个变量在默认路径相等，但没有类型隔离。未来新增一个改写步骤，很容易把 masked/converted 字节写进 cassette，或者让路由 key 从改写后的字节派生。

**（8）masking 在热路径上总是全量扫描。**

请求侧对每个 body 跑 DFA；响应侧只要 masking 开启就安装 restorer，即使这个请求根本没有产生任何假值。对大 body 和高并发场景，这是不必要的固定成本。

## 3. 目标与非目标

### 3.1 目标

1. **可靠性**：任何失败都有明确分类和可预期行为；热路径不因审计能力增加而抖动。
2. **稳定性**：配置提交与路由发布原子；转换失败不污染上游健康；字节改写不破坏 replay。
3. **功能性**：多渠道/多协议/coding plan；模型广场；优先级 + 会话 + 故障转移路由；IR 驱动的检测/改写/审计。
4. **可扩展性**：新增协议 = 新增 codec 包；新增检测 = 新增 Hook 包；新增渠道 = 新增 preset 数据；新增路由策略 = 新增 `pkg/route` 策略。
5. **可复用性**：IR、检测、usage、上游解析、路由算法都能被外部 Go 程序 import，不依赖 Trajecta 的数据库和 HTTP server。

### 3.2 非目标

- 不做通用 API 网关、计费系统、多租户 SaaS。
- 不追求覆盖所有厂商私有字段；不能承载的字段显式 loss 或拒绝。
- 不在热路径运行需要网络、数据库或秒级计算的检测。
- 不把流式协议转换作为第一阶段目标。
- 不为每个厂商写一套独立的转发实现；厂商差异优先用 preset 数据 + 少量 driver 表达。

## 4. 设计原则

### 4.1 Native first

能原生直通就绝不转换。协议转换是最后手段，且必须逐字段说明 loss。

### 4.2 Raw first

- 客户端原始请求字节是唯一事实源；
- 上游 wire 字节是可选证据，不替代原始字节；
- 数据库只存索引、摘要、findings，不存解析后的 IR 树；
- 审计时从 cassette 重算。

### 4.3 One parse, one planner

- 一个请求最多一次 envelope 扫描；
- 需要语义时再解析一次 wire tree；
- 转换才做 semantic decode；
- 热路径和 Inspector 共用同一个 `pkg/route.Plan`。

### 4.4 Hot path minimal

热路径只允许：

- 线性扫描；
- 必要的 wire parse；
- span patch；
- 已声明预算内的 Hook；
- 异步 finalize。

禁止：

- 网络调用；
- 同步数据库写；
- 无界解析/聚合；
- 慢检测。

### 4.5 pkg 提供能力，internal 负责编排

- `pkg/`：纯能力、显式输入输出、可外部引用；
- `internal/`：数据库、HTTP、配置、进程、UI、任务队列；
- `pkg` 不 import `internal`，不读全局配置，不打开数据库。

### 4.6 Loss is explicit

转换不静默丢字段。每个 loss 有字段、原因、严重级别、策略；能拒绝就拒绝，能降级就记录。

### 4.7 Hooks at declared points

检测/改写能力挂在 IR 的稳定工作点上，声明 lane、point、needs、fail policy。核心不感知具体检测器。

### 4.8 Replay untouched

`pkg/replay` 和 V2/V3 cassette 读取保持兼容；任何格式变化先改 `pkg/recordfile`，再改录制/监控/回放。

## 5. 总体架构

### 5.1 三个平面

```text
┌──────────────────────────────────────────────────────────────┐
│                        Control Plane                         │
│  Provider Presets · Channels · Models · Auth · Policies      │
│  Postgres + ConfigurationTransaction + Router Snapshot       │
└───────────────────────────┬──────────────────────────────────┘
                            │ immutable snapshot
┌───────────────────────────▼──────────────────────────────────┐
│                        Gateway Core                          │
│  Edge → Identity → Plan → Transform → Egress → Ingress        │
│         → Record → Finalize                                  │
└───────────────┬──────────────────────────────┬───────────────┘
                │                              │
       Hot Lane Hooks                    Recorder
   (mask/usage/policy/rewrite)      (raw cassette, minimal meta)
                │                              │
        ┌───────▼────────┐          ┌──────────▼──────────────┐
        │   Upstreams    │          │      Audit Plane        │
        │ native/local/  │          │ reparse → hooks →       │
        │ converted      │          │ findings / ATIF / cache │
        └────────────────┘          └─────────────────────────┘
```

- **Control Plane**：配置、凭据、模型目录、路由策略、Hook 策略。写入必须经过 `ConfigurationTransaction`，提交成功后才发布路由快照。
- **Gateway Core**：唯一的热路径编排。协议无关；通过 `pkg/xir`、`pkg/route`、`pkg/upstream` 获得能力。
- **Audit Plane**：从 cassette 重解析，运行慢 Hook，产出 findings/ATIF；不修改已发出的流量。

### 5.2 两条执行通道

| 通道 | 触发 | 预算 | 能力 |
| --- | --- | --- | --- |
| **Hot** | 请求内联 | 毫秒级；无网络/DB | envelope、wire parse、span patch、mask、usage、block |
| **Audit** | 请求后 / 按需 / 批处理 | 秒级；可联网/DB | 完整 semantic、慢检测、LLM judge、ATIF |
| **Control** | 配置变更 | 事务级 | preset/channel/model/policy 写入、probe、reload |

Hook 必须声明自己属于哪个 lane。核心只运行当前 lane 允许的 Hook。

### 5.3 核心流程

```text
HTTP request
  │
  ├─ 0. Edge: normalize path, auth, body limit
  │
  ├─ 1. Identity: envelope scan
  │      model / stream / protocol / operation
  │      session ref / state refs / harness
  │
  ├─ 2. Plan: pkg/route.Plan
  │      target / mode / wire protocol / tier / affinity
  │      hook plan / loss policy
  │
  ├─ 3. Request transform
  │      Body{Client, Wire}
  │      native & no hooks → Wire = Client
  │      else → wire parse → hooks → patches → convert → mask
  │
  ├─ 4. Egress
  │      upstream call, retry/failover
  │      只为最终 exchange 写 raw cassette
  │
  ├─ 5. Ingress
  │      passthrough copy / stream decode → hooks → encode
  │      restore before convert; usage extract on raw upstream
  │
  ├─ 6. Record
  │      cassette raw exchange + minimal meta
  │      routing/usage/transform 摘要写 DB
  │
  └─ 7. Finalize (async)
         index, parse job, findings job
```

## 6. 包边界：`pkg` 与 `internal`

### 6.1 判定规则

一个能力放进 `pkg/`，当且仅当：

1. 不 import `internal/*`；
2. 不读全局配置、不打开数据库、不启动 HTTP server；
3. 输入输出显式；网络、时钟、存储通过参数或接口注入；
4. 可以被仓库外的 Go 程序直接 import；
5. 有独立、离线可跑的单元测试；
6. API 有稳定意图，不绑定 Trajecta 的数据库 schema 或 UI。

不满足的能力放 `internal/`。

### 6.2 `pkg` 清单

| 包 | 职责 | 主要 API | 依赖 |
| --- | --- | --- | --- |
| `pkg/xir` | Exchange IR 类型与语义 | `Request`、`Response`、`Part`、`Tool`、`Usage`、`Loss` | `pkg/usage` |
| `pkg/xir/wire` | 无损 JSON/SSE span 解析与 patch | `Document`、`Node`、`Edit`、`Apply` | 无 |
| `pkg/xir/codec/*` | 各协议 decode/encode | `Codec`、`DecodeOptions`、`EncodeOptions` | `pkg/xir` |
| `pkg/xir/hook` | Hook 契约与计划 | `Hook`、`Event`、`Result`、`Point`、`Plan`、`Registry` | `pkg/xir` |
| `pkg/xir/convert` | 语义转换与 loss ledger | `Convert`、`LossPolicy`、`Loss` | `pkg/xir` |
| `pkg/xir/stream` | SSE 帧编解码与事件装配 | `Frame`、`Decoder`、`Encoder`、`Assembler` | `pkg/xir`、`pkg/usage` |
| `pkg/xir/audit` | 审计投影 | `Observation`、`Build`、`BuildObservation`、`EvidencePath` | `pkg/xir`、`pkg/usage` |
| `pkg/detect` | 通用检测框架 | `Scanner`、`Match`、`Span`、`Engine` | 无 |
| `pkg/detect/secretmask` | `pkg/detect` 的 secretmask 适配 | `Scanner` | `pkg/detect`、`pkg/secretmask` |
| `pkg/secretmask` | 凭据/PII 形状识别与保形假值 | `Vault`、`Scanner`、`MaskEvent` | 无（由 internal 适配 `detect.Scanner`） |
| `pkg/usage` | 统一 usage、合并、归一化 | `Usage`、`Merge`、`Add`、`Normalize` | 无 |
| `pkg/upstream` | provider preset、endpoint、auth、模型发现 | `Preset`、`Resolve`、`BuildURL`、`ApplyAuth`、`DiscoverModels` | `pkg/xir` |
| `pkg/route` | 路由算法 | `Request`、`Candidate`、`PlanResult`、`Policy`、`Plan`、`Explain` | `pkg/xir` |
| `pkg/harness` | 客户端 harness 识别 | `Detect`、`Detection` | 无 |
| `pkg/session` | 会话信号提取与 fingerprint | `Resolve`、`Ref` | 无 |
| `pkg/modelinfo` | 模型静态元数据 | `Lookup`、`Facts` | Argus |
| `pkg/recordfile` | cassette 格式 | 现有 API | 无 |
| `pkg/replay` | 测试回放 | 现有 API | 无 |

说明：

- `pkg/upstream` 是纯解析/构造，不持久化渠道。数据库渠道由 `internal/channel` 负责。
- `pkg/route` 是纯决策，不持有健康状态。健康、亲和、快照由调用方注入；当前线上选择仍由 `internal/router` 执行，迁移计划见 M08/M10。
- `pkg/harness` 只读 headers/path；`pkg/session` 只提取信号，不决定路由。
- `pkg/modelinfo` 是 Argus 的薄包装，保持内存查询。

### 6.3 `internal` 清单

| 包 | 职责 |
| --- | --- |
| `internal/gateway` | 热路径编排：Body、转换、重试、上游调用、录制接线、本地 Responses 接入 |
| `internal/channel` | 数据库渠道/模型服务、probe、模型同步、配置投影 |
| `internal/router` | 运行时快照、健康、熔断、affinity 状态；当前仍执行线上选择，计划迁到 `pkg/route` |
| `internal/store` | Postgres/SQLite schema、查询、事务、派生状态 |
| `internal/credential` | 凭据加密、OAuth 流程、refresh、状态 |
| `internal/hooks` | 配置绑定、Trajecta 专属 Hook、内置 Hook 注册 |
| `internal/audit` | 审计任务、缓存、findings 持久化 |
| `internal/monitor` | Monitor HTTP API 与 UI 装配 |
| `internal/identity` | 组合 `pkg/harness` + `pkg/session` + 请求上下文 |
| `internal/config` | YAML/env 基础设施配置 |
| `internal/auth` | 用户/token/JWT |
| `internal/limit` | 限流 |
| `internal/netproxy` | 出站代理策略 |
| `internal/responses` | 本地 Responses runtime |
| `internal/recorder` | cassette 写入与 finalize（或并入 gateway） |
| `internal/observeworker` | parse job worker |
| `internal/reanalysis` | 重分析 job |
| `internal/trajectory` | ATIF 导出的业务层（底层用 `pkg/xir/audit`） |
| `internal/modelprobe` | 模型连通性/接口探测 |
| `internal/mcpserver` | MCP 工具面 |
| `internal/cli` | 运维 CLI |
| `cmd/*` | 二进制入口 |

### 6.4 依赖方向

```text
独立能力包（无内部依赖）：
  pkg/usage
  pkg/detect
  pkg/secretmask
  pkg/harness
  pkg/modelinfo        → Argus
  pkg/recordfile
  pkg/replay

IR 与上层能力：
  pkg/xir              → pkg/usage
  pkg/xir/wire         → 无
  pkg/xir/codec/*      → pkg/xir
  pkg/xir/hook         → pkg/xir
  pkg/xir/convert      → pkg/xir
  pkg/xir/audit        → pkg/xir

  pkg/session          → 无
  pkg/upstream         → pkg/xir
  pkg/route            → pkg/xir

编排：
  internal/*           → pkg/* 和 internal/*
  cmd/*                → internal/*
```

强制规则：

- `pkg` 不 import `internal`（CI 检查）；
- `pkg` 不 import `net/http` 的 server 侧包；需要 HTTP client 时通过参数注入；
- `pkg` 不读环境变量/全局配置；
- 不允许循环依赖；
- 新增 `pkg` 公开 API 必须写 doc comment 和至少一个外部使用示例（测试或 example）。

### 6.5 对外可引用性

外部程序可以直接使用：

```go
import (
    "github.com/kingfs/Trajecta/pkg/xir"
    "github.com/kingfs/Trajecta/pkg/xir/codec/chat"
    "github.com/kingfs/Trajecta/pkg/xir/convert"
    "github.com/kingfs/Trajecta/pkg/route"
    "github.com/kingfs/Trajecta/pkg/upstream"
    "github.com/kingfs/Trajecta/pkg/secretmask"
)
```

示例场景：

- 其他代理复用 XIR 做协议转换；
- 测试工具复用 `pkg/secretmask` 做脱敏；
- 自建网关复用 `pkg/upstream` 的 preset/URL/auth 规则；
- 研究工具复用 `pkg/route` 做离线选路比较。

`internal/` 不承诺外部兼容，可以随 Trajecta 业务演进。

### 6.6 旧包迁移映射

| 旧位置 | 新位置 | 说明 |
| --- | --- | --- |
| `internal/proxy/handler.go` | `internal/gateway` + 薄 `internal/proxy` | 业务编排迁到 gateway；proxy 只做 HTTP 适配 |
| `internal/router` 的选择逻辑 | `pkg/route` | 纯决策；router 保留健康/亲和/快照 |
| `internal/routeplan` 的 DTO | `pkg/route` | Inspector 与热路径共用 |
| `internal/upstream` 的纯解析 | `pkg/upstream` | preset、URL、auth、capabilities |
| `internal/upstream` 的 store 投影 | `internal/channel` | 数据库渠道/模型服务 |
| `internal/modelinfo` | `pkg/modelinfo` | Argus 薄包装，内存查询 |
| `internal/harness` | `pkg/harness` | 只读 header 分类 |
| `router/sticky.go` 的会话提取 | `pkg/session` + `internal/identity` | 统一信号源；router 只消费 fingerprint |
| `pkg/llm` + `pkg/protocolconvert` | `pkg/xir` codec + `pkg/xir/convert` | 保留旧包作为兼容 facade，逐步下线 |
| `pkg/observe` 的 parser | `pkg/xir/audit` | 一个版本周期内 differential test，之后下线 |
| `internal/recorder` | `internal/gateway` 或保留 `internal/recorder` | 取决于 Body/录制拆分；优先保留独立 recorder |

迁移期间允许新旧并存，但**新能力只加在新包**；旧包只修 bug，不再扩功能。

## 7. Exchange IR（XIR）总览

> 详细契约见后续 M01–M04 模块文档；本节固定抽象和边界。

### 7.1 设计要求

XIR 必须同时满足：

1. **无损**：原生直通和改写路径保留未知字段、字段顺序、空白、转义和字节 span。
2. **可转换**：四种协议之间有共同语义模型，能表达文本、工具、推理、多模态、usage、stop reason、state refs。
3. **可检测/改写**：每个内容片段能定位到原始字节 span；Hook 可以只改一个字段而不重编码整个 body。
4. **可流式**：流式事件不聚合、不破坏首字节延迟；支持逐帧 Hook。
5. **可审计**：从 cassette 重建同一份 IR；finding 能带 evidence path。
6. **可扩展**：新增协议只加 codec；新增能力只加 Hook；不修改核心。
7. **高性能**：默认路径不做 semantic decode；wire parse 是线性、低分配；patch 一次应用。

### 7.2 一个 IR，三种视图

```text
                    ┌──────────────────────────┐
                    │      xir.Document        │
                    │  raw bytes + wire tree   │
                    └────────────┬─────────────┘
                                 │
          ┌──────────────────────┼──────────────────────┐
          ▼                      ▼                      ▼
    Wire view              Semantic view           Audit view
  Node / Span / Edit     Request / Response      Observation / ATIF
  (byte-preserving)      (typed, protocol-neutral) (findings/evidence)
```

- **Wire view** 是底层事实：字节、偏移、JSON 结构。
- **Semantic view** 是类型化投影：转换、路由特征、Hook 的语义输入。
- **Audit view** 是审计投影：节点树、工具调用、资源、findings。

三者不是三套 parser：wire parse 一次；semantic 按需 decode；audit 从同一 Document 投影。

### 7.3 Wire 视图

核心类型：

```go
package wire

type Kind uint8

const (
    Object Kind = iota
    Array
    String
    Number
    Bool
    Null
)

type Span struct {
    Start, End int
}

type Node struct {
    Kind    Kind
    Span    Span
    Members []Member // 保持顺序；允许重复 key
    Items   []*Node
    Str     string   // 惰性解码
}

type Member struct {
    Key     string
    KeySpan Span
    Value   *Node
}

type Document struct {
    Protocol xir.Protocol
    Raw      []byte
    Root     *Node
}
```

解析特性：

- 单次线性扫描；
- 不构造 `map[string]any`；
- 当前解析时 eager 解码字符串；后续可改为惰性 `DecodeString` 以降低分配。
- 保留未知字段和顺序；
- 容忍重复 key、尾随空白；
- 对非法 JSON 返回带偏移的错误。

改写：

```go
type EditMode uint8

const (
    ReplaceRaw EditMode = iota
    ReplaceString
    Delete
)

type Edit struct {
    Span   Span
    Bytes  []byte
    Mode   EditMode
    Reason string
    // PreserveLength 为 true 时，替换字节长度必须与 Span 长度一致。
    PreserveLength bool
}

func Apply(raw []byte, edits []Edit) ([]byte, error)
```

规则：

- 按 span 从后往前应用；
- 重叠 edit 直接报错；
- 默认验证结果是合法 JSON；
- 长度变化由 Gateway 重算 `Content-Length`；
- 响应侧不信任上游 `Content-Length`；
- `PreserveLength` 用于凭据遮蔽等硬约束。

### 7.4 Semantic 视图

```go
package xir

type Exchange struct {
    Protocol  Protocol
    Operation Operation
    Request   *Request
    Response  *Response
}

type Request struct {
    Model      string
    System     []Part
    Messages   []Message
    Tools      []Tool
    ToolChoice ToolChoice
    Params     Params
    State      StateRefs
    Extensions map[string]json.RawMessage // key 是协议名，如 "responses"
}

type Message struct {
    Role  Role
    Parts []Part
    Ref   *wire.Node
}

type Part struct {
    Kind Kind

    Text       string
    ToolCall   *ToolCall
    ToolResult *ToolResult
    Media      *Media
    Reasoning  *Reasoning
    Sealed     *Sealed

    Ref *wire.Node
}
```

关键类型：

- `Sealed`：协议原生不透明块，例如 Anthropic `thinking` / `redacted_thinking`、Responses `encrypted_content`、Gemini `thoughtSignature`、Bedrock `reasoningContent`。**不解码、不跨协议伪造、不参与敏感扫描**。
- `Extensions`：按协议保存未建模字段；同协议重编码可带回，跨协议进入 loss ledger。
- `StateRefs`：`previous_response_id`、`conversation`、`prompt_cache_key` 等；用于硬/软亲和。
- `Params`：temperature、top_p、top_k、max_tokens、stop、response_format、seed、penalties、parallel_tool_calls、reasoning、logprobs 等。
- `Usage`：来自 `pkg/usage`，统一 input/output/reasoning/cache read/write。

协议概念映射：

| 概念 | Chat Completions | Responses | Anthropic Messages | Gemini generateContent |
| --- | --- | --- | --- | --- |
| system | `messages[role=system/developer]` | `instructions` | `system` | `systemInstruction` |
| user text | `content` parts | `input_text` | `text` block | `parts[].text` |
| assistant text | `message.content` | `output_text` | `text` block | `parts[].text` |
| tool 声明 | `tools[].function` | 扁平 `tools[]` | `tools[].input_schema` | `functionDeclarations` |
| tool 调用 | `tool_calls` | `function_call` item | `tool_use` block | `functionCall` part |
| tool 结果 | `role=tool` | `function_call_output` | `tool_result` block | `functionResponse` part |
| reasoning | `reasoning_content` / `reasoning` | `reasoning` item | `thinking` / `redacted_thinking` | `thought` / `thoughtSignature` |
| 图片/文件 | `image_url` / `input_audio` / `file` | `input_image` / `input_file` | `image` / `document` | `inlineData` / `fileData` |
| usage | `usage` | `usage` | `usage` | `usageMetadata` |
| stop | `finish_reason` | `status` / `incomplete_details` | `stop_reason` | `finishReason` |

表格只表达共同语义；协议独有字段走 `Extensions` 或 `Sealed`。

### 7.5 Hook 视图

```go
package hook

type Lane uint8

const (
    LaneHot Lane = 1 << iota
    LaneAudit
)

type Point string

const (
    PointRequestEnvelope   Point = "request.envelope"
    PointRequestSystem     Point = "request.system"
    PointRequestMessage    Point = "request.message"
    PointRequestToolDecl   Point = "request.tool_declaration"
    PointRequestToolCall   Point = "request.tool_call.arguments"
    PointRequestToolResult Point = "request.tool_result"
    PointRequestMetadata   Point = "request.metadata"

    PointResponseOutput     Point = "response.output"
    PointResponseReasoning  Point = "response.reasoning"
    PointResponseToolCall   Point = "response.tool_call.arguments"
    PointResponseToolResult Point = "response.tool_result"
    PointResponseUsage      Point = "response.usage"
    PointResponseError      Point = "response.error"

    PointStreamEvent       Point = "stream.event"
    PointStreamTextDelta   Point = "stream.delta.text"
    PointStreamReasonDelta Point = "stream.delta.reasoning"
    PointStreamToolArgs    Point = "stream.delta.tool_args"

    PointExchangeFinalize Point = "exchange.finalize"
)

type Needs uint8

const (
    NeedsWire Needs = 1 << iota
    NeedsSemantic
    NeedsStream
)

type Hook interface {
    ID() string
    Points() []Point
    Lanes() Lane
    Needs() Needs
    Priority() int
    FailPolicy() FailPolicy

    Handle(ctx context.Context, ev *Event) (Result, error)
}
```

`Event` 同时携带 wire 和 semantic，Hook 按需使用：

```go
type Event struct {
    Lane      Lane
    Point     Point
    Protocol  Protocol
    Operation Operation

    Doc      *wire.Document
    Request  *Request
    Response *Response
    Part     *Part
    Node     *wire.Node

    Path string
    Span wire.Span
    Text string
    Meta Meta
}

type Result struct {
    Findings []Finding
    Edits    []Edit
    Block    *Block
    Usage    *Usage
    Tags     map[string]string
}
```

执行规则：

- Hook plan 在配置变更时编译；热路径不查 map、不反射。
- `NeedsSemantic` 决定是否 decode；只做字节检测的 Hook 不触发 semantic。
- 同一 point 的 edit 冲突时 fail-closed。
- `FailOpen` 用于观测；`FailClosed` 用于 mask、策略阻断、凭据处理。
- 热 lane Hook 有硬预算；超时按 fail policy。
- 慢检测只能声明 `LaneAudit`。

内置 Hook 规划：

| Hook | Lane | 主要 Point | 说明 |
| --- | --- | --- | --- |
| `secretmask` | Hot | request.egress、response.ingress、stream.delta | 复用 Ragel DFA，只扫 eligible span |
| `usage` | Hot + Audit | response.usage、stream.event | 独立包，产出统一 Usage |
| `tool-guard` | Hot | request.tool_call.arguments | 工具参数策略、阻断/改写 |
| `pii` | Audit | 文本/工具点 | 慢速 PII 检测 |
| `prompt-injection` | Audit | message、tool_result | 可接外部模型 |
| `resource` | Audit | tool_call、tool_result | 提取路径/URL |
| `cost` | Audit | usage | 价格、成本归因 |
| `redaction` | Hot | error、metadata | 日志脱敏 |

### 7.6 转换与 loss

转换采用 **semantic hub**：`A → XIR → B`，不为 12 个协议对写 12 套转换器。

规则：

1. **同协议永远字节直通**，不经过 semantic encode。
2. 每个字段归类：`Carried`、`Mapped`、`Degraded`、`Unsupported`。
3. `LossPolicy`：
   - `strict`：任何 fatal loss 直接拒绝；
   - `warn`：记录 loss，继续；
   - `drop`：仅限展示性字段。
4. **Stateful 字段不可转换**：`previous_response_id`、`conversation`、`background`、`store` 在目标协议没有等价物时拒绝。
5. **Sealed 字段只在同协议保留**；跨协议不伪造签名。
6. **Adaptive unfit memory**：上游拒绝某字段后，记录 `(channel, model, protocol, field)`，后续自动避开并写审计事件。

质量矩阵：

| 源 → 目标 | Chat | Responses | Messages | Gemini |
| --- | --- | --- | --- | --- |
| Chat | 直通 | good | fair | fair |
| Responses | good | 直通 | fair | fair |
| Messages | fair | fair | 直通 | discouraged |
| Gemini | fair | fair | discouraged | 直通 |

`discouraged` 不是不能转，而是默认不允许自动选路，除非显式策略允许。

### 7.7 Streaming

```go
type StreamEvent struct {
    Kind     StreamKind
    Span     wire.Span
    Text     string
    ToolID   string
    ToolName string
    Usage    *Usage
}

type StreamDecoder interface {
    Next(ctx context.Context) (*StreamEvent, error)
}

type StreamEncoder interface {
    Write(ctx context.Context, ev *StreamEvent) error
    Flush(ctx context.Context) error
    Close(ctx context.Context) error
}
```

两种模式：

- **Passthrough**：默认 `io.Copy`，不解析；需要 usage 时只扫 SSE `data:` 行。
- **Transform**：`StreamDecoder → Hook → StreamEncoder`，逐帧处理；mask/restore 只处理文本、reasoning、tool args 三类 delta。

流式协议转换后置，但接口先定。实现时必须是带状态机的逐帧转换，`Finalize` 补终止事件和最终 usage；禁止聚合式重编码。

### 7.8 Audit 投影

```text
cassette → wire.Document → semantic.Request/Response → xir/audit.Observation
```

`Observation` 保留现有 `pkg/observe` 的节点树、稳定 ID、证据路径思路，但不再维护第二套协议 parser。旧 `pkg/observe` 在一个版本周期内保留，用 differential test 对比，确认一致后下线。

### 7.9 与 magpie / relaykit 的取舍

**magpie**（MIT，可参考实现）：

- IR 用 `Part` 表达 text/image/file/tool_call/tool_result/thinking/web_search；
- `Sealed` 保留 Anthropic/Responses 的原生推理块；
- `Request` 保留大量协议原生 raw 字段；
- native-first 的候选判断 + 明确的强制转换例外；
- affinity 带 `Why`，可持久化；
- adaptive `unfit`：让上游"教"网关它不接受什么；
- 录制与还原的 writer 顺序。

不照搬：magpie 的 IR 混入了大量 agent/bridge/routing 状态；Trajecta 的 XIR 应该更小、更稳定。

**relaykit**（AGPL，仅设计参考，不复制代码）：

- pairwise DTO + 窄中立 side channel；
- 转换结果带 quality/steps/diagnostics；
- 流式状态机 + `Finalize`；
- loss policy（allow/safe/strict）。

采纳其"loss 显式、流式状态化"的思想，但不采用"Chat DTO 作为 pivot"。

**Trajecta 的选择**：一份 wire 事实 + 一份较小的 semantic hub + sealed/extensions + loss ledger。比 magpie 的 IR 轻，比 naive universal struct 保真。

## 8. 渠道与模型控制面

### 8.1 Preset 是端点数据

```go
type Preset struct {
    ID        string
    Name      string
    Kind      Kind // vendor, relay, local
    Endpoints map[xir.Protocol]string
    Auth      AuthSpec
    Plans     []PlanVariant
    Models    ModelSource
    Headers   map[string]string
    DocsURL   string
    KeysURL   string
}

type PlanVariant struct {
    ID          string
    Name        string
    Endpoints   map[xir.Protocol]string
    ModelFilter string
}
```

示例：

| Preset | Chat | Responses | Messages | 说明 |
| --- | --- | --- | --- | --- |
| OpenAI | `https://api.openai.com/v1` | 同左 | — | API key |
| Anthropic | — | — | `https://api.anthropic.com` | x-api-key |
| DeepSeek | `https://api.deepseek.com/v1` | 同左 | `https://api.deepseek.com/anthropic` | 一个渠道多协议 |
| OpenRouter | `https://openrouter.ai/api/v1` | probe 后启用 | `https://openrouter.ai/api` | 知名中转 |
| Zhipu / Z.ai | 普通 / coding 两 plan | coding plan `/api/v1` | `.../api/anthropic` | plan key |
| Kimi Code | `https://api.kimi.ai/coding/v1` | — | `https://api.kimi.ai/coding` | membership |
| Vertex | — | — | Gemini | 用户 project/location |

协议支持度来自三处：preset 默认、模型 probe/detect、运行期 unfit。

### 8.2 Channel / ChannelModel

```go
type Channel struct {
    ID        string
    Name      string
    PresetID  string
    Endpoints map[xir.Protocol]string
    Protocols []xir.Protocol
    Auth      CredentialRef
    Priority  int
    Weight    float64
    Enabled   bool
}

type ChannelModel struct {
    ChannelID string
    Model     string
    WireNames map[xir.Protocol]string
    Protocols []xir.Protocol
    Enabled   bool
    Source    ModelSource
    Metadata  ModelMetadata
}
```

协议在 channel 和 model 两层都是多选。模型广场按模型聚合，渠道详情按模型展示协议 badge。

### 8.3 认证

```go
type AuthSpec struct {
    Mode    AuthMode // api_key, oauth, cli_credential, none
    Header  string
    Scheme  string
    OAuth   *OAuthSpec
    Refresh *RefreshSpec
    Headers []HeaderTemplate
}

type OAuthSpec struct {
    AuthorizeURL  string
    TokenURL      string
    DeviceCodeURL string // 可选
    ClientID      string
    Scopes        []string
    RedirectURI   string
    PKCE          bool
    ExtraParams   map[string]string
}

type Credential struct {
    Mode         AuthMode
    APIKey       string
    AccessToken  string
    RefreshToken string
    ExpiresAt    time.Time
    AccountID    string
    Plan         string
    Scopes       []string
}

// Driver 是本轮只预留、不实现的接口。
type Driver interface {
    ID() string
    Begin(ctx context.Context, req AuthRequest) (Flow, error)
    Exchange(ctx context.Context, req AuthRequest, code string) (Credential, error)
    Refresh(ctx context.Context, cred Credential) (Credential, error)
    Apply(ctx context.Context, cred Credential, req *http.Request) error
    Status(cred Credential) CredentialStatus
}
```

**本轮落地范围**：

1. **API key / plan key**：实现。覆盖大多数官方渠道和 coding plan；plan 只是 endpoint 变体。
2. **CLI credential**：预留读取接口，默认关闭；只读本机已登录的 Claude Code / Codex / Gemini CLI 凭据。
3. **OAuth driver**：**只预留接口和存储字段，不实现流程**。后续可接 Claude Code、ChatGPT/Codex、Gemini Code Assist、GitHub Copilot；显式 opt-in，标注 ToS 风险。

预留的存储：

```text
channel_credentials
  id, channel_id, auth_type, label,
  secret_ciphertext, refresh_ciphertext,
  expires_at, account_id, plan, scopes_json,
  status, created_at, updated_at
```

预留的 API：

```text
POST /api/channels/{id}/auth/start
POST /api/channels/{id}/auth/callback
GET  /api/channels/{id}/auth/status
```

当前没有注册的 OAuth driver 时：

- `auth/start` 返回 501 `oauth_not_supported`，错误码稳定；
- UI 在 preset 支持 OAuth 但 driver 未注册时显示 disabled 选项；
- 数据库/preset 字段可以保存 `auth_type=oauth`，但请求不会选中该渠道，直到 driver 实现；
- `pkg/upstream.ApplyAuth` 只接受 API key / access token，不包含任何厂商 OAuth 逻辑。

凭据加密存储；未来 refresh token 由后台刷新，失败进入 `auth_expired`，渠道降级而不是随机 500。

### 8.4 模型广场

- 卡片：模型名、厂商、系列、描述、上下文/输出、模态、能力、支持渠道数、协议 badge、健康、最近用量。
- 详情：按渠道列优先级、启用状态、协议、wire name、健康、延迟、错误率。
- 操作：启用/禁用、调整优先级、测试/识别接口、查看 trace。

### 8.5 配置事务

所有管理写入继续走 `store.ConfigurationTransaction`：

- 持配置锁 + 单 SQL 事务；
- 回调显式 commit；
- commit 成功后才发布路由快照；
- 模型启用/禁用立即生效；
- 失败回滚，不发布半套配置。

## 9. 路由

### 9.1 Plan

```go
package route

type Mode string

const (
    ModeNative  Mode = "native"
    ModeLocal   Mode = "local_responses"
    ModeConvert Mode = "convert"
)

type Request struct {
    Model     string
    Protocol  xir.Protocol
    Operation xir.Operation
    Stream    bool
    Features  Features
    State     xir.StateRefs
    Session   SessionRef
}

type Plan struct {
    Target     Target
    Mode       Mode
    Wire       xir.Protocol
    WireModel  string
    Tier       int
    Affinity   AffinityDecision
    Hooks      *hook.Plan
    Loss       []xir.Loss
    Candidates []Candidate
    Reason     string
}

func Plan(ctx context.Context, req Request, snap Snapshot, policy Policy) (*Plan, error)
```

```go
type PriorityMode string

const (
    PriorityCompat PriorityMode = "compat"
    PriorityStrict PriorityMode = "strict"
)

type Policy struct {
    ModeOrder       []Mode       // 默认 native, local_responses, convert
    PriorityMode    PriorityMode // compat | strict
    AllowConversion bool
    AllowLocal      bool
    StickyTTL       time.Duration
}
```

`pkg/route` 是纯决策；`internal/router` 提供 `Snapshot`、健康、亲和状态。

### 9.2 候选与优先级

选择顺序：

```text
1. Mode: Native > Local Responses > Convert
2. Within mode:
   compat → cost/health across all candidates; priority only breaks ties
   strict → highest priority tier with a selectable candidate
3. Within tier (strict) / within mode (compat):
   hard affinity > soft affinity > health/weight/cost
4. Failover: exclude failed target, descend a tier (strict) or re-score (compat)
5. All native tiers exhausted → local / convert
```

这和需求一致：

- 协议匹配直通优先；
- 同模式内按 `priority_mode` 解释优先级（`strict` 严格 tier，`compat` 现状）；
- 协议转换最后；
- 欠费/限频/服务错误时降 tier，而不是全场 cost 重排。

`priority` 是 tier，不是 tiebreak。同 tier 内才用 weight、健康、P2C。

#### `priority_mode` 是什么

`priority_mode` 不是另一个优先级字段，而是**解释 `priority` 的方式**。它只解决一个兼容性问题：现有安装的 `priority` 事实上只是排序/同分兜底，直接改成严格 tier 会改变路由行为。

| 取值 | 语义 | 适用 |
| --- | --- | --- |
| `compat` | 保持现状：所有候选参与成本/健康比较，`priority` 只在分数相同时决定 | 旧安装默认；迁移期 |
| `strict` | `priority` 是层级：只考虑当前最高、且仍有可用候选的 tier；低 tier 只在故障转移时使用 | 新安装默认；显式开启 |

示例：A(priority=100, native)、B(50, native)、C(10, native)、D(200, convert)。

- `strict`：A 健康时选 A；A 欠费/熔断后选 B；B 也不可用才选 C；D 只有 native 全部不可用时才考虑。
- `compat`：即使 A 健康，成本/健康分数也可能让 B 胜出；`priority` 只在同分时起作用。

注意顺序：**先按 mode（native > local > convert），再按 `priority_mode` 处理 tier**。所以高优先级的转换渠道不会击败低优先级的原生渠道。

配置：

```yaml
routing:
  selection:
    priority_mode: compat   # 旧安装默认；新安装 strict
```

模型级可在 `route_policies.policy_json` 覆盖：

```json
{"priority_mode": "strict"}
```

交互规则：

- **硬亲和**始终覆盖 tier（服务端状态不能换上游）；
- **软亲和**在 `strict` 下只在当前最高可用 tier 内生效，避免主渠道恢复后会话仍粘在备用渠道；
- **失败转移**通过 target/model/credential 冷却实现，不修改 `priority` 数字；
- 切换 `compat → strict` 前，Inspector 应能预览 tier 顺序和受影响的模型。

### 9.3 亲和

**软亲和**：

- chat/messages/普通会话；
- key = session fingerprint + model；
- 目标：命中同一上游的 prompt cache；
- 失败/繁忙可 spill/rebind。

**硬亲和**：

- `previous_response_id`、`conversation` 等服务端状态；
- 只允许绑定过的 target；
- 不可用时：`pkg/route` 直接返回 `session_upstream_unavailable`；调用方可在 planner 之前决定是否改用本地 Responses runtime（该回退尚未实现）；
- 不静默换上游。

会话识别统一到 `pkg/session`（`internal/router` 已使用；`pkg/harness` 由 recorder 用于客户端分类）：

```go
type SessionRef struct {
    Key         string
    Fingerprint string
    Kind        Kind
    Confidence  Confidence
    Source      string
}
```

只存 fingerprint，原始 session 值不进入 trace/DB/cassette。

### 9.4 失败分类

| 分类 | 信号 | 动作 |
| --- | --- | --- |
| `quota` | 402、余额/欠费 | 渠道/模型冷却到 reset；降 tier |
| `rate` | 429 + Retry-After | 窗口冷却；降 tier |
| `auth` | 401/403 | 凭据 expired；不重试同凭据 |
| `model_missing` | 404 + model 文案 | channel-model 标记不可用 |
| `shape` | 400/422 unknown field | 记录 unfit；去掉字段重试一次 |
| `context` | 400 context length | 不重试同模型 |
| `upstream` | 5xx/timeout | 熔断；换 target |
| `refusal` | 安全/内容拒绝 | 不算渠道故障 |
| `conversion` | 本地转换失败 | 只尝试剩余原生候选；不消耗退避预算 |

分类器独立成包/子包，表驱动测试，不散落在 handler。

### 9.5 Explain

`POST /api/routing/inspect` 调用同一个 `route.Plan`，`DryRun=true`：

- 不读写 affinity；
- 不增加 inflight；
- 返回候选的 tier/mode/filter reason/loss/affinity；
- UI 能解释"为什么没选更高优先级渠道"。

## 10. 热路径与审计路径

### 10.1 热路径阶段

| 阶段 | 解析 | 说明 |
| --- | --- | --- |
| Edge | 无 | path 归一化、auth、body limit |
| Identity | 一次 envelope 扫描 | model/stream/protocol/session/state refs |
| Plan | 无 body parse | `pkg/route.Plan` |
| Transform | 按需 wire parse | 原生无 Hook：`Wire=Client`；否则 parse 一次、patch 一次 |
| Egress | 无 | 发送、重试；只为最终 exchange 写 cassette，失败 attempt 进 DB |
| Ingress | 按需 stream decode | 原生无变换：copy；否则 decoder→hooks→encoder |
| Finalize | 异步 | DB 索引、parse job、findings job；不重写 cassette |

### 10.2 Body 与字节保真

```go
type Body struct {
    Client  []byte    // 客户端原始字节，永远不变
    Wire    []byte    // 实际发往上游；nil 表示同 Client
    Changed bool
    Changes []Change  // 进 DB 的转换证据，不写 cassette
}
```

- `Client` 进 cassette，是 replay 的事实源；
- `Wire` 默认不持久化；`cassette.capture_wire` 开启时才写 sidecar；
- `Changes` 记录 mask/conversion/model alias/hook rewrite 的摘要，写 DB；
- 路由 key / session fingerprint / 模型解析都从 `Client` 派生；
- 响应侧对称：`Upstream` + `Client`。

### 10.3 性能预算

| 场景 | 目标 |
| --- | --- |
| 原生直通、无 Hook | 额外开销 < 5%；1 次 envelope scan |
| mask 命中 | 只复制一次 body |
| mask 未命中 | 不复制 body |
| 协议转换 | 只在 fallback 发生；不增加解析次数 |
| 流式直通 | 32 KiB 缓冲；无额外 writer |
| 流式 restore | 仅在请求产生过 mask 时安装 |

### 10.4 审计路径

```text
trace click / reanalyze job
  → load DB 索引（路由/usage/session/转换摘要）
  → load cassette
  → parse Client（默认）；启用 wire capture 时再 parse Wire
  → xir.Exchange
  → audit hook plan
  → findings + observation + ATIF
  → optional persist
```

缓存键：`(cassette hash, hook plan version, parser version)`。缓存是可删除的派生数据。

## 11. 存储与格式

> 详细设计见 [数据放置与证据模型](./02-data-placement.md)。本节只固定边界。

### 11.1 三层

| 层 | 内容 | 丢失后果 |
| --- | --- | --- |
| **文件** | 原始 HTTP request/response + 最小 meta | 无法 replay/审计，不可接受 |
| **数据库** | 渠道/模型/凭据配置、trace 索引、路由决策、usage、session、findings | 可查询信息丢失；配置不可丢，派生数据可重算 |
| **内存** | 健康、熔断、inflight、软亲和、编译计划、缓存 | 重启后重建，可接受 |

原则：

- 文件只回答"交换了什么字节"；
- DB 回答"为什么这样路由、结果如何"；
- 内存回答"现在哪条链路健康、哪个会话粘在哪里"；
- 不把路由/usage/timeline 写进 cassette。

### 11.2 文件：V3 最小化

继续使用 V3：

```text
# trajecta/v3
# meta: {最小字段}
<空行>
<原始 HTTP request>
<分隔换行>
<原始 HTTP response>
```

- 默认 **0 条 event**；
- meta 只保留解析/关联必需字段：`version`、`trace_id`、`time`、`endpoint`、`protocol`、`model`、`stream`、`exchange_kind/role/parent/sequence`、`truncated`/`synthetic`；
- 路由候选、target、tier、affinity、usage、harness、mask、conversion 全部进 DB；
- 只为**最终 exchange** 创建 cassette：重试只写 DB `route_attempts`；
- 写入路径：`<trace_id>.http.tmp` → 写 prelude/request/response → rename；不再 finalize 时 prepend/重写；
- 上游 wire 字节默认不存；需要精确取证时用 opt-in `cassette.capture_wire` 写 sidecar。

### 11.3 数据库

```text
channel_configs       渠道、endpoints、协议、优先级、权重、启用
channel_models        模型、wire names、协议、启用、来源、quirks
channel_credentials   凭据密文、auth_type、expiry、account、plan、scopes
route_policies        priority_mode、conversion、affinity 等
hook_configs          Hook 启用与配置

logs                  trace 索引：endpoint/model/status/duration/usage/
                      harness/session/selected target/route mode/tier/
                      conversion 摘要/error_class
route_attempts        每次重试的 target/status/error/latency
response_routes       response_id → upstream，硬亲和跨重启

trace_observations    解析摘要；节点树不落库
findings / resources  审计结果
credential_mask_audits 遮蔽真值/假值对（加密）
analysis_jobs         重分析任务
```

### 11.4 为什么不做 Cassette V4

V4 原本想在一个文件里同时保存"客户端可见字节"和"上游 wire 字节"。不采用：

1. 文件是原始证据，不是决策日志；双段会让 replay、解析、人工阅读复杂化。
2. 路由/usage/转换摘要属于 DB，放文件会重复且容易不一致。
3. 文件操作更多；当前 recorder 已经按 attempt 写文件、失败删除、finalize 时 prepend 重写，双段只会放大。
4. 多数审计用 raw client 字节 + DB mask audit/conversion 摘要即可回答；需要逐字节取证时用 opt-in sidecar。
5. replay 只依赖客户端可见交换，V4 对它没有收益。

如果未来出现"必须默认保存 wire"的合规要求，再单独评估；当前不引入。

## 12. 可靠性与安全

### 12.1 可靠性

- 失败分类明确；客户端错误不污染上游健康；转换失败不消耗退避预算。
- 配置事务 + 快照发布；运行中的快照不描述数据库拒绝的配置。
- 重试有预算和上限；all-targets-open 有退避，但不无限等待。
- 流式响应逐次 flush；不因审计/录制阻塞首字节。
- 异步 finalize 有界队列；满时回退内联，不能丢记录。
- 所有派生数据可重算；cassette 损坏时明确报错，不猜测。

### 12.2 安全

- 凭据加密存储；OAuth refresh token 单独保护。
- 敏感信息不进入日志、trace index、cassette prelude。
- 审计页面访问受 auth 保护；findings 可包含敏感值时必须标记。
- 自定义渠道的 base URL / header 不允许注入到日志或错误消息。
- 外部检测器默认关闭；启用的 DLP/LLM judge 要明确数据出境风险。

## 13. 模块拆分与路线

### 13.1 批次

**批次 A：IR 地基**

- M01 XIR 基础；
- M05 检测框架；
- M06 usage；
- 验收：span parser、类型、检测、usage 可用；不接主路径；fuzz/benchmark 通过。

**批次 B：协议能力**

- M02 codecs；
- M03 Hook 框架；
- M04 转换 + 流式接口；
- 验收：四协议 golden fixture；同协议字节直通；loss ledger；Hook 计划。

**批次 C：控制面与选路**

- M07 upstream/preset/auth；
- M08 route；
- M09 harness/session；
- 验收：离线 fake store/HTTP 下单渠道、多协议、优先级、亲和、失败分类。

**批次 D：主路径接入**

- M10 gateway pipeline；
- M11 channel control plane；
- 验收：feature flag 下与旧路径差分；字节/cassette/replay 一致；性能不退化。

**批次 E：审计与产品面**

- M12 audit/ATIF/cassette 最小化；
- M13 monitor/model square；
- M14 cleanup；
- 验收：旧包下线；文档同步；全量测试/race/bench。

### 13.2 阶段门禁

每个批次结束必须满足：

- 公开 API 冻结；
- 离线测试 + race；
- 差分测试通过；
- 性能预算达标；
- 回滚开关可用；
- `docs/` 同步当前行为。

### 13.3 最小闭环与后置项

模块文档不要求一次写完。建议先形成一条可验证的最小闭环：

```text
M01 XIR 基础
  → M02 Chat/Responses codec
  → M03 Hook 框架
  → M06 Usage
  → M07 Upstream preset
  → M08 Route
  → M10 Gateway 接入（feature flag）
```

这条闭环完成后，就能验证：一个原生 Chat 请求在不改变字节的前提下，走完 identity → plan → hook → record；协议转换和审计仍走旧路径。

可并行：

- M05 检测可与 M01 并行（`pkg/detect` 本身不依赖 XIR）；
- M09 会话识别可与 M08 并行；
- M11 渠道控制面可与 M10 并行，但 schema 契约要先定；
- M13 UI 在 M11 API 稳定后开始。

可后置：

- M04 的流式转换（接口先定，实现后置）；
- M12 的 DB timeline 迁移（先把路由/usage 从 cassette event 搬到 DB）；
- 可选的 wire capture sidecar（`cassette.capture_wire`，默认关闭）；
- M14 的旧包删除（至少保留一个版本周期）；
- OAuth coding plan、多凭据、动态插件。

## 14. 决策记录

| ID | 决策 | 理由 |
| --- | --- | --- |
| D-001 | 一个 XIR，wire + semantic + audit 三视图 | 避免 parser 漂移；wire 保真，semantic 支撑转换 |
| D-002 | 算法能力放 `pkg`，业务编排放 `internal` | 可复用、可测试、边界清晰 |
| D-003 | Native 全部 tier 优先于 convert | 符合"直通优先、转换最后" |
| D-004 | `priority_mode` 决定 `priority` 的解释：`compat` 现状、`strict` 严格 tier | 兼容旧安装，同时支持主/备渠道 |
| D-005 | 软亲和 + 硬亲和分离 | `previous_response_id` 不能静默换上游 |
| D-006 | 原始客户端字节是事实源 | replay、审计、证据链 |
| D-007 | Hook 先编译期 Go 包 + registry | 稳定、可测试；动态插件后置 |
| D-008 | 流式转换后置 | 工作量大；原生优先已覆盖多数 |
| D-009 | Coding plan 先 plan-key；OAuth 只预留接口，后续实现 | 多数 plan 只是 endpoint 变体；OAuth 复杂且有 ToS 风险 |
| D-010 | 不引入 Cassette V4；V3 最小 meta、默认无 event | 文件只存 raw HTTP，路由/usage 进 DB |
| D-011 | 路由/usage/harness/转换摘要进 DB，不进 cassette | 可查询、可重建；文件保持原始证据 |
| D-012 | 只为最终 exchange 创建 cassette；重试只记 DB | 减少文件操作；失败分析仍可查询 |
| D-013 | 上游 wire 字节默认不存，opt-in sidecar | 控制体积；精确取证时才开启 |

## 15. 风险与开放问题

### 15.1 风险

1. **Semantic hub 丢厂商字段**：用 `Extensions` + `Sealed` + `unfit` 缓解；跨协议无法承载必须拒绝。
2. **严格 priority 改变现有行为**：默认 `compat`，新安装默认 `strict`，UI 明示。
3. **Coding plan OAuth 稳定性/合规性**：独立 driver、显式 opt-in、失败降级。
4. **流式转换正确性**：先非流式；流式单独 fixture 矩阵。
5. **重构期间双路径**：feature flag + 差分测试；不做长期双轨。

### 15.2 开放问题

| 问题 | 影响 | 建议 |
| --- | --- | --- |
| 旧安装是否自动切到 `strict`？ | 影响所有现有路由 | 不自动；默认 compat，UI 提供预览后手动切换 |
| OAuth 首个 driver 做哪个？ | 工作量/ToS | 先预留接口；实现时从 Claude Code 或 Codex 选一个 |
| 是否需要默认保存上游 wire 字节？ | 取证/成本 | 默认不存；opt-in sidecar |
| 是否支持多渠道多凭据？ | 路由/UI/schema | schema 预留；Phase 2 后实现 |
| Hook 是否允许外部进程/WASM？ | 插件生态 | 先编译期接口；外部插件另立决策 |
| 转换的 `discouraged` 是否允许显式开启？ | 功能/质量 | 允许 per-model 显式开启，默认关闭 |

## 16. 参考

### 16.1 外部实现

- [magpie](https://github.com/yetone/magpie)：preset 端点数据、native-first、affinity、unfit、sealed reasoning、录制/还原顺序。MIT。
- [relaykit](https://github.com/QuantumNous/new-api/tree/main/relaykit)：转换诊断、loss policy、流式状态机。AGPL，仅设计参考，不复制代码。

### 16.2 仓库内相关文档

- [数据放置与证据模型](./02-data-placement.md)
- [架构与代码地图](../docs/ARCHITECTURE.md)
- [协议与 Provider](../docs/PROTOCOLS_AND_PROVIDERS.md)
- [协议转换](../docs/PROTOCOL_CONVERSION.md)
- [观测与审计](../docs/OBSERVATION_AND_AUDIT.md)
- [路由与凭据](../docs/ROUTING_AND_CREDENTIALS.md)
- [敏感信息遮蔽](../docs/SECRET_MASKING.md)

### 16.3 下一步

1. 评审本文、[`02-data-placement.md`](./02-data-placement.md) 与 [`TEMPLATE.md`](./TEMPLATE.md)；
2. 按 [`modules/README.md`](./modules/README.md) 的批次 A 开始写 M01/M05/M06；
3. M01 契约稳定后再写 M02/M03/M04；
4. 每个模块文档 `Accepted` 后才进入实现。

## 17. 附录：IR 参考实现拆解

本节回答"为什么 XIR 这样抽象"。参考对象是 magpie（MIT，可借鉴实现）与 relaykit（AGPL，仅设计参考，不复制代码）。

### 17.1 magpie 的 IR 结构

magpie 的 `internal/gateway/ir.go` 是"单一语义中枢 + 协议原生保真"的代表：

```text
Request
  Model / System / Messages / Tools / ToolChoice
  MaxTokens / Temp / TopP / Stop / Stream
  Effort / Thinking / Parallel / WebSearch
  CacheKey / Include / ClientMetadata / Text / Metadata
  AccessPrograms / ImageConfig / GenConfig / Safeguards
  Format / Namespaced / ... 约 30 个协议保真字段

Message
  Role + []Part

Part
  Kind: text | image | file | tool_call | tool_result | thinking | web_search
  Text / MediaType / Data / URL / Detail
  ID / Name / Args
  CallID / IsError / Images / Loads
  Signature / SignedBy / Sealed / SealedBy

Result
  ID / Model / Parts / Usage / Stop / ...

Event
  KStart | KText | KThink | KSig | KToolStart | KToolArgs
  KStop | KUsage | KError | KSearch | KImage | KSealed | KThinkStart | KAlive
```

值得借鉴：

1. **`Sealed` 是一等字段**：Anthropic 的 `redacted_thinking`、Responses 的 reasoning item 以原始字节保存，只由签名它的协议重建。跨协议时降级为无文本的 reasoning，不伪造签名。
2. **协议原生字段显式保留**：`Include`、`ClientMetadata`、`Text`、`Metadata`、`GenConfig`、`ImageConfig`、`AccessPrograms` 等不是"厂商怪癖"，而是链路保真所必需。XIR 用 `Extensions map[string]json.RawMessage` 表达同类需求，避免把每个厂商字段塞进根结构体。
3. **流式是事件流，不是聚合结果**：`KToolStart` / `KToolArgs` / `KSealed` / `KAlive` 等事件让逐帧转换和 keepalive 成为可能。
4. **usage 合并是显式规则**：非零覆盖、缓存读写拆分、Anthropic 的 prompt 总数不含 cache；XIR 的 `pkg/usage` 应保留这些语义。
5. **native-first 的候选判断**：`relayable = from ∈ usable(model)`，强制转换只发生在有明确理由的少数场景。

不照搬：

- `Request` 混入了 Claude bridge、Codex 账号、routing group、session 等业务状态；
- 大量厂商字段直接挂在根结构体上，长期维护成本高；
- 没有独立的诊断/loss 模型，兼容靠"记住被拒绝的字段"。

XIR 的取舍：保留 `Sealed`、事件流、usage 语义；把厂商字段收敛到 `Extensions`；把 bridge/routing/账号状态留在 `internal/`，不进 IR。

### 17.2 relaykit 的转换结构

relaykit 不是"一个 universal IR"，而是：

```text
4 个协议原生 DTO（Chat / Responses / Claude / Gemini）
+ 窄中立 side channel（tool set、reasoning intent、usage）
+ 2 个转换 registry（request / response）
+ 声明式 direct / multi-hop 路径
+ quality（good / fair / discouraged）
+ diagnostics（warning / error）
+ stream state（每步一个 state，Finalize 补终止事件）
```

值得借鉴：

1. **转换结果是带诊断的值**，不是"成功/失败"二值；字段丢失有 `Path`、`Severity`、`From`、`To`。
2. **路由表是数据**：`(from,to)` → converter、quality、steps；注册时校验路径连续性。
3. **流式状态机 + Finalize**：多跳转换可以组合；终止事件和最终 usage 由 finalize 负责。
4. **loss policy**：`allow` / `safe` / `strict`，把"是否允许有损"交给调用方。

不照搬：

- 以 Chat DTO 作为 pivot 会让厂商字段泄漏进"中立"类型；
- 每个协议对都手写转换器，维护成本高；
- 没有 routing/auth/HTTP，不能直接作为网关。

XIR 的取舍：用 semantic hub 降低转换器数量；用 `Loss` 保留 relaykit 的诊断思想；用 `StreamDecoder/Encoder + Finalize` 保留流式状态机；不把 DTO 作为公共契约。

### 17.3 概念映射

| magpie / relaykit 概念 | XIR 对应 | 说明 |
| --- | --- | --- |
| `Part` / content block | `xir.Part` | 文本、图片、文件、工具、推理、拒绝、引用、搜索 |
| `Sealed` | `xir.Sealed` | 协议原生不透明块，逐字节保留 |
| `Request` 的协议原生字段 | `Request.Extensions[protocol]` | 未建模字段按协议保存 |
| `Usage` | `pkg/usage.Usage` | 统一计数 + 来源 + 原始 usage |
| `Event` | `xir.StreamEvent` | 流式事件 |
| `diagnostics` | `xir.Loss` + `hook.Finding` | 转换损失 vs 检测发现分开 |
| `toolconv.Set` | `Request.Tools` + `ToolChoice` | 工具声明/选择统一 |
| `reasoning.Intent` | `Part.Reasoning` + `Params` | effort/budget/visibility |
| `convmeta` | `xir.ConvertOptions` | 模型、目标协议、策略、media resolver |
| `ResponseStreamState` | `xir/stream.State` | 每路流一个状态机 |

### 17.4 由参考实现推导出的 IR 不变量

1. **同协议 encode 必须能回到原始字节**（或至少语义等价且不丢未知字段）；否则直通路径不应经过 IR。
2. **Sealed 块只能由原协议编码器写出**；跨协议不得重写其内容。
3. **每个 Part 必须能定位到 wire span**；否则 finding/rewrite 无法给出证据。
4. **流式事件必须能独立处理**；不得要求先聚合整个响应。
5. **转换 loss 必须可序列化**；能进入 Plan、DB 摘要、审计结果。
6. **usage 合并必须是幂等的**；重复事件不能把计数翻倍。
7. **state refs 与普通 metadata 分开**；否则硬亲和会被错误地当成 cache hint。

### 17.5 需要 M01/M02 回答的问题

以下问题不在总体文档中拍死，留到模块文档：

1. `Node` 是否使用 arena/池？生命周期如何与请求绑定？
2. 字符串解码是 eager 还是 lazy？转义字符串的 span 映射规则？
3. `Part.Ref` 指向 `Node` 还是 `Span`？节点树被 patch 后如何保持一致？
4. `Extensions` 的 key 是协议枚举还是字符串？如何处理同一协议多版本？
5. `Loss` 的严重级别如何与 HTTP 状态码/客户端错误码映射？
6. 流式 `StreamEvent` 是否复用 `Part`，还是独立类型？
7. audit 投影是否保留现有 `pkg/observe` 的节点 ID 算法？如何做差分？
8. codec 是否共享 JSON schema 校验，还是每个协议独立？
9. 媒体 resolver 的接口和超时/大小限制？
10. 转换的 `unfit` 记忆存储位置和失效策略？

## 18. 术语表

| 术语 | 含义 |
| --- | --- |
| **XIR / Exchange IR** | Trajecta 的统一交换中间表示，包含 wire、semantic、audit 三种视图 |
| **Wire view** | 无损字节结构：`Document`、`Node`、`Span`、`Edit` |
| **Semantic view** | 类型化协议中立模型：`Request`、`Response`、`Part`、`Tool` |
| **Audit view** | 从 XIR 投影的审计结构：`Observation`、findings、ATIF |
| **Sealed** | 协议原生的不透明块，逐字节保留，只由原协议编码器写出 |
| **Loss** | 协议转换中无法承载或降级的字段，带严重级别和策略 |
| **Unfit** | 某个上游拒绝过的字段/请求形状，后续自动避开 |
| **Preset** | 预置的 provider 端点、认证、模型来源数据 |
| **Plan variant** | 同一 preset 下的 plan/region 端点变体（coding plan、区域） |
| **Channel** | 一个已配置的上游连接 + 凭据 + 端点集合 |
| **ChannelModel** | 渠道下的一个模型，含协议、wire name、启用状态 |
| **Candidate** | 路由计划中的一个可尝试目标 |
| **Mode** | 执行模式：native、local_responses、convert |
| **Tier** | 渠道优先级层级；`strict` 下同 mode 内严格优先 |
| **`priority_mode`** | 解释 `priority` 的方式：`compat`（现状，成本优先、priority 同分兜底）或 `strict`（严格 tier） |
| **Soft affinity** | 为 prompt cache 尽量粘住同一上游，可失败转移 |
| **Hard affinity** | 服务端状态要求必须粘住同一上游，不可静默切换 |
| **Minimal cassette** | 只含 raw HTTP + 最小 meta 的 V3 文件；不带路由/usage/timeline event |
| **Wire sidecar** | opt-in 保存 mask/convert 后上游字节的附加文件，默认关闭 |
| **Hot lane** | 请求内联的轻量执行通道 |
| **Audit lane** | 请求后/按需的完整分析通道 |
| **Hook** | 挂在 IR 工作点上的检测/改写能力 |
| **Point** | Hook 声明的工作点，如 `request.tool_call.arguments` |
| **Finding** | Hook 产出的可审计发现，带证据路径 |
