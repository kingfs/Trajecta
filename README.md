<div align="center">

# Trajecta

**The Agent Trajectory & Traffic Engine**

把真实的 LLM API 流量，变成可回放、可审计、可 review 的测试资产。

[![Go CI](https://github.com/kingfs/Trajecta/actions/workflows/ci.yml/badge.svg)](https://github.com/kingfs/Trajecta/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/kingfs/Trajecta?color=blue)](https://github.com/kingfs/Trajecta/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](./LICENSE)
[![Go Version](https://img.shields.io/github/go-mod/go-version/kingfs/Trajecta)](./go.mod)

[English](./README_EN.md) | 简体中文

<img src="./images/monitor-overview.png" alt="Trajecta Monitor 概览" width="900">

</div>

## 为什么需要 Trajecta

给 LLM 应用写测试很难：真实调用慢、要花钱、结果不稳定；手写 mock 会随上游演进而漂移，而且恰好丢掉真正会出问题的部分——流式分片、工具调用、token usage、状态码和耗时。

Trajecta 是一个**本地优先的录制 / 回放代理**。开发时把 SDK 或 CLI 的流量指向它，请求照常转发到真实上游，同时把完整 HTTP 交换原样落盘为 `.http` cassette；之后在单元测试里挂上 `pkg/replay` 离线回放，测试不再触网、不再计费、结果确定。

- **录制真实字节**：请求 / 响应原文、SSE 分片、状态码、耗时，一字不改地保存
- **回放零成本**：`replay.NewTransport()` 直接挂到任意 SDK 的 `http.Client` 上
- **文件可读可 diff**：cassette 是文本，可以 code review、可以手工修、可以随 PR 一起提交
- **看得见的轨迹**：Monitor 把每条 trace 解析成统一 timeline——消息、工具调用、token、路由决策
- **事实源清晰**：cassette 永远是事实源，应用数据库（生产 Postgres / 本地 SQLite）只是派生索引

## 核心能力

| 能力 | 说明 |
| --- | --- |
| 透明代理 | 记录并转发 OpenAI Chat Completions、OpenAI Responses、Anthropic Messages、Google GenAI、Vertex 等协议族的请求 |
| 离线回放 | `pkg/replay` 实现 `http.RoundTripper`，单元测试无需网络与 API key |
| 流式保真 | 逐片保存 SSE 数据流；回放按原始分片顺序交付字节，不做限速或时序重放 |
| 录制格式 | `LLM_PROXY_V3` 紧凑 prelude + 原始 HTTP 字节；继续兼容读取 V2 记录 |
| Monitor UI | Go embed 的 React 界面：请求列表、会话聚合、trace 详情、路由决策、审计、模型与服务商管理 |
| 轨迹解析 | 把 cassette 解析成 Observation IR，产出危险工具调用、敏感数据等 findings |
| 会话轨迹导出 | 把一次 session 的累积历史导出为 ATIF v1.8 JSONL |
| Agent 接口 | MCP server 暴露 21 个查询 / 重分析工具，让 agent 直接读轨迹而不是抓 HTML |
| 多上游路由 | 按渠道、权重、p2c 策略与粘性路由选上游，支持模型别名与 per-model 能力覆盖 |
| 存储分层 | 生产用 Postgres（含 checked-in SQL 迁移），本地用 SQLite，cassette 始终是事实源 |

## 快速开始

### 方式一：Docker Compose（推荐）

需要 Docker 与 Docker Compose。

```bash
git clone https://github.com/kingfs/Trajecta.git && cd Trajecta
cp .env.example .env          # 修改 POSTGRES_PASSWORD
docker compose up -d
docker compose exec trajecta /app/bin/trajecta \
  -c /app/config/config.yaml auth init-user --username admin --password 'change-me-123'
```

- Monitor：<http://localhost:8081>，用上面的用户名密码登录
- Proxy：<http://localhost:8080/v1>，把 SDK 的 `base_url` 指向它

登录后在 `Providers` 页面配置上游地址与 API key，在 `Tokens` 页面生成个人 token：SDK 的 `api_key` 填这个 token，请求就会被代理并录制。宿主机端口由 `.env` 里的 `TRAJECTA_HOST_SERVER_PORT` / `TRAJECTA_HOST_MONITOR_PORT` 控制。

### 方式二：从源码运行

需要 Go 1.25+，[Task](https://taskfile.dev) 可选但推荐。

```bash
task build && task run                  # 默认 Postgres-first 配置：config/config.yaml
CONFIG=config/examples/local-sqlite.yaml task run   # 只想用本地 SQLite
```

不想用 Task 也可以直接跑：

```bash
export TRAJECTA_DATABASE_DSN='postgres://trajecta:trajecta@localhost:5432/trajecta?sslmode=disable'
go run ./cmd/server -c config/config.yaml
```

### 方式三：从 llm-tracelab 升级

项目已改名为 Trajecta（`v2.0.0`）。已有部署运行迁移脚本即可（默认 dry-run，只报告不写入）：

```bash
scripts/migrate-to-trajecta.sh --env-file .env --output-dir ./data/traces
scripts/migrate-to-trajecta.sh --apply --env-file .env --output-dir ./data/traces
```

脚本会改写 `.env` 中的 `LLM_TRACELAB_*` key、重命名本地 SQLite 及其 `-wal`/`-shm`、统计 cassette 魔数版本，并列出需要人工确认的项。破坏性变化与兼容策略见 [CHANGELOG](./CHANGELOG.md)。

## 5 分钟：录制一次调用，然后在测试里回放

**1. 让一次真实调用经过代理**（token 来自 Monitor 的 `Tokens` 页面）：

```bash
export TRAJECTA_TOKEN=llmtl_xxx
curl -s http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer ${TRAJECTA_TOKEN}" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"Hello"}],"stream":true}'
```

**2. 代理把这次交换写成 cassette**，路径是 `<output_dir>/<上游 host>/<model>/<yyyy>/<mm>/<dd>/<timestamp>.http`：

```text
# trajecta/v3
# meta: {"version":"LLM_PROXY_V3","meta":{"request_id":"…","time":"…","model":"gpt-4o-mini",
#        "provider":"openai","operation":"chat.completions","endpoint":"/v1/chat/completions",
#        "method":"POST","status_code":200,"duration_ms":842,"ttft_ms":210,"routing_policy":"p2c"},
#        "layout":{"req_header_len":231,"req_body_len":96,"res_header_len":120,
#        "res_body_len":512,"is_stream":true},
#        "usage":{"prompt_tokens":18,"completion_tokens":42,"total_tokens":60}}
# event: {"type":"request","time":"…","method":"POST","url":"/v1/chat/completions","body_bytes":96}
# event: {"type":"response","time":"…","status_code":200,"is_stream":true,"body_bytes":512}
# event: {"type":"llm.output_text.delta","time":"…","is_stream":true,"message":"Hello"}
# event: {"type":"llm.usage","time":"…","attributes":{"total_tokens":60}}

POST /v1/chat/completions HTTP/1.1
Content-Type: application/json
Authorization: Bearer sk-***

{"model":"gpt-4o-mini","messages":[{"role":"user","content":"Hello"}],"stream":true}

HTTP/1.1 200 OK
Content-Type: text/event-stream

data: {"choices":[{"delta":{"content":"Hello"}}]}
…
data: [DONE]
```

（示例为节选，`…` 表示省略。默认 `debug.mask_key: true` 会在落盘前把请求头中的 `Authorization` / `api-key` / `x-api-key` / `x-goog-api-key` 值替换成 `fake-key-logging`；其余请求头与请求、响应正文原样保存，这正是回放能够保真的原因。）

**3. 在单元测试里回放这个文件**，不需要网络，也不需要 API key：

```go
import (
    "context"
    "net/http"
    "testing"

    "github.com/kingfs/Trajecta/pkg/replay"
    "github.com/sashabaranov/go-openai"
)

func TestChat(t *testing.T) {
    cfg := openai.DefaultConfig("fake-key")
    cfg.BaseURL = "http://localhost/v1" // URL 不重要，Transport 会拦截
    cfg.HTTPClient = &http.Client{
        Transport: replay.NewTransport("testdata/chat.http"),
    }
    client := openai.NewClientWithConfig(cfg)

    resp, err := client.CreateChatCompletion(context.Background(), openai.ChatCompletionRequest{
        Model:    "gpt-4o-mini",
        Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "Hello"}},
    })
    if err != nil {
        t.Fatal(err)
    }
    _ = resp
}
```

任何走 `http.Client` 的 SDK 都能这样接：把 `Transport` 换掉即可，业务代码一行不用改。

## 支持的上游

| 协议族 | provider preset | 支持级别 |
| --- | --- | --- |
| OpenAI-compatible | `openai`、`openrouter`、`fireworks`、`together`、`groq`、`xai`、`github`、`deepseek`、`moonshot`、`cerebras`、`perplexity` 等 | verified / compatible |
| OpenAI Responses | 原生 `/v1/responses` 上游直通，或由本地 runtime 翻译为 chat completions | verified |
| Anthropic Messages | `anthropic` | verified |
| Google GenAI | `google_genai`、`google`、`gemini` | verified |
| Vertex AI | `vertex`（`vertex_express` / `vertex_project_location`） | verified |
| Azure OpenAI | `azure`（v1 与 deployment 两种路由） | verified |
| vLLM | `vllm` | verified |

完整 preset 清单（registry 25 个键，去重后 20 个独立 provider）、能力声明规则、路由 profile 组合与协议差异见 [docs/PROTOCOLS_AND_PROVIDERS.md](./docs/PROTOCOLS_AND_PROVIDERS.md)。代理是**协议感知的直通 + 录制/解析**，不在转发热路径上做跨协议翻译；唯一的例外是 `/v1/responses` 可以由本地 runtime 承接。

## 架构一览

```text
   SDK / CLI / Codex / Claude Code
                │  OpenAI · Anthropic · Google · Vertex
                ▼
┌──────────────────────────────────────────────────────────┐
│ trajecta serve                                           │
│                                                          │
│   proxy ──▶ router ──▶ 上游 provider（渠道 / 凭据 / 限流）  │
│     │         │                                          │
│     │         └──▶ 路由决策、粘性路由、失败转移             │
│     ▼                                                    │
│   recorder ──▶ .http cassette  ← 回放与详情的事实源        │
│     │                                                    │
│     ├──▶ 语义解析 ──▶ Observation IR ──▶ findings        │
│     └──▶ 索引 ──▶ 应用数据库（生产 Postgres / 本地 SQLite）│
└──────────────────────────────────────────────────────────┘
       │                                   │
       ▼                                   ▼
  Monitor UI（go:embed）              MCP server
                                           │
                                           ▼
                              pkg/replay ──▶ 单元测试离线回放
```

| 目录 | 职责 |
| --- | --- |
| `cmd/server` | CLI 入口与全部子命令（serve、db、auth、provider、models、audit、analyze、doctor…） |
| `internal/proxy` | 反向代理、协议入口归一化、流式响应拦截 |
| `internal/recorder` | cassette 录制与落盘 |
| `pkg/recordfile` | 录制格式解析与写入（V3 写入，V2 / 旧 magic 兼容读取） |
| `pkg/replay` | 单元测试用的回放 `http.RoundTripper` |
| `pkg/llm` | 跨厂商请求 / 响应 / stream transcript / usage 归一化 |
| `internal/store` | 应用数据库与 metadata 索引 |
| `internal/upstream`、`internal/channel` | 上游解析、渠道配置、能力与探测 |
| `internal/responses` | 本地 Responses runtime、hosted tools、审计查询 |
| `internal/observe`、`internal/analyzer` | 语义解析管道与审计 findings |
| `internal/monitor` | Monitor HTTP API 与内嵌 UI |
| `internal/mcpserver` | MCP 工具面 |
| `internal/trajectory` | ATIF v1.8 会话轨迹重建与导出 |

数据流、并发一致性与事实源边界的完整说明见 [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md)。

## 文档

| 我想…… | 看这里 |
| --- | --- |
| 了解现在实现了什么、没实现什么 | [当前实现状态](./docs/IMPLEMENTATION_STATUS.md) |
| 理解代码组织与数据流 | [架构与代码地图](./docs/ARCHITECTURE.md) |
| 把 SDK / CLI 接到代理上 | [代理使用示例](./docs/PROXY_USAGE_EXAMPLES.md) |
| 接入上游 provider、看协议矩阵 | [协议族与上游 Provider](./docs/PROTOCOLS_AND_PROVIDERS.md)、[协议参考](./docs/protocol-reference/README.md) |
| 管理渠道、模型、凭据与限流 | [路由、渠道与凭据](./docs/ROUTING_AND_CREDENTIALS.md) |
| 用 Codex 或本地 Responses runtime | [本地 Responses Runtime](./docs/RESPONSES_RUNTIME.md) |
| 使用 Monitor 界面 | [Monitor 使用指南](./docs/MONITOR_GUIDE.md) |
| 让 agent 通过 MCP 查询轨迹 | [MCP 使用指南](./docs/MCP_GUIDE.md) |
| 部署、迁移与数据保留 | [存储与部署](./docs/STORAGE_AND_DEPLOYMENT.md) |
| 长期运行 Postgres 的调优与排障 | [PostgreSQL 运维手册](./docs/POSTGRES_OPERATIONS.md) |
| 参与开发、跑测试与 CI | [开发与测试](./docs/DEVELOPMENT.md)、[CONTRIBUTING](./CONTRIBUTING.md) |
| 看每个版本改了什么 | [CHANGELOG](./CHANGELOG.md) |
| 面向 AI agent 的项目约定 | [AGENTS.md](./AGENTS.md) |

完整文档索引见 [docs/README.md](./docs/README.md)。

## 界面

<div align="center">
<img src="./images/monitor-traces.png" alt="请求列表" width="46%">
<img src="./images/monitor-trace-detail.png" alt="trace 详情：timeline、协议、审计与性能" width="46%">
<br>
<img src="./images/monitor-providers.png" alt="服务商与渠道管理" width="70%">
</div>

trace 详情把一次交换拆成 Routing & Conversation、Protocol、Audit、Performance、Raw 五个视图；审计 findings、路由决策与原始协议都在同一页。

## 项目状态与边界

当前版本 `v2.0.0`，MIT 许可。明确不做的事：

- 公网多租户中转、计费 / 充值 / 订阅分发
- 转发热路径上的跨协议转换（`/v1/responses` 的本地 runtime 是唯一例外）
- hosted `file_search` / `code_interpreter` / `computer_use` 的真实执行 lifecycle 与容器级沙箱

这些边界与当前实现的完整对照见 [docs/IMPLEMENTATION_STATUS.md](./docs/IMPLEMENTATION_STATUS.md)。

## 参与贡献

欢迎 issue 与 PR。开始之前请读 [CONTRIBUTING.md](./CONTRIBUTING.md)：它说明了提交前要跑的验证命令、录制格式的兼容性要求，以及文档约定（`docs/` 只描述当前代码事实）。

```bash
task check:quick     # 格式检查、lint 与短测试
task check:full      # 追加全量测试、e2e、race 与完整构建
```

## License

[MIT](./LICENSE)
