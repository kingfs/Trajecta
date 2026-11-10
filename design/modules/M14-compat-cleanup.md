# M14 兼容与清理

## 0. 头部

| 字段 | 值 |
| --- | --- |
| 模块 ID | M14 |
| 模块名 | 兼容与清理 |
| 归属 | 全仓库 |
| 状态 | 部分落地 |
| 依赖模块 | M10–M13 |
| 最后更新 | 2026-11-10 |

## 1. 已落地

- `priority_mode=compat` 默认；`strict` 可选；
- `plan_engine=legacy` 默认；`route` opt-in 且可热切换；
- `protocol_conversion_engine=legacy` 默认；`xir` opt-in；
- `trace.minimal_cassette` 仓库默认开启，旧配置保持完整 prelude；
- 旧 preset 矩阵与新 `pkg/upstream` catalog 桥接；
- gateway 请求/响应核心从 proxy 抽出，proxy 保留类型别名兼容测试；alias rewrite 原语已移入 gateway；catalog→运行时 preset 一致性测试与自动补齐防止注册表漂移；未来 pkg preset 自动获得运行时默认 spec。

## 2. 待做

- 评估 `plan_engine=route` 是否默认；
- 清理旧 event 读取/双轨路径；
- 删除不再使用的旧 preset/转换适配代码；
- M13 的浏览器 E2E 纳入验收；
- 持续更新 `docs/IMPLEMENTATION_STATUS.md` 的能力基线。

## 3. 不变量

- 默认配置必须保持历史行为；
- 所有新引擎/格式必须可回退；
- 旧 cassette V2/V3 读取兼容不变。
