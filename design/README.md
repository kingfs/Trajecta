# Trajecta 2.0 重构设计

> 状态：Draft v0.1  
> 更新时间：2026-11-09  
> 范围：整体架构、包边界、核心流程、模块拆分  
> 事实源说明：`docs/` 只描述当前代码事实；`design/` 只描述目标设计，不声称已经实现。两者不混用。

## 1. 为什么单独放在 `design/`

`AGENTS.md` 规定 `docs/` 描述当前代码事实，计划、路线图和阶段设计不进入 `docs/`。本次重构需要一套可持续维护的设计文档，因此放在仓库根目录的 `design/`：

- `docs/`：当前行为、约束、操作指南。
- `design/`：目标架构、模块契约、迁移方案、决策记录。
- 模块实现完成后，当前行为同步进 `docs/`，`design/` 中对应条目标记为已落地。

## 2. 阅读顺序

1. [总体架构](./01-overall-architecture.md)：定位、原则、包边界、核心流程、模块路线。
2. [数据放置与证据模型](./02-data-placement.md)：文件/数据库/内存的边界，cassette 最小化，路由信息为什么不进文件。
3. [模块文档模板](./TEMPLATE.md)：每个模块设计必须包含的章节和验收口径。
4. [模块索引](./modules/README.md)：模块拆分、依赖顺序、当前状态。

后续每个模块单独成文，按依赖顺序推进，不一次性展开所有细节。

## 3. 重构模板

每个模块设计文档必须遵循 [`TEMPLATE.md`](./TEMPLATE.md)。最低要求：

- 模块目标与非目标；
- 当前实现与约束；
- 对外 `pkg` API（Go 签名或等价契约）；
- `internal` 集成方式；
- 数据模型、配置、协议/格式变化；
- 算法、不变量、失败语义；
- 性能、安全、可观测性；
- 测试、基准、验收标准；
- 迁移、兼容、回滚；
- 开放问题。

模板不是行政格式。没有写清楚"失败时怎么办"、"字节是否变化"、"是否在热路径"的模块文档，不应进入实现。

## 4. 模块地图

模块按依赖顺序排列。前一模块的公开契约稳定后，后一模块才能进入实现；可以并行写文档，但不并行改同一层契约。

| ID | 模块 | 归属 | 主要产物 | 状态 |
| --- | --- | --- | --- | --- |
| M01 | Exchange IR 基础 | `pkg/xir/wire`、`pkg/xir` | span JSON、Document、Node、Edit、Request/Response schema | Landed (P1) |
| M02 | 协议 Codec | `pkg/xir/codec/*` | chat/responses/messages/gemini 的 decode/encode | Landed (P2) |
| M03 | Hook 框架 | `pkg/xir/hook` | Point、Hook、Event、Result、Plan、Registry | Landed (P2) |
| M04 | 协议转换与流式 | `pkg/xir/convert`、`pkg/xir/stream` | loss ledger、非流式转换器、SSE 帧/装配 | Landed (P2) |
| M05 | 检测与敏感信息 | `pkg/detect`、`pkg/secretmask` | Scanner、Match、DFA 适配、opaque span | Landed (P1) |
| M06 | Usage 与成本 | `pkg/usage` | 统一 Usage、合并、归一化（无 Cost API） | Landed (P1) |
| M07 | 上游目录与认证 | `pkg/upstream` | preset、endpoint、auth spec、模型发现 | Landed (P2) |
| M08 | 路由引擎 | `pkg/route` | PlanResult、候选、优先级、亲和、失败分类 | Landed (P2) |
| M09 | Harness 与 Session | `pkg/harness`、`pkg/session` | 客户端识别、会话信号、fingerprint | Landed (P2) |
| M10 | Gateway 流水线 | `internal/gateway` | Body 已落地；阶段拆分待做 | 部分 |
| M11 | 渠道控制面 | `internal/channel`、`internal/store` | 渠道/模型 CRUD、probe、配置事务、catalog API | 部分 |
| M12 | 审计与证据 | `internal/audit`、`pkg/xir/audit` | 审计投影已落地；重解析、findings、ATIF、cassette 最小化待做 | 进行中 |
| M13 | Monitor 与模型广场 | `internal/monitor`、`web/monitor-ui` | 渠道向导、模型广场、路由解释 | 待设计 |
| M14 | 兼容与清理 | 全仓库 | 旧包 facade、feature flag、删除死代码 | 待设计 |

## 5. 推进方式

每个模块按以下步骤推进：

1. **写模块文档**：按模板，明确 API、失败语义、性能预算和验收标准。
2. **评审契约**：优先评审 `pkg` 公开 API 和 `internal` 依赖方向；契约不稳不动代码。
3. **实现 + 测试**：模块自身的单元测试、离线 fixture、benchmark 必须随代码提交。
4. **接入验证**：在 feature flag 下接入主路径，跑差分测试（新旧字节/决策对比）。
5. **更新文档**：`design/` 标记状态；行为变化同步进 `docs/` 和 `AGENTS.md`。
6. **清理**：旧路径确认无调用后删除，不长期双轨。

## 6. 命名与约定

### 6.1 Exchange IR

统一称 **Exchange IR（XIR）**，包名 `pkg/xir`。不使用 "IR" 单独指代，避免和现有 `pkg/observe` 的 Observation IR 混淆。

### 6.2 `pkg` 与 `internal` 的判定

一个能力放进 `pkg/` 当且仅当：

- 不依赖 `internal/*`；
- 不读全局配置、不打开数据库、不启动 HTTP server；
- 输入输出是显式参数，网络/时钟/存储通过接口或函数参数注入；
- 可以被仓库外的 Go 程序直接 import；
- 有独立的、离线的单元测试。

否则放 `internal/`。

### 6.3 依赖方向

```text
pkg/*  →  只能依赖 pkg/*
internal/*  →  可以依赖 pkg/* 和 internal/*
cmd/*  →  依赖 internal/*
```

新增 `pkg` 依赖必须无环；用 CI 检查 `pkg` 不 import `internal`。

### 6.4 事件与错误

- `pkg` 返回结构化错误/诊断，不打印日志、不 panic（除编程错误）。
- 热路径的失败必须分类：客户端错误、上游错误、配置错误、转换错误、策略拒绝。
- 任何会改变字节的路径必须说明 `Content-Length`、流式 flush、cassette 记录的处理。

## 7. 决策摘要

| ID | 决策 | 状态 |
| --- | --- | --- |
| D-001 | 一个 XIR，wire + semantic + audit 三视图 | 已定 |
| D-002 | 算法与协议能力放 `pkg`，Trajecta 业务编排放 `internal` | 已定 |
| D-003 | 原生直通优先于协议转换；模式优先于渠道优先级 | 已定 |
| D-004 | `priority_mode` 决定 `priority` 的解释：`compat`（现状）或 `strict`（严格 tier） | 已定，默认按安装类型迁移 |
| D-005 | 会话亲和分软亲和与硬亲和 | 已定 |
| D-006 | 原始客户端字节是事实源；上游 wire 字节是可选证据 | 已定 |
| D-007 | Hook 先做编译期 Go 包 + registry，动态插件后置 | 已定 |
| D-008 | 流式协议转换后置，接口先定 | 已定 |
| D-009 | Coding plan 先支持 plan-key 端点；OAuth 只预留接口，后续实现 | 已定 |
| D-010 | 不引入 Cassette V4；V3 最小 meta，默认不写 event | 已定 |
| D-011 | 路由/usage/harness/转换摘要进 DB，不进 cassette | 已定 |
| D-012 | 只为最终 exchange 创建 cassette；重试只记 DB | 已定 |
| D-013 | 上游 wire 字节默认不存，opt-in sidecar | 已定 |

详细论证见 [总体架构](./01-overall-architecture.md) 第 14 节。

## 8. 文档清单

- [总体架构](./01-overall-architecture.md)
- [数据放置与证据模型](./02-data-placement.md)
- [模块模板](./TEMPLATE.md)
- [模块索引](./modules/README.md)
- 模块文档：待逐个创建（M01–M14）。

## 9. 维护规则

- 设计文档与代码冲突时，先判断是文档过期还是实现偏离；不要默默让两者不一致。
- 任何跨模块契约变化，必须同时更新本文件第 4 节的状态和相关模块文档。
- 决策变更写进第 7 节，并注明替代了哪条旧决策。
- 不在 `design/` 放临时讨论、聊天记录和未整理的草稿。
