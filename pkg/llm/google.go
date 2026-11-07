package llm

import "encoding/json"

// ========== Google Gemini generateContent 映射 ==========

type GeminiPart struct {
	Text string `json:"text,omitempty"`
	// Thought marks a part that carries the model's internal reasoning instead of answer text.
	Thought bool `json:"thought,omitempty"`
	// FunctionCall carries a complete tool call: Gemini sends the name and an argument object in one
	// part, unlike the OpenAI-compatible stream which sends arguments incrementally.
	FunctionCall *GeminiFunctionCall `json:"functionCall,omitempty"`
	// FunctionResponse answers a function call. Messages and Chat express the
	// same thing as a tool_result block / role "tool" message.
	FunctionResponse *GeminiFunctionResponse `json:"functionResponse,omitempty"`
}

type GeminiFunctionCall struct {
	Name string          `json:"name,omitempty"`
	Args json.RawMessage `json:"args,omitempty"`
}

type GeminiFunctionResponse struct {
	Name     string         `json:"name,omitempty"`
	Response map[string]any `json:"response,omitempty"`
}

// argsText renders the argument object the way the OpenAI-compatible surface spells it: a compact JSON
// string, so one cassette shape covers every protocol family.
func (call *GeminiFunctionCall) argsText() string {
	if call == nil || len(call.Args) == 0 {
		return ""
	}
	return marshalCompactString(call.Args)
}

type GeminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []GeminiPart `json:"parts"`
}

type GeminiSafetySetting struct {
	Category  string `json:"category"`
	Threshold string `json:"threshold"`
}

type GeminiGenerationConfig struct {
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"topP,omitempty"`
	TopK            *int     `json:"topK,omitempty"`
	MaxOutputTokens *int     `json:"maxOutputTokens,omitempty"`
	StopSequences   []string `json:"stopSequences,omitempty"`
}

type GeminiToolFunctionDeclaration struct {
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Parameters  JSONSchema `json:"parameters,omitempty"`
}

type GeminiTool struct {
	FunctionDeclarations []GeminiToolFunctionDeclaration `json:"functionDeclarations,omitempty"`
}

// GeminiFunctionCallingConfig is the Gemini spelling of tool_choice:
// AUTO / NONE / ANY plus an optional allow-list of function names.
type GeminiFunctionCallingConfig struct {
	Mode                 string   `json:"mode,omitempty"`
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

type GeminiToolConfig struct {
	FunctionCallingConfig *GeminiFunctionCallingConfig `json:"functionCallingConfig,omitempty"`
}

type GeminiGenerateContentRequest struct {
	Contents          []GeminiContent         `json:"contents"`
	SystemInstruction *GeminiContent          `json:"systemInstruction,omitempty"`
	GenerationConfig  *GeminiGenerationConfig `json:"generationConfig,omitempty"`
	SafetySettings    []GeminiSafetySetting   `json:"safetySettings,omitempty"`
	Tools             []GeminiTool            `json:"tools,omitempty"`
	ToolConfig        *GeminiToolConfig       `json:"toolConfig,omitempty"`
}

type GeminiSafetyRating struct {
	Category    string `json:"category"`
	Probability string `json:"probability"`
	Blocked     bool   `json:"blocked"`
}

type GeminiCandidate struct {
	Content       GeminiContent        `json:"content"`
	FinishReason  string               `json:"finishReason"`
	SafetyRatings []GeminiSafetyRating `json:"safetyRatings,omitempty"`
}

type GeminiUsageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

type GeminiResponse struct {
	Candidates     []GeminiCandidate    `json:"candidates"`
	PromptFeedback map[string]any       `json:"promptFeedback,omitempty"`
	UsageMetadata  *GeminiUsageMetadata `json:"usageMetadata,omitempty"`
}

// ---- LLMRequest -> GeminiGenerateContentRequest ----

func (r *LLMRequest) ToGemini() GeminiGenerateContentRequest {
	// A functionResponse can only be named after the call it answers, and the
	// IR keeps the name on the call, not on the result. Resolve it first.
	toolNames := make(map[string]string)
	for i := range r.Messages {
		for j := range r.Messages[i].Content {
			c := &r.Messages[i].Content[j]
			if c.isToolUse() {
				id := firstNonEmpty(c.ToolCallID, c.ID)
				if id != "" && c.ToolName != "" {
					toolNames[id] = c.ToolName
				}
			}
		}
	}

	contents := make([]GeminiContent, 0, len(r.Messages))
	for i := range r.Messages {
		m := &r.Messages[i]
		role := geminiRole(m.Role)
		parts := make([]GeminiPart, 0, len(m.Content))
		for j := range m.Content {
			c := &m.Content[j]
			switch c.Type {
			case "text":
				if c.Text != "" {
					parts = append(parts, GeminiPart{Text: c.Text})
				}
			case "tool_use":
				args := c.ToolArgs
				if args == nil {
					args = map[string]any{}
				}
				raw, err := json.Marshal(args)
				if err != nil {
					raw = []byte("{}")
				}
				parts = append(parts, GeminiPart{FunctionCall: &GeminiFunctionCall{
					Name: c.ToolName,
					Args: raw,
				}})
			case "tool_result":
				name := firstNonEmpty(c.ToolName, toolNames[firstNonEmpty(c.ToolCallID, c.ID)])
				if name == "" {
					// Gemini cannot name the response, so the request would be
					// rejected upstream. Drop it and let the caller see the
					// missing tool name as an unsupported shape at conversion
					// time instead of an opaque 400.
					continue
				}
				value := ToolResultValue(*c)
				if value == nil {
					value = ""
				}
				parts = append(parts, GeminiPart{FunctionResponse: &GeminiFunctionResponse{
					Name:     name,
					Response: map[string]any{"result": value},
				}})
			}
		}
		if len(parts) == 0 {
			continue
		}
		// Gemini expects an alternating user/model conversation. Merge
		// consecutive turns with the same role; an Anthropic history may have
		// several user turns in a row.
		if len(contents) > 0 && contents[len(contents)-1].Role == role {
			contents[len(contents)-1].Parts = append(contents[len(contents)-1].Parts, parts...)
			continue
		}
		contents = append(contents, GeminiContent{Role: role, Parts: parts})
	}

	var sys *GeminiContent
	if len(r.System) > 0 {
		parts := make([]GeminiPart, 0, len(r.System))
		for i := range r.System {
			c := &r.System[i]
			if c.Type == "text" && c.Text != "" {
				parts = append(parts, GeminiPart{Text: c.Text})
			}
		}
		if len(parts) > 0 {
			// systemInstruction is a Content but not a conversation turn; Gemini
			// documents it without a role (the field is optional).
			sys = &GeminiContent{Parts: parts}
		}
	}

	var genCfg *GeminiGenerationConfig
	if r.Temperature != nil || r.TopP != nil || r.TopK != nil || r.MaxTokens != nil || len(r.StopSeq) > 0 {
		genCfg = &GeminiGenerationConfig{
			Temperature:     r.Temperature,
			TopP:            r.TopP,
			TopK:            r.TopK,
			MaxOutputTokens: r.MaxTokens,
			StopSequences:   r.StopSeq,
		}
	}

	safety := make([]GeminiSafetySetting, 0, len(r.SafetySettings))
	for i := range r.SafetySettings {
		s := &r.SafetySettings[i]
		safety = append(safety, GeminiSafetySetting{
			Category:  s.Category,
			Threshold: s.Threshold,
		})
	}

	tools := make([]GeminiTool, 0, 1)
	if len(r.Tools) > 0 {
		fd := make([]GeminiToolFunctionDeclaration, 0, len(r.Tools))
		for i := range r.Tools {
			t := &r.Tools[i]
			fd = append(fd, GeminiToolFunctionDeclaration{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			})
		}
		tools = append(tools, GeminiTool{FunctionDeclarations: fd})
	}

	return GeminiGenerateContentRequest{
		Contents:          contents,
		SystemInstruction: sys,
		GenerationConfig:  genCfg,
		SafetySettings:    safety,
		Tools:             tools,
		ToolConfig:        geminiToolConfig(r.ToolChoice),
	}
}

func geminiRole(role string) string {
	if canonicalRole(role) == "assistant" {
		return "model"
	}
	return "user"
}

func geminiToolConfig(choice *ToolChoice) *GeminiToolConfig {
	if choice == nil {
		return nil
	}
	config := &GeminiFunctionCallingConfig{}
	switch choice.Mode {
	case "auto":
		config.Mode = "AUTO"
	case "none":
		config.Mode = "NONE"
	case "required":
		config.Mode = "ANY"
	case "tool":
		if choice.Name == "" {
			return nil
		}
		config.Mode = "ANY"
		config.AllowedFunctionNames = []string{choice.Name}
	default:
		return nil
	}
	return &GeminiToolConfig{FunctionCallingConfig: config}
}

// ---- GeminiGenerateContentRequest -> LLMRequest ----

func fromGenerateContentRequest(req GeminiGenerateContentRequest) LLMRequest {
	llmReq := LLMRequest{
		Model: "", // Gemini model name is usually in URL, not body
	}

	// ---- System ----
	if req.SystemInstruction != nil {
		sys := req.SystemInstruction
		llmReq.System = make([]LLMContent, 0, len(sys.Parts))
		for i := range sys.Parts {
			p := &sys.Parts[i]
			if p.Text == "" {
				continue
			}
			llmReq.System = append(llmReq.System, LLMContent{
				Type: "text",
				Text: p.Text,
			})
		}
	}

	// ---- Messages ----
	llmReq.Messages = make([]LLMMessage, 0, len(req.Contents))
	for i := range req.Contents {
		c := &req.Contents[i]

		contents := make([]LLMContent, 0, len(c.Parts))
		for j := range c.Parts {
			p := &c.Parts[j]
			if p.Text == "" {
				continue
			}
			contents = append(contents, LLMContent{
				Type: "text",
				Text: p.Text,
			})
		}

		llmReq.Messages = append(llmReq.Messages, LLMMessage{
			Role:    c.Role,
			Content: contents,
		})
	}

	// ---- GenerationConfig ----
	if req.GenerationConfig != nil {
		cfg := req.GenerationConfig
		llmReq.Temperature = cfg.Temperature
		llmReq.TopP = cfg.TopP
		llmReq.TopK = cfg.TopK
		llmReq.MaxTokens = cfg.MaxOutputTokens
		llmReq.StopSeq = cfg.StopSequences
	}

	// ---- Safety ----
	llmReq.SafetySettings = make([]LLMSafetyConfig, 0, len(req.SafetySettings))
	for i := range req.SafetySettings {
		s := &req.SafetySettings[i]
		llmReq.SafetySettings = append(llmReq.SafetySettings, LLMSafetyConfig{
			Category:  s.Category,
			Threshold: s.Threshold,
		})
	}

	// ---- Tools ----
	llmReq.Tools = make([]LLMTool, 0, len(req.Tools))
	for i := range req.Tools {
		t := &req.Tools[i]
		for j := range t.FunctionDeclarations {
			fd := &t.FunctionDeclarations[j]
			llmReq.Tools = append(llmReq.Tools, LLMTool{
				Name:        fd.Name,
				Description: fd.Description,
				Parameters:  fd.Parameters,
			})
		}
	}

	// ---- Tool choice ----
	if req.ToolConfig != nil && req.ToolConfig.FunctionCallingConfig != nil {
		cfg := req.ToolConfig.FunctionCallingConfig
		switch cfg.Mode {
		case "AUTO":
			llmReq.ToolChoice = &ToolChoice{Mode: "auto"}
		case "NONE":
			llmReq.ToolChoice = &ToolChoice{Mode: "none"}
		case "ANY":
			if len(cfg.AllowedFunctionNames) == 1 {
				llmReq.ToolChoice = &ToolChoice{Mode: "tool", Name: cfg.AllowedFunctionNames[0]}
			} else {
				llmReq.ToolChoice = &ToolChoice{Mode: "required"}
			}
		}
	}

	return llmReq
}

// ---- GeminiResponse -> LLMResponse ----

func GeminiToLLM(resp GeminiResponse) LLMResponse {
	return fromGenerateContentResponse(resp)
}

func fromGenerateContentResponse(resp GeminiResponse) LLMResponse {
	cands := getCandidateSlice()
	safetyAll := make([]LLMSafetyRating, 0, len(resp.Candidates))

	for i := range resp.Candidates {
		c := &resp.Candidates[i]

		content := getContentSlice()
		toolCalls := make([]LLMToolCall, 0, len(c.Content.Parts))
		for j := range c.Content.Parts {
			p := &c.Content.Parts[j]
			switch {
			case p.FunctionCall != nil:
				// Gemini delivers a whole call in one part. Dropping it here (the part only carried
				// `text` before) left the parsed response and the cassette events without the tool
				// call while the observation parser recorded one.
				toolCalls = append(toolCalls, LLMToolCall{
					Type:     "function",
					Name:     p.FunctionCall.Name,
					ArgsText: p.FunctionCall.argsText(),
				})
			case p.FunctionResponse != nil:
				content = append(content, LLMContent{
					Type:     "tool_result",
					ToolName: p.FunctionResponse.Name,
					Text:     marshalCompactString(p.FunctionResponse.Response),
				})
			case p.Thought:
				// The observation parser classifies `thought` parts as reasoning nodes; mapping them
				// to "text" made the same bytes read as answer text everywhere else.
				content = append(content, LLMContent{
					Type: "thinking",
					Text: p.Text,
				})
			default:
				content = append(content, LLMContent{
					Type: "text",
					Text: p.Text,
				})
			}
		}

		safety := make([]LLMSafetyRating, 0, len(c.SafetyRatings))
		for j := range c.SafetyRatings {
			s := &c.SafetyRatings[j]
			sr := LLMSafetyRating{
				Category:    s.Category,
				Probability: s.Probability,
				Blocked:     s.Blocked,
			}
			safety = append(safety, sr)
			safetyAll = append(safetyAll, sr)
		}

		cands = append(cands, LLMCandidate{
			Index:        i,
			Role:         c.Content.Role,
			Content:      content,
			ToolCalls:    toolCalls,
			FinishReason: c.FinishReason,
			Extensions: map[string]any{
				"safety_ratings": safety,
			},
		})
	}

	var usage *LLMUsage
	if resp.UsageMetadata != nil {
		u := LLMUsage{
			InputTokens:  resp.UsageMetadata.PromptTokenCount,
			OutputTokens: resp.UsageMetadata.CandidatesTokenCount,
			TotalTokens:  resp.UsageMetadata.TotalTokenCount,
		}
		usage = &u
	}

	if len(cands) == 0 {
		if refusal := geminiPromptFeedbackRefusal(resp.PromptFeedback); refusal != nil {
			cands = append(cands, LLMCandidate{
				Index:   0,
				Role:    "model",
				Refusal: refusal,
			})
		}
	}

	return LLMResponse{
		Candidates: cands,
		Usage:      usage,
		Safety:     safetyAll,
		Extensions: map[string]any{
			"prompt_feedback": resp.PromptFeedback,
		},
	}
}

func geminiPromptFeedbackRefusal(promptFeedback map[string]any) *LLMRefusal {
	if len(promptFeedback) == 0 {
		return nil
	}
	reason := "blocked"
	if blockReason, ok := promptFeedback["blockReason"].(string); ok && blockReason != "" {
		reason = blockReason
	}
	message := marshalCompactString(promptFeedback)
	if message == "" {
		message = reason
	}
	return &LLMRefusal{
		Reason:  reason,
		Message: message,
	}
}

// ---- LLMResponse -> GeminiResponse ----

func (r *LLMResponse) toGenerateContentResponse() GeminiResponse {
	cands := make([]GeminiCandidate, 0, len(r.Candidates))

	for i := range r.Candidates {
		c := &r.Candidates[i]
		parts := make([]GeminiPart, 0, len(c.Content)+len(c.ToolCalls))
		if text := c.ResponseText(); text != "" {
			parts = append(parts, GeminiPart{Text: text})
		}
		for _, call := range c.ResponseToolCalls() {
			args := call.Args
			if args == nil && call.ArgsText != "" {
				_ = json.Unmarshal([]byte(call.ArgsText), &args)
			}
			raw, err := json.Marshal(args)
			if err != nil || args == nil {
				raw = []byte("{}")
			}
			parts = append(parts, GeminiPart{FunctionCall: &GeminiFunctionCall{
				Name: call.Name,
				Args: raw,
			}})
		}
		if len(parts) == 0 && c.Refusal != nil && c.Refusal.Message != "" {
			parts = append(parts, GeminiPart{Text: c.Refusal.Message})
		}

		cands = append(cands, GeminiCandidate{
			Content: GeminiContent{
				Role:  "model",
				Parts: parts,
			},
			FinishReason: c.FinishReason,
		})
	}

	var usage *GeminiUsageMetadata
	if r.Usage != nil {
		usage = &GeminiUsageMetadata{
			PromptTokenCount:     r.Usage.InputTokens,
			CandidatesTokenCount: r.Usage.OutputTokens,
			TotalTokenCount:      r.Usage.TotalTokens,
		}
	}

	return GeminiResponse{
		Candidates:    cands,
		UsageMetadata: usage,
	}
}
