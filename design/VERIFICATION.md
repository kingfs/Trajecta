# 集成验证记录

## 2026-11-09

环境：Go 1.26.9；`GOCACHE=/tmp/trajecta-gocache`、`GOMODCACHE=/tmp/trajecta-gomodcache`；离线测试。

| 检查 | 命令 | 结果 |
| --- | --- | --- |
| 构建 | `go build ./cmd/server ./cmd/trajecta` | 通过 |
| 全量测试 | `go test ./...` | 通过 |
| 格式 | `gofmt -l internal/gateway internal/audit internal/router internal/upstream internal/proxy internal/monitor` | 无输出 |
| 静态检查 | `go vet ./internal/gateway ./internal/audit ./internal/router ./internal/upstream ./internal/proxy` | 通过 |
| Race：gateway/audit/router/upstream | `go test -race ./internal/gateway ./internal/audit ./internal/router ./internal/upstream` | 通过 |
| Race：proxy | `go test -race ./internal/proxy` | 通过（约 154s） |
| Race：monitor | `go test -race ./internal/monitor` | 通过（约 81s） |
| 前端构建 | `vite build`（临时链接主 worktree 的 `node_modules`） | 通过 |
| 前端单测 | `node --test "tests-unit/**/*.test.js"` | 243 通过 / 0 失败 |

覆盖的关键路径：

- 请求：alias、stream-options、opt-in conversion、secret mask、`Body{Client,Record,Wire}`；
- 响应：`ResponseBody`、`ResponsePipeline`、`ResponseTransform`、usage、restore、cassette 写入；
- 路由：compat/strict priority、`plan_engine=route`（opt-in）、sticky、per-protocol endpoints；
- 审计：cassette → XIR → audit、Chat SSE 装配、`detections`、单 trace ATIF；
- 录制：minimal cassette + 完整 header 入 DB、minimal timeline fallback。

已知未覆盖/未完成：

- 跨协议流式转换；
- OAuth driver 的实际网络流程；
- per-model 多协议 wire name；
- `plan_engine=route` 尚未设为默认。

## 2026-11-10 代码问题修复

| 修复 | 验证 |
| --- | --- |
| `UsageSniffer` nil source/count 安全 | gateway 单测 + race |
| Qwen/GLM Anthropic coding-plan 预设 | preset endpoint/base URL/API 测试 |
| PATCH 渠道校验已有 coding-plan key | `TestChannelPatchValidatesExistingCodingPlanKey` |
| minimal cassette raw/detail 共享时间线 fallback | timeline 单测 |
| plan 元数据收敛为单表 | `TestPlanPresetMetadataMatchesPresets` |
| preset `no_list` 提示 | provider-presets API 测试 + UI build/单测 |

验证命令：

- `go build ./cmd/server ./cmd/trajecta`
- `go test ./...`
- `go vet ./internal/gateway ./internal/monitor ./internal/upstream ./pkg/upstream`
- `go test -race ./internal/gateway ./internal/upstream ./pkg/upstream`
- `go test -race ./internal/monitor`
- 前端 `vite build` + 243 个单元测试

## 2026-11-10 核心功能收尾验收

| 项 | 结果 |
| --- | --- |
| per-model protocols / wire name / UI | 通过（schema/store/API/channel/router/proxy/UI + 单测） |
| gateway 单轨化 | 通过（Body/Transform/Pipeline/alias/stream options/conversion headers/canonical usage） |
| upstream 单轨化 | 通过（catalog 自动补齐、auth header、普通 URL 委托 pkg；Azure/Vertex/tokenize 保留） |
| findings 统一 | 通过（analyzer findings + XIR detections 合并，`source=xir` 无明文） |
| 流式转换评估 | 完成，2.0 保持 non-goal；12 个协议对显式拒绝测试 |
| 旧路径清理 | gateway/proxy 别名和未使用 helper 已删除 |
| CLI 测试数据竞争 | 修复（测试 root 不再写包级 runtime） |

验证矩阵：

- `go build ./cmd/server ./cmd/trajecta`：通过
- `go vet ./...`：通过
- `go test -race ./...`：通过（exit 0，无 DATA RACE）
- `golangci-lint`（gateway/upstream/channel/pkg/upstream）：0 issues
- 前端 `vite build`：通过
- 前端 `node --test tests-unit/**/*.test.js`：243 passed

保留的行为决策/非目标：

- `plan_engine=route` 仍 opt-in，未做默认化；
- Azure/Vertex/tokenize 的 URL 逻辑仍在 internal；
- `protocolConversion` 的决策/引擎选择仍在 proxy；
- 跨协议流式转换未实现；
- OAuth 未实现，保持官方 API key；
- 浏览器 E2E 未纳入本次自动化。

- `/data/gateway-test` 真实环境验证与问题记录见 [`GATEWAY_TEST_REPORT.md`](./GATEWAY_TEST_REPORT.md)。

## 2026-11-10 第二轮：a26655b 全面环境回归与修复

环境：`/data/gateway-test`（proxy `:28080` / monitor `:28081` / Postgres 17 `:25432`）。
第一轮基线：`main` @ `a26655b`；修复分支：`fix/gateway-test-round2` @ `d9797bc` + `a637fe1`。
完整报告：[`GATEWAY_TEST_REPORT.md`](./GATEWAY_TEST_REPORT.md)。

### 第一轮发现（修复前）

| 检查 | 结果 |
| --- | --- |
| `go build` / `go vet` / `gofmt` / `go test ./...` | 通过（69 包 ok） |
| `go test -race ./...` | 第一次 1 个路由器成本测试的非确定性失败；第二次通过 |
| 前端 build / 243 单测 / typecheck | 通过；typecheck 不覆盖 JSX |
| Playwright（HEAD） | **失败**：194 passed / 20 failed，全部由 `/channels` 的 `setForm is not defined` 引起 |
| DB 迁移（fresh/upgrade/down/SQLite/atlas.sum） | 全部通过 |
| 渠道/preset、per-model、四协议代理、路由、转换、审计 | 核心通过；发现 UI 阻断、Gemini URL/wire、Vertex Express auth、`upstream_model`、alias+wire、protocols 局部 PATCH、decision trace 字段、epsilon 语义问题 |

### 修复后复测

| 检查 | 结果 |
| --- | --- |
| `go build ./cmd/server ./cmd/trajecta` | 通过（镜像 `trajecta:fix-round2`） |
| `go vet ./...` / `gofmt -l` | 通过 / 无输出 |
| `go test -count=1 ./...` | 通过（69 包 ok，0 FAIL） |
| `go test -race -count=1 ./...` | **通过**（69 包 ok，无 DATA RACE） |
| 前端 `vite build` / 243 单测 | 通过；dist 已随修复更新 |
| Playwright（修复后） | **216 passed / 0 failed / 4 skipped**（含新增的官方/自定义创建用例） |
| 真实浏览器 UI | 列表、路由设置、新建弹窗、详情、协议编辑/刷新、模型广场全部通过；官方/自定义渠道创建均成功并清理 |
| API 矩阵（第一轮 64 项） | **64/64 通过** |
| API 矩阵（第二批 35 项） | **35/35 通过** |
| 转换（legacy/xir） | messages/gemini→chat、原生优先、流式拒绝、fatal 400、alias+wire 通过 |
| 渠道创建 | 官方目录点击即填 endpoint（URL 仅高级覆盖）；自定义 Base URL/key/协议/模型可手填；`POST /api/channels` 的 `models` 同事务创建 |
| DB 迁移 | 未改 schema；上一轮 DB 报告继续有效 |

保留：压缩请求体的转换错误仍返回 502（fail-closed 的 streaming 推断先于转换判定），见报告 5.3；不阻塞交付。
UI typecheck 仍不覆盖 JSX，建议把 Playwright mock 套件纳入 CI。

判定：第一轮发现的问题已修复并复测；核心 2.0 能力达到预期。
