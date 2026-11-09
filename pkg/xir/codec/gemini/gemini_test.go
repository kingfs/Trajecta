package gemini

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/pkg/usage"
	"github.com/kingfs/Trajecta/pkg/xir"
)

const requestFixture = `{
  "model": "gemini-2.5-pro",
  "systemInstruction": {"parts": [{"text": "You are helpful."}]},
  "contents": [
    {"role": "user", "parts": [
      {"text": "What is the weather?"},
      {"inlineData": {"mimeType": "image/png", "data": "aGVsbG8="}}
    ]},
    {"role": "model", "parts": [
      {"functionCall": {"name": "get_weather", "args": {"city": "Paris"}}, "thoughtSignature": "call-sig"},
      {"text": "Looking up", "thought": true, "thoughtSignature": "thought-sig"}
    ]}
  ],
  "tools": [{"functionDeclarations": [{
    "name": "get_weather",
    "description": "Look up the weather",
    "parameters": {"type": "object", "properties": {"city": {"type": "string"}}}
  }]}],
  "toolConfig": {"functionCallingConfig": {"mode": "ANY", "allowedFunctionNames": ["get_weather"]}},
  "generationConfig": {
    "temperature": 0.7,
    "topP": 0.9,
    "topK": 40,
    "maxOutputTokens": 1024,
    "stopSequences": ["END"],
    "responseMimeType": "application/json",
    "responseSchema": {"type": "object"},
    "seed": 42,
    "presencePenalty": 0.1,
    "frequencyPenalty": 0.2,
    "thinkingConfig": {"thinkingBudget": 512, "includeThoughts": true, "thinkingLevel": "HIGH"},
    "candidateCount": 2
  },
  "safetySettings": [{"category": "HARM_CATEGORY_HATE_SPEECH", "threshold": "BLOCK_MEDIUM_AND_ABOVE"}]
}`

const responseFixture = `{
  "candidates": [{
    "content": {"role": "model", "parts": [
      {"text": "Hello"},
      {"functionCall": {"name": "get_weather", "args": {"city": "Paris"}}, "thoughtSignature": "call-sig"},
      {"text": "I thought about it", "thought": true, "thoughtSignature": "thought-sig"}
    ]},
    "finishReason": "STOP"
  }],
  "usageMetadata": {
    "promptTokenCount": 10,
    "candidatesTokenCount": 20,
    "totalTokenCount": 35,
    "cachedContentTokenCount": 3,
    "thoughtsTokenCount": 5
  },
  "modelVersion": "gemini-2.5-pro",
  "responseId": "response-1"
}`

func mustDoc(t *testing.T, protocol xir.Protocol, operation xir.Operation, body string) *xir.Document {
	t.Helper()
	doc, err := xir.ParseDocument(protocol, operation, []byte(body))
	if err != nil {
		t.Fatalf("ParseDocument: %v", err)
	}
	return doc
}

func decodeRequest(t *testing.T, codec Codec, doc *xir.Document) *xir.Request {
	t.Helper()
	req, err := codec.DecodeRequest(context.Background(), doc, xir.DecodeOptions{})
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	return req
}

func decodeResponse(t *testing.T, codec Codec, doc *xir.Document) *xir.Response {
	t.Helper()
	resp, err := codec.DecodeResponse(context.Background(), doc, xir.DecodeOptions{})
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	return resp
}

func encodeRequest(t *testing.T, codec Codec, req *xir.Request) []byte {
	t.Helper()
	raw, err := codec.EncodeRequest(context.Background(), req, xir.EncodeOptions{})
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	return raw
}

func encodeResponse(t *testing.T, codec Codec, resp *xir.Response) []byte {
	t.Helper()
	raw, err := codec.EncodeResponse(context.Background(), resp, xir.EncodeOptions{})
	if err != nil {
		t.Fatalf("EncodeResponse: %v", err)
	}
	return raw
}

func jsonObject(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return object
}

func TestDecodeRequest(t *testing.T) {
	codec := Codec{}
	req := decodeRequest(t, codec, mustDoc(t, xir.ProtocolGemini, xir.OperationGenerateContent, requestFixture))

	if req.Model != "gemini-2.5-pro" {
		t.Fatalf("model = %q", req.Model)
	}
	if req.Ref == nil {
		t.Fatal("request ref is nil")
	}
	if len(req.System) != 1 || req.System[0].Kind != xir.PartText || req.System[0].Text != "You are helpful." {
		t.Fatalf("system = %+v", req.System)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("messages = %d", len(req.Messages))
	}
	if req.Messages[0].Role != xir.RoleUser || req.Messages[0].Ref == nil {
		t.Fatalf("user message = %+v", req.Messages[0])
	}
	if len(req.Messages[0].Parts) != 2 {
		t.Fatalf("user parts = %d", len(req.Messages[0].Parts))
	}
	if req.Messages[0].Parts[0].Kind != xir.PartText || req.Messages[0].Parts[0].Text != "What is the weather?" {
		t.Fatalf("user text = %+v", req.Messages[0].Parts[0])
	}
	image := req.Messages[0].Parts[1]
	if image.Kind != xir.PartImage || image.Media == nil || image.Media.Data != "aGVsbG8=" || image.Media.MediaType != "image/png" {
		t.Fatalf("image = %+v media=%+v", image, image.Media)
	}
	if req.Messages[1].Role != xir.RoleModel {
		t.Fatalf("model role = %q", req.Messages[1].Role)
	}

	callPart := req.Messages[1].Parts[0]
	if callPart.Kind != xir.PartToolCall || callPart.ToolCall == nil || callPart.ToolCall.Name != "get_weather" {
		t.Fatalf("tool call = %+v", callPart)
	}
	if string(callPart.ToolCall.Args) != `{"city": "Paris"}` && string(callPart.ToolCall.Args) != `{"city":"Paris"}` {
		t.Fatalf("args = %s", callPart.ToolCall.Args)
	}
	if callPart.ToolCall.ArgsText != `{"city":"Paris"}` {
		t.Fatalf("args text = %q", callPart.ToolCall.ArgsText)
	}
	if callPart.Reasoning == nil || callPart.Reasoning.Signature != "call-sig" ||
		callPart.Reasoning.Sealed == nil || callPart.Reasoning.Sealed.Protocol != xir.ProtocolGemini ||
		callPart.Reasoning.Sealed.Kind != "thoughtSignature" {
		t.Fatalf("call reasoning = %+v", callPart.Reasoning)
	}
	if callPart.Sealed == nil || callPart.Sealed.Protocol != xir.ProtocolGemini {
		t.Fatalf("call sealed = %+v", callPart.Sealed)
	}

	thoughtPart := req.Messages[1].Parts[1]
	if thoughtPart.Kind != xir.PartReasoning || thoughtPart.Text != "Looking up" {
		t.Fatalf("thought part = %+v", thoughtPart)
	}
	if thoughtPart.Reasoning == nil || thoughtPart.Reasoning.Text != "Looking up" ||
		thoughtPart.Reasoning.Signature != "thought-sig" ||
		thoughtPart.Reasoning.Sealed == nil || thoughtPart.Reasoning.Sealed.Kind != "thought" {
		t.Fatalf("thought reasoning = %+v", thoughtPart.Reasoning)
	}

	if len(req.Tools) != 1 || req.Tools[0].Name != "get_weather" || req.Tools[0].Kind != "function" ||
		req.Tools[0].Description != "Look up the weather" || req.Tools[0].Ref == nil {
		t.Fatalf("tools = %+v", req.Tools)
	}
	if !json.Valid(req.Tools[0].Schema) {
		t.Fatalf("schema = %s", req.Tools[0].Schema)
	}
	if req.ToolChoice.Mode != "tool" || req.ToolChoice.Name != "get_weather" {
		t.Fatalf("tool choice = %+v", req.ToolChoice)
	}

	if req.Params.Temperature == nil || *req.Params.Temperature != 0.7 {
		t.Fatalf("temperature = %v", req.Params.Temperature)
	}
	if req.Params.TopP == nil || *req.Params.TopP != 0.9 {
		t.Fatalf("topP = %v", req.Params.TopP)
	}
	if req.Params.TopK == nil || *req.Params.TopK != 40 {
		t.Fatalf("topK = %v", req.Params.TopK)
	}
	if req.Params.MaxTokens == nil || *req.Params.MaxTokens != 1024 {
		t.Fatalf("maxOutputTokens = %v", req.Params.MaxTokens)
	}
	if !reflect.DeepEqual(req.Params.Stop, []string{"END"}) {
		t.Fatalf("stop = %v", req.Params.Stop)
	}
	if req.Params.Seed == nil || *req.Params.Seed != 42 {
		t.Fatalf("seed = %v", req.Params.Seed)
	}
	if req.Params.PresencePenalty == nil || *req.Params.PresencePenalty != 0.1 {
		t.Fatalf("presencePenalty = %v", req.Params.PresencePenalty)
	}
	if req.Params.FrequencyPenalty == nil || *req.Params.FrequencyPenalty != 0.2 {
		t.Fatalf("frequencyPenalty = %v", req.Params.FrequencyPenalty)
	}
	if req.Params.ThinkingBudget == nil || *req.Params.ThinkingBudget != 512 {
		t.Fatalf("thinkingBudget = %v", req.Params.ThinkingBudget)
	}
	if req.Params.Thinking == nil || !*req.Params.Thinking {
		t.Fatalf("includeThoughts = %v", req.Params.Thinking)
	}
	if req.Params.ReasoningEffort != "HIGH" {
		t.Fatalf("thinkingLevel = %q", req.Params.ReasoningEffort)
	}

	var mime string
	if err := json.Unmarshal(req.Params.Extra[ParamResponseMimeType], &mime); err != nil || mime != "application/json" {
		t.Fatalf("responseMimeType extra = %s (%v)", req.Params.Extra[ParamResponseMimeType], err)
	}
	if !json.Valid(req.Params.Extra[ParamResponseSchema]) {
		t.Fatalf("responseSchema extra = %s", req.Params.Extra[ParamResponseSchema])
	}
	if string(req.Params.Extra["candidateCount"]) != "2" {
		t.Fatalf("candidateCount extra = %s", req.Params.Extra["candidateCount"])
	}
	if string(req.Params.Extra["thinkingConfig.thinkingLevel"]) != `"HIGH"` {
		t.Fatalf("thinkingLevel extra = %s", req.Params.Extra["thinkingConfig.thinkingLevel"])
	}

	extension, err := extensionObject(req.Extensions)
	if err != nil {
		t.Fatal(err)
	}
	safety, ok := extension["safetySettings"]
	if !ok || !json.Valid(safety) || !strings.HasPrefix(string(safety), "[") {
		t.Fatalf("safety = %s", safety)
	}
}

func TestDecodeRequestMetaAndModelPath(t *testing.T) {
	codec := Codec{}
	ctx := context.Background()

	doc := mustDoc(t, xir.ProtocolGemini, xir.OperationGenerateContent, `{}`)
	req, err := codec.DecodeRequestWithMeta(ctx, doc, xir.DecodeOptions{}, RequestMeta{
		Path:   "/v1beta/models/gemini-2.0-flash:streamGenerateContent?alt=sse",
		Stream: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.Model != "gemini-2.0-flash" || !req.Stream {
		t.Fatalf("model=%q stream=%v", req.Model, req.Stream)
	}

	// A model in the body wins over the path and explicit meta.
	doc = mustDoc(t, xir.ProtocolGemini, xir.OperationGenerateContent, `{"model":"body-model"}`)
	req, err = codec.DecodeRequestWithMeta(ctx, doc, xir.DecodeOptions{}, RequestMeta{
		Path:  "/v1beta/models/path-model:generateContent",
		Model: "explicit-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.Model != "body-model" {
		t.Fatalf("model = %q", req.Model)
	}

	// An explicit meta model wins over the path.
	doc = mustDoc(t, xir.ProtocolGemini, xir.OperationGenerateContent, `{}`)
	req, err = codec.DecodeRequestWithMeta(ctx, doc, xir.DecodeOptions{}, RequestMeta{
		Path:  "/v1beta/models/path-model:generateContent",
		Model: "explicit-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.Model != "explicit-model" {
		t.Fatalf("model = %q", req.Model)
	}

	// A body model on a request with no meta model comes from the body.
	req = decodeRequest(t, codec, mustDoc(t, xir.ProtocolGemini, xir.OperationGenerateContent, `{"model":"body-only"}`))
	if req.Model != "body-only" {
		t.Fatalf("model = %q", req.Model)
	}
}

func TestModelFromPath(t *testing.T) {
	cases := map[string]string{
		"/v1beta/models/gemini-2.0-flash:generateContent":                                                  "gemini-2.0-flash",
		"/v1/models/gemini-pro:streamGenerateContent?alt=sse":                                              "gemini-pro",
		"/v1beta/projects/p/locations/us-central1/publishers/google/models/gemini-2.5-pro:generateContent": "gemini-2.5-pro",
		"/v1beta/models/gemini-2.0-flash%3AgenerateContent":                                                "gemini-2.0-flash",
		"models/gemini-2.0-flash":                                                                          "gemini-2.0-flash",
		"/v1beta/tunedModels/my-model:generateContent":                                                     "my-model",
		"gemini-2.0-flash": "gemini-2.0-flash",
		"/v1beta/models/":  "",
		"/health":          "",
		"":                 "",
	}
	for path, want := range cases {
		if got := ModelFromPath(path); got != want {
			t.Fatalf("ModelFromPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestRequestRoundTrip(t *testing.T) {
	codec := Codec{}
	first := decodeRequest(t, codec, mustDoc(t, xir.ProtocolGemini, xir.OperationGenerateContent, requestFixture))
	encoded := encodeRequest(t, codec, first)

	object := jsonObject(t, encoded)
	if _, ok := object["model"]; ok {
		t.Fatalf("request body must not carry a model member: %s", encoded)
	}
	contents, ok := object["contents"].([]any)
	if !ok || len(contents) != 2 {
		t.Fatalf("contents = %#v", object["contents"])
	}
	firstContent := contents[0].(map[string]any)
	parts := firstContent["parts"].([]any)
	inline := parts[1].(map[string]any)["inlineData"].(map[string]any)
	if inline["mimeType"] != "image/png" || inline["data"] != "aGVsbG8=" {
		t.Fatalf("inlineData = %#v", inline)
	}
	if _, ok := parts[1].(map[string]any)["inline_data"]; ok {
		t.Fatalf("inline_data key emitted: %s", encoded)
	}
	modelParts := contents[1].(map[string]any)["parts"].([]any)
	call := modelParts[0].(map[string]any)["functionCall"].(map[string]any)
	if _, ok := call["args"].(map[string]any); !ok {
		t.Fatalf("functionCall args is not an object: %#v", call["args"])
	}
	generation := object["generationConfig"].(map[string]any)
	if generation["responseMimeType"] != "application/json" {
		t.Fatalf("responseMimeType = %#v", generation["responseMimeType"])
	}
	if _, ok := generation["responseSchema"].(map[string]any); !ok {
		t.Fatalf("responseSchema = %#v", generation["responseSchema"])
	}
	if generation["candidateCount"] != float64(2) {
		t.Fatalf("candidateCount = %#v", generation["candidateCount"])
	}
	thinking := generation["thinkingConfig"].(map[string]any)
	if thinking["thinkingBudget"] != float64(512) || thinking["includeThoughts"] != true || thinking["thinkingLevel"] != "HIGH" {
		t.Fatalf("thinkingConfig = %#v", thinking)
	}
	tools := object["tools"].([]any)
	decl := tools[0].(map[string]any)["functionDeclarations"].([]any)[0].(map[string]any)
	if decl["name"] != "get_weather" || decl["description"] != "Look up the weather" {
		t.Fatalf("declaration = %#v", decl)
	}
	toolConfig := object["toolConfig"].(map[string]any)["functionCallingConfig"].(map[string]any)
	if toolConfig["mode"] != "ANY" || !reflect.DeepEqual(toolConfig["allowedFunctionNames"], []any{"get_weather"}) {
		t.Fatalf("toolConfig = %#v", toolConfig)
	}
	if _, ok := object["safetySettings"].([]any); !ok {
		t.Fatalf("safetySettings = %#v", object["safetySettings"])
	}

	// Decode the encoded body and encode it again. The second body must be
	// semantically identical to the first.
	second := decodeRequestWithPath(t, codec, encoded, "/v1beta/models/gemini-2.5-pro:generateContent")
	reencoded := encodeRequest(t, codec, second)
	if !reflect.DeepEqual(jsonObject(t, encoded), jsonObject(t, reencoded)) {
		t.Fatalf("request round trip differs:\nfirst:  %s\nsecond: %s", encoded, reencoded)
	}
	if second.Model != "gemini-2.5-pro" {
		t.Fatalf("round trip model = %q", second.Model)
	}
}

func decodeRequestWithPath(t *testing.T, codec Codec, raw []byte, path string) *xir.Request {
	t.Helper()
	doc, err := xir.ParseDocument(xir.ProtocolGemini, xir.OperationGenerateContent, raw)
	if err != nil {
		t.Fatal(err)
	}
	req, err := codec.DecodeRequestWithPath(context.Background(), doc, xir.DecodeOptions{}, path)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestDecodeResponse(t *testing.T) {
	codec := Codec{}
	resp := decodeResponse(t, codec, mustDoc(t, xir.ProtocolGemini, xir.OperationGenerateContent, responseFixture))

	if resp.Model != "gemini-2.5-pro" || resp.ID != "response-1" {
		t.Fatalf("model=%q id=%q", resp.Model, resp.ID)
	}
	if resp.Ref == nil {
		t.Fatal("response ref is nil")
	}
	if len(resp.Parts) != 3 {
		t.Fatalf("parts = %d", len(resp.Parts))
	}
	if resp.Parts[0].Kind != xir.PartText || resp.Parts[0].Text != "Hello" {
		t.Fatalf("text = %+v", resp.Parts[0])
	}
	if resp.Parts[1].Kind != xir.PartToolCall || resp.Parts[1].ToolCall == nil || resp.Parts[1].ToolCall.Name != "get_weather" {
		t.Fatalf("tool call = %+v", resp.Parts[1])
	}
	if resp.Parts[1].Reasoning == nil || resp.Parts[1].Reasoning.Signature != "call-sig" {
		t.Fatalf("call reasoning = %+v", resp.Parts[1].Reasoning)
	}
	if resp.Parts[2].Kind != xir.PartReasoning || resp.Parts[2].Reasoning == nil ||
		resp.Parts[2].Reasoning.Signature != "thought-sig" || resp.Parts[2].Reasoning.Sealed.Kind != "thought" {
		t.Fatalf("reasoning = %+v", resp.Parts[2])
	}
	if resp.StopReason != xir.FinishToolUse {
		t.Fatalf("stop reason = %q", resp.StopReason)
	}
	if resp.Usage == nil {
		t.Fatal("usage is nil")
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 20 || resp.Usage.TotalTokens != 35 ||
		resp.Usage.CacheReadTokens != 3 || resp.Usage.ReasoningTokens != 5 || resp.Usage.Source != "gemini" {
		t.Fatalf("usage = %+v", resp.Usage)
	}
}

func TestResponseRoundTrip(t *testing.T) {
	codec := Codec{}
	first := decodeResponse(t, codec, mustDoc(t, xir.ProtocolGemini, xir.OperationGenerateContent, responseFixture))
	encoded := encodeResponse(t, codec, first)

	object := jsonObject(t, encoded)
	candidates := object["candidates"].([]any)
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v", candidates)
	}
	candidate := candidates[0].(map[string]any)
	if candidate["finishReason"] != "STOP" {
		t.Fatalf("finishReason = %#v", candidate["finishReason"])
	}
	content := candidate["content"].(map[string]any)
	if content["role"] != "model" {
		t.Fatalf("role = %#v", content["role"])
	}
	parts := content["parts"].([]any)
	if parts[0].(map[string]any)["text"] != "Hello" {
		t.Fatalf("parts = %#v", parts)
	}
	call := parts[1].(map[string]any)
	if call["thoughtSignature"] != "call-sig" {
		t.Fatalf("call signature = %#v", call)
	}
	args, ok := call["functionCall"].(map[string]any)["args"].(map[string]any)
	if !ok || args["city"] != "Paris" {
		t.Fatalf("call args = %#v", call["functionCall"])
	}
	thought := parts[2].(map[string]any)
	if thought["text"] != "I thought about it" || thought["thought"] != true || thought["thoughtSignature"] != "thought-sig" {
		t.Fatalf("thought part = %#v", thought)
	}
	usageMetadata := object["usageMetadata"].(map[string]any)
	if usageMetadata["promptTokenCount"] != float64(10) ||
		usageMetadata["candidatesTokenCount"] != float64(20) ||
		usageMetadata["totalTokenCount"] != float64(35) ||
		usageMetadata["cachedContentTokenCount"] != float64(3) ||
		usageMetadata["thoughtsTokenCount"] != float64(5) {
		t.Fatalf("usage = %#v", usageMetadata)
	}
	if object["modelVersion"] != "gemini-2.5-pro" || object["responseId"] != "response-1" {
		t.Fatalf("model/id = %#v / %#v", object["modelVersion"], object["responseId"])
	}

	second := decodeResponse(t, codec, mustDoc(t, xir.ProtocolGemini, xir.OperationGenerateContent, string(encoded)))
	reencoded := encodeResponse(t, codec, second)
	if !reflect.DeepEqual(jsonObject(t, encoded), jsonObject(t, reencoded)) {
		t.Fatalf("response round trip differs:\nfirst:  %s\nsecond: %s", encoded, reencoded)
	}
	if second.StopReason != xir.FinishToolUse {
		t.Fatalf("second stop reason = %q", second.StopReason)
	}
}

func TestThoughtAndSignature(t *testing.T) {
	codec := Codec{}
	body := `{"candidates":[{"content":{"parts":[
		{"text":"visible","thoughtSignature":"sig-only"},
		{"text":"hidden","thought":true,"thoughtSignature":"sig-thought"},
		{"thoughtSignature":"bare"}
	]},"finishReason":"STOP"}]}`
	resp := decodeResponse(t, codec, mustDoc(t, xir.ProtocolGemini, xir.OperationGenerateContent, body))
	if len(resp.Parts) != 3 {
		t.Fatalf("parts = %d", len(resp.Parts))
	}
	for i, part := range resp.Parts {
		if part.Kind != xir.PartReasoning || part.Reasoning == nil || part.Reasoning.Sealed == nil ||
			part.Reasoning.Sealed.Protocol != xir.ProtocolGemini {
			t.Fatalf("part %d = %+v", i, part)
		}
	}
	if resp.Parts[0].Text != "visible" || resp.Parts[0].Reasoning.Sealed.Kind != "thoughtSignature" {
		t.Fatalf("sig-only part = %+v", resp.Parts[0])
	}
	if resp.Parts[1].Text != "hidden" || resp.Parts[1].Reasoning.Sealed.Kind != "thought" {
		t.Fatalf("thought part = %+v", resp.Parts[1])
	}
	if resp.Parts[2].Text != "" || resp.Parts[2].Reasoning.Signature != "bare" {
		t.Fatalf("bare signature part = %+v", resp.Parts[2])
	}

	encoded := encodeResponse(t, codec, resp)
	parts := jsonObject(t, encoded)["candidates"].([]any)[0].(map[string]any)["content"].(map[string]any)["parts"].([]any)
	first := parts[0].(map[string]any)
	if first["text"] != "visible" || first["thoughtSignature"] != "sig-only" {
		t.Fatalf("sig-only encoded = %#v", first)
	}
	if _, ok := first["thought"]; ok {
		t.Fatalf("signature-only part gained thought: %#v", first)
	}
	second := parts[1].(map[string]any)
	if second["thought"] != true || second["thoughtSignature"] != "sig-thought" {
		t.Fatalf("thought encoded = %#v", second)
	}
	third := parts[2].(map[string]any)
	if third["thoughtSignature"] != "bare" {
		t.Fatalf("bare encoded = %#v", third)
	}
	if _, ok := third["text"]; ok {
		t.Fatalf("bare signature gained text: %#v", third)
	}
}

func TestInlineAndFileMedia(t *testing.T) {
	codec := Codec{}
	body := `{"contents":[{"role":"user","parts":[
		{"inlineData":{"mimeType":"image/jpeg","data":"aGVsbG8="}},
		{"fileData":{"mimeType":"application/pdf","fileUri":"gs://bucket/doc.pdf"}}
	]}]}`
	req := decodeRequest(t, codec, mustDoc(t, xir.ProtocolGemini, xir.OperationGenerateContent, body))
	if len(req.Messages) != 1 || len(req.Messages[0].Parts) != 2 {
		t.Fatalf("request = %+v", req)
	}
	image := req.Messages[0].Parts[0]
	if image.Kind != xir.PartImage || image.Media == nil || image.Media.Kind != xir.MediaImage ||
		image.Media.Data != "aGVsbG8=" || image.Media.MediaType != "image/jpeg" {
		t.Fatalf("image = %+v", image)
	}
	file := req.Messages[0].Parts[1]
	if file.Kind != xir.PartFile || file.Media == nil || file.Media.Kind != xir.MediaFile ||
		file.Media.URL != "gs://bucket/doc.pdf" || file.Media.MediaType != "application/pdf" {
		t.Fatalf("file = %+v", file)
	}

	encoded := encodeRequest(t, codec, req)
	parts := jsonObject(t, encoded)["contents"].([]any)[0].(map[string]any)["parts"].([]any)
	if _, ok := parts[0].(map[string]any)["inlineData"].(map[string]any); !ok {
		t.Fatalf("inlineData = %#v", parts[0])
	}
	fileData, ok := parts[1].(map[string]any)["fileData"].(map[string]any)
	if !ok || fileData["fileUri"] != "gs://bucket/doc.pdf" || fileData["mimeType"] != "application/pdf" {
		t.Fatalf("fileData = %#v", parts[1])
	}
	if strings.Contains(string(encoded), "inline_data") || strings.Contains(string(encoded), "file_data") {
		t.Fatalf("snake_case media key emitted: %s", encoded)
	}
}

func TestFunctionCallingRequestAndResult(t *testing.T) {
	codec := Codec{}
	body := `{
		"contents":[
			{"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"city":"Paris"}}}]},
			{"role":"user","parts":[{"functionResponse":{"name":"get_weather","response":{"result":"sunny"}}}]}
		],
		"tools":[{"functionDeclarations":[{"name":"get_weather","parameters":{"type":"object"}}]}]
	}`
	req := decodeRequest(t, codec, mustDoc(t, xir.ProtocolGemini, xir.OperationGenerateContent, body))
	if len(req.Messages) != 2 {
		t.Fatalf("messages = %d", len(req.Messages))
	}
	call := req.Messages[0].Parts[0]
	if call.Kind != xir.PartToolCall || call.ToolCall == nil || call.ToolCall.Name != "get_weather" {
		t.Fatalf("call = %+v", call)
	}
	result := req.Messages[1].Parts[0]
	if result.Kind != xir.PartToolResult || result.ToolResult == nil || result.ToolResult.Name != "get_weather" {
		t.Fatalf("result = %+v", result)
	}
	if result.ToolResult.Text != "sunny" || !json.Valid(result.ToolResult.JSON) {
		t.Fatalf("result = %+v", result.ToolResult)
	}

	encoded := encodeRequest(t, codec, req)
	object := jsonObject(t, encoded)
	contents := object["contents"].([]any)
	callArgs := contents[0].(map[string]any)["parts"].([]any)[0].(map[string]any)["functionCall"].(map[string]any)["args"]
	if _, ok := callArgs.(map[string]any); !ok {
		t.Fatalf("call args = %#v", callArgs)
	}
	response := contents[1].(map[string]any)["parts"].([]any)[0].(map[string]any)["functionResponse"]
	responseObject, ok := response.(map[string]any)
	if !ok {
		t.Fatalf("functionResponse = %#v", response)
	}
	if _, ok := responseObject["response"].(map[string]any); !ok {
		t.Fatalf("functionResponse.response is not an object: %#v", responseObject["response"])
	}
}

func TestToolChoiceMappings(t *testing.T) {
	codec := Codec{}
	cases := []struct {
		body string
		want xir.ToolChoice
	}{
		{`{"toolConfig":{"functionCallingConfig":{"mode":"AUTO"}}}`, xir.ToolChoice{Mode: "auto"}},
		{`{"toolConfig":{"functionCallingConfig":{"mode":"NONE"}}}`, xir.ToolChoice{Mode: "none"}},
		{`{"toolConfig":{"functionCallingConfig":{"mode":"ANY"}}}`, xir.ToolChoice{Mode: "required"}},
		{`{"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["a"]}}}`, xir.ToolChoice{Mode: "tool", Name: "a"}},
		{`{"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["a","b"]}}}`, xir.ToolChoice{Mode: "required"}},
		{`{}`, xir.ToolChoice{}},
	}
	for _, tc := range cases {
		req := decodeRequest(t, codec, mustDoc(t, xir.ProtocolGemini, xir.OperationGenerateContent, tc.body))
		if req.ToolChoice != tc.want {
			t.Fatalf("body %s: got %+v want %+v", tc.body, req.ToolChoice, tc.want)
		}
	}

	encoded := encodeRequest(t, codec, &xir.Request{
		Messages:   []xir.Message{{Role: xir.RoleUser, Parts: []xir.Part{{Kind: xir.PartText, Text: "hi"}}}},
		ToolChoice: xir.ToolChoice{Mode: "tool", Name: "a"},
	})
	config := jsonObject(t, encoded)["toolConfig"].(map[string]any)["functionCallingConfig"].(map[string]any)
	if config["mode"] != "ANY" || !reflect.DeepEqual(config["allowedFunctionNames"], []any{"a"}) {
		t.Fatalf("toolConfig = %#v", config)
	}
}

func TestResponseRefusal(t *testing.T) {
	codec := Codec{}
	body := `{"promptFeedback":{"blockReason":"SAFETY","safetyRatings":[{"category":"HARM_CATEGORY_HATE_SPEECH"}]},"usageMetadata":{"promptTokenCount":1,"totalTokenCount":1}}`
	resp := decodeResponse(t, codec, mustDoc(t, xir.ProtocolGemini, xir.OperationGenerateContent, body))
	if resp.StopReason != xir.FinishRefusal || resp.StopDetail != "SAFETY" {
		t.Fatalf("stop = %q / %q", resp.StopReason, resp.StopDetail)
	}
	if len(resp.Parts) != 1 || resp.Parts[0].Kind != xir.PartRefusal || resp.Parts[0].Text != "SAFETY" {
		t.Fatalf("parts = %+v", resp.Parts)
	}
	extension, err := extensionObject(resp.Extensions)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := extension["promptFeedback"]; !ok {
		t.Fatal("promptFeedback extension missing")
	}

	encoded := encodeResponse(t, codec, resp)
	object := jsonObject(t, encoded)
	if _, ok := object["candidates"]; ok {
		t.Fatalf("refusal-only response emitted candidates: %s", encoded)
	}
	feedback := object["promptFeedback"].(map[string]any)
	if feedback["blockReason"] != "SAFETY" {
		t.Fatalf("promptFeedback = %#v", feedback)
	}
	if _, ok := feedback["safetyRatings"]; !ok {
		t.Fatalf("safetyRatings not preserved: %#v", feedback)
	}

	second := decodeResponse(t, codec, mustDoc(t, xir.ProtocolGemini, xir.OperationGenerateContent, string(encoded)))
	if second.StopReason != xir.FinishRefusal || len(second.Parts) != 1 || second.Parts[0].Kind != xir.PartRefusal {
		t.Fatalf("second = %+v", second)
	}
}

func TestErrorEnvelope(t *testing.T) {
	codec := Codec{}
	body := `{"error":{"code":400,"message":"API key not valid","status":"INVALID_ARGUMENT","details":[{"reason":"API_KEY_INVALID"}]}}`
	resp := decodeResponse(t, codec, mustDoc(t, xir.ProtocolGemini, xir.OperationGenerateContent, body))
	if resp.Error == nil {
		t.Fatal("error is nil")
	}
	if resp.Error.Status != 400 || resp.Error.Code != "INVALID_ARGUMENT" || resp.Error.Message != "API key not valid" {
		t.Fatalf("error = %+v", resp.Error)
	}
	if resp.StopReason != xir.FinishError {
		t.Fatalf("stop = %q", resp.StopReason)
	}
	if !json.Valid(resp.Error.Raw) {
		t.Fatalf("raw = %s", resp.Error.Raw)
	}

	encoded := encodeResponse(t, codec, resp)
	object := jsonObject(t, encoded)
	errorObject := object["error"].(map[string]any)
	if errorObject["code"] != float64(400) || errorObject["status"] != "INVALID_ARGUMENT" || errorObject["message"] != "API key not valid" {
		t.Fatalf("error = %#v", errorObject)
	}
	if details, ok := errorObject["details"].([]any); !ok || len(details) != 1 {
		t.Fatalf("details = %#v", errorObject["details"])
	}

	// An Error without Raw is rendered from the neutral fields.
	encoded = encodeResponse(t, codec, &xir.Response{Error: &xir.Error{
		Status:  429,
		Code:    "RESOURCE_EXHAUSTED",
		Message: "slow down",
	}})
	errorObject = jsonObject(t, encoded)["error"].(map[string]any)
	if errorObject["code"] != float64(429) || errorObject["status"] != "RESOURCE_EXHAUSTED" || errorObject["message"] != "slow down" {
		t.Fatalf("synthetic error = %#v", errorObject)
	}
}

func TestUsageWithoutTotal(t *testing.T) {
	codec := Codec{}
	body := `{"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":3,"thoughtsTokenCount":2}}`
	resp := decodeResponse(t, codec, mustDoc(t, xir.ProtocolGemini, xir.OperationGenerateContent, body))
	if resp.Usage == nil || resp.Usage.TotalTokens != 9 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	if resp.StopReason != xir.FinishStop {
		t.Fatalf("stop = %q", resp.StopReason)
	}

	encoded := encodeResponse(t, codec, &xir.Response{
		Parts:      []xir.Part{{Kind: xir.PartText, Text: "hi"}},
		StopReason: xir.FinishStop,
		Usage:      &usage.Usage{InputTokens: 4, OutputTokens: 3, ReasoningTokens: 2},
	})
	usage := jsonObject(t, encoded)["usageMetadata"].(map[string]any)
	if usage["totalTokenCount"] != float64(9) {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestFinishReasonMappings(t *testing.T) {
	codec := Codec{}
	cases := map[string]xir.FinishReason{
		"STOP":                      xir.FinishStop,
		"MAX_TOKENS":                xir.FinishLength,
		"SAFETY":                    xir.FinishContentFilter,
		"RECITATION":                xir.FinishContentFilter,
		"MALFORMED_FUNCTION_CALL":   xir.FinishError,
		"FINISH_REASON_UNSPECIFIED": xir.FinishUnknown,
	}
	for reason, want := range cases {
		body := `{"candidates":[{"content":{"parts":[{"text":"x"}]},"finishReason":"` + reason + `"}]}`
		resp := decodeResponse(t, codec, mustDoc(t, xir.ProtocolGemini, xir.OperationGenerateContent, body))
		if resp.StopReason != want {
			t.Fatalf("reason %s: got %q want %q", reason, resp.StopReason, want)
		}
	}

	// STOP with a function call is a tool-use turn.
	body := `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"f","args":{}}}]},"finishReason":"STOP"}]}`
	resp := decodeResponse(t, codec, mustDoc(t, xir.ProtocolGemini, xir.OperationGenerateContent, body))
	if resp.StopReason != xir.FinishToolUse {
		t.Fatalf("stop = %q", resp.StopReason)
	}

	// An unknown reason is kept in StopDetail.
	body = `{"candidates":[{"content":{"parts":[]},"finishReason":"SOMETHING_NEW"}]}`
	resp = decodeResponse(t, codec, mustDoc(t, xir.ProtocolGemini, xir.OperationGenerateContent, body))
	if resp.StopReason != xir.FinishUnknown || resp.StopDetail != "SOMETHING_NEW" {
		t.Fatalf("stop = %q detail = %q", resp.StopReason, resp.StopDetail)
	}

	// Tool use encodes back to STOP.
	encoded := encodeResponse(t, codec, &xir.Response{
		Parts:      []xir.Part{{Kind: xir.PartToolCall, ToolCall: &xir.ToolCall{Name: "f", Args: json.RawMessage(`{}`)}}},
		StopReason: xir.FinishToolUse,
	})
	if got := jsonObject(t, encoded)["candidates"].([]any)[0].(map[string]any)["finishReason"]; got != "STOP" {
		t.Fatalf("finishReason = %#v", got)
	}
}

func TestSealedUnknownPart(t *testing.T) {
	codec := Codec{}
	body := `{"contents":[{"role":"user","parts":[{"executableCode":{"language":"PYTHON","code":"print(1)"}}]}]}`
	req := decodeRequest(t, codec, mustDoc(t, xir.ProtocolGemini, xir.OperationGenerateContent, body))
	if len(req.Messages) != 1 || len(req.Messages[0].Parts) != 1 {
		t.Fatalf("request = %+v", req)
	}
	part := req.Messages[0].Parts[0]
	if part.Kind != xir.PartSealed || part.Sealed == nil || part.Sealed.Protocol != xir.ProtocolGemini {
		t.Fatalf("part = %+v", part)
	}
	encoded := encodeRequest(t, codec, req)
	parts := jsonObject(t, encoded)["contents"].([]any)[0].(map[string]any)["parts"].([]any)
	if _, ok := parts[0].(map[string]any)["executableCode"].(map[string]any); !ok {
		t.Fatalf("sealed part lost: %#v", parts[0])
	}
}

func TestEmptyBodies(t *testing.T) {
	codec := Codec{}
	req := decodeRequest(t, codec, mustDoc(t, xir.ProtocolGemini, xir.OperationGenerateContent, `{}`))
	if len(req.Messages) != 0 || len(req.System) != 0 {
		t.Fatalf("request = %+v", req)
	}
	encoded := encodeRequest(t, codec, req)
	if string(encoded) != `{"contents":[]}` {
		t.Fatalf("encoded empty request = %s", encoded)
	}

	resp := decodeResponse(t, codec, mustDoc(t, xir.ProtocolGemini, xir.OperationGenerateContent, `{}`))
	if len(resp.Parts) != 0 || resp.StopReason != xir.FinishUnknown {
		t.Fatalf("response = %+v", resp)
	}
	encoded = encodeResponse(t, codec, resp)
	if string(encoded) != `{}` {
		t.Fatalf("encoded empty response = %s", encoded)
	}
}

func TestDecodeMalformedInput(t *testing.T) {
	codec := Codec{}
	requests := []string{
		`[]`,
		`"x"`,
		`{"contents":"x"}`,
		`{"contents":[{"parts":"x"}]}`,
		`{"contents":[{"parts":[{"text":5}]}]}`,
		`{"contents":[{"parts":[{"functionCall":{"name":"f","args":[]}}]}]}`,
		`{"contents":[{"parts":[{"inlineData":"x"}]}]}`,
		`{"generationConfig":{"temperature":"hot"}}`,
		`{"generationConfig":{"topK":1.5}}`,
		`{"generationConfig":{"thinkingConfig":{"thinkingBudget":"lots"}}}`,
		`{"toolConfig":{"functionCallingConfig":{"mode":"AUTO","allowedFunctionNames":"x"}}}`,
		`{"tools":[{"functionDeclarations":[{"name":"f","parameters":"x"}]}]}`,
		`{"safetySettings":{}}`,
	}
	for _, body := range requests {
		if _, err := codec.DecodeRequest(context.Background(), mustDoc(t, xir.ProtocolGemini, xir.OperationGenerateContent, body), xir.DecodeOptions{}); err == nil {
			t.Fatalf("request %s was accepted", body)
		}
	}

	responses := []string{
		`[]`,
		`{"error":"boom"}`,
		`{"candidates":"x"}`,
		`{"candidates":[{"content":{"parts":"x"}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":5}]}}]}`,
		`{"usageMetadata":{"promptTokenCount":"x"}}`,
		`{"promptFeedback":"x"}`,
	}
	for _, body := range responses {
		if _, err := codec.DecodeResponse(context.Background(), mustDoc(t, xir.ProtocolGemini, xir.OperationGenerateContent, body), xir.DecodeOptions{}); err == nil {
			t.Fatalf("response %s was accepted", body)
		}
	}

	if _, err := xir.ParseDocument(xir.ProtocolGemini, xir.OperationGenerateContent, []byte(`{"contents":`)); err == nil {
		t.Fatal("invalid JSON was accepted")
	}
	if _, err := codec.DecodeRequest(context.Background(), nil, xir.DecodeOptions{}); err == nil {
		t.Fatal("nil document was accepted")
	}
}

func TestEncodeErrors(t *testing.T) {
	codec := Codec{}
	ctx := context.Background()

	if _, err := codec.EncodeRequest(ctx, nil, xir.EncodeOptions{}); err == nil {
		t.Fatal("nil request was accepted")
	}
	if _, err := codec.EncodeResponse(ctx, nil, xir.EncodeOptions{}); err == nil {
		t.Fatal("nil response was accepted")
	}
	if _, err := codec.EncodeRequest(ctx, &xir.Request{
		Messages: []xir.Message{{Role: xir.RoleUser, Parts: []xir.Part{{
			Kind:     xir.PartToolCall,
			ToolCall: &xir.ToolCall{Name: "f", Args: json.RawMessage(`[1]`)},
		}}}},
	}, xir.EncodeOptions{}); err == nil {
		t.Fatal("array args were accepted")
	}
	if _, err := codec.EncodeRequest(ctx, &xir.Request{
		Tools: []xir.Tool{{Name: "f", Schema: json.RawMessage(`[1]`)}},
	}, xir.EncodeOptions{}); err == nil {
		t.Fatal("array schema was accepted")
	}
	if _, err := codec.EncodeRequest(ctx, &xir.Request{
		Extensions: map[string]json.RawMessage{ExtensionKey: json.RawMessage(`{"safetySettings":{}}`)},
	}, xir.EncodeOptions{}); err == nil {
		t.Fatal("non-array safety settings were accepted")
	}
	if _, err := codec.EncodeResponse(ctx, &xir.Response{
		Extensions: map[string]json.RawMessage{ExtensionKey: json.RawMessage(`{"promptFeedback":[]}`)},
	}, xir.EncodeOptions{}); err == nil {
		t.Fatal("non-object promptFeedback was accepted")
	}
}

func TestEncodeConsecutiveTurnsMerge(t *testing.T) {
	codec := Codec{}
	req := &xir.Request{Messages: []xir.Message{
		{Role: xir.RoleUser, Parts: []xir.Part{{Kind: xir.PartText, Text: "one"}}},
		{Role: xir.RoleUser, Parts: []xir.Part{{Kind: xir.PartText, Text: "two"}}},
		{Role: xir.RoleModel, Parts: []xir.Part{{Kind: xir.PartText, Text: "three"}}},
	}}
	encoded := encodeRequest(t, codec, req)
	contents := jsonObject(t, encoded)["contents"].([]any)
	if len(contents) != 2 {
		t.Fatalf("contents = %#v", contents)
	}
	if got := contents[0].(map[string]any)["parts"].([]any); len(got) != 2 {
		t.Fatalf("merged user parts = %#v", got)
	}
}

func TestSystemRoleMessageFoldsIntoSystemInstruction(t *testing.T) {
	codec := Codec{}
	req := &xir.Request{
		System: []xir.Part{{Kind: xir.PartText, Text: "top"}},
		Messages: []xir.Message{
			{Role: xir.RoleSystem, Parts: []xir.Part{{Kind: xir.PartText, Text: "folded"}}},
			{Role: xir.RoleUser, Parts: []xir.Part{{Kind: xir.PartText, Text: "hi"}}},
		},
	}
	encoded := encodeRequest(t, codec, req)
	object := jsonObject(t, encoded)
	system := object["systemInstruction"].(map[string]any)["parts"].([]any)
	if len(system) != 2 {
		t.Fatalf("system = %#v", system)
	}
	if contents := object["contents"].([]any); len(contents) != 1 {
		t.Fatalf("contents = %#v", contents)
	}
}

func TestDecodeRequestWithPathAndStream(t *testing.T) {
	codec := Codec{}
	doc := mustDoc(t, xir.ProtocolGemini, xir.OperationGenerateContent, `{}`)
	req, err := codec.DecodeRequestWithPath(context.Background(), doc, xir.DecodeOptions{}, "/v1beta/models/gemini-2.0-flash:generateContent")
	if err != nil {
		t.Fatal(err)
	}
	if req.Model != "gemini-2.0-flash" || req.Stream {
		t.Fatalf("model=%q stream=%v", req.Model, req.Stream)
	}
}

func TestExtensionPreservesUnknownFields(t *testing.T) {
	codec := Codec{}
	requestBody := `{
		"contents":[{"role":"user","parts":[{"text":"hi"}]}],
		"cachedContent":"cachedContents/abc",
		"labels":{"team":"search"},
		"safetySettings":[{"category":"HARM_CATEGORY_HATE_SPEECH","threshold":"BLOCK_NONE"}]
	}`
	req := decodeRequest(t, codec, mustDoc(t, xir.ProtocolGemini, xir.OperationGenerateContent, requestBody))
	encoded := encodeRequest(t, codec, req)
	object := jsonObject(t, encoded)
	if object["cachedContent"] != "cachedContents/abc" {
		t.Fatalf("cachedContent = %#v", object["cachedContent"])
	}
	labels, ok := object["labels"].(map[string]any)
	if !ok || labels["team"] != "search" {
		t.Fatalf("labels = %#v", object["labels"])
	}
	if _, ok := object["safetySettings"].([]any); !ok {
		t.Fatalf("safetySettings = %#v", object["safetySettings"])
	}

	responseBody := `{
		"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}],
		"someFutureField":{"value":1}
	}`
	resp := decodeResponse(t, codec, mustDoc(t, xir.ProtocolGemini, xir.OperationGenerateContent, responseBody))
	encoded = encodeResponse(t, codec, resp)
	object = jsonObject(t, encoded)
	future, ok := object["someFutureField"].(map[string]any)
	if !ok || future["value"] != float64(1) {
		t.Fatalf("someFutureField = %#v", object["someFutureField"])
	}
}
