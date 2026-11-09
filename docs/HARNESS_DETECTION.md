# 客户端 Harness 识别

Trajecta 记录的是 HTTP 交换本身，一个 `POST /v1/messages` 既可能来自 Claude Code，也可能来自一次 curl 或单测。为了在 trace 列表里把「Claude Code 发出的请求」和「测试套件发出的请求」分开，录制路径会从请求本身推断客户端 harness（`pkg/harness`），并把结论写成 trace 标签。

识别是**只读旁路**：它不修改请求头、请求体或发往上游的字节，也不改变响应；结果只进入 cassette prelude、trace 索引与 Monitor 展示。它也不是安全边界——所有厂商都允许用户自定义 User-Agent 或 header（`ANTHROPIC_CUSTOM_HEADERS`、`GEMINI_CLI_CUSTOM_HEADERS`、`LITELLM_USER_AGENT`、`CODEX_INTERNAL_ORIGINATOR_OVERRIDE`），因此标签描述的是「这段流量看起来是谁」，而不是「证明是谁」。

## 输入与判定顺序

识别**只读请求头和 URL，绝不读 body**。harness 身份是客户端建连时冻结的事实，写在它自己发出的头里；读 body 既慢（要猜提示词在哪个位置）、又容易被用户对话里粘贴的提示词冒充，所以这条路不走。三个输入位置：

1. **User-Agent 的产品 token 与版本**（`parseUserAgent` 解析）：`/` 前的第一段是产品（如 `deepseek-harness`、`codex_exec`），版本是紧随其后的数字段（唯一用到正则的地方，见下）。无 `/` 的空格形式（`codex-cli 0.153.4`）与裸 token（`pi`）同样解析。
2. **产品私有 header**：`originator`、`x-app`、`anthropic-beta`、`x-codex-*`、`x-opencode-*`、`Copilot-Integration-Id`、`Editor-Plugin-Version`、`X-Msh-*`、`x-cursor-client-version` 等具名头、`x-continue-session-id`、`HTTP-Referer`、`X-Title` 等——每个产品只认它自己写的头；只有 `x-codex-*`、`x-opencode-*` 用整段前缀扫描（`X-Msh-*` 先精确匹配 `x-msh-platform`，再前缀扫描兜底），其余都是具名头：按头名精确查找，值按大小写不敏感的相等或包含比较（`x-app` 相等、`anthropic-beta` / `editor-plugin-version` / `http-referer` 包含）。
3. **URL path/query**：如 Gemini 的 `:generateContent` / `:streamGenerateContent`、Responses 的 `/responses`。

匹配是**纯字符串运算，不是正则**：产品 token 要求精确相等或以 `-`/`_` / `.` 分隔续接（`codex-tui_rs` 认 `codex-tui`，但 `continuedev` 不认 `continue`）；header 值用大小写不敏感的相等/包含；私有头命名空间用前缀扫描（`x-codex-*`、`x-msh-*`）。唯一的正则在 `signals.go` 的 `versionRe`（`^v?[0-9]+(\.[0-9]+)+([-+.][0-9A-Za-z.-]+)?$`），只用来判断 UA 里的版本段长什么样、值是什么。

规则按「具体 → 泛化」排序，命中第一条 high confidence 规则即停止；否则取已匹配规则中 confidence 最高者。任何一条证据只记录**信号名字**（如 `user-agent:claude-cli`、`header:originator:codex_exec`），不记录 header 值。

## 置信度

| 置信度 | 含义 |
| --- | --- |
| `high` | 产品级 UA token、或只有该产品写的 header。网关规则（LiteLLM/Portkey/OpenRouter）也是 high：网关背后的 agent 不可见，但网关自己的 UA token/私有头唯一指向网关 |
| `medium` | 多条头信号互相印证、但单看任一条都不足以命名产品（如 Copilot 的 `VScode-SessionId` + `Editor-Version` 组合） |
| `low` | 预留：无产品信号的 SDK fallback 目前不返回 ID，所以还没有规则产生 low |

识别结果为空也是正常答案：手写 curl、内部合成请求（如 `/v1/models` 聚合响应）或 header 被网关剥离的请求没有可识别客户端。

## 已识别的产品

下表列出当前 `pkg/harness/catalog.go` 的规则。产品信号来自各 harness 自己发布的代码。

| `client_harness` | 显示名 | 类型 | 主要信号 |
| --- | --- | --- | --- |
| `claude-code` | Claude Code | `agent-cli` | UA `claude-cli/` 或 `claude-code/`；`anthropic-beta` 含 `claude-code`（同时带 `x-app: cli` 时额外记一条证据，不单独定类） |
| `codex-cli` | Codex CLI | `agent-cli` | `originator: codex_cli_rs` / `codex-tui` / `codex-tui_rs` / `codex_exec`（非交互 `codex exec` 模式）；UA `codex_cli_rs/` / `codex-cli/` / `codex-tui/` / `codex_exec/`；任意 `x-codex-*` header |
| `codex-vscode` | Codex (VS Code) | `ide-extension` | `originator: codex_vscode` |
| `codex-app` | Codex App | `agent-app` | `originator: codex_atlas` / `codex_chatgpt_desktop` |
| `qwen-code` | Qwen Code | `agent-cli` | UA `QwenCode/` 或 `qwen-code/`（优先于 Claude Code，见「已知误判」） |
| `gemini-cli` | Gemini CLI | `agent-cli` | UA `GeminiCLI/`（含 `GeminiCLI-<client>/`）或 `gemini-cli/` |
| `gemini-code-assist` | Gemini Code Assist | `ide-extension` | UA `CloudCodeVSCode/` 或 `proxy_client=geminicli` |
| `opencode` | OpenCode | `agent-cli` | UA `opencode/`；任意 `x-opencode-*` header |
| `deepseek-harness` | DeepSeek Harness | `agent-cli` | UA `deepseek-harness/<version> (+https://github.com/deepseek-ai/deepseek-harness)`（dsh 的 attribution 只发这一条 UA，不发私有 header） |
| `factory-droid` | Factory Droid | `agent-cli` | UA `factory-cli/<version>` |
| `kimi-code` | Kimi Code | `agent-cli` | UA `KimiCLI/<version>`（旧 Python kimi-cli）、`kimi-cli/<version>` 或 `kimi-code-cli/<version>`；`X-Msh-Platform`（任意非空值，旧 `kimi_cli` 与新 `kimi_code_cli` 都发）或任意 `X-Msh-*` 设备指纹头 |
| `pi` | Pi | `agent-cli` | UA 产品 token 精确等于 `pi`：详细形态 `pi/<version> (<platform>; <runtime>/<ver>; <arch>)`，或短形态裸 `pi` |
| `cursor` | Cursor | `ide-extension` | `x-cursor-client-version` / `x-cursor-checksum` / `x-cursor-config-version` header 或 UA `cursor/` |
| `windsurf` | Windsurf | `ide-extension` | UA `Windsurf/`、`Codeium/`；`x-codeium-request-id` / `x-codeium-client-version` |
| `github-copilot` | GitHub Copilot Chat | `ide-extension` | `Copilot-Integration-Id`、`Editor-Plugin-Version: copilot-chat/...`；`VScode-SessionId` + `Editor-Version` |
| `continue` | Continue | `ide-extension` | UA `Continue/`；`x-continue-session-id` |
| `cline` | Cline | `ide-extension` | UA `Cline/`；`HTTP-Referer: https://cline.bot`；`X-Title: Cline` + `X-CLIENT-TYPE`；`originator: cline` + `session_id`（Codex 账号透传） |
| `roo-code` | Roo Code | `ide-extension` | UA `RooCode/`；`HTTP-Referer: .../RooVetGit/Roo-Cline`；`X-Title: Roo Code` + `X-CLIENT-TYPE` |
| `kilo-code` | Kilo Code | `ide-extension` | UA `Kilo-Code/` / `kilo-code/`（少数发产品 UA 的构建） |
| `aider` | Aider | `agent-cli` | UA `aider/`；实际流量通常经 LiteLLM，因此更常见的是 `litellm` 标签 |
| `crush` | Crush | `agent-cli` | UA `Charm-Crush/` 或 `crush/` |
| `goose` | Goose | `agent-cli` | UA `goose/` |
| `amp` | Amp | `agent-cli` | UA `amp/`（精确匹配）或 `amp-cli/` |
| `openhands` | OpenHands | `agent-app` | UA `OpenHands/` |
| `zed` | Zed | `ide-extension` | UA `Zed/`；`x-zed-version` / `x-zed-client` |
| `litellm` | LiteLLM | `gateway` | UA `litellm/`；`x-litellm-call-id` / `x-litellm-model-id` |
| `portkey` | Portkey | `gateway` | `x-portkey-trace-id` / `x-portkey-config` |
| `openrouter` | OpenRouter | `gateway` | `x-openrouter-title` / `x-openrouter-categories` / `x-openrouter-app-visibility` |

普通 SDK 调用不会被当成 agent harness，但会保留 SDK 标签：SDK 家族先由 UA product（如 `OpenAI/Python`、`Anthropic/JS`）决定；UA 没有厂商 token、但带有 `x-stainless-*` 或已识别出语言时，再按协议头与路径兜底（`anthropic-version` 或 `/messages` → Anthropic，`/chat/completions`、`/responses`、`/embeddings` → OpenAI）。语言取 UA 后缀、`x-stainless-lang` 或 `x-goog-api-client`（`gl-python/...` 等），`x-stainless-*` 不是必需条件，因此 `OpenAI/Python` 得到 `sdk:openai-python`、`Anthropic/JS` 得到 `sdk:anthropic-typescript`；经 Google 生成客户端得到 `sdk:google-genai-python`，UA 含 `ai-sdk-provider-utils/` 的 Vercel AI SDK 得到 `sdk:vercel-ai-sdk`。这些 trace 的 `client_harness` 为空，只有标签。

## 标签（tags）

识别结果同时生成一组稳定标签，排序去重后写入 trace：

| 标签 | 含义 |
| --- | --- |
| `harness:<id>` | 命中的产品（与 `client_harness` 相同） |
| `kind:<kind>` | `agent-cli` / `ide-extension` / `agent-app` / `gateway` |
| `sdk:<family>` | 请求明显由某个 SDK 发出，如 `sdk:openai-python`、`sdk:anthropic-typescript`、`sdk:vercel-ai-sdk` |
| `capability:streaming` | Accept 为 SSE，或 Gemini 流式 path/query（`:streamGenerateContent`、`alt=sse`） |
| `capability:responses-api` | URL path 含 `/responses` |

能力标签只从请求头与 URL 推断——body 里的 tools/reasoning/cache 等形状不再读，所以这些旧标签（`capability:tool-use` 等）从此不再产生。标签是辅助信息而非协议证明：头上证明不了，就不打。

## 落点

一次请求完成录制时，`internal/recorder` 在写 cassette 前调用 `harness.Detect`，结论写入四处：

1. **cassette meta**（`pkg/recordfile.MetaData`）：`client_harness`、`client_harness_name`、`client_harness_version`、`client_harness_kind`、`client_harness_confidence`、`client_harness_tags`。raw `.http` 因此可以单独回答「这是谁发的」，replay 不受影响。
2. **cassette prelude 事件**：一条 `# event: {"type":"harness.detected", ...}`，attributes 含 `harness`、`name`、`version`、`kind`、`confidence`、`tags`、`evidence`。这是持久、可读、可回放的记录。
3. **application DB 的 `logs` 行**：上述六个字段的索引列（`client_harness`、`client_harness_name`、`client_harness_version`、`client_harness_kind`、`client_harness_confidence`、`client_harness_tags_json`），由 Postgres migration `20261105120000_add_log_client_harness` 与 SQLite 启动 schema 建立。列不加索引：唯一的谓词是列表过滤，`recorded_at` 已有索引负责收窄窗口，`logs` 是最热的写入表，多一个索引是纯写放大。
4. **API 与 UI**：`GET /api/traces` 的每一行返回这六个字段并支持 `?harness=<id>`（大小写不敏感精确匹配）；trace 详情的 `header.meta` 带同一组字段；`list_traces` MCP 工具暴露同样的过滤参数。Monitor 在请求列表显示可点击的 harness 标签（点击即按该 harness 过滤），详情页显示能力/SDK 标签。

## 边界与已知误判

- **只识别客户端可见的交换。** 本地 Responses runtime 的内部上游 `/v1/chat/completions`（`exchange_kind=model`）不参与识别：它的 header 由本进程生成，识别它会把内部调用误标成客户端。列表里的 child exchange 因此没有 harness 标签。
- **Qwen Code 会冒充 Claude Code。** Qwen Code 在非 `*.anthropic.com` base URL 上发送 `claude-cli/<version>` + `x-app: cli`；不读 body 后无法再用其系统提示词区分，这类流量会被标成 `claude-code`。只有发自有 UA（`QwenCode/`）的流量才标为 `qwen-code`。
- **无产品头的 harness 不可识别。** Kilo Code、Goose 等默认构建只发 Anthropic/SDK 头、不发产品 UA 时，流量无标签（空结果是正常答案）。机会性补充：发 `Kilo-Code/` UA 的构建仍可识别。
- **pi 会借用 Kimi 的身份。** pi（以及 omp 等 fork）在 `kimi-coding` provider 上把 UA 伪装成 `KimiCLI/<version>` 以通过 Kimi 的用量策略，这类流量会被标成 `kimi-code`，而不是 `pi`——外观即标签，伪装本身就是流量写明的事实。
- **历史 cassette 不回填。** 本次改动之前录制的 trace 没有分类字段；它们不会因为重新索引而获得标签（prelude 里没有这些字段）。如需识别历史流量，需要重新录制。
- **网关会改写 UA。** LiteLLM 等网关会把 UA 改写成自己的产品 token；此类 trace 会得到 `litellm` 网关标签，客户端 harness 不可见。
- **标签不是鉴权事实。** 可用于筛选、审计来源与排障，不能用于准入控制。

## 扩展方式

新增一个 harness：在 `pkg/harness/catalog.go` 按「具体 → 泛化」的顺序加一条 `rule`（只用头与 URL 信号），在 `detect_test.go` 补至少一个真实请求头形状的用例。不要为了让识别命中而修改录制或转发路径，不要把 header 值写进 `Evidence`，不要重新引入 body 扫描。

## 测试

- `pkg/harness/detect_test.go`：各产品 header/path 指纹、body 无关性（提示词不参与判定）、SDK-only 标签、未知请求、产品 token 边界（`continuedev` 不是 `continue`）、空格形式 UA（`codex-cli 0.153.4`）、Qwen 伪装的已知落点、规则顺序契约、证据不含原文。
- `internal/recorder/recorder_test.go`：meta 字段与 `harness.detected` 事件写入、model child 跳过、SDK-only 不写事件。
- `internal/store` / `internal/monitor`：字段 round-trip、`?harness=` 过滤、列表 JSON 暴露。
