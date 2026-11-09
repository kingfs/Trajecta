# Trajecta 2.0 实施进度

> 分支：`refactor/trajecta-2.0`  
> worktree：`worktrees/trajecta-2.0`  
> 基于：`main`（设计文档已提交）  
> 更新：2026-11-09

## 目标

在保持 `priority_mode=compat` 默认兼容的前提下，落地 `pkg/xir`、`pkg/usage`、`pkg/detect`、`pkg/upstream`、`pkg/route`、`pkg/session` 等能力包；把 harness 迁到 `pkg`；接入关键 core 路径；完成单元、差分和集成测试；分阶段合并 `main`。

## 阶段与状态

| 阶段 | 内容 | 状态 |
| --- | --- | --- |
| P0 | 设计文档、worktree、基线测试 | 完成 |
| P1 | `pkg/usage`、`pkg/xir` 类型、`pkg/xir/wire` | 完成 |
| P2 | 四协议 codec、hook、convert | 完成（待提交） |
| P3 | `pkg/detect`、`pkg/upstream`、`pkg/route`、`pkg/session` | 完成（待提交） |
| P4 | `pkg/harness` 迁移、`priority_mode=compat` 接入 | 完成（待提交） |
| P5 | `internal/gateway`/proxy 接入、集成测试 | 待开始 |
| P6 | 合并 main、清理与文档同步 | 待开始 |

## 已落地

- `pkg/usage`：统一 Usage、非零合并、Normalize、测试。
- `pkg/xir`：Protocol/Operation、Request/Response/Part/Tool/Params/StateRefs/Loss/Codec/Stream 类型、测试。
- `pkg/xir/wire`：span JSON parser、Node/Member/Span、Get/Index/Decode、Edit/Apply、重叠/长度校验、测试。
- `design/`：总体架构、数据放置、模板、模块索引。

## 已完成的能力包

| 能力 | 位置 |
| --- | --- |
| 统一 Usage | `pkg/usage` |
| XIR 类型 / span parser | `pkg/xir`、`pkg/xir/wire` |
| 四协议 codec | `pkg/xir/codec/{chat,responses,messages,gemini}` |
| Hook 框架 | `pkg/xir/hook` |
| 转换 hub | `pkg/xir/convert` |
| 检测框架 | `pkg/detect` |
| 上游 preset/auth | `pkg/upstream` |
| 路由决策 | `pkg/route` |
| 会话信号 | `pkg/session` |
| Harness 分类 | `pkg/harness`（从 internal 迁出） |

## 已接入 core

- `internal/router`：`router.selection.priority_mode` 默认 `compat`，支持 `strict`；decision trace 记录 `priority_mode`/`selected_tier`；兼容性测试覆盖。
- `internal/router` sticky：改用 `pkg/session` 解析会话信号，保持原有 header 优先级。
- `internal/monitor`：`/api/settings/routing` 增加 `priority_mode`，PATCH 后热更新 router。

## 待办

- `internal/proxy`/`internal/gateway`：用 XIR envelope/wire 替换重复解析（先 feature flag），并保持字节/cassette/replay 一致。
- 渠道控制面：把 `pkg/upstream` 的 preset/endpoint/auth 接到创建渠道流程；多协议模型与模型广场。
- 审计路径：`pkg/xir/audit` 投影、findings、ATIF；cassette 最小 meta（按 design/02）。
- 模块文档：M01–M14 按模板补齐，至少先补 M01/M02/M03/M06/M07/M08。
- 集成测试：四协议 codec 往返、router 兼容、proxy 字节保真、cassette/replay、race。
- 每阶段结束：`gofmt`、`go test`、`go vet`，提交并合并 `main`。

## 验证命令

```bash
export GOCACHE=/tmp/trajecta-gocache
go test ./pkg/usage ./pkg/xir/... ./pkg/detect ./pkg/upstream ./pkg/route ./pkg/session
go test ./internal/router ./internal/proxy ./pkg/recordfile ./pkg/replay
```
