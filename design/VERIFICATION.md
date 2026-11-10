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
