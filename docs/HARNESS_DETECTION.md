# 客户端 Harness 识别

Trajecta 记录的是 HTTP 交换本身，一个 `POST /v1/messages` 既可能来自 Claude Code，也可能来自一次 curl 或单测。为了在 trace 列表里把「Claude Code 发出的请求」和「测试套件发出的请求」分开，录制路径会从请求本身推断客户端 harness（`internal/harness`），并把结论写成 trace 标签。

识别是**只读旁路**：它不修改请求头、请求体或发往上游的字节，也不改变响应；结果只进入 cassette prelude、trace 索引与 Monitor 展示。它也不是安全边界——所有厂商都允许用户自定义 User-Agent 或 header（`ANTHROPIC_CUSTOM_HEADERS`、`GEMINI_CLI_CUSTOM_HEADERS`、`LITELLM_USER_AGENT`、`CODEX_INTERNAL_ORIGINATOR_OVERRIDE`），因此标签描述的是「这段流量看起来是谁」，而不是「证明是谁」。

## 输入与判定顺序

识别分两阶段，头优先、body 兜底：

**阶段一（快速路径）**：只读请求头与 URL，不碰 body。`User-Agent` 的产品 token 与版本，以及少数产品私有 header（`originator`、`x-app`、`x-codex-*`、`x-opencode-*`、`copilot-integration-id`、`x-msh-*`、`x-portkey-*`、`x-openrouter-*` 等），加上 URL path/query（例如 Gemini 的 `:generateContent` / `:streamGenerateContent`）。命中第一条 **high confidence** 规则即返回，body 字节一次都不读——绝大多数 harness 都发产品级 UA 或私有 header，所以这条路径覆盖了几乎全部流量。唯一的例外是 Claude Code：Qwen Code 会伪装 `claude-cli` UA，阶段一命中的 `claude-code` 一律再经过阶段二的 Qwen 提示词核对才接受。

**阶段二（兜底）**：阶段一没有 high 命中时才扫描 body：系统提示词原句、工具名、协议字段。body 只扫描**头部 512 KiB + 尾部 256 KiB**（`bodyPrefixScan` / `bodySuffixScan`），标记匹配直接在字节窗口上做，不复制 body；其中提示词原句只搜**头部窗口**（系统提示词/`instructions`/`systemInstruction` 都在 body 开头，尾部是工具数组与最近对话——用户粘贴的 “You are Codex” 不在头部，就不能认领这段流量）。阶段二按「具体 → 泛化」排序跑完整规则表，取置信度最高者。

任何一条证据只记录**信号名字**（如 `prompt:you-are-claude-code`），不记录命中的原文、header 值或 prompt 内容。

## 置信度

| 置信度 | 含义 |
| --- | --- |
| `high` | 产品级 UA token、只有该产品写的 header，或独有系统提示词原句。网关规则（LiteLLM/Portkey/OpenRouter）也是 high：网关背后的 agent 不可见，但网关自己的 UA token/私有头唯一指向网关 |
| `medium` | 工具名组合、上游家族共享的提示词，或需要多条弱信号互相印证 |
| `low` | 预留：无产品信号的 SDK fallback 目前不返回 ID，所以还没有规则产生 low |

识别结果为空也是正常答案：手写 curl、内部合成请求（如 `/v1/models` 聚合响应）或 header 被网关剥离的请求没有可识别客户端。

## 已识别的产品

下表列出当前 `internal/harness/catalog.go` 的规则。产品信号来自各 harness 自己发布的代码；标「提示词」的规则只依赖请求体，因此 header 被剥离时仍可识别。

| `client_harness` | 显示名 | 类型 | 主要信号 |
| --- | --- | --- | --- |
| `claude-code` | Claude Code | `agent-cli` | UA `claude-cli/` 或 `claude-code/`；`x-app: cli` + `anthropic-beta` 含 `claude-code`；提示词 `You are Claude Code` |
| `codex-cli` | Codex CLI | `agent-cli` | `originator: codex_cli_rs` / `codex-tui` / `codex_exec`（非交互 `codex exec` 模式）；UA `codex_cli_rs/` / `codex_exec/`；任意 `x-codex-*` header；提示词 `You are a coding agent running in the Codex CLI` |
| `codex-vscode` | Codex (VS Code) | `ide-extension` | `originator: codex_vscode` |
| `codex-app` | Codex App | `agent-app` | `originator: codex_atlas` / `codex_chatgpt_desktop` |
| `qwen-code` | Qwen Code | `agent-cli` | UA `QwenCode/`；提示词 `You are Qwen Code`（优先于 Claude Code，见「已知误判」） |
| `gemini-cli` | Gemini CLI | `agent-cli` | UA `GeminiCLI/`（含 `GeminiCLI-<client>/`）；提示词 `You are Gemini CLI`；Gemini 工具集 |
| `gemini-code-assist` | Gemini Code Assist | `ide-extension` | UA `CloudCodeVSCode/` 或 `proxy_client=geminicli` |
| `opencode` | OpenCode | `agent-cli` | UA `opencode/`；任意 `x-opencode-*` header；提示词 `You are opencode` |
| `deepseek-harness` | DeepSeek Harness | `agent-cli` | UA `deepseek-harness/<version> (+https://github.com/deepseek-ai/deepseek-harness)`（dsh 的 attribution 只发这一条 UA，不发私有 header） |
| `factory-droid` | Factory Droid | `agent-cli` | UA `factory-cli/<version>` |
| `kimi-code` | Kimi Code | `agent-cli` | UA `KimiCLI/<version>`（旧 Python kimi-cli）或 `kimi-code-cli/<version>`；`X-Msh-Platform`（`kimi_cli` / `kimi_code_cli`）或任意 `X-Msh-*` 设备指纹头 |
| `pi` | Pi | `agent-cli` | UA 产品 token 精确等于 `pi`：详细形态 `pi/<version> (<platform>; <runtime>/<ver>; <arch>)`，或短形态裸 `pi` |
| `cursor` | Cursor | `ide-extension` | `x-cursor-*` header 或 UA `cursor/`；提示词 `You are Cursor`；Cursor 工具名组合 |
| `windsurf` | Windsurf | `ide-extension` | UA `Windsurf/`、`Codeium/`；`x-codeium-*` |
| `github-copilot` | GitHub Copilot Chat | `ide-extension` | `Copilot-Integration-Id`、`Editor-Plugin-Version: copilot-chat/...`；`VScode-SessionId` + `Editor-Version` |
| `continue` | Continue | `ide-extension` | UA `Continue/`；`x-continue-session-id`；提示词 `You are Continue` |
| `cline` | Cline | `ide-extension` | UA `Cline/`；`HTTP-Referer: https://cline.bot`；`X-Title: Cline` + `X-CLIENT-TYPE`；提示词 `You are Cline` |
| `roo-code` | Roo Code | `ide-extension` | UA `RooCode/`；`HTTP-Referer: .../RooVetGit/Roo-Cline`；提示词 `You are Roo` |
| `kilo-code` | Kilo Code | `ide-extension` | 提示词 `You are Kilo Code`（该产品不发产品级 UA） |
| `aider` | Aider | `agent-cli` | UA `aider/`；实际流量通常经 LiteLLM，因此更常见的是 `litellm` 标签 |
| `crush` | Crush | `agent-cli` | UA `Charm-Crush/`；提示词 `You are Crush` |
| `goose` | Goose | `agent-cli` | 提示词 `You are Goose`（中等置信度） |
| `amp` | Amp | `agent-cli` | UA `amp/`；提示词 `You are Amp`（中等置信度） |
| `openhands` | OpenHands | `agent-app` | UA `OpenHands/`；提示词 `You are OpenHands`（中等置信度） |
| `zed` | Zed | `ide-extension` | UA `Zed/`；`x-zed-*` |
| `litellm` | LiteLLM | `gateway` | UA `litellm/`；`x-litellm-*` |
| `portkey` | Portkey | `gateway` | `x-portkey-*` |
| `openrouter` | OpenRouter | `gateway` | `x-openrouter-title` / `x-openrouter-categories` |

普通 SDK 调用不会被当成 agent harness，但会保留 SDK 标签：UA `OpenAI/Python`、`Anthropic/JS` 等结合 `x-stainless-*` 得到 `sdk:openai-python` / `sdk:anthropic-typescript`，经 Google 生成客户端（`x-goog-api-client: gl-python/...`）得到 `sdk:google-genai-python`，Vercel AI SDK 得到 `sdk:vercel-ai-sdk`。这些 trace 的 `client_harness` 为空，只有标签。

## 标签（tags）

识别结果同时生成一组稳定标签，排序去重后写入 trace：

| 标签 | 含义 |
| --- | --- |
| `harness:<id>` | 命中的产品（与 `client_harness` 相同） |
| `kind:<kind>` | `agent-cli` / `ide-extension` / `agent-app` / `gateway` |
| `sdk:<family>` | 请求明显由某个 SDK 发出，如 `sdk:openai-python`、`sdk:anthropic-typescript`、`sdk:vercel-ai-sdk` |
| `capability:streaming` | `stream: true`、Accept 为 SSE，或 Gemini 的流式 path |
| `capability:tool-use` | 请求带 tools/functions/tool_choice 或历史工具调用 |
| `capability:reasoning` | 带 reasoning/thinking 相关字段 |
| `capability:prompt-cache` | 带 cache_control、prompt_cache_key 等显式缓存字段 |
| `capability:vision` | 带图片输入字段 |
| `capability:responses-api` | 走 Responses 协议或带 `instructions` |
| `capability:structured-output` | 带 response_format/json_schema/responseSchema |

能力标签来自请求形状，是辅助信息而非协议证明：在超大 body 上跳过深读时宁可少一个标签，也不会猜一个。

## 落点

一次请求完成录制时，`internal/recorder` 在写 cassette 前调用 `harness.Detect`，结论写入四处：

1. **cassette meta**（`pkg/recordfile.MetaData`）：`client_harness`、`client_harness_name`、`client_harness_version`、`client_harness_kind`、`client_harness_confidence`、`client_harness_tags`。raw `.http` 因此可以单独回答「这是谁发的」，replay 不受影响。
2. **cassette prelude 事件**：一条 `# event: {"type":"harness.detected", ...}`，attributes 含 `harness`、`name`、`version`、`kind`、`confidence`、`tags`、`evidence`。这是持久、可读、可回放的记录。
3. **application DB 的 `logs` 行**：上述六个字段的索引列（`client_harness`、`client_harness_name`、`client_harness_version`、`client_harness_kind`、`client_harness_confidence`、`client_harness_tags_json`），由 Postgres migration `20261105120000_add_log_client_harness` 与 SQLite 启动 schema 建立。列不加索引：唯一的谓词是列表过滤，`recorded_at` 已有索引负责收窄窗口，`logs` 是最热的写入表，多一个索引是纯写放大。
4. **API 与 UI**：`GET /api/traces` 的每一行返回这六个字段并支持 `?harness=<id>`（大小写不敏感精确匹配）；trace 详情的 `header.meta` 带同一组字段；`list_traces` MCP 工具暴露同样的过滤参数。Monitor 在请求列表显示可点击的 harness 标签（点击即按该 harness 过滤），详情页显示能力/SDK 标签。

## 边界与已知误判

- **只识别客户端可见的交换。** 本地 Responses runtime 的内部上游 `/v1/chat/completions`（`exchange_kind=model`）不参与识别：它的 header 由本进程生成、body 是客户端请求的转换结果，识别它会把内部调用误标成客户端。列表里的 child exchange 因此没有 harness 标签。
- **Qwen Code 会冒充 Claude Code。** Qwen Code 在非 `*.anthropic.com` base URL 上发送 `claude-cli/<version>` + `x-app: cli`；规则表把 Qwen Code 放在 Claude Code 之前，并用其自身系统提示词兜底。若网关同时剥离了 body 中的系统提示词，这类流量会被标成 `claude-code`——这是已知的、无法从剩余字节区分的边界。
- **pi 会借用 Kimi 的身份。** pi（以及 omp 等 fork）在 `kimi-coding` provider 上把 UA 伪装成 `KimiCLI/<version>` 以通过 Kimi 的用量策略，这类流量会被标成 `kimi-code`，而不是 `pi`——外观即标签，伪装本身就是流量写明的事实。
- **body 只读取首尾窗口。** 标记不在窗口内的超大请求可能识别不到；系统提示词与工具声明实际都落在窗口内。
- **历史 cassette 不回填。** 本次改动之前录制的 trace 没有分类字段；它们不会因为重新索引而获得标签（prelude 里没有这些字段）。如需识别历史流量，需要重新录制。
- **网关会改写 UA。** LiteLLM 等网关会把 UA 改写成自己的产品 token；此类 trace 会得到 `litellm` 网关标签，客户端 harness 不可见。
- **标签不是鉴权事实。** 可用于筛选、审计来源与排障，不能用于准入控制。

## 扩展方式

新增一个 harness：在 `internal/harness/catalog.go` 按「具体 → 泛化」的顺序加一条 `rule`，在 `detect_test.go` 补至少一个真实请求形状的用例；如引入新的能力标签，在 `signals.go` 的 `capabilities` 中增加标记。不要为了让识别命中而修改录制或转发路径，也不要把 header 值或 prompt 原文写进 `Evidence`。

## 测试

- `internal/harness/detect_test.go`：各产品 header/body/path 指纹、Qwen 冒充（快速路径下仍正确）、SDK-only 标签、未知请求、大 body 窗口、尾部提示词不认领、产品 token 边界（`continuedev` 不是 `continue`）、空格形式 UA（`codex-cli 0.153.4`）、规则顺序契约、证据不含原文。
- `internal/recorder/recorder_test.go`：meta 字段与 `harness.detected` 事件写入、model child 跳过、SDK-only 不写事件。
- `internal/store` / `internal/monitor`：字段 round-trip、`?harness=` 过滤、列表 JSON 暴露。
