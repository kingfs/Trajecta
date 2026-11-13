// Package schema describes the logical structure of the Exchange IR as data.
//
// It is the static, protocol-neutral shape behind the Monitor's XIR structure
// map: one tree of named nodes (exchange -> request/response/stream -> groups
// -> content parts -> fields), each node optionally carrying the hook.Point
// that names it in the pkg/xir/hook vocabulary. Detection plugins declare the
// points they act on, so a reader can overlay a plugin catalog onto this tree
// and see exactly which nodes a plugin observes.
//
// The description is metadata only. It does not parse, decode or project any
// payload, and it carries no traffic data: the audit projection of a real
// exchange lives in pkg/xir/audit. Node labels are bilingual (English plus
// Simplified Chinese) because the Monitor renders both locales.
package schema

import "github.com/kingfs/Trajecta/pkg/xir/hook"

// Version identifies the shape of the structure document. Bump it when nodes
// or points are added or renamed so a cached client can tell them apart.
const Version = 1

// Node kinds. They drive the icon and colour family a renderer gives a node;
// they are presentation categories, not Go types.
const (
	KindRoot    = "root"    // the exchange itself
	KindSection = "section" // request, response, stream
	KindGroup   = "group"   // a collection or struct of children
	KindPart    = "part"    // one content block or message container
	KindField   = "field"   // a scalar or leaf value
	KindSealed  = "sealed"  // an opaque protocol-native block
)

// Node flags. They state structural facts a detector overlay must respect.
const (
	// FlagSealed marks a block whose bytes must round-trip untouched. It maps
	// to no hook point: the audit projection gives it no Text, so no text
	// detector is ever fed it (see pkg/xir/audit.NodePoint).
	FlagSealed = "sealed"
	// FlagNoText marks a node that carries no human-readable text by
	// construction (media bytes, sealed state).
	FlagNoText = "no-text"
	// FlagMedia marks normalized media payloads (image/file/audio/video).
	FlagMedia = "media"
	// FlagRecursive marks a node whose children repeat the part vocabulary,
	// such as a tool result's content parts.
	FlagRecursive = "recursive"
)

// Node is one named position in the logical structure.
type Node struct {
	ID            string     `json:"id"`
	Kind          string     `json:"kind"`
	Type          string     `json:"type"`
	Name          string     `json:"name"`
	NameZh        string     `json:"name_zh"`
	Description   string     `json:"description,omitempty"`
	DescriptionZh string     `json:"description_zh,omitempty"`
	Point         hook.Point `json:"point,omitempty"`
	Flags         []string   `json:"flags,omitempty"`
	Children      []string   `json:"children,omitempty"`
}

// PointInfo describes one hook point of the pkg/xir/hook vocabulary.
//
// Reserved marks a point that belongs to the vocabulary but has no producer in
// the current audit projection: a scanner may not declare it, because no node
// maps to it yet. An observation detector may still read the same logical field
// (its points are catalog metadata), so the map shows the distinction instead
// of implying a live audit hook.
type PointInfo struct {
	Point         hook.Point `json:"point"`
	Section       string     `json:"section"`
	Name          string     `json:"name"`
	NameZh        string     `json:"name_zh"`
	Description   string     `json:"description,omitempty"`
	DescriptionZh string     `json:"description_zh,omitempty"`
	Reserved      bool       `json:"reserved,omitempty"`
}

// Representation describes one of the two views the IR keeps of an exchange.
type Representation struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	NameZh        string `json:"name_zh"`
	Description   string `json:"description,omitempty"`
	DescriptionZh string `json:"description_zh,omitempty"`
}

// Structure is the whole static document: the node tree rooted at Root plus
// the hook-point vocabulary and the two-representation note.
type Structure struct {
	Version         int              `json:"version"`
	Root            string           `json:"root"`
	Representations []Representation `json:"representations"`
	Nodes           []Node           `json:"nodes"`
	Points          []PointInfo      `json:"points"`
}

// Build returns the logical structure of the Exchange IR. The returned value
// is freshly allocated on every call, so a caller may mutate it freely.
func Build() *Structure {
	return &Structure{
		Version:         Version,
		Root:            "exchange",
		Representations: representations(),
		Nodes:           nodes(),
		Points:          points(),
	}
}

func representations() []Representation {
	return []Representation{
		{
			ID:            "wire",
			Name:          "Wire view",
			NameZh:        "Wire 视图",
			Description:   "The lossless JSON tree (pkg/xir/wire). Every node keeps its byte span into the original payload, so a rewrite can touch exactly one field without re-encoding the body.",
			DescriptionZh: "无损 JSON 树（pkg/xir/wire）。每个节点保留其在原始载荷中的字节区间，改写可以只触及一个字段而不重新编码整个 body。",
		},
		{
			ID:            "semantic",
			Name:          "Semantic view",
			NameZh:        "语义视图",
			Description:   "The typed Request/Response (pkg/xir). Codecs decode it from the wire view and encode it back for a target protocol; detection and rewrite hooks may operate on either view.",
			DescriptionZh: "类型化的 Request/Response（pkg/xir）。codec 从 wire 视图解码、再为目标协议编码回去；检测与改写 hook 可以作用于任一视图。",
		},
	}
}

func nodes() []Node {
	return []Node{
		{
			ID: "exchange", Kind: KindRoot, Type: "xir.Exchange",
			Name: "Exchange", NameZh: "交换",
			Description:   "One decoded request/response pair with its protocol identity and conversion losses. The IR deliberately carries no routing, database or agent state.",
			DescriptionZh: "一次解码后的请求/响应对，携带协议身份与转换损耗。IR 刻意不携带路由、数据库或 agent 状态。",
			Point:         hook.PointExchangeFinalize,
			Children:      []string{"request", "response", "stream"},
		},

		// ---- Request section ----
		{
			ID: "request", Kind: KindSection, Type: "xir.Request",
			Name: "Request", NameZh: "请求",
			Description:   "The protocol-neutral model request: envelope, instructions, conversation, tool declarations, parameters and state references.",
			DescriptionZh: "协议中立的模型请求：信封、系统指令、会话、工具声明、生成参数与服务端状态引用。",
			Children:      []string{"request.envelope", "request.system", "request.messages", "request.tools", "request.params", "request.metadata", "request.state"},
		},
		{
			ID: "request.envelope", Kind: KindGroup, Type: "model · stream",
			Name: "Envelope", NameZh: "信封",
			Description:   "The routing-facing fields the proxy reads without decoding the body: the model name and the stream flag.",
			DescriptionZh: "代理无需解码 body 即可读取的路由字段：模型名与流式开关。",
			Point:         hook.PointRequestEnvelope,
			Children:      []string{"request.model", "request.stream"},
		},
		{
			ID: "request.model", Kind: KindField, Type: "string",
			Name: "Model", NameZh: "模型名",
			Description:   "The requested model; the proxy may rewrite an alias here without re-encoding the body (xir.RewriteModel).",
			DescriptionZh: "请求的模型名；代理可在此改写别名而不重新编码 body（xir.RewriteModel）。",
		},
		{
			ID: "request.stream", Kind: KindField, Type: "bool",
			Name: "Stream", NameZh: "流式开关",
			Description:   "Whether the client asked for a streamed response; streaming requests are never protocol-converted.",
			DescriptionZh: "客户端是否要求流式响应；流式请求不做协议转换。",
		},
		{
			ID: "request.system", Kind: KindGroup, Type: "[]xir.Part",
			Name: "System instructions", NameZh: "系统指令",
			Description:   "The system prompt as parts. Protocols that carry it as a system/developer message keep it in Messages instead; nothing is lifted or duplicated.",
			DescriptionZh: "以内容块表示的系统提示词。以 system/developer 消息承载的协议仍保留在 Messages 中，不做提取或复制。",
			Point:         hook.PointRequestSystem,
			Children:      []string{"request.system.part"},
		},
		{
			ID: "request.system.part", Kind: KindPart, Type: "xir.Part",
			Name: "Instruction part", NameZh: "指令内容块",
			Description:   "One instruction block, usually text; the same part vocabulary as message content.",
			DescriptionZh: "一个指令块，通常为文本；与消息内容使用同一套内容块词汇。",
			Point:         hook.PointRequestSystem,
		},
		{
			ID: "request.messages", Kind: KindGroup, Type: "[]xir.Message",
			Name: "Messages", NameZh: "会话消息",
			Description:   "The conversation turns in order.",
			DescriptionZh: "按顺序排列的会话轮次。",
			Children:      []string{"request.message"},
		},
		{
			ID: "request.message", Kind: KindPart, Type: "xir.Message",
			Name: "Message", NameZh: "消息",
			Description:   "One turn with a normalized role: system, developer, user, assistant, tool or model. System/developer turns map to the request.system point; the rest to request.message.",
			DescriptionZh: "一个轮次，角色归一化为 system、developer、user、assistant、tool 或 model。system/developer 轮次映射到 request.system 挂点，其余映射到 request.message。",
			Point:         hook.PointRequestMessage,
			Children:      []string{"request.message.parts"},
		},
		{
			ID: "request.message.parts", Kind: KindGroup, Type: "[]xir.Part",
			Name: "Content parts", NameZh: "内容块",
			Description:   "The content blocks of one turn. Each kind maps to its own hook point; sealed blocks map to none.",
			DescriptionZh: "一个轮次的内容块。每种类型映射到各自的 hook 挂点；密封块不映射任何挂点。",
			Children:      []string{"request.message.text", "request.message.media", "request.message.tool_call", "request.message.tool_result", "request.message.reasoning", "request.message.sealed"},
		},
		{
			ID: "request.message.text", Kind: KindPart, Type: "xir.Part(text)",
			Name: "Text", NameZh: "文本",
			Description:   "Human-readable text; the primary surface text detectors scan.",
			DescriptionZh: "人类可读文本；文本检测器的主要扫描面。",
			Point:         hook.PointRequestMessage,
		},
		{
			ID: "request.message.media", Kind: KindPart, Type: "xir.Part(image|file|audio|video)",
			Name: "Media", NameZh: "媒体",
			Description:   "Normalized image/file/audio/video payloads. The audit projection keeps only metadata (kind, URL, media type, filename); inline bytes are never copied into a node, so there is no text to scan and no hook point is fed.",
			DescriptionZh: "归一化的图像/文件/音频/视频载荷。审计投影只保留元数据（类型、URL、媒体类型、文件名）；内联字节从不复制进节点，因此没有可扫描的文本，也不会有挂点被喂入。",
			Flags:         []string{FlagMedia, FlagNoText},
		},
		{
			ID: "request.message.tool_call", Kind: KindPart, Type: "xir.ToolCall",
			Name: "Tool call", NameZh: "工具调用",
			Description:   "A model-issued call in the assistant history: id, name and JSON arguments. The arguments are the scannable surface.",
			DescriptionZh: "助手历史中模型发起的调用：id、名称与 JSON 参数。参数是可扫描面。",
			Point:         hook.PointRequestToolCall,
		},
		{
			ID: "request.message.tool_result", Kind: KindPart, Type: "xir.ToolResult",
			Name: "Tool result", NameZh: "工具结果",
			Description:   "The client's answer to a tool call: text, structured JSON and nested content parts.",
			DescriptionZh: "客户端对工具调用的回答：文本、结构化 JSON 与嵌套内容块。",
			Point:         hook.PointRequestToolResult,
			Flags:         []string{FlagRecursive},
			Children:      []string{"request.message.tool_result.content"},
		},
		{
			ID: "request.message.tool_result.content", Kind: KindGroup, Type: "[]xir.Part",
			Name: "Result content", NameZh: "结果内容",
			Description:   "Nested parts of a tool result; the same part vocabulary repeats here (text, media, sealed, ...).",
			DescriptionZh: "工具结果的嵌套内容块；同一套内容块词汇在此重复（文本、媒体、密封块等）。",
			Point:         hook.PointRequestToolResult,
			Flags:         []string{FlagRecursive},
		},
		{
			ID: "request.message.reasoning", Kind: KindPart, Type: "xir.Reasoning",
			Name: "Reasoning", NameZh: "推理",
			Description:   "Visible reasoning carried back in the request history. On the request side it maps to request.message; a sealed child block may sit beside the visible text.",
			DescriptionZh: "请求历史中带回的可见推理。请求侧映射到 request.message；可见文本旁可能存在密封子块。",
			Point:         hook.PointRequestMessage,
			Children:      []string{"request.message.reasoning.sealed"},
		},
		{
			ID: "request.message.reasoning.sealed", Kind: KindSealed, Type: "xir.Sealed",
			Name: "Sealed reasoning", NameZh: "密封推理",
			Description:   "Signed or encrypted reasoning (encrypted_content, signature, redacted thinking). MustRoundTrip: rewriting any byte makes the provider reject the request, so no content scanner is ever fed it.",
			DescriptionZh: "签名或加密的推理（encrypted_content、signature、redacted thinking）。MustRoundTrip：改写任何字节都会导致提供方拒绝请求，因此内容扫描器从不接触它。",
			Flags:         []string{FlagSealed, FlagNoText},
		},
		{
			ID: "request.message.sealed", Kind: KindSealed, Type: "xir.Sealed",
			Name: "Sealed block", NameZh: "密封块",
			Description:   "A protocol-native opaque block preserved byte for byte. It carries no Text by construction and maps to no hook point.",
			DescriptionZh: "按字节保留的协议原生不透明块。构造上不携带 Text，不映射任何 hook 挂点。",
			Flags:         []string{FlagSealed, FlagNoText},
		},
		{
			ID: "request.tools", Kind: KindGroup, Type: "[]xir.Tool",
			Name: "Tool declarations", NameZh: "工具声明",
			Description:   "The tools the client declares, plus the tool_choice directive (auto/none/required/tool).",
			DescriptionZh: "客户端声明的工具，以及 tool_choice 指令（auto/none/required/tool）。",
			Children:      []string{"request.tool"},
		},
		{
			ID: "request.tool", Kind: KindPart, Type: "xir.Tool",
			Name: "Tool", NameZh: "工具",
			Description:   "One declaration: name, description and JSON schema. The description and schema are text-bearing surfaces.",
			DescriptionZh: "一条声明：名称、描述与 JSON schema。描述与 schema 是携带文本的表面。",
			Point:         hook.PointRequestToolDecl,
		},
		{
			ID: "request.params", Kind: KindGroup, Type: "xir.Params",
			Name: "Generation params", NameZh: "生成参数",
			Description:   "temperature, top_p/top_k, output caps, stop sequences, penalties, seed, reasoning effort/thinking budget, response format and the extra fields the semantic model does not carry.",
			DescriptionZh: "temperature、top_p/top_k、输出上限、停止序列、惩罚项、seed、推理强度/思考预算、响应格式，以及语义模型未承载的额外字段。",
		},
		{
			ID: "request.metadata", Kind: KindGroup, Type: "user · metadata",
			Name: "Metadata", NameZh: "元数据",
			Description:   "The user identifier and the metadata map. The whole-body hot scan covers these bytes; the request.metadata point names them for field-scoped hooks.",
			DescriptionZh: "user 标识与 metadata 映射。整 body 热扫描覆盖这些字节；request.metadata 挂点为字段级 hook 命名它们。",
			Point:         hook.PointRequestMetadata,
		},
		{
			ID: "request.state", Kind: KindGroup, Type: "xir.StateRefs",
			Name: "State references", NameZh: "状态引用",
			Description:   "previous_response_id, conversation, prompt_cache_key and store. They force hard upstream affinity and are never rewritten by detection.",
			DescriptionZh: "previous_response_id、conversation、prompt_cache_key 与 store。它们强制上游亲和性，检测从不改写它们。",
		},

		// ---- Response section ----
		{
			ID: "response", Kind: KindSection, Type: "xir.Response",
			Name: "Response", NameZh: "响应",
			Description:   "The protocol-neutral model response: output parts, usage, stop reason and error.",
			DescriptionZh: "协议中立的模型响应：输出内容块、用量、停止原因与错误。",
			Children:      []string{"response.output", "response.usage", "response.stop", "response.error"},
		},
		{
			ID: "response.output", Kind: KindGroup, Type: "[]xir.Part",
			Name: "Output parts", NameZh: "输出内容块",
			Description:   "The model output in order. Text-like parts map to response.output; reasoning, tool calls and tool results map to their own points.",
			DescriptionZh: "按顺序排列的模型输出。文本类内容块映射到 response.output；推理、工具调用与工具结果映射到各自的挂点。",
			Point:         hook.PointResponseOutput,
			Children:      []string{"response.text", "response.refusal", "response.reasoning", "response.tool_call", "response.tool_result", "response.citation", "response.search", "response.sealed"},
		},
		{
			ID: "response.text", Kind: KindPart, Type: "xir.Part(text)",
			Name: "Text", NameZh: "文本",
			Description:   "The model's visible answer text.",
			DescriptionZh: "模型的可见回答文本。",
			Point:         hook.PointResponseOutput,
		},
		{
			ID: "response.refusal", Kind: KindPart, Type: "xir.Part(refusal)",
			Name: "Refusal", NameZh: "拒答",
			Description:   "A model refusal block; a safety detector's primary surface.",
			DescriptionZh: "模型拒答块；安全检测器的主要表面。",
			Point:         hook.PointResponseOutput,
		},
		{
			ID: "response.reasoning", Kind: KindPart, Type: "xir.Reasoning",
			Name: "Reasoning", NameZh: "推理",
			Description:   "Visible model reasoning (text or summary). Maps to response.reasoning; sealed reasoning beside it maps to no point.",
			DescriptionZh: "可见的模型推理（文本或摘要）。映射到 response.reasoning；其旁的密封推理不映射任何挂点。",
			Point:         hook.PointResponseReasoning,
			Children:      []string{"response.reasoning.sealed"},
		},
		{
			ID: "response.reasoning.sealed", Kind: KindSealed, Type: "xir.Sealed",
			Name: "Sealed reasoning", NameZh: "密封推理",
			Description:   "Encrypted or signed reasoning state. MustRoundTrip blocks are excluded from replacement structurally: the projection gives them no Text.",
			DescriptionZh: "加密或签名的推理状态。MustRoundTrip 块在结构上被排除在改写之外：投影不赋予它们 Text。",
			Flags:         []string{FlagSealed, FlagNoText},
		},
		{
			ID: "response.tool_call", Kind: KindPart, Type: "xir.ToolCall",
			Name: "Tool call", NameZh: "工具调用",
			Description:   "A call the model just issued: name and JSON arguments. Behavior detectors read the arguments here.",
			DescriptionZh: "模型刚发起的调用：名称与 JSON 参数。行为检测器在此读取参数。",
			Point:         hook.PointResponseToolCall,
		},
		{
			ID: "response.tool_result", Kind: KindPart, Type: "xir.ToolResult",
			Name: "Tool result", NameZh: "工具结果",
			Description:   "A hosted-tool result surfaced in the response, with nested content parts and an is_error flag.",
			DescriptionZh: "响应中呈现的托管工具结果，携带嵌套内容块与 is_error 标志。",
			Point:         hook.PointResponseToolResult,
			Flags:         []string{FlagRecursive},
		},
		{
			ID: "response.citation", Kind: KindPart, Type: "xir.Citation",
			Name: "Citation", NameZh: "引用",
			Description:   "One model citation: title, URL and the cited span.",
			DescriptionZh: "一条模型引用：标题、URL 与被引用区间。",
			Point:         hook.PointResponseOutput,
		},
		{
			ID: "response.search", Kind: KindPart, Type: "xir.Search",
			Name: "Web search", NameZh: "联网搜索",
			Description:   "A model-run web search: query and hits.",
			DescriptionZh: "模型执行的联网搜索：查询与命中结果。",
			Point:         hook.PointResponseOutput,
		},
		{
			ID: "response.sealed", Kind: KindSealed, Type: "xir.Sealed",
			Name: "Sealed block", NameZh: "密封块",
			Description:   "A protocol-native opaque output block, preserved byte for byte and never decoded into text.",
			DescriptionZh: "协议原生的不透明输出块，按字节保留，从不解码为文本。",
			Flags:         []string{FlagSealed, FlagNoText},
		},
		{
			ID: "response.usage", Kind: KindField, Type: "usage.Usage",
			Name: "Usage", NameZh: "用量",
			Description:   "The provider-reported token usage, normalized into pkg/usage.",
			DescriptionZh: "提供方报告的 token 用量，归一化为 pkg/usage。",
			Point:         hook.PointResponseUsage,
		},
		{
			ID: "response.stop", Kind: KindField, Type: "xir.FinishReason",
			Name: "Stop reason", NameZh: "停止原因",
			Description:   "The normalized completion reason: stop, length, tool_use, content_filter, refusal, error.",
			DescriptionZh: "归一化的完成原因：stop、length、tool_use、content_filter、refusal、error。",
		},
		{
			ID: "response.error", Kind: KindField, Type: "xir.Error",
			Name: "Error", NameZh: "错误",
			Description:   "The normalized provider/proxy error: status, type, code, message.",
			DescriptionZh: "归一化的提供方/代理错误：status、type、code、message。",
			Point:         hook.PointResponseError,
		},

		// ---- Stream section ----
		{
			ID: "stream", Kind: KindSection, Type: "xir.StreamEvent",
			Name: "Stream events", NameZh: "流式事件",
			Description:   "The normalized event stream (pkg/xir/stream): SSE frames decoded into typed events with spans back into the raw frame. Stream hooks are the reserved future host; no detection plugin declares stream points today.",
			DescriptionZh: "归一化的事件流（pkg/xir/stream）：SSE 帧解码为带类型的事件，并保留指回原始帧的区间。流式 hook 是预留的未来宿主；今天没有检测插件声明流式挂点。",
			Children:      []string{"stream.event", "stream.text", "stream.reasoning", "stream.tool_args", "stream.lifecycle"},
		},
		{
			ID: "stream.event", Kind: KindGroup, Type: "xir.StreamEvent",
			Name: "Event envelope", NameZh: "事件信封",
			Description:   "Kind, protocol, span and timestamp shared by every streamed event.",
			DescriptionZh: "每个流式事件共享的 Kind、协议、区间与时间戳。",
			Point:         hook.PointStreamEvent,
		},
		{
			ID: "stream.text", Kind: KindPart, Type: "text delta",
			Name: "Text delta", NameZh: "文本增量",
			Description:   "An incremental run of answer text.",
			DescriptionZh: "回答文本的增量片段。",
			Point:         hook.PointStreamTextDelta,
		},
		{
			ID: "stream.reasoning", Kind: KindPart, Type: "reasoning delta",
			Name: "Reasoning delta", NameZh: "推理增量",
			Description:   "An incremental run of visible reasoning.",
			DescriptionZh: "可见推理的增量片段。",
			Point:         hook.PointStreamReasonDelta,
		},
		{
			ID: "stream.tool_args", Kind: KindPart, Type: "tool args delta",
			Name: "Tool args delta", NameZh: "工具参数增量",
			Description:   "An incremental run of tool-call arguments, framed by tool_start/tool_end.",
			DescriptionZh: "工具调用参数的增量片段，由 tool_start/tool_end 框定。",
			Point:         hook.PointStreamToolArgs,
		},
		{
			ID: "stream.lifecycle", Kind: KindGroup, Type: "start · stop · usage · error · ping",
			Name: "Lifecycle events", NameZh: "生命周期事件",
			Description:   "Start, stop, usage, error and ping events; sealed deltas keep their bytes untouched.",
			DescriptionZh: "start、stop、usage、error 与 ping 事件；密封增量保持字节不动。",
			Point:         hook.PointStreamEvent,
		},
	}
}

func points() []PointInfo {
	return []PointInfo{
		{
			Point: hook.PointRequestEnvelope, Section: "request",
			Name: "Request envelope", NameZh: "请求信封",
			Description:   "The model name and stream flag the routing envelope reads.",
			DescriptionZh: "路由信封读取的模型名与流式开关。",
			Reserved:      true,
		},
		{
			Point: hook.PointRequestSystem, Section: "request",
			Name: "System instructions", NameZh: "系统指令",
			Description:   "System/developer prompt parts, wherever the protocol carries them.",
			DescriptionZh: "system/developer 提示词内容块，无论协议将其承载在何处。",
		},
		{
			Point: hook.PointRequestMessage, Section: "request",
			Name: "Message content", NameZh: "消息内容",
			Description:   "Conversation text and visible reasoning on the request side.",
			DescriptionZh: "请求侧的会话文本与可见推理。",
		},
		{
			Point: hook.PointRequestToolDecl, Section: "request",
			Name: "Tool declaration", NameZh: "工具声明",
			Description:   "A declared tool's name, description and schema.",
			DescriptionZh: "已声明工具的名称、描述与 schema。",
		},
		{
			Point: hook.PointRequestToolCall, Section: "request",
			Name: "Tool call arguments (request)", NameZh: "工具调用参数（请求）",
			Description:   "Arguments of a model call carried in the assistant history.",
			DescriptionZh: "助手历史中携带的模型调用参数。",
		},
		{
			Point: hook.PointRequestToolResult, Section: "request",
			Name: "Tool result (request)", NameZh: "工具结果（请求）",
			Description:   "The client's answer to a call, including nested content parts.",
			DescriptionZh: "客户端对调用的回答，包括嵌套内容块。",
		},
		{
			Point: hook.PointRequestMetadata, Section: "request",
			Name: "Request metadata", NameZh: "请求元数据",
			Description:   "The user identifier and metadata map.",
			DescriptionZh: "user 标识与 metadata 映射。",
			Reserved:      true,
		},
		{
			Point: hook.PointResponseOutput, Section: "response",
			Name: "Response output", NameZh: "响应输出",
			Description:   "Text-like output parts: text, refusal, citation, search.",
			DescriptionZh: "文本类输出内容块：文本、拒答、引用、搜索。",
		},
		{
			Point: hook.PointResponseReasoning, Section: "response",
			Name: "Response reasoning", NameZh: "响应推理",
			Description:   "Visible model reasoning in the response.",
			DescriptionZh: "响应中的可见模型推理。",
		},
		{
			Point: hook.PointResponseToolCall, Section: "response",
			Name: "Tool call arguments (response)", NameZh: "工具调用参数（响应）",
			Description:   "Arguments of a call the model just issued.",
			DescriptionZh: "模型刚发起的调用的参数。",
		},
		{
			Point: hook.PointResponseToolResult, Section: "response",
			Name: "Tool result (response)", NameZh: "工具结果（响应）",
			Description:   "A hosted-tool result surfaced in the response.",
			DescriptionZh: "响应中呈现的托管工具结果。",
		},
		{
			Point: hook.PointResponseUsage, Section: "response",
			Name: "Usage", NameZh: "用量",
			Description:   "The normalized token usage of the response.",
			DescriptionZh: "响应归一化后的 token 用量。",
			Reserved:      true,
		},
		{
			Point: hook.PointResponseError, Section: "response",
			Name: "Error", NameZh: "错误",
			Description:   "The normalized provider/proxy error.",
			DescriptionZh: "归一化的提供方/代理错误。",
			Reserved:      true,
		},
		{
			Point: hook.PointStreamEvent, Section: "stream",
			Name: "Stream event", NameZh: "流式事件",
			Description:   "The envelope of any streamed event, including lifecycle frames.",
			DescriptionZh: "任意流式事件的信封，包括生命周期帧。",
			Reserved:      true,
		},
		{
			Point: hook.PointStreamTextDelta, Section: "stream",
			Name: "Text delta", NameZh: "文本增量",
			Description:   "An incremental run of answer text.",
			DescriptionZh: "回答文本的增量片段。",
			Reserved:      true,
		},
		{
			Point: hook.PointStreamReasonDelta, Section: "stream",
			Name: "Reasoning delta", NameZh: "推理增量",
			Description:   "An incremental run of visible reasoning.",
			DescriptionZh: "可见推理的增量片段。",
			Reserved:      true,
		},
		{
			Point: hook.PointStreamToolArgs, Section: "stream",
			Name: "Tool args delta", NameZh: "工具参数增量",
			Description:   "An incremental run of tool-call arguments.",
			DescriptionZh: "工具调用参数的增量片段。",
			Reserved:      true,
		},
		{
			Point: hook.PointExchangeFinalize, Section: "exchange",
			Name: "Exchange finalize", NameZh: "交换收尾",
			Description:   "The whole exchange after the response completes; the reserved place for cross-cutting hooks.",
			DescriptionZh: "响应完成后的整个交换；横切 hook 的预留位置。",
			Reserved:      true,
		},
	}
}
