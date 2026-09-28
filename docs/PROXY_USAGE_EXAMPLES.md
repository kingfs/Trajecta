# 代理使用示例

本文给出把 OpenAI SDK、OpenAI Responses / Codex、Claude Code 等客户端接到本地 Trajecta 代理的最小可用示例。所有示例指向同一台代理：proxy 监听 `8080`，Monitor 监听 `8081`（见 `config/config.yaml`）。

代理 API 需要个人 token，认证方式是 `Authorization: Bearer <token>`；代理入口只校验这个 header，不校验 `x-api-key`。token 可在 Monitor 的 `Tokens` 页面创建，也可用 CLI 创建，完整 token 只在创建时输出一次。

## 前置条件：本地启动

`config/config.yaml` 是 Postgres-first 配置，`database.dsn` 为空，直接 `task run` 无法启动。二选一：

```bash
# 方式一：导出 Postgres DSN，继续使用 config/config.yaml
export TRAJECTA_DATABASE_DSN='postgres://user:pass@host:5432/trajecta?sslmode=disable'
task run

# 方式二：改用本地 SQLite 配置（CONFIG 是 Taskfile 变量；直接运行二进制时用 -c 指定配置）
CONFIG=config/examples/local-sqlite.yaml task run
```

首次使用需要先有用户，再签发 token（`auth create-token` 只会为已存在的启用用户签发）：

```bash
go run ./cmd/server -c config/examples/local-sqlite.yaml auth init-user --username admin --password 'change-me'
go run ./cmd/server -c config/examples/local-sqlite.yaml auth create-token --username admin --name local-dev
```

后续示例统一使用这两个 shell 变量；如果 `server.port` 不是 `8080`，请同步修改它们以及 SDK 的 `base_url`：

```bash
export TRAJECTA_URL=http://localhost:8080
export TRAJECTA_TOKEN=llmtl_xxx
```

## 入口与端口

代理先归一化客户端请求路径，再决定路由、录制与转发：

| 客户端入口 | 归一化 endpoint | 用途 |
| --- | --- | --- |
| `/v1/chat/completions` | `/v1/chat/completions` | OpenAI-compatible Chat Completions |
| `/v1/responses`、`/responses` | `/v1/responses` | OpenAI Responses / Codex |
| `/v1/messages`、`/anthropic/messages`、`/anthropic/v1/messages` | `/v1/messages` | Anthropic Messages / Claude Code |
| `/anthropic/messages/count_tokens`、`/anthropic/v1/messages/count_tokens` | `/v1/messages/count_tokens` | Anthropic token 计数 |
| `/v1/tokenize` | `/tokenize` | vLLM 分词 |
| `/v1/detokenize` | `/detokenize` | vLLM 反分词 |
| `/v1/models` | `/v1/models` | 聚合的 OpenAI-compatible 模型列表 |
| `/v1/models/{id}` | `/v1/models/{id}` | 单模型详情（从聚合列表合成，不转发上游） |
| `/api/show`（Ollama） | `/api/show` | Ollama 模型详情（从聚合列表合成，不转发上游） |
| `/v1/embeddings` | `/v1/embeddings` | 只被分类，不可路由：`pkg/llm.AdapterFor` 没有 embeddings adapter，路由判定的 `supportsPath` 因此永远返回 false |

入口归一化、各协议族的 endpoint 覆盖与 provider 能力要求的完整矩阵见 [协议族与上游 Provider](./PROTOCOLS_AND_PROVIDERS.md) 与 [协议参考](./protocol-reference/implemented-protocols.md)。

代理是协议感知的透传与录制，`/v1/messages` 只会路由到 Anthropic Messages 上游，不会退化成 `/v1/chat/completions`；跨协议翻译的边界与非目标见 [协议族与上游 Provider](./PROTOCOLS_AND_PROVIDERS.md)。除 `/v1/responses` 的本地 Responses runtime 外没有其他特例，见下文。

请求里的 `model` 必须由至少一个已启用渠道提供，否则请求被拒绝；缺失模型回退策略见 [路由、渠道与凭据](./ROUTING_AND_CREDENTIALS.md)。

## 查询模型

```bash
curl -H "Authorization: Bearer ${TRAJECTA_TOKEN}" \
  "${TRAJECTA_URL}/v1/models" | jq
```

## OpenAI-Compatible Chat Completions

非流式（`gpt-4o-mini` 取自 `config/examples/local-sqlite.yaml`，请替换成你的上游实际提供的模型）：

```bash
curl "${TRAJECTA_URL}/v1/chat/completions" \
  -H "Authorization: Bearer ${TRAJECTA_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"1+1=? Just answer with a number."}],"max_completion_tokens":64}'
```

流式：

```bash
curl -N "${TRAJECTA_URL}/v1/chat/completions" \
  -H "Authorization: Bearer ${TRAJECTA_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"讲一个20字笑话"}],"max_completion_tokens":128,"stream":true,"stream_options":{"include_usage":true}}'
```

对 `/v1/chat/completions` 的流式请求，代理会在缺失或未开启时补上 `stream_options.include_usage=true`，使流式 trace 也能记录 usage。

## OpenAI SDK

SDK 通常把 `api_key` 放到 `Authorization: Bearer <api_key>`，因此把个人 token 填进 `api_key`，并把 `base_url` 指向代理的 `/v1`：

```python
import os
from openai import OpenAI

client = OpenAI(
    base_url=os.getenv("TRAJECTA_BASE_URL", "http://localhost:8080/v1"),
    api_key=os.environ["TRAJECTA_TOKEN"],
)

resp = client.chat.completions.create(
    model="gpt-4o-mini",
    messages=[{"role": "user", "content": "ping"}],
)
print(resp.choices[0].message.content)
```

## OpenAI Responses / Codex

`/v1/responses` 和别名 `/responses` 都被无条件接受。Codex 的 `base_url` 指向 `http://localhost:8080/v1`（请求 `/v1/responses`）或根地址 `http://localhost:8080`（请求 `/responses`）都命中同一入口：

```bash
curl "${TRAJECTA_URL}/v1/responses" \
  -H "Authorization: Bearer ${TRAJECTA_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o-mini","input":"ping"}'
```

同一个 endpoint 按**模型**在两个执行方式之间二选一，没有 YAML 开关：

1. 原生 Responses 上游：模型声明了 Responses 能力时，请求透传给该上游的 `/v1/responses`。
2. 本地 Responses runtime：模型只声明 chat completions 能力时，由本地 runtime 把请求编排成对上游 `/v1/chat/completions` 的调用；该执行方式始终可用。

要整体关闭本地翻译，在 Monitor 的 Routing 设置里把 `responses_strategy` 设为 `native_only`。按模型的能力判定、`responses_strategy` 的完整取值与配置来源见 [本地 Responses Runtime](./RESPONSES_RUNTIME.md) 与 [路由、渠道与凭据](./ROUTING_AND_CREDENTIALS.md)。

## Claude Code / Anthropic Messages

Claude Code 走 Anthropic Messages 协议，base URL 指向代理根地址 `http://localhost:8080` 即可，它会自行请求 `/v1/messages`；`/anthropic/messages` 与 `/anthropic/v1/messages` 也都归一化到 `/v1/messages`。认证仍使用 Trajecta 的个人 token：代理入口只接受 `Authorization: Bearer <token>`。

手工 curl 示例（模型名取自 `config/examples/anthropic.yaml`）：

```bash
curl "${TRAJECTA_URL}/v1/messages" \
  -H "Authorization: Bearer ${TRAJECTA_TOKEN}" \
  -H "Content-Type: application/json" \
  -H "anthropic-version: 2023-06-01" \
  -d '{"model":"claude-3-5-sonnet-latest","max_tokens":64,"messages":[{"role":"user","content":"ping"}]}'
```

Trajecta 不会把 Anthropic Messages 请求转换成 OpenAI-compatible 请求，`/v1/messages` 必须路由到支持 Anthropic Messages 的上游或兼容网关；base URL 不要重复追加 `/v1`。

## 常见排查

不同上游即使标称 OpenAI-compatible，也可能只支持部分 endpoint 或有更严格的消息规则。常见排查顺序：

1. 确认 client base URL 是否重复了 `/v1`：SDK 的 `base_url` 到 `/v1` 为止，Claude Code 用根地址。
2. 确认请求的 endpoint 是否被目标上游支持，以及 `model` 是否由已启用渠道提供。
3. 在 Monitor 的 `Routing` / `Events` / trace detail 查看选中路由与 provider 错误，见 [Monitor 使用指南](./MONITOR_GUIDE.md)。
4. 用 [协议参考](./protocol-reference/README.md) 判断是否协议族不匹配。

相关文档：接入与凭据管理见 [路由、渠道与凭据](./ROUTING_AND_CREDENTIALS.md)，协议族与 provider preset 见 [协议族与上游 Provider](./PROTOCOLS_AND_PROVIDERS.md)。
