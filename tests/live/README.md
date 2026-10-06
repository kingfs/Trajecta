# Live 端到端测试（真实上游）

`tests/live` 是一套**需要真实上游凭据**的端到端测试，用来验证「本地构建的 Trajecta 镜像 + 真实 LLM 网关」这条完整链路：代理入口、录制落盘、索引入库、Monitor 只读/写面、MCP、观测分析、以及「录制 → 回放」保真。

默认**全部跳过**：只有显式设置 `TRAJECTA_LIVE_UPSTREAM=1` 才会执行，因此 `go test ./...` 在离线环境仍然是确定性的。

## 依赖

- Docker（构建并运行镜像）
- 一个 OpenAI 兼容 + Anthropic 兼容的上游网关（本仓库作者使用白泽网关）
- `go` 工具链、`curl`、`jq`

## 快速开始

```bash
# 1) 准备上游凭据
export TRAJECTA_LIVE_API_KEY=sk-...
export TRAJECTA_LIVE_CHAT_API_BASE=https://<gateway>/api/openai
export TRAJECTA_LIVE_RESPONSES_API_BASE=https://<gateway>/api/openai
export TRAJECTA_LIVE_MESSAGES_API_BASE=https://<gateway>/api/anthropic
export TRAJECTA_LIVE_MODEL=deepseek-flash

# 2) 构建镜像
docker build -t trajecta:local \
  --build-arg GOPROXY='https://goproxy.cn,direct' \
  --build-arg GOSUMDB=off .

# 3) 起栈 + 初始化凭据 + 跑测试
tests/live/run-live.sh
```

`run-live.sh` 会：`docker compose up -d` → 等端口 → `auth init-user` / `auth create-token` → 导出 `TRAJECTA_LIVE_PROXY_TOKEN` → 执行 `go test ./tests/live -v`。

## 环境变量

| 变量 | 必填 | 说明 |
|---|---|---|
| `TRAJECTA_LIVE_UPSTREAM` | 是 | 置 `1` 才运行；否则所有用例 `t.Skip` |
| `TRAJECTA_LIVE_API_KEY` | 是 | 上游 API key（写入配置时用 `$env:TRAJECTA_LIVE_API_KEY`） |
| `TRAJECTA_LIVE_CHAT_API_BASE` | 是 | Chat Completions 上游 base（需含 API 前缀） |
| `TRAJECTA_LIVE_RESPONSES_API_BASE` | 是 | Responses 上游 base |
| `TRAJECTA_LIVE_MESSAGES_API_BASE` | 是 | Anthropic Messages 上游 base |
| `TRAJECTA_LIVE_MODEL` | 否 | 默认 `deepseek-flash` |
| `TRAJECTA_LIVE_SERVER_URL` | 否 | 默认 `http://127.0.0.1:18080` |
| `TRAJECTA_LIVE_MONITOR_URL` | 否 | 默认 `http://127.0.0.1:18081` |
| `TRAJECTA_LIVE_PROXY_TOKEN` | 是 | `auth create-token` 生成的 API token |
| `TRAJECTA_LIVE_MONITOR_USER` / `_PASSWORD` | 是 | Monitor 登录账号 |
| `TRAJECTA_LIVE_TRACE_DIR` | 否 | 宿主机上的 cassette 目录（回放保真用例需要） |
| `TRAJECTA_LIVE_FAILOVER_URL` / `_TOKEN` / `_DIR` | 否 | 故障切换实例（`config/live-failover.yaml`，端口 18094/18095）：一个高优先级渠道指向不可达地址，另一个正常 |
| `TRAJECTA_LIVE_OUTAGE_URL` / `_TOKEN` / `_DIR` | 否 | 全渠道不可达实例（`config/live-outage.yaml`，端口 18096/18097），用于固定「全部候选失败」的错误契约 |
| `TRAJECTA_LIVE_CHAOS_URL` / `_TOKEN` | 否 | 混沌实例（`config/live-chaos.yaml`，端口 18092/18093）：默认是 `rate=1.0` 的 `delay: 2500ms` 规则，测 `action: "error"` 时把它改成 `action: "error"` + `status_code: 503` + `message: "chaos-injected-503"` |

## 用例地图

| 文件 | 覆盖 |
|---|---|
| `live_test.go` | 核心链路：chat / 流式 / 工具调用 / messages / models / count_tokens / 录制落盘 / Monitor 只读面与 trace 关联 / 负例 |
| `auth_mcp_test.go` | 代理鉴权、Monitor 登录与 token 生命周期、MCP 初始化/工具列表/工具调用 |
| `runtime_test.go` | 本地 Responses runtime：策略切换、信封、续写、函数调用与结果回灌、compact、本地流式、未实现 hosted tool 拒绝 |
| `routing_test.go` | 路由预演、路由汇总、会话归组、并发 8 路 |
| `replay_fidelity_test.go` | 真实录制的 cassette 经官方 `go-openai` SDK 回放，逐字段/逐字节比对 |
| `analysis_test.go` | 批量重分析任务链路（父任务 + 子任务终态） |
| `monitor_write_test.go` | Monitor 写面：模型别名 validate → create → 用别名打真实流量 → list → delete |
| `truncation_test.go` | 本地 runtime 的截断语义（当前以 `t.Skipf` 追踪 BUG-LIVE-2，修复后自动转为断言） |
| `fault_test.go` | 故障注入与路由韧性：候选失败自动切换、全渠道不可达的错误契约、chaos error 注入、开路渠道的 selectable 一致性（后两项分别以 `t.Skipf` 追踪 BUG-FAULT-1 / BUG-FAULT-3） |

## 注意

- 这些用例**会产生真实上游费用**（每次运行数十次模型调用，且 deepseek-flash 会消耗 reasoning token）。
- 代理入口 `/v1/*` 强制鉴权，所有请求都要带 `Authorization: Bearer $TRAJECTA_LIVE_PROXY_TOKEN`。
- Monitor `/api/*` 需要 JWT（`/api/auth/login`），MCP `/mcp` 需要 API token。
- 测试会切换 `routing.settings.responses_strategy`，`withResponsesStrategy` 会在用例结束时恢复原值。
- `run-live.sh` 使用的配置文件在 `tests/live/config/`，其中 key 走 `$env:` 展开，**不要把真实 key 写进仓库**。
- `fault_test.go` 的用例在缺少对应的 `*_URL` 时整条 `t.Skip`，所以只跑主栈（`run-live.sh`）不会受影响。要复现故障注入结论，按 `tests/live/config/live-{failover,outage,chaos}.yaml` 各起一个实例（端口 18094–18097、18092/18093），分别导出 `TRAJECTA_LIVE_*_URL` / `_TOKEN` / `_DIR`（token 用 `server auth create-token -c <该配置>` 生成，注意 CLI 也需要 `TRAJECTA_LIVE_FAILOVER_DIR` / `_OUTAGE_DIR` / `_CHAOS_DIR` 这几个 `$env:` 变量）。
- `live-outage.yaml` 的用例会在**约 30s 后**才拿到 502（重试预算，见 `internal/proxy/handler.go:90`），属预期行为。
