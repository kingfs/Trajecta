# 模块设计索引

> 模块文档按依赖顺序推进。状态为 `Draft` 的文档可以讨论，`Accepted` 后才能进入实现。  
> 文件名在文档创建前用代码标记，不生成空文件；创建后改为链接。

| ID | 模块 | 计划文档 | 归属 | 依赖 | 状态 |
| --- | --- | --- | --- | --- | --- |
| M01 | Exchange IR 基础 | `M01-xir-foundation.md` | `pkg/xir`、`pkg/xir/wire` | 无 | 待创建 |
| M02 | 协议 Codec | `M02-xir-codecs.md` | `pkg/xir/codec/*` | M01、M06 | 待创建 |
| M03 | Hook 框架 | `M03-xir-hooks.md` | `pkg/xir/hook` | M01、M02 | 待创建 |
| M04 | 协议转换与流式 | `M04-xir-convert-stream.md` | `pkg/xir/convert`、`pkg/xir/stream` | M02、M03 | 待创建 |
| M05 | 检测与敏感信息 | `M05-detect.md` | `pkg/detect`、`pkg/secretmask` | M01 | 待创建 |
| M06 | Usage 与成本 | `M06-usage.md` | `pkg/usage` | 无 | 待创建 |
| M07 | 上游目录与认证 | `M07-upstream.md` | `pkg/upstream` | M01、M06 | 待创建 |
| M08 | 路由引擎 | `M08-route.md` | `pkg/route` | M01、M07 | 待创建 |
| M09 | Harness 与 Session | `M09-identity.md` | `pkg/harness`、`pkg/session` | M01 | 待创建 |
| M10 | Gateway 流水线 | `M10-gateway.md` | `internal/gateway` | M02–M09 | 待创建 |
| M11 | 渠道控制面 | `M11-channel-control-plane.md` | `internal/channel`、`internal/store` | M07、M08 | 待创建 |
| M12 | 审计与证据 | `M12-audit.md` | `internal/audit`、`pkg/xir/audit` | M01–M06 | 待创建 |
| M13 | Monitor 与模型广场 | `M13-monitor.md` | `internal/monitor`、`web/monitor-ui` | M07、M08、M11 | 待创建 |
| M14 | 兼容与清理 | `M14-compat-cleanup.md` | 全仓库 | M10–M13 | 待创建 |

## 推进批次

### 批次 A：IR 地基

- M01、M06、M05；
- 目标：span parser、类型系统、usage、检测框架可用；不接主路径。

### 批次 B：协议能力

- M02、M03、M04；
- 目标：四种协议 decode/encode、Hook 计划、loss ledger；用 golden fixture 验证。

### 批次 C：控制面与选路

- M07、M08、M09；
- 目标：preset/auth、路由计划、会话识别；先用 fake store/HTTP client 验证。

### 批次 D：主路径接入

- M10、M11；
- 目标：gateway 流水线、Body、渠道/模型写路径；feature flag 下与旧路径差分。

### 批次 E：审计与产品面

- M12、M13、M14；
- 目标：审计重解析、ATIF、cassette 最小化、模型广场/渠道向导、旧路径清理。

## 文档创建顺序

1. M01：先把 IR 的 wire/schema 契约定死；
2. M06、M05：小包先落地，验证 `pkg` 边界；
3. M02、M03、M04：协议能力；
4. M07、M08、M09：控制面与选路；
5. M10、M11：主路径；
6. M12、M13、M14：审计、产品面、清理。

## 文档评审检查表

每个模块文档进入 `Accepted` 前，至少确认：

- [ ] `pkg` 公开 API 可被外部程序使用，且不依赖 `internal`；
- [ ] 失败语义明确（客户端错误/上游错误/配置错误/策略拒绝）；
- [ ] 字节是否变化、`Content-Length` 如何处理已说明；
- [ ] 热路径性能预算有数字；
- [ ] 测试包含离线 fixture 和差分测试；
- [ ] 迁移与回滚可执行；
- [ ] 与总体架构的决策不冲突，或已登记新决策。
