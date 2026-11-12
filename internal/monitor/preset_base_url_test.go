package monitor

import (
	"testing"

	"github.com/kingfs/Trajecta/internal/store"
)

func TestDefaultPresetBaseURL(t *testing.T) {
	cases := []struct {
		name    string
		preset  string
		family  string
		apiType string
		want    string
	}{
		{name: "openai chat", preset: "openai", want: "https://api.openai.com/v1"},
		{name: "openai responses", preset: "openai", apiType: "responses", want: "https://api.openai.com/v1"},
		{name: "anthropic messages", preset: "anthropic", want: "https://api.anthropic.com"},
		{name: "deepseek chat", preset: "deepseek", want: "https://api.deepseek.com/v1"},
		{name: "deepseek messages", preset: "deepseek", apiType: "messages", want: "https://api.deepseek.com/anthropic"},
		{name: "openrouter messages", preset: "openrouter", family: "anthropic_messages", want: "https://openrouter.ai/api"},
		{name: "qwen coding", preset: "qwen_coding", want: "https://coding.dashscope.aliyuncs.com/v1"},
		{name: "qwen coding intl", preset: "qwen_coding_intl", want: "https://coding-intl.dashscope.aliyuncs.com/v1"},
		{name: "qwen coding anthropic", preset: "qwen_coding_anthropic", family: "anthropic_messages", apiType: "messages", want: "https://coding.dashscope.aliyuncs.com/apps/anthropic"},
		{name: "qwen coding intl anthropic", preset: "qwen_coding_intl_anthropic", family: "anthropic_messages", apiType: "messages", want: "https://coding-intl.dashscope.aliyuncs.com/apps/anthropic"},
		{name: "zai coding anthropic", preset: "zai_coding_anthropic", family: "anthropic_messages", apiType: "messages", want: "https://api.z.ai/api/anthropic"},
		{name: "zhipu coding anthropic", preset: "zhipu_coding_anthropic", family: "anthropic_messages", apiType: "messages", want: "https://open.bigmodel.cn/api/anthropic"},
		{name: "qwen token plan", preset: "qwen_token_plan", want: "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1"},
		{name: "qwen token plan intl", preset: "qwen_token_plan_intl", want: "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1"},
		{name: "zai coding", preset: "zai_coding", want: "https://api.z.ai/api/coding/paas/v4"},
		{name: "zhipu coding", preset: "zhipu_coding", want: "https://open.bigmodel.cn/api/coding/paas/v4"},
		{name: "kimi coding", preset: "kimi_coding", family: "anthropic_messages", apiType: "messages", want: "https://api.kimi.ai/coding"},
		{name: "google gemini", preset: "google", family: "google_genai", want: "https://generativelanguage.googleapis.com/v1beta"},
		{name: "azure has no universal endpoint", preset: "azure", want: ""},
		{name: "custom", preset: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := defaultPresetBaseURL(tc.preset, "", tc.family, tc.apiType); got != tc.want {
				t.Fatalf("defaultPresetBaseURL(%q,%q,%q) = %q, want %q", tc.preset, tc.family, tc.apiType, got, tc.want)
			}
		})
	}
}

func TestChannelRecordFromRequestFillsPresetBaseURL(t *testing.T) {
	record := channelRecordFromRequest(channelUpsertRequest{
		Name:           "OpenAI",
		ProviderPreset: "openai",
	}, storeChannelRecordForTest())
	if record.BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("BaseURL = %q, want preset endpoint", record.BaseURL)
	}

	record = channelRecordFromRequest(channelUpsertRequest{
		Name:           "Custom",
		BaseURL:        "https://custom.invalid/v1",
		ProviderPreset: "openai",
	}, storeChannelRecordForTest())
	if record.BaseURL != "https://custom.invalid/v1" {
		t.Fatalf("explicit BaseURL = %q, want override", record.BaseURL)
	}
}

func storeChannelRecordForTest() store.ChannelConfigRecord {
	return store.ChannelConfigRecord{}
}
