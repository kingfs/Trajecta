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
