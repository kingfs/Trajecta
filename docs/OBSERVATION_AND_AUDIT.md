# 语义解析、Observation IR 与审计

## 目标与边界

Trajecta 的观测层把记录下来的原始 HTTP 交换解析成语义结构，用于协议展示、审计和长期行为分析。它围绕三类事实工作：

- `.http` cassette 中保存的原始 HTTP request/response bytes 是唯一原始证据。
- Observation IR（`pkg/observe`）是解析后的中间表示，可由 cassette 重算。
- Findings 由确定性检测器基于 Observation IR 产生，检测器不直接读写 cassette。

边界：解析由后台 worker 在记录之后异步进行，检测只在显式 scan/reanalyze 时执行，两者都不在转发热路径上；解析不做 provider 协议互转，请求互转只存在于 `/v1/responses` 的本地运行时；审计结果不替代原始证据，任何 finding 都必须带 evidence path。

相关文档：[ARCHITECTURE.md](./ARCHITECTURE.md)、[RESPONSES_RUNTIME.md](./RESPONSES_RUNTIME.md)、[protocol-reference/README.md](./protocol-reference/README.md)。

## 解析管道

解析链路（后台 worker）：`.http cassette（LLM_PROXY_V3 或 legacy V2） → recordfile.ParsePrelude/ExtractSections → observe.ParseInput → observe.Registry.Select → TraceObservation（Observation IR） → store.SaveObservation`。
检测链路（只在显式 `analyze scan`、`analyze reanalyze`、`analyze batch --scan` 或 Monitor 重分析时触发）：`TraceObservation → analyzer.Runner.Analyze → store.SaveFindings`。后台解析 worker 不运行检测器，因此只经过代理转发、没有做过 scan 的 trace 不会产生 finding 行。

- recorder 写完 cassette 后通过 `store.EnqueueParseJob` 入队；`internal/observeworker` 以 5s 间隔、每批 10 条消费 `parse_jobs`：`store.ClaimParseJobs` 用一条 `UPDATE … RETURNING` 把队首若干条置为 `running` 并原子地返回（Postgres 的子查询用 `FOR UPDATE SKIP LOCKED`，SQLite 没有行锁，靠写锁加进程内 claim 互斥和 `status = 'queued'` 谓词保证不重复认领），随后对每条跑 `observeworker.ReparseTrace` 再 `SaveObservation`。`internal/reanalysis` 的 `analysis_jobs` 用同一套方式认领（`store.ClaimAnalysisJobsForWorker`），所以并发的 server 进程与 CLI 不会重复消费同一个任务。
- `SaveObservation` 的 semantic nodes 与 `SaveFindings` 的 findings 都按参数上限分批写入（各 900 个绑定参数一条多行 `INSERT`），同一批里重复的 `(trace_id, node_id)` / `(trace_id, finding_id)` 先折叠，避免 Postgres 拒绝整条语句。
- 人工与批量重算由 `internal/reanalysis` 的 job 系统驱动，读取同一 IR 与检测器。

## Observation IR 顶层结构

以 `pkg/observe/ir.go` 为准（字段名与 JSON tag 原样）：

```go
type TraceObservation struct {
	TraceID          string         `json:"trace_id"`
	Provider         string         `json:"provider"`
	Operation        string         `json:"operation"`
	Endpoint         string         `json:"endpoint"`
	Model            string         `json:"model"`
	ExchangeKind     string         `json:"exchange_kind,omitempty"`
	ExchangeRole     string         `json:"exchange_role,omitempty"`
	ParentExchangeID string         `json:"parent_exchange_id,omitempty"`
	SequenceIndex    int            `json:"sequence_index,omitempty"`
	RequestAuditID   string         `json:"request_audit_id,omitempty"`
	ResponseID       string         `json:"response_id,omitempty"`
	Parser           string         `json:"parser"`
	ParserVersion    string         `json:"parser_version"`
	Status           ParseStatus    `json:"status"`
	Warnings         []ParseWarning `json:"warnings,omitempty"`

	Request  ObservationRequest  `json:"request"`
	Response ObservationResponse `json:"response"`
	Stream   ObservationStream   `json:"stream,omitempty"`
	Tools    ObservationTools    `json:"tools,omitempty"`
	Usage    ObservationUsage    `json:"usage,omitempty"`
	Timings  ObservationTimings  `json:"timings,omitempty"`
	Safety   ObservationSafety   `json:"safety,omitempty"`
	Findings []Finding           `json:"findings,omitempty"`
	RawRefs  RawReferences       `json:"raw_refs,omitempty"`
}

type ObservationSafety struct {
	Blocked bool `json:"blocked,omitempty"`
	Refused bool `json:"refused,omitempty"`
}

type ObservationUsage struct {
	InputTokens         int `json:"input_tokens,omitempty"`
	OutputTokens        int `json:"output_tokens,omitempty"`
	TotalTokens         int `json:"total_tokens,omitempty"`
	ReasoningTokens     int `json:"reasoning_tokens,omitempty"`
	CacheCreationTokens int `json:"cache_creation_tokens,omitempty"`
	CacheReadTokens     int `json:"cache_read_tokens,omitempty"`
}

type ObservationTimings struct {
	StartedAt    time.Time `json:"started_at,omitempty"`
	CompletedAt  time.Time `json:"completed_at,omitempty"`
	DurationMs   int64     `json:"duration_ms,omitempty"`
	TTFTMs       int64     `json:"ttft_ms,omitempty"`
	TokensPerSec float64   `json:"tokens_per_sec,omitempty"`
}

type RawReferences struct {
	CassettePath  string `json:"cassette_path,omitempty"`
	RequestStart  int64  `json:"request_start,omitempty"`
	RequestEnd    int64  `json:"request_end,omitempty"`
	ResponseStart int64  `json:"response_start,omitempty"`
	ResponseEnd   int64  `json:"response_end,omitempty"`
}

type ObservationRequest struct {
	Instructions []SemanticNode `json:"instructions,omitempty"`
	Messages     []SemanticNode `json:"messages,omitempty"`
	Inputs       []SemanticNode `json:"inputs,omitempty"`
	Tools        []SemanticNode `json:"tools,omitempty"`
	Config       map[string]any `json:"config,omitempty"`
	Nodes        []SemanticNode `json:"nodes,omitempty"`
}

type ObservationResponse struct {
	Outputs     []SemanticNode `json:"outputs,omitempty"`
	Candidates  []SemanticNode `json:"candidates,omitempty"`
	ToolCalls   []SemanticNode `json:"tool_calls,omitempty"`
	ToolResults []SemanticNode `json:"tool_results,omitempty"`
	Reasoning   []SemanticNode `json:"reasoning,omitempty"`
	Refusals    []SemanticNode `json:"refusals,omitempty"`
	Safety      []SemanticNode `json:"safety,omitempty"`
	Errors      []SemanticNode `json:"errors,omitempty"`
	Nodes       []SemanticNode `json:"nodes,omitempty"`
}

type ObservationStream struct {
	Events               []StreamEvent  `json:"events,omitempty"`
	AccumulatedText      string         `json:"accumulated_text,omitempty"`
	AccumulatedReasoning string         `json:"accumulated_reasoning,omitempty"`
	AccumulatedToolCalls []SemanticNode `json:"accumulated_tool_calls,omitempty"`
	Errors               []SemanticNode `json:"errors,omitempty"`
}
```

## SemanticNode 与 NormalizedType

```go
type SemanticNode struct {
	ID             string          `json:"id"`
	ProviderType   string          `json:"provider_type"`
	NormalizedType NormalizedType  `json:"normalized_type"`
	Role           string          `json:"role,omitempty"`
	Path           string          `json:"path"`
	Index          int             `json:"index,omitempty"`
	Text           string          `json:"text,omitempty"`
	JSON           json.RawMessage `json:"json,omitempty"`
	Raw            json.RawMessage `json:"raw,omitempty"`
	Metadata       map[string]any  `json:"metadata,omitempty"`
	ParentID       string          `json:"parent_id,omitempty"`
	Children       []SemanticNode  `json:"children,omitempty"`
}
```

- `ProviderType` 保留 provider 原生类型（`function_call`、`tool_use`、`functionCall` 等）。
- `Path` 是原始 JSON path（如 `$.output[2].arguments`）；`Raw` 是原始 JSON 节点，`JSON` 是规范化节点。
- `ID` 由 `StableNodeID(section, path, providerType, index)` 生成（sha1 前 16 位 hex，前缀 `node_`），同一 cassette 可稳定重算。
- `EvidencePath(traceID, node)` 生成 `trace#<traceID>#node#<id>#path#<path>` 形式的证据路径。

`NormalizedType` 常量集合（`ir.go`）：

```text
instruction  message  text  reasoning  refusal
tool_declaration  tool_call  tool_call_delta  tool_result
server_tool_call  server_tool_result
code  code_result  file  image  citation  safety  usage  error  unknown
```

工具声明、调用与结果汇总在 `ObservationTools`；`ToolOwner` 取值为 `model_requested`、`client_executed`、`provider_executed`、`unknown`。

节点树与扁平表可互转：`FlattenNodes` 生成 `FlatSemanticNode{Node, ParentID, Depth}`，`RebuildNodeTree` 按 `Index`（同 index 按 ID）排序还原。

## 各协议族解析要点

Registry 默认顺序是 entry → openai → anthropic → gemini，取第一个 `CanParse` 为真的 parser；四个 parser 版本都是 `0.1.0`。

没有任何 parser 命中时，`observe.Registry.Parse` 返回 `observe.NoParserError`（满足 `errors.Is(err, observe.ErrNoParser)`），`observeworker.ReparseTrace` 不把它当成解析失败，而是落库一条 `Status = unsupported` 的 observation 并附带 `no_parser` warning：这类 exchange（例如没有 parser 的 `/v1/embeddings`）本身是合法流量，记成 `failed` 会让 parse job 永久失败、后续每次对该 trace 的重分析都重复失败。Monitor 的 observation 状态过滤在 `parsed`/`failed`/`queued`/`running` 之外也接受 `unsupported`。

### Entry / 客户端可见交换

`NewEntryParser` 在 `input.ExchangeKind == "entry"` 或 prelude `input.Header.Meta.ExchangeKind == "entry"` 时命中。它记录入口侧原始节点（`client_request`、`client_response`、`client_response_stream`，`NormalizedType=unknown`）；Responses SSE 走 Responses 流式解析。模型、operation、status、usage 与 exchange 元数据来自 cassette prelude 和请求体。

### OpenAI Chat Completions / Responses / Models

`openAIParser` 命中条件：operation 为 `chat.completions`、`responses` 或 `models`，且 provider 为 OpenAI 兼容（`openai_compatible`、`azure_openai`、`vllm`）或 `unknown`/空。

- Chat：请求 `messages[]`、`tools[]` 等；响应 `choices[].message`、`tool_calls`、`finish_reason`；流式处理 `delta.content`、`delta.reasoning_content`/`delta.reasoning`、`delta.tool_calls` 与最终 usage chunk。
- Responses：请求 `input`/`instructions`；响应与流式事件覆盖 `message`、`reasoning`、`function_call`、`custom_tool_call`、`local_shell_call`、`apply_patch`、`web_search_call`、`file_search_call`、`computer_call`、`code_interpreter_call`、`mcp_call`，以及 `function_call_output`、`custom_tool_call_output`、`mcp_call_output`、`web_search_call_output`、`file_search_call_output`、`computer_call_output`、`code_interpreter_call_output`；`refusal` 与 `error` 也单独归一。`local_shell_call` 与 `apply_patch` 目前没有对应的 `*_output` 映射，未知 item 保留为 `unknown`。
- 归一映射见 `normalizedResponsesType` 与 `normalizedContentType`：`input_text`/`output_text`/`text`→`text`，`input_image`/`image_url`→`image`，`input_file`/`file`→`file`，`reasoning`/`summary_text`/`reasoning_text`→`reasoning`，`refusal`→`refusal`。
- Chat `finish_reason` 只在取值 `content_filter` 时才额外生成一个 `ProviderType=finish_reason`、`NormalizedType=safety` 的节点并置 `Safety.Blocked`（流式与非流式各一处分支，见 `pkg/observe/openai.go`）；其它取值不生成 safety 节点：非流式写进 choice 节点的 `Metadata["finish_reason"]`，流式只用于判定流终止。

### Anthropic Messages

`anthropicParser` 命中 provider `anthropic`、operation `messages`，或 endpoint `/v1/messages`、`/v1/messages/count_tokens`。

- 顶层 `system` 映射为 `instruction`；`messages[].content` 同时支持 string 与 content block array。
- Content block 归一（`normalizedAnthropicType`）：`text`→`text`，`thinking`/`redacted_thinking`→`reasoning`，`tool_use`→`tool_call`，`tool_result`→`tool_result`，`server_tool_use`→`server_tool_call`，`web_search_tool_result`/`web_fetch_tool_result`/`code_execution_tool_result`/`bash_code_execution_tool_result`/`text_editor_code_execution_tool_result`/`tool_search_tool_result`→`server_tool_result`，`image`→`image`，`document`→`file`，`web_search_result`/`citation`→`citation`，`*_error`/`error`→`error`。
- 流式事件：只有 `content_block_start`、`content_block_delta`、`message_delta`、`error` 有处理分支；`message_start`、`content_block_stop`、`message_stop` 等其它事件仍会作为 `StreamEvent` 追加，但归一类型为 `unknown`。`content_block_delta` 识别 `text_delta`→`text`、`thinking_delta`→`reasoning`、`input_json_delta`→`tool_call_delta`，其它 delta 类型落入 `unknown`。

### Google Gemini / Vertex

`geminiParser` 命中 provider `google_genai`/`vertex_native`、operation `generate_content`，或 endpoint 含 `generateContent`；Vertex 复用同一 parser。

- `Part` 类型识别顺序为 `text`、`inlineData`、`fileData`、`functionCall`、`functionResponse`、`executableCode`、`codeExecutionResult`、`toolCall`、`toolResponse`，以及布尔 `thought`；无可识别键时为 `part`/`unknown`。
- 归一：`thought`→`reasoning`，`inlineData`→`image`，`fileData`→`file`，`functionCall`→`tool_call`，`functionResponse`→`tool_result`，`executableCode`→`code`，`codeExecutionResult`→`code_result`，`toolCall`→`server_tool_call`，`toolResponse`→`server_tool_result`。
- `safetyRatings`、`promptFeedback` 收成 `safety` 节点；`candidates[].finishReason` 被记录并参与 safety 汇总。

### OpenAI-compatible

OpenAI-compatible 不等同于 OpenAI，但复用 `openAIParser`：未知字段不会导致失败，常见扩展（`reasoning_content`、`reasoning`、vendor-specific usage details、额外 finish reason）保留在节点 `Metadata`/`Raw` 中。

解析器遇到损坏的流式 JSON 或缺失 section 时追加 `ParseWarning{Code, Message, Path}`，而不是返回错误。

## Exchange 记录模型

两类 HTTP exchange 是一等观测对象：

- entry exchange：客户端到 Trajecta 的 HTTP 交换。本地 Responses server-mode 的入口在 `internal/proxy.(*Handler).serveLocalResponsesWithBody` 外层由 `responsesEntryRecorder` 以 write-through tee 录制（`SiteURL=http://trajecta.local`，`ExchangeKind=entry`、`ExchangeRole=client_request`），并追加 `responses.entry.target` 事件记录目标 path。普通 reverse proxy 请求由同一 recorder pipeline 记录，未写 `exchange_kind` 时按 `model` 索引。
- model exchange：Trajecta 到上游模型 provider 的 HTTP 交换，由 recorder 正常记录，承载 replay 所需的原始响应。

taxonomy 由两个独立字段表达（`recordfile.MetaData`，全部可选、additive）：

```go
ExchangeID       string `json:"exchange_id,omitempty"`
ExchangeKind     string `json:"exchange_kind,omitempty"`
ExchangeRole     string `json:"exchange_role,omitempty"`
ParentExchangeID string `json:"parent_exchange_id,omitempty"`
SequenceIndex    int    `json:"sequence_index,omitempty"`
TraceID          string `json:"trace_id,omitempty"`
```

- `exchange_kind`：写入侧只产生 `entry` 与 `model`；读取侧的 client-visible 判定还会接受空值与 legacy 的 `proxy`（`clientVisibleLogClause`），所以 `proxy` 是读取兼容值而非写入值。
- `exchange_role`：写入侧产生 `client_request`、`primary_model_call`、`tool_followup_model_call`、`compact_model_call`。前两个是 `pkg/observe` 在 metadata 缺省时的推断值（`exchange_kind=entry` 推 `client_request`，否则推 `primary_model_call`）；后两个来自 `internal/responses/runtime/chat.go` 的常量。Responses runtime 在发起上游调用前决定 role，recorder 只持久化传入的 metadata。此外 analyzer 在 `scope.Role` 为空且 kind 为 `model` 时把 scope 视为 `legacy_model`（`internal/analyzer/analyzer.go`），这个值不会写回 cassette 或索引。
- 关联字段：`request_audit_id` 是 Responses 根关联 id；`response_id`、`client_request_id`、`conversation_id` 来自 meta；`trace_id` 与 `cassette_path` 把 model exchange 指回原始 cassette。

索引落点（应用数据库）：

- `logs`：cassette trace 索引，带 `exchange_id`、`exchange_kind`、`exchange_role`、`parent_exchange_id`、`sequence_index`（`ent/schema/trace_log.go`，字符串列 `NOT NULL DEFAULT ''`，`sequence_index` 为 `NOT NULL DEFAULT 0`）。
- `trace_observations`：观测 IR 索引，带 `exchange_kind`、`exchange_role`、`parent_exchange_id`、`sequence_index`；**没有** `exchange_id` 列（`ent/schema/trace_observation.go`、`ent/postgres-migrations/20260625123000_add_trace_observation_exchange_fields.up.sql`）。
- `upstream_exchanges`：Responses server-mode 的 model exchange 明细，带 `exchange_id`、`exchange_kind`、`exchange_role`、`parent_exchange_id`、`sequence_index` 这些同名 nullable 列（`ent/schema/upstream_exchange.go`）。行内 `request_id` 与 `trace_id` 同值（recorder prelude `meta.request_id` = `logs.request_id`）；`trace_id` 是历史命名，按名连接 `logs.trace_id`（UUID）会静默返回 0 行，可靠连接键是 `cassette_path = logs.path` 或 `request_id = logs.request_id`。
- `request_audits`：entry/client_request 的根；读模型用 `syntheticEntryExchange` 合成 `exchange_id="entry:"+request_audit_id`、`exchange_kind=entry`、`exchange_role=client_request`、`sequence_index=0`。
- `execution_events` 与 `tool_call_audits`：生命周期事件与工具调用审计。

读取回退分布在多处，职责不同：`normalizeExchangeView`（`internal/responses/audit/query.go`）与 `observeworker.applyExchangeFallbacks`（`internal/observeworker/worker.go`）只处理 kind 与 role——缺失 kind 时按元数据与路径提示推断，且 `applyExchangeFallbacks` 只在 kind 非空时补 role（entry→`client_request`、model→`primary_model_call`），两者都无法判定时保持空值；kind 与 role 成对兜底为 `model` / `primary_model_call` 发生在 MCP 的 cassette loader（`internal/mcpserver/responses_audit.go` 的 `responsesModelExchangeFromAudit`）与索引回填（`internal/store/exchange_backfill.go`，按 kind 推断 role：entry→`client_request`、model→`primary_model_call`）里。Monitor 只补 kind：model 交换兜底为 `model`（`internal/monitor/server_responses.go` 的 `responsesModelExchangeFromUpstream`、`responsesApplyCassetteMetadata`），entry 交换兜底为 `entry`、role 兜底为 `client_request`（同文件的 `responsesEntryExchangeFromAudit`），role 为空的 model 交换保持空值，不会补成 `primary_model_call`。这些路径都不碰 `trace_id`；缺失 `trace_id` 时回退到 V3 `meta.request_id` 的行为在 Monitor 与 MCP 的 cassette loader 里（`internal/monitor/server_responses.go` 的 `responsesEntryExchangeFromAudit`、`internal/mcpserver/responses_audit.go` 的 `responsesModelExchangeFromAudit`）。

读模型 `internal/responses/audit.RequestAuditTrace` 返回 `EntryExchange`、`ModelExchanges`（`[]UpstreamExchangeView`，字段含 `exchange_id`/`exchange_kind`/`exchange_role`/`parent_exchange_id`/`sequence_index`/`trace_id`/`cassette_path`）、`UpstreamExchanges` 别名、`RawCassettes` 与 `Diagnostics`。MCP `responses_audit_trace` 输出 `entry_exchange`、`model_exchanges` 与带 kind 的 `raw_cassettes`，并保留 `upstream_exchanges` alias。详见 [MONITOR_GUIDE.md](./MONITOR_GUIDE.md) 与 [MCP_GUIDE.md](./MCP_GUIDE.md)。

## Findings、Severity 与 Category

```go
type Finding struct {
	ID              string         `json:"id"`
	TraceID         string         `json:"trace_id,omitempty"`
	Category        string         `json:"category"`
	Severity        Severity       `json:"severity"`
	Confidence      float64        `json:"confidence"`
	Title           string         `json:"title"`
	Description     string         `json:"description,omitempty"`
	EvidencePath    string         `json:"evidence_path"`
	EvidenceExcerpt string         `json:"evidence_excerpt,omitempty"`
	NodeID          string         `json:"node_id,omitempty"`
	Detector        string         `json:"detector"`
	DetectorVersion string         `json:"detector_version"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	CreatedAt       time.Time      `json:"created_at,omitempty"`
}
```

`Severity` 取 `info`、`low`、`medium`、`high`、`critical`。`Runner.Analyze` 按 severity 降序、ID 升序稳定排序，并补全 `TraceID`、`Detector`、`DetectorVersion`、`CreatedAt`、`ID`。

当前四个检测器实际产生的 category：

- `dangerous_shell`：`filesystem_destructive_operation`（critical）、`dangerous_command`（high）、`unsafe_code_execution`（high）、`credential_leak`（high，凭据文件访问）。
- `credential`：`credential_leak`（high）。
- `provider_safety`：`provider_safety_block`（medium）、`model_refusal`（low）。
- `tool_error`：`tool_result_error`（medium）。

Finding ID 由 `stableFindingID` 基于 trace、category、severity、node、evidence 与 detector 版本生成 sha1，保证重算稳定。检测按 exchange scope 过滤：entry observation 只跑 `credential`，其余检测器只作用于 model observation；每个 finding 的 `Metadata` 会写入 `exchange_kind` 与 `exchange_scope`；`exchange_role` 只在 `scope.Role` 非空时才写入（`internal/analyzer/analyzer.go` 的 `attachFindingExchangeScope`）。

## 检测器

统一接口（`internal/analyzer`）：

```go
type Detector interface {
	Name() string
	Version() string
	Detect(context.Context, observe.TraceObservation) ([]observe.Finding, error)
}
```

`DefaultDetectors()` 返回 `DangerousShellDetector`、`CredentialDetector`、`ProviderSafetyDetector`、`ToolErrorDetector`。

- dangerous_shell：扫描 `obs.Tools.Calls[].ArgsText`/`ArgsJSON`，以及类型为 `tool_call`/`server_tool_call`/`code` 的节点文本；用正则 `destructiveRMPattern` 识别 `rm -rf /`、`rm -rf ~`，并用关键字识别 `sudo rm`、`mkfs`、`diskutil erase`、`dd if=`、`chmod -R 777`、`chown -R`、`nc -e`、`bash -i`、`curl|wget ... | sh/bash`、`.ssh/id_rsa`、`.aws/credentials`、`security find-generic-password`、`env | curl`。
- credential：对所有节点扫描 `credentialPatterns`：OpenAI key `sk-...`、AWS `AKIA...`、GitHub `gh[pousr]_...`、`-----BEGIN ... PRIVATE KEY-----`、Bearer token、`postgres|mysql|mongodb://` 连接串、Cookie header，以及带用户名密码的 URL。检测是 observe-only，不修改请求或响应。
- provider_safety：把 `safety` 节点映射为 `provider_safety_block`、`refusal` 节点映射为 `model_refusal`，并叠加 `ObservationSafety.Blocked`/`Refused` 汇总位。
- tool_error：`obs.Tools.Results[].IsError` 为真，或 `tool_result`/`server_tool_result`/`error` 节点的 status/text 含错误信号时报告 `tool_result_error`。

## 重解析与重分析入口

`internal/reanalysis` 提供 job 化的 reparse、rescan、repair-usage、reanalyze，覆盖 trace、session、batch 三种 target：

- 步骤常量：`reparse_observation`、`scan_findings`、`repair_usage`、`session_analysis`。
- `ReparseTrace` 重建 Observation IR 并保存；`RescanTrace` 只读已存观测重跑检测器；`RepairTraceUsage` 用 `llm.ResponsePipeline` 从响应体重抽 usage 并更新索引（仅 `RewriteCassette` 时重写 V3 prelude，不改变 raw payload）。
- 结果携带 `RequestNodes`、`ResponseNodes`、`StreamEvents`、`FindingCount`、`CriticalFindings`、`HighFindings`，并写入 `analysis_jobs`。

CLI 入口（`cmd/server/analyze.go`）：`analyze reparse`、`analyze scan`、`analyze repair-usage`、`analyze reanalyze`、`analyze batch` 是 `Hidden: true` 的隐藏内部子命令（`analyze refresh`、`analyze session`、`analyze backfill-exchanges` 与 `audit query`、`audit tool-calls` 不隐藏）。完整命令清单由 [STORAGE_AND_DEPLOYMENT.md](./STORAGE_AND_DEPLOYMENT.md) 维护，这里只保留重分析相关入口。

`analyze backfill-exchanges` 调用 `store.BackfillExchangeMetadata`，只更新 DB 索引、不重写 cassette，返回 `scanned`、`updated_model`、`updated_entry`、`legacy_or_unknown`、`missing_cassette`、`conflicts`、`dry_run`，并支持 `--dry-run`。

`internal/sessionanalysis` 生成 `session_summary`（`AnalyzerName=session_summary`、`AnalyzerVersion=0.1.0`）并保存为 analysis run。

## 非目标与未实现

- 不做 provider 协议互转；唯一例外是 `/v1/responses` 本地运行时的内部编排。
- 审计层没有 PII 检测器，身份证号、手机号、邮箱、内网 URL/IP 等不在检测范围内。
- 没有性能检测器；TTFT、tokens/s、cache hit、错误率等指标不产生 finding。
- 没有 LLM 语义总结层，`session_summary` 之外的模型化分析未实现。
- 敏感信息只有 observe 模式，`redact_at_rest` 与 `inline_redact` 未实现。
- 通用 `exchanges` 图表现未引入，exchange 关系仍由现有索引表的同名字段表达：`logs` 与 `trace_observations` 用 `NOT NULL DEFAULT ''`（`sequence_index` 为 `NOT NULL DEFAULT 0`），`upstream_exchanges` 用 nullable 列。
- `pkg/replay` 只回放单个 `.http`，不读取数据库，也不编排 entry 与多个 model cassette。
- 重分析链路不改写 V2 cassette；legacy V2→V3 的格式升级由 `migrate` / `trajecta upgrade cassettes` 单独完成。V3 cassette 只在 `analyze repair-usage --rewrite-cassette`（或 `analyze batch --repair-usage --rewrite-cassette`）下重写 prelude 的 usage，raw payload 不变。缺失 taxonomy 只在读取与查询时推断。
- auth 失败发生在入口 handler 外层时不产生 entry cassette。
