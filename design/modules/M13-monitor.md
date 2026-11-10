# M13 Monitor 与模型广场

## 0. 头部

| 字段 | 值 |
| --- | --- |
| 模块 ID | M13 |
| 模块名 | Monitor 与模型广场 |
| 归属 | `internal/monitor`、`web/monitor-ui` |
| 状态 | 部分落地 |
| 依赖模块 | M07、M08、M11 |
| 最后更新 | 2026-11-10 |

## 1. 已落地

- 模型广场：每模型聚合渠道、`interfaces`、顶层 `protocols`、Argus facts；
- 渠道创建：preset 显示名、notes、`no_list` 提示、内置目录、base URL 自动填充；
- 渠道 endpoints：per-protocol `endpoints` 编辑；
- 路由设置：`priority_mode`、`plan_engine` 热切换；
- 审计视图：`/api/traces/<id>/xir`、`/trajectory` 的数据消费；
- 嵌入式 `ui/dist` 已随源码构建提交。

## 2. 待做

- 真实浏览器 Playwright E2E（当前只做 build + 单元测试）；
- coding plan 渠道创建后的“添加模型”引导；
- 模型广场/渠道详情对 plan 与 endpoint 的更完整展示；
- 旧 event 读取路径与新时间线 fallback 的统一。

## 3. 测试

- 前端 `node --test tests-unit/**/*.test.js`：243 通过；
- `vite build` 通过；
- monitor Go 测试覆盖 provider-presets、catalog、channel、trace XIR/ATIF。
