package monitor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/internal/store"
)

func TestChannelCreateRejectsWrongQwenCodingPlanKey(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	handler := channelListCreateAPIHandler(st, nil, nil)

	body := `{"name":"Qwen Coding","provider_preset":"qwen_coding","api_type":"chat_completions","protocol_family":"openai_compatible","api_key":"sk-standard-dashscope"}`
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/channels", strings.NewReader(body)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var payload map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload["error"], "sk-sp-") {
		t.Fatalf("error = %q", payload["error"])
	}

	body = `{"name":"Qwen Coding","provider_preset":"qwen_coding","api_type":"chat_completions","protocol_family":"openai_compatible","api_key":"sk-sp-test"}`
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/channels", strings.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("valid key status = %d body=%s", rr.Code, rr.Body.String())
	}
	var created channelItem
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.BaseURL != "https://coding.dashscope.aliyuncs.com/v1" {
		t.Fatalf("base URL = %q", created.BaseURL)
	}
}
