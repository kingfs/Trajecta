package session

// P4 parity corpus: fromBody 的定向扫描快路径必须与 P4 之前的全量
// json.Unmarshal 实现逐字节一致。本文件在 P4 落地时以旧实现为 oracle
// 对拍验证（含 fuzz），期望值同时冻结为 golden；oracle 验证后删除，
// golden 语料长期守住行为。覆盖：五种信号源与优先级、metadata 嵌套、
// 大小写不敏感键、转义/非 ASCII 键、重复键、类型错误整体拒绝、畸形
// body、Unicode 空白、深度限制与大 body。

import (
	"net/http"
	"strings"
	"testing"
)

func wantRef(key string, kind Kind, conf Confidence, source string) Ref {
	return Ref{Key: key, Fingerprint: Fingerprint(key), Kind: kind, Confidence: conf, Source: source}
}

func parityCorpus() []struct {
	name string
	body string
	want Ref
} {
	deepOver := `{"session_id":"s","a":` + strings.Repeat("[", 10001) + strings.Repeat("]", 10001) + `}`
	deepOverNoSignal := `{"a":` + strings.Repeat("[", 10001) + strings.Repeat("]", 10001) + `}`
	deepOK := `{"session_id":"s","a":` + strings.Repeat("[", 9998) + strings.Repeat("]", 9998) + `}`
	largeSignal := `{"model":"gpt-5","messages":[` + strings.Repeat(`{"role":"user","content":"hello world"},`, 200) + `{"role":"user","content":"last"}],"session_id":"s-large"}`
	largeNoSignal := `{"model":"gpt-5","messages":[` + strings.Repeat(`{"role":"user","content":"hello world"},`, 200) + `{"role":"user","content":"last"}]}`

	return []struct {
		name string
		body string
		want Ref
	}{
		// --- 五种信号源 ---
		{"previous_response_id", `{"previous_response_id":"r1"}`, wantRef("r1", KindPreviousResponse, ConfidenceHigh, "body:previous_response_id")},
		{"prompt_cache_key", `{"prompt_cache_key":"c1"}`, wantRef("c1", KindPromptCache, ConfidenceMedium, "body:prompt_cache_key")},
		{"metadata.session_id", `{"metadata":{"session_id":"m1"}}`, wantRef("m1", KindMetadata, ConfidenceMedium, "body:metadata.session_id")},
		{"conversation_id", `{"conversation_id":"v1"}`, wantRef("v1", KindConversation, ConfidenceHigh, "body:conversation_id")},
		{"session_id", `{"session_id":"s1"}`, wantRef("s1", KindBody, ConfidenceMedium, "body:session_id")},

		// --- 优先级 ---
		{"all five previous wins", `{"session_id":"s","conversation_id":"v","metadata":{"session_id":"m"},"prompt_cache_key":"c","previous_response_id":"r"}`, wantRef("r", KindPreviousResponse, ConfidenceHigh, "body:previous_response_id")},
		{"metadata beats conversation and session", `{"session_id":"s","conversation_id":"v","metadata":{"session_id":"m"}}`, wantRef("m", KindMetadata, ConfidenceMedium, "body:metadata.session_id")},
		{"prompt_cache beats metadata", `{"metadata":{"session_id":"m"},"prompt_cache_key":"c"}`, wantRef("c", KindPromptCache, ConfidenceMedium, "body:prompt_cache_key")},
		{"conversation beats session", `{"session_id":"s","conversation_id":"v"}`, wantRef("v", KindConversation, ConfidenceHigh, "body:conversation_id")},
		{"blank previous falls through", `{"previous_response_id":"  ","prompt_cache_key":"c"}`, wantRef("c", KindPromptCache, ConfidenceMedium, "body:prompt_cache_key")},
		{"all blank", `{"previous_response_id":" ","prompt_cache_key":"\t","metadata":{"session_id":" "},"conversation_id":"","session_id":"  "}`, Ref{}},
		{"null previous falls through", `{"previous_response_id":null,"session_id":"s"}`, wantRef("s", KindBody, ConfidenceMedium, "body:session_id")},

		// --- 值形态 ---
		{"value trimmed", `{"session_id":"  s  "}`, wantRef("s", KindBody, ConfidenceMedium, "body:session_id")},
		{"value escape", `{"session_id":"a\u0062c"}`, wantRef("abc", KindBody, ConfidenceMedium, "body:session_id")},
		{"value unicode", `{"session_id":"会话-α"}`, wantRef("会话-α", KindBody, ConfidenceMedium, "body:session_id")},
		{"value invalid utf8 sanitized", "{\"session_id\":\"a\xffb\"}", wantRef("a\ufffdb", KindBody, ConfidenceMedium, "body:session_id")},
		{"value empty", `{"session_id":""}`, Ref{}},
		{"value braces and quotes", `{"session_id":"a\"b{c}[d]","x":"}"}`, wantRef(`a"b{c}[d]`, KindBody, ConfidenceMedium, "body:session_id")},

		// --- 大小写不敏感键（encoding/json 字段匹配） ---
		{"Session_Id", `{"Session_Id":"s"}`, wantRef("s", KindBody, ConfidenceMedium, "body:session_id")},
		{"SESSION_ID", `{"SESSION_ID":"s"}`, wantRef("s", KindBody, ConfidenceMedium, "body:session_id")},
		{"Previous_Response_ID", `{"Previous_Response_ID":"r"}`, wantRef("r", KindPreviousResponse, ConfidenceHigh, "body:previous_response_id")},
		{"Prompt_Cache_Key", `{"Prompt_Cache_Key":"c"}`, wantRef("c", KindPromptCache, ConfidenceMedium, "body:prompt_cache_key")},
		{"Conversation_Id", `{"Conversation_Id":"v"}`, wantRef("v", KindConversation, ConfidenceHigh, "body:conversation_id")},
		{"METADATA nested Session_ID", `{"METADATA":{"Session_ID":"m"}}`, wantRef("m", KindMetadata, ConfidenceMedium, "body:metadata.session_id")},

		// --- 转义键 ---
		{"escaped first letter", `{"\u0073ession_id":"s"}`, wantRef("s", KindBody, ConfidenceMedium, "body:session_id")},
		{"escaped underscore", `{"session\u005fid":"s"}`, wantRef("s", KindBody, ConfidenceMedium, "body:session_id")},
		{"escaped uppercase", `{"\u0053ESSION_ID":"s"}`, wantRef("s", KindBody, ConfidenceMedium, "body:session_id")},
		{"escaped metadata inner key", `{"metadata":{"\u0073ession_id":"m"}}`, wantRef("m", KindMetadata, ConfidenceMedium, "body:metadata.session_id")},
		{"escaped unrelated key skipped", `{"\u006dessages":[],"session_id":"s"}`, wantRef("s", KindBody, ConfidenceMedium, "body:session_id")},
		{"escaped metadata no signal", `{"\u006detadata":{"user_id":"u"}}`, Ref{}},
		{"escaped metadata escaped signal", `{"\u006detadata":{"\u0073ession_id":"m"}}`, wantRef("m", KindMetadata, ConfidenceMedium, "body:metadata.session_id")},

		// --- 非 ASCII 折叠键（ſ -> s, Kelvin K -> k） ---
		{"long s key", `{"ſession_id":"s"}`, wantRef("s", KindBody, ConfidenceMedium, "body:session_id")},
		{"kelvin k key", `{"prompt_cache_Key":"c"}`, wantRef("c", KindPromptCache, ConfidenceMedium, "body:prompt_cache_key")},
		{"unicode unrelated key with signal", `{"会话":"x","session_id":"s"}`, wantRef("s", KindBody, ConfidenceMedium, "body:session_id")},
		{"unicode unrelated key only", `{"模型":"gpt"}`, Ref{}},

		// --- 重复键 ---
		{"duplicate last wins", `{"session_id":"a","session_id":"b"}`, wantRef("b", KindBody, ConfidenceMedium, "body:session_id")},
		{"duplicate earlier type error rejects", `{"session_id":5,"session_id":"b"}`, Ref{}},
		{"duplicate later type error rejects", `{"session_id":"a","session_id":5}`, Ref{}},
		{"duplicate mixed case last wins", `{"session_id":"a","Session_ID":"b"}`, wantRef("b", KindBody, ConfidenceMedium, "body:session_id")},
		{"duplicate metadata last wins", `{"metadata":{"session_id":"m1"},"metadata":{"session_id":"m2"}}`, wantRef("m2", KindMetadata, ConfidenceMedium, "body:metadata.session_id")},
		{"duplicate metadata last non-object", `{"metadata":{"session_id":"m"},"metadata":"x"}`, Ref{}},
		{"duplicate metadata last object", `{"metadata":"x","metadata":{"session_id":"m"}}`, wantRef("m", KindMetadata, ConfidenceMedium, "body:metadata.session_id")},

		// --- 类型错误：被跟踪字段非字符串非 null 时整体拒绝 ---
		{"session_id number", `{"session_id":5}`, Ref{}},
		{"session_id bool rejects whole body", `{"session_id":true,"previous_response_id":"r"}`, Ref{}},
		{"previous object", `{"previous_response_id":{}}`, Ref{}},
		{"prompt_cache array", `{"prompt_cache_key":[]}`, Ref{}},
		{"conversation float", `{"conversation_id":1.5}`, Ref{}},
		{"untracked field type ignored", `{"session_id":"s","model":123}`, wantRef("s", KindBody, ConfidenceMedium, "body:session_id")},
		{"untracked nested type ignored", `{"session_id":"s","messages":[{"content":5}]}`, wantRef("s", KindBody, ConfidenceMedium, "body:session_id")},

		// --- metadata 形态 ---
		{"metadata string", `{"metadata":"str","session_id":"s"}`, wantRef("s", KindBody, ConfidenceMedium, "body:session_id")},
		{"metadata array", `{"metadata":[1,2],"session_id":"s"}`, wantRef("s", KindBody, ConfidenceMedium, "body:session_id")},
		{"metadata null", `{"metadata":null,"session_id":"s"}`, wantRef("s", KindBody, ConfidenceMedium, "body:session_id")},
		{"metadata number", `{"metadata":5,"session_id":"s"}`, wantRef("s", KindBody, ConfidenceMedium, "body:session_id")},
		{"metadata bool", `{"metadata":true,"session_id":"s"}`, wantRef("s", KindBody, ConfidenceMedium, "body:session_id")},
		{"metadata inner type error only kills metadata", `{"metadata":{"session_id":5},"session_id":"s"}`, wantRef("s", KindBody, ConfidenceMedium, "body:session_id")},
		{"metadata blank falls through", `{"metadata":{"session_id":"  "},"session_id":"s"}`, wantRef("s", KindBody, ConfidenceMedium, "body:session_id")},
		{"metadata deep session_id ignored", `{"metadata":{"nested":{"session_id":"deep"}},"session_id":"s"}`, wantRef("s", KindBody, ConfidenceMedium, "body:session_id")},
		{"metadata empty object", `{"metadata":{}}`, Ref{}},
		{"metadata user_id only (Anthropic)", `{"metadata":{"user_id":"u"}}`, Ref{}},
		{"metadata array of objects", `{"metadata":[{"session_id":"m"}],"session_id":"s"}`, wantRef("s", KindBody, ConfidenceMedium, "body:session_id")},

		// --- 畸形 body ---
		{"bare bad", `{bad`, Ref{}},
		{"unclosed object", `{"session_id":"s"`, Ref{}},
		{"trailing word", `{"session_id":"s"} trailing`, Ref{}},
		{"trailing object", `{"session_id":"s"}{}`, Ref{}},
		{"trailing comma", `{"session_id":"s",}`, Ref{}},
		{"missing value", `{"session_id":}`, Ref{}},
		{"NaN", `{"session_id":"s","x":NaN}`, Ref{}},
		{"leading zero number", `{"session_id":"s","x":01}`, Ref{}},
		{"bad exponent", `{"session_id":"s","x":1e}`, Ref{}},
		{"plus number", `{"session_id":"s","x":+1}`, Ref{}},
		{"unterminated value", `{"session_id":"unterminated}`, Ref{}},
		{"raw control char in string", "{\"session_id\":\"a\x01b\"}", Ref{}},
		{"mismatched brackets", `{"session_id":"s","x":[1,2}`, Ref{}},
		{"not json", `not json`, Ref{}},
		{"array root", `[]`, Ref{}},
		{"string root", `"string"`, Ref{}},
		{"number root", `123`, Ref{}},
		{"null root", `null`, Ref{}},
		{"empty", ``, Ref{}},
		{"whitespace only", "   \n\t", Ref{}},
		{"empty object", `{}`, Ref{}},
		{"two objects", `{"x":1}{"session_id":"s"}`, Ref{}},
		{"fake key inside string value", `{"x":"\"session_id\":\"fake\"","session_id":"s"}`, wantRef("s", KindBody, ConfidenceMedium, "body:session_id")},
		{"escaped backslash value", `{"x":"\\","session_id":"s"}`, wantRef("s", KindBody, ConfidenceMedium, "body:session_id")},

		// --- 空白 ---
		{"ascii whitespace around", " \t\r\n{\"session_id\":\"s\"}\n ", wantRef("s", KindBody, ConfidenceMedium, "body:session_id")},
		{"nbsp prefix trimmed", "\u00a0{\"session_id\":\"s\"}", wantRef("s", KindBody, ConfidenceMedium, "body:session_id")},
		{"u2028 suffix trimmed", "{\"session_id\":\"s\"}\u2028", wantRef("s", KindBody, ConfidenceMedium, "body:session_id")},
		{"nbsp inside not whitespace", "{\u00a0\"session_id\":\"s\"}", Ref{}},

		// --- 深度与大 body ---
		{"depth over limit with signal", deepOver, Ref{}},
		{"depth over limit no signal", deepOverNoSignal, Ref{}},
		{"depth within limit", deepOK, wantRef("s", KindBody, ConfidenceMedium, "body:session_id")},
		{"large body signal at end", largeSignal, wantRef("s-large", KindBody, ConfidenceMedium, "body:session_id")},
		{"large body no signal", largeNoSignal, Ref{}},

		// --- 近似但不匹配的键 ---
		{"suffix key", `{"session_id_extra":"x"}`, Ref{}},
		{"prefix key", `{"xsession_id":"x"}`, Ref{}},
		{"short key", `{"session":"x"}`, Ref{}},
		{"metadata-like key", `{"metadatax":{"session_id":"m"}}`, Ref{}},
		{"metadata inner suffix key", `{"metadata":{"session_id_extra":"m"}}`, Ref{}},
	}
}

// TestResolveParityCorpus 断言新实现与冻结的 golden 期望完全一致
// （Key/Fingerprint/Kind/Confidence/Source 全字段）。
func TestResolveParityCorpus(t *testing.T) {
	for _, tc := range parityCorpus() {
		t.Run(tc.name, func(t *testing.T) {
			got := Resolve(nil, []byte(tc.body))
			if got != tc.want {
				t.Fatalf("Resolve(%q)\n got  %+v\n want %+v", tc.body, got, tc.want)
			}
		})
	}
}

// TestResolveParityHeaderCombinations 断言头信号与 body 组合时头优先，
// 且头路径不受 body 扫描影响。
func TestResolveParityHeaderCombinations(t *testing.T) {
	headerSets := []http.Header{
		nil,
		{},
		func() http.Header { h := http.Header{}; h.Set("Session_id", "hdr"); return h }(),
		func() http.Header { h := http.Header{}; h.Set("X-Codex-Window-Id", "win:turn"); return h }(),
		func() http.Header {
			h := http.Header{}
			h.Set("X-Codex-Turn-Metadata", `{"session_id":"codex"}`)
			return h
		}(),
		func() http.Header { h := http.Header{}; h.Set("Session_id", "  "); return h }(),
	}
	for _, h := range headerSets {
		for _, tc := range parityCorpus() {
			got := Resolve(h, []byte(tc.body))
			var want Ref
			if h != nil {
				if ref, ok := fromHeaders(h); ok {
					want = ref
				} else {
					want = tc.want
				}
			} else {
				want = tc.want
			}
			if got != want {
				t.Fatalf("Resolve(%v, %q)\n got  %+v\n want %+v", h, tc.body, got, want)
			}
		}
	}
}
