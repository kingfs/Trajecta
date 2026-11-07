package store

import "testing"

// The model square needs channel names and availability, never credentials. A
// channel whose local secret cannot be decrypted must not take the whole page
// down, so the metadata read deliberately skips decryption and returns no
// secret material at all.
func TestListChannelConfigMetadataSkipsUndecryptableSecrets(t *testing.T) {
	t.Parallel()

	st, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if _, err := st.UpsertChannelConfig(ChannelConfigRecord{
		ID: "metadata-only", Name: "Metadata Only", BaseURL: "https://metadata.invalid/v1",
		ProviderPreset: "openai", APIType: "chat_completions", ProtocolFamily: "openai_compatible",
		APIKeyCiphertext: []byte("sk-test"), HeadersJSON: `{"Authorization":"Bearer test"}`, Enabled: true,
	}); err != nil {
		t.Fatalf("UpsertChannelConfig() error = %v", err)
	}
	if _, err := st.db.Exec(`UPDATE channel_configs SET api_key_ciphertext = ? WHERE id = ?`, []byte("tlsec:v1:not-base64"), "metadata-only"); err != nil {
		t.Fatalf("corrupt api key: %v", err)
	}

	if _, err := st.ListChannelConfigs(); err == nil {
		t.Fatal("ListChannelConfigs() succeeded with an undecryptable key, want an error")
	}
	configs, err := st.ListChannelConfigMetadata()
	if err != nil {
		t.Fatalf("ListChannelConfigMetadata() error = %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("ListChannelConfigMetadata() = %+v", configs)
	}
	config := configs[0]
	if config.ID != "metadata-only" || config.Name != "Metadata Only" || !config.Enabled || config.ProviderPreset != "openai" {
		t.Fatalf("metadata config = %+v", config)
	}
	if len(config.APIKeyCiphertext) != 0 || config.HeadersJSON != "{}" {
		t.Fatalf("metadata read returned secret material: key=%q headers=%q", config.APIKeyCiphertext, config.HeadersJSON)
	}
}
