package secretmask

import (
	"bytes"
	"strings"
	"testing"
)

// rep is a shorthand for the sample builders below.
func rep(s string, n int) string { return strings.Repeat(s, n) }

// positiveSamples holds one "obviously fake but shape-legal" string per rule.
// TestRuleMetadataComplete requires every rule in ruleTable to appear here, so
// a rule added to rules.rl without a sample (or vice versa) fails the build.
var positiveSamples = []struct {
	rule RuleID
	text string
}{
	{RulePEMPrivateKey, "-----BEGIN RSA PRIVATE KEY-----"},
	{RuleJWT, "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"},
	{RuleWebhookSecret, "whsec_" + rep("A", 43) + "="},

	{RuleOpenAILegacy, "sk-" + rep("A", 20) + "T3BlbkFJ" + rep("B", 20)},
	{RuleOpenAIProject, "sk-proj-" + rep("A", 58) + "T3BlbkFJ" + rep("B", 58)},
	{RuleOpenAIServiceAccount, "sk-svcacct-" + rep("A", 58) + "T3BlbkFJ" + rep("B", 58)},
	{RuleOpenAIAdmin, "sk-admin-" + rep("A", 58) + "T3BlbkFJ" + rep("B", 58)},
	{RuleOpenAIEphemeralKey, "ek_" + rep("0123456789abcdef", 1) + rep("0123456789abc", 1)},

	{RuleAnthropicAPIKey, "sk-ant-api03-" + rep("A", 95)},
	{RuleAnthropicAdminKey, "sk-ant-admin01-" + rep("A", 95)},
	{RuleAnthropicOAuthToken, "sk-ant-oat01-" + rep("A", 40)},
	{RuleAnthropicSessionKey, "sk-ant-sid01-" + rep("A", 60)},
	{RuleAnthropicAWSKey, "aws-external-anthropic-api-key-" + rep("A", 70)},

	{RuleBedrockLongTermKey, "ABSK" + rep("A", 109)},
	{RuleBedrockShortTermKey, "bedrock-api-key-YmVkcm9jay5hbWF6b25hd3MuY29t" + rep("A", 60)},

	{RuleGoogleAPIKey, "AIza" + rep("A", 35)},
	{RuleGoogleAPIKeyAQ, "AQ." + rep("Ab1", 10)},
	{RuleGoogleOAuthClientSecret, "GOCSPX-" + rep("A", 28)},
	{RuleGoogleAccessToken, "ya29." + rep("A", 50)},
	{RuleGoogleRefreshToken, "1//" + rep("A", 40)},

	{RuleXAIAPIToken, "xai-token-" + rep("A", 40)},
	{RuleXAIClientSecret, "xai-client-secret." + rep("A", 30)},
	{RuleXAIAPIKey, "xai-" + rep("A", 40)},

	{RuleOpenRouterAPIKey, "sk-or-v1-" + rep("0123456789abcdef", 4)},
	{RuleDatabricksPAT, "dapi" + rep("0123456789abcdef", 2)},
	{RuleLangfusePublicKey, "pk-lf-01234567-89ab-cdef-0123-456789abcdef"},
	{RuleLangfuseSecretKey, "sk-lf-01234567-89ab-cdef-0123-456789abcdef"},
	{RuleLangSmithPersonalToken, "lsv2_pt_" + rep("0123456789abcdef", 2) + "_" + rep("0123456789", 1)},
	{RuleLangSmithServiceKey, "lsv2_sk_" + rep("0123456789abcdef", 2) + "_" + rep("0123456789", 1)},
	{RuleBasetenAPIKey, "b10_" + rep("A", 8) + "." + rep("B", 32)},
	{RuleHeliconeAPIKey, "sk-helicone-" + rep("a", 7) + "-" + rep("b", 7) + "-" + rep("c", 7) + "-" + rep("d", 7)},
	{RuleWeightsAndBiasesKey, "wandb_v1_" + rep("a", 27) + "_" + rep("b", 49)},
	{RuleTogetherAPIKey, "tgp_v1_" + rep("A", 43)},
	{RuleGroqAPIKey, "gsk_" + rep("A", 52)},
	{RuleReplicateToken, "r8_" + rep("A", 37)},
	{RulePerplexityAPIKey, "pplx-" + rep("A", 48)},
	{RuleJinaAPIKey, "jina_" + rep("A", 60)},
	{RuleHuggingFaceToken, "hf_" + rep("a", 34)},
	{RuleHuggingFaceOrgToken, "api_org_" + rep("a", 34)},
	{RuleVoyageAPIKey, "pa-" + rep("A", 24)},
	{RuleCerebrasAPIKey, "csk-" + rep("A", 24)},
	{RuleAnyscaleAPIKey, "esct_" + rep("A", 20)},
	{RuleFireworksAPIKey, "fw_" + rep("A", 20)},
	{RuleFireworksProjectKey, "fpk_" + rep("A", 20)},
	{RuleModalTokenID, "ak-" + rep("A", 20)},
	{RuleModalTokenSecret, "as-" + rep("A", 20)},
	{RuleRunPodAPIKey, "rpa_" + rep("A", 20)},
	{RuleCartesiaAPIKey, "sk_car_" + rep("A", 20)},
	{RuleOpenPipeAPIKey, "opk_" + rep("A", 32)},
	{RulePromptLayerAPIKey, "pl_" + rep("0123456789abcdef", 2)},
	{RuleNomicAPIKey, "nk-" + rep("A", 40)},
	{RuleRunwayAPIKey, "key_" + rep("A", 48)},
	{RuleTraceloopAPIKey, "tl_" + rep("A", 32)},
	{RuleFriendliAPIKey, "flp_" + rep("A", 32)},
	{RuleLobeHubAPIKey, "lobehub-" + rep("A", 32)},
	{RuleLangflowAPIKey, "lf_" + rep("A", 32)},
	{RuleDifyAPIKey, "app-" + rep("A", 32)},
	{RulePortkeyAPIKey, "pk-" + rep("A", 32)},

	{RuleZhipuAPIKey, rep("0123456789abcdef", 2) + "." + rep("A", 16)},
	{RuleDashScopeCodingKey, "sk-sp-" + rep("A", 20)},
	{RuleDashScopeTemporaryKey, "st-" + rep("A", 20)},
	{RuleAliyunAccessKeyID, "LTAI" + rep("A", 16)},
	{RuleBaiduQianfanAPIKey, "bce-v3/ALTAK-" + rep("A", 20) + "/" + rep("B", 20)},
	{RuleMiniMaxAPIKey, "sk-api-" + rep("A", 20)},
	{RuleMiniMaxCodingKey, "sk-cp-" + rep("A", 20)},

	{RuleSkDashGeneric, "sk-" + rep("a1", 16)},
	{RuleSkUnderscoreGeneric, "sk_" + rep("A1", 16)},
}

// noiseSamples are benign strings that must never produce a span. Trajecta
// transcripts are full of these: commit ids, checksums, object ids and
// placeholder text.
var noiseSamples = []string{
	"3f2a9c8e1b4d5a6f7c8e9d0a1b2c3d4e5f607182",                         // git SHA-1, 40 hex
	"d41d8cd98f00b204e9800998ecf8427e",                                 // MD5, 32 hex
	"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", // SHA-256, 64 hex
	"550e8400-e29b-41d4-a716-446655440000",                             // UUID v4
	"QmFzZTY0QmxvYlRoYXRJc05vdEFDcmVkZW50aWFsMTIzNDU2",                 // base64 blob
	"YOUR_API_KEY", "<token>", "changeme", "REPLACE_ME",
	"sk-short", "as-well-as", "ak-", "pk-", "pl_",
	"the quick brown fox jumps over the lazy dog",
	"func handleRequest(w http.ResponseWriter, r *http.Request) {}",
}

func TestPositiveSamples(t *testing.T) {
	for _, tc := range positiveSamples {
		t.Run(strings.TrimPrefix(ruleTable[tc.rule].Name, "unknown"), func(t *testing.T) {
			spans := Scan([]byte(tc.text))
			for _, sp := range spans {
				if sp.ID == tc.rule {
					return
				}
			}
			var got []string
			for _, sp := range spans {
				r, _ := sp.Rule()
				got = append(got, r.Name+":"+string(sp.Bytes([]byte(tc.text))))
			}
			t.Fatalf("rule %q not matched by %q; got %v", ruleTable[tc.rule].Name, tc.text, got)
		})
	}
}

func TestNegativeSamples(t *testing.T) {
	for _, text := range noiseSamples {
		if spans := Scan([]byte(text)); len(spans) != 0 {
			var got []string
			for _, sp := range spans {
				r, _ := sp.Rule()
				got = append(got, r.Name+"="+string(sp.Bytes([]byte(text))))
			}
			t.Errorf("false positive on %q: %v", text, got)
		}
	}
}

// TestRuleMetadataComplete keeps rule.go and rules.rl in step. The DFA cannot
// report a rule that the metadata table does not know, and a metadata entry
// with no sample means a rule was dropped from the machine.
func TestRuleMetadataComplete(t *testing.T) {
	covered := make(map[RuleID]bool, len(positiveSamples))
	for _, tc := range positiveSamples {
		if covered[tc.rule] {
			t.Errorf("rule %q has more than one positive sample", ruleTable[tc.rule].Name)
		}
		covered[tc.rule] = true
	}
	for id := RuleUnknown + 1; id < ruleIDCount; id++ {
		if ruleTable[id].Name == "" {
			t.Errorf("rule %d has no metadata entry", id)
		}
		if !covered[id] {
			t.Errorf("rule %q has no positive sample", ruleTable[id].Name)
		}
	}
	for _, tc := range positiveSamples {
		if tc.rule == RuleUnknown || tc.rule >= ruleIDCount {
			t.Errorf("sample references unknown rule %d", tc.rule)
		}
	}
}

// TestSpansDoNotOverlap guards the longest-match contract: the scanner consumes
// what it matched, so two spans can never cover the same byte.
func TestSpansDoNotOverlap(t *testing.T) {
	var b strings.Builder
	for _, tc := range positiveSamples {
		b.WriteString("noise before ")
		b.WriteString(tc.text)
		b.WriteString(" noise after\n")
	}
	spans := Scan([]byte(b.String()))
	for i := 1; i < len(spans); i++ {
		if spans[i].Start < spans[i-1].End {
			t.Fatalf("span %d %+v overlaps previous %+v", i, spans[i], spans[i-1])
		}
	}
}

// TestMixedTranscript checks a realistic fragment: several credentials plus
// hashes, and asserts each credential is attributed to the right rule.
func TestMixedTranscript(t *testing.T) {
	doc := strings.Join([]string{
		"AWS_ACCESS_KEY_ID not covered by this milestone",
		"OPENAI_API_KEY=" + "sk-" + rep("A", 20) + "T3BlbkFJ" + rep("B", 20),
		"git commit 3f2a9c8e1b4d5a6f7c8e9d0a1b2c3d4e5f607182",
		"GITHUB_TOKEN=" + "ghp_" + rep("A", 36), // not covered by this milestone
		"ANTHROPIC_API_KEY=" + "sk-ant-api03-" + rep("A", 95),
		"checksum d41d8cd98f00b204e9800998ecf8427e",
		"DASHSCOPE_API_KEY=" + "sk-" + rep("c3", 16),
		"ZHIPUAI_API_KEY=" + rep("0123456789abcdef", 2) + "." + rep("A", 16),
	}, "\n")

	want := map[RuleID]int{
		RuleOpenAILegacy:    1,
		RuleAnthropicAPIKey: 1,
		RuleSkDashGeneric:   1,
		RuleZhipuAPIKey:     1,
	}
	got := map[RuleID]int{}
	for _, sp := range Scan([]byte(doc)) {
		got[sp.ID]++
	}
	for id, n := range want {
		if got[id] != n {
			r, _ := RuleFor(id)
			t.Errorf("rule %q: got %d spans, want %d (all: %v)", r.Name, got[id], n, got)
		}
	}
}

// benchmarkPayload builds a realistic transcript-shaped buffer: mostly prose
// and hashes, with a few credentials. Real traffic is dominated by the former,
// so throughput on this shape is what a proxy actually pays.
func benchmarkPayload() []byte {
	var b strings.Builder
	for i := 0; i < 400; i++ {
		b.WriteString("commit 3f2a9c8e1b4d5a6f7c8e9d0a1b2c3d4e5f607182\n")
		b.WriteString("blob d41d8cd98f00b204e9800998ecf8427e\n")
		b.WriteString("id 550e8400-e29b-41d4-a716-446655440000\n")
		b.WriteString("  return fmt.Errorf(\"upstream returned %d\", resp.StatusCode)\n")
	}
	b.WriteString("OPENAI_API_KEY=sk-" + rep("A", 20) + "T3BlbkFJ" + rep("B", 20) + "\n")
	b.WriteString("ANTHROPIC_API_KEY=sk-ant-api03-" + rep("A", 95) + "\n")
	b.WriteString("ZHIPUAI_API_KEY=" + rep("0123456789abcdef", 2) + "." + rep("A", 16) + "\n")
	return []byte(b.String())
}

func BenchmarkScan(b *testing.B) {
	payload := benchmarkPayload()
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Scan(payload)
	}
}

// BenchmarkAppendFindings measures the steady-state cost when a caller reuses one
// span buffer, which is how the proxy will drive the detector.
func BenchmarkAppendFindings(b *testing.B) {
	payload := benchmarkPayload()
	dst := make([]Finding, 0, 32)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dst = Detector{}.AppendFindings(dst[:0], payload)
	}
}

// BenchmarkScanNoSecrets is the worst case for the match loop: no rule can
// short-circuit, and every byte is pushed through the DFA.
func BenchmarkScanNoSecrets(b *testing.B) {
	payload := benchmarkPayload()
	cut := bytes.LastIndexByte(payload, '\n')
	for i := 0; i < 2; i++ {
		cut = bytes.LastIndexByte(payload[:cut], '\n')
	}
	payload = payload[:cut]
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Scan(payload)
	}
}
