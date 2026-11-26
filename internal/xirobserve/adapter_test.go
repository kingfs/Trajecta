package xirobserve

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kingfs/Trajecta/pkg/llm"
	"github.com/kingfs/Trajecta/pkg/observe"
	"github.com/kingfs/Trajecta/pkg/recordfile"
)

func TestChatProjectionKeepsMessageTextAndCorrelatesToolResult(t *testing.T) {
	request := `{"model":"gpt-4.1","messages":[` +
		`{"role":"system","content":"You are a careful agent."},` +
		`{"role":"user","content":"list the files"},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"exec_command","arguments":"{\"cmd\":\"ls -la\"}"}}]},` +
		`{"role":"tool","tool_call_id":"call_1","content":"permission denied"}]}`
	response := `{"id":"chatcmpl-e2e","object":"chat.completion","model":"gpt-4.1","choices":[{"index":0,"message":{"role":"assistant","content":"I could not list the files."},"finish_reason":"stop"}],"usage":{"prompt_tokens":60,"completion_tokens":12,"total_tokens":72}}`
	input := observe.ParseInput{
		Header: recordfile.RecordHeader{Meta: recordfile.MetaData{
			Provider:  llm.ProviderOpenAICompatible,
			Operation: llm.OperationChatCompletions,
			Endpoint:  "/v1/chat/completions",
			Model:     "gpt-4.1",
		}},
		RequestBody:  []byte(request),
		ResponseBody: []byte(response),
	}

	got, err := Parse(context.Background(), input)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(got.Request.Messages) != 4 {
		t.Fatalf("request messages = %d, want 4", len(got.Request.Messages))
	}
	if got.Request.Messages[0].Text != "You are a careful agent." {
		t.Fatalf("system message text = %q", got.Request.Messages[0].Text)
	}
	if got.Request.Messages[0].Path != "$.messages[0]" || len(got.Request.Messages[0].Raw) == 0 {
		t.Fatalf("system message projection = %+v", got.Request.Messages[0])
	}

	callMessage := got.Request.Messages[2]
	if callMessage.Text != "" || len(callMessage.Children) != 1 {
		t.Fatalf("assistant tool-call message = %+v", callMessage)
	}
	call := callMessage.Children[0]
	if call.NormalizedType != observe.NodeToolCall || call.Path != "$.messages[2].tool_calls[0]" {
		t.Fatalf("tool-call node = %+v", call)
	}
	var callRaw struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	if err := json.Unmarshal(call.Raw, &callRaw); err != nil {
		t.Fatalf("decode tool-call raw: %v", err)
	}
	if callRaw.ID != "call_1" || callRaw.Function.Name != "exec_command" || callRaw.Function.Arguments != `{"cmd":"ls -la"}` {
		t.Fatalf("tool-call wire shape = %+v", callRaw)
	}

	result := got.Request.Messages[3]
	if result.NormalizedType != observe.NodeToolResult || result.Role != "tool" || result.Text != "permission denied" {
		t.Fatalf("tool-result message = %+v", result)
	}
	if result.Path != "$.messages[3]" || !json.Valid(result.Raw) {
		t.Fatalf("tool-result raw/path = %s / %q", result.Raw, result.Path)
	}
	var resultRaw map[string]any
	if err := json.Unmarshal(result.Raw, &resultRaw); err != nil {
		t.Fatalf("decode tool-result raw: %v", err)
	}
	if resultRaw["tool_call_id"] != "call_1" {
		t.Fatalf("tool-result wire call id = %#v", resultRaw["tool_call_id"])
	}

	if len(got.Response.Nodes) != 1 || got.Response.Nodes[0].Path != "$.choices[0]" {
		t.Fatalf("response choice nodes = %+v", got.Response.Nodes)
	}
	choice := got.Response.Nodes[0]
	if len(choice.Children) != 1 || choice.Children[0].Path != "$.choices[0].message" || choice.Children[0].Text != "I could not list the files." {
		t.Fatalf("response message = %+v", choice.Children)
	}
}

func TestResponsesProjectionReadsShorthandOutputMessage(t *testing.T) {
	request := `{"model":"gpt-5","input":[{"role":"system","content":"You are a careful agent."},{"role":"user","content":"list the files"}]}`
	response := `{"id":"resp-e2e","object":"response","model":"gpt-5","output":[{"role":"assistant","content":[{"type":"output_text","text":"I would list the files."}]}]}`
	input := observe.ParseInput{
		Header: recordfile.RecordHeader{Meta: recordfile.MetaData{
			Provider:  llm.ProviderOpenAICompatible,
			Operation: llm.OperationResponses,
			Endpoint:  "/v1/responses",
			Model:     "gpt-5",
		}},
		RequestBody:  []byte(request),
		ResponseBody: []byte(response),
	}

	got, err := Parse(context.Background(), input)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(got.Request.Inputs) != 2 || got.Request.Inputs[0].Text != "You are a careful agent." {
		t.Fatalf("request input nodes = %+v", got.Request.Inputs)
	}
	if len(got.Response.Outputs) != 1 {
		t.Fatalf("response outputs = %+v", got.Response.Outputs)
	}
	message := got.Response.Outputs[0]
	if message.NormalizedType != observe.NodeMessage || message.Path != "$.output[0]" || message.Text != "I would list the files." {
		t.Fatalf("shorthand response message = %+v", message)
	}
	if len(message.Children) != 1 || message.Children[0].NormalizedType != observe.NodeText {
		t.Fatalf("shorthand response content = %+v", message.Children)
	}
}
