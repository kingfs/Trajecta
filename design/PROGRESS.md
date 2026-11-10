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
| P2 | 四协议 codec、hook、convert | 完成，已合并 main |
| P3 | `pkg/detect`、`pkg/upstream`、`pkg/route`、`pkg/session` | 完成，已合并 main |
| P4 | `pkg/harness` 迁移、`priority_mode=compat` 接入 | 完成，已合并 main |
| P5 | core 接入、集成测试 | 部分完成：XIR envelope、preset base URL、priority_mode/session/harness 已接入；gateway 拆分未开始 |
| P6 | 合并 main、清理与文档同步 | 进行中：M01–M09 文档已落地，旧包清理未开始 |

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
| SSE 帧/装配 | `pkg/xir/stream` |
| 审计投影 | `pkg/xir/audit` |
| secretmask 检测适配 | `pkg/detect/secretmask` |
| 检测框架 | `pkg/detect`、`pkg/detect/secretmask` |
| 上游 preset/auth | `pkg/upstream` |
| 路由决策 | `pkg/route` |
| 会话信号 | `pkg/session` |
| Harness 分类 | `pkg/harness`（从 internal 迁出） |

## 已接入 core

- `internal/router`：路由特征读取改用 `pkg/xir.ParseEnvelope`，一次 span 扫描替代原来的 `map[string]any` 解析；model/stream/tools/response_format/max tokens/Gemini 路径语义保持。
- `internal/audit`：cassette 按需解析为 XIR 并调用 `pkg/xir/audit`；`GET /api/traces/<id>/xir` 已暴露投影；Chat SSE 响应会装配为完整响应，其他流式协议只解码 request 并返回 warning；节点文本经 `pkg/detect` 返回不含值的 `detections`。
- `internal/gateway`：`Body{Client,Wire}`、`RequestTransform`、`ResponseBody{Upstream,Client}`、`UsageSniffer`、`ResponsePipeline`、`ResponseTransform` 已落地；请求侧 alias → conversion → masking、响应侧 Restorer → Sniffer、转换/header 重写固定在 gateway。
- `internal/monitor`：模型广场条目新增 `protocols`（可用渠道 interfaces 的并集），用于协议 badge；`priority_mode` UI 已加入。
- `internal/recorder`：`trace.minimal_cassette` prelude 只保留 raw exchange + 解析/布局元数据，DB 仍用完整 header；仓库默认配置开启，旧配置保持完整；proxy e2e 验证配置能到达最终 prelude。
- `internal/monitor`：模型广场卡片展示协议 badge；minimal prelude 的 trace 详情仍从 raw 生成 request/response 时间线；新增单 trace ATIF 导出 `GET /api/traces/<id>/trajectory`；`GET /api/catalog/providers`，暴露 `pkg/upstream` 的 provider preset（endpoints/auth/plans/models，无密钥）；旧 `GET /api/provider-presets` 也带 endpoints/protocols，创建表单自动填 base URL，并可通过“浏览内置目录”选择 preset。前端 `ui/dist` 已重新构建。
- `internal/upstream`：渠道 `capabilities.endpoints` 支持 per-protocol base URL；proxy 按 path/conversion 协议选择 endpoint；渠道 API 顶层 `endpoints` 与 Monitor 高级选项可编辑，产物已重建。
- `internal/router`：新增 opt-in `router.selection.plan_engine=route`，候选过滤仍由 router 完成，最终排序交给 `pkg/route.Plan`；默认 `legacy` 保持兼容；`PATCH /api/settings/routing` 与 Monitor 设置面板可热切换。
- `internal/proxy`：model alias 改写改用 `xir.RewriteModel`，stream chat 的 `stream_options.include_usage` 注入改用 `xir.EnsureStreamIncludeUsage`；两者只 patch 目标 span，保留键顺序、空白、未知字段和数字字面量。
- `internal/router`：`router.selection.priority_mode` 默认 `compat`，支持 `strict`；decision trace 记录 `priority_mode`/`selected_tier`；兼容性测试覆盖。
- `internal/router` sticky：改用 `pkg/session` 解析会话信号，保持原有 header 优先级。
- `internal/monitor`：`/api/settings/routing` 增加 `priority_mode`，PATCH 后热更新 router；UI 增加在 `RoutingSettingsPanel` 中的选择器。
- `internal/monitor`：创建渠道时，若只给 preset + key 而未填 base URL，从 `pkg/upstream` 选择匹配协议的默认端点；显式 base URL 优先。
- `internal/proxy`：新增 opt-in `protocol_conversion_engine=xir`，请求/响应转换可走 `pkg/xir/convert`；默认仍是 legacy，避免行为变化。
- `pkg/xir`：12 对最小跨协议请求转换矩阵通过；wire parser fuzz 约 99 万次执行无失败。
- 全仓 `go test ./...` 通过；XIR stream/audit、detect/secretmask 适配测试通过（stream 覆盖率 88.6%，audit 82.9%）。

## 模块文档

M01–M09 与 M12（audit 投影部分）已按模板落地，索引见 `design/modules/README.md`。M10、M11、M13、M14 仍待创建。

## 最近提交

- `323422f` feat(pkg): add XIR and reusable capability packages
- `5284105` refactor(harness): move client classifier to pkg/harness
- `bf5a281` feat(routing): add priority_mode compat/strict and session resolver
- `12ac40d` feat(channels): fill base URL from a known provider preset
- `c009b15` perf(routing): read routing features through the XIR envelope
- `4386d54` feat(monitor): expose priority mode and harden XIR tests
- `0b55533` docs: add M01-M09 module designs and align contracts
- `6485507` feat(proxy): add opt-in XIR conversion engine
- `a9ffce1` feat(xir): add SSE primitives, audit projection and secretmask adapter
- `888d257` docs: record XIR stream/audit phase

## 验证结果

- `go test ./...`：通过（含 internal/proxy、monitor、store、router、pkg/xir/*）。
- `go build ./cmd/server ./cmd/trajecta`：通过。
- wire parser fuzz：约 99 万次执行，无失败。
- 12 对最小跨协议请求转换矩阵：通过。
- `priority_mode` compat/strict 与 native-beats-convert：通过。

## 待办

- `internal/proxy`/`internal/gateway`：继续把 XIR wire/Hook 接到请求变换与响应流；当前只接入了路由 envelope。
- 渠道控制面完整化：多协议 `channel_models`、模型广场、OAuth driver（接口已预留）。
- 审计路径：`pkg/xir/audit` 投影、findings、ATIF；cassette 最小 meta（按 design/02）。
- 流式：`StreamDecoder/StreamEncoder` 实现与流式协议转换（当前明确拒绝）。
- 集成测试：proxy 字节保真、cassette/replay 差分、race、benchmark 门槛。
- 清理：`pkg/llm`/`pkg/protocolconvert` 的迁移评估；不急于删除。

## 验证命令

```bash
export GOCACHE=/tmp/trajecta-gocache
go test ./pkg/usage ./pkg/xir/... ./pkg/detect ./pkg/upstream ./pkg/route ./pkg/session
go test ./internal/router ./internal/proxy ./pkg/recordfile ./pkg/replay
```

## 集成验证

最近一次构建、全量测试、race、vet 与前端单测记录见 [VERIFICATION.md](./VERIFICATION.md)。

当前事实基线中的 2.0 能力包与接入点已同步到 `docs/IMPLEMENTATION_STATUS.md`。

- `pkg/upstream`：新增 `qwen_coding` / `qwen_coding_intl` / `zai_coding` / `zhipu_coding` 官方 coding-plan 预设；Qwen 为 `sk-sp-` API key + 专用 endpoint，GLM 为各平台 API key + coding endpoint。
