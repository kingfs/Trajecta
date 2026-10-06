package live

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func monitorDelete(t *testing.T, path string) httpResult {
	t.Helper()
	return request(t, http.MethodDelete, monitorURL()+path, nil, map[string]string{
		"Authorization": "Bearer " + monitorToken(t),
	})
}

// TestLiveModelAliasLifecycle exercises the Monitor write path for model
// aliases end to end: validate -> create -> route real traffic through the
// alias -> list -> delete by internal id -> gone.
func TestLiveModelAliasLifecycle(t *testing.T) {
	requireLive(t)

	alias := fmt.Sprintf("live-alias-%d", time.Now().UnixNano()%1000000)
	payload := map[string]any{
		"alias":        alias,
		"target_model": modelName(),
		"channel_id":   "baizhi-openai",
		"enabled":      true,
	}

	validate := monitorRequest(t, http.MethodPost, "/api/model-aliases/validate", payload)
	if validate.Status != http.StatusOK {
		t.Fatalf("validate alias: want 200, got %s", validate)
	}
	var validated struct {
		Valid  bool     `json:"valid"`
		Errors []string `json:"errors"`
	}
	decodeJSON(t, validate.Body, &validated)
	if !validated.Valid {
		t.Fatalf("validate alias: not valid, errors=%v", validated.Errors)
	}

	created := monitorRequest(t, http.MethodPost, "/api/model-aliases", payload)
	if created.Status != http.StatusOK && created.Status != http.StatusCreated {
		t.Fatalf("create alias: want 2xx, got %s", created)
	}
	var createdBody struct {
		ID    string `json:"id"`
		Alias string `json:"alias"`
	}
	decodeJSON(t, created.Body, &createdBody)
	if createdBody.ID == "" || createdBody.Alias != alias {
		t.Fatalf("create alias: unexpected body: %s", truncate(string(created.Body), 300))
	}
	t.Cleanup(func() {
		res := monitorDelete(t, "/api/model-aliases/"+createdBody.ID)
		if res.Status != http.StatusOK && res.Status != http.StatusNoContent && res.Status != http.StatusNotFound {
			t.Errorf("cleanup alias %s: unexpected status %s", createdBody.ID, res)
		}
	})

	// The alias must be routable through the proxy.
	body := map[string]any{
		"model":      alias,
		"messages":   []map[string]string{{"role": "user", "content": "Reply with exactly: ok"}},
		"max_tokens": 128,
	}
	routed := proxyRequest(t, http.MethodPost, "/v1/chat/completions", body, nil)
	if routed.Status != http.StatusOK {
		t.Fatalf("routing through alias %s: want 200, got %s", alias, routed)
	}
	var completion struct {
		Model string `json:"model"`
	}
	decodeJSON(t, routed.Body, &completion)
	if completion.Model != modelName() {
		t.Errorf("alias %s resolved to model=%q, want %q", alias, completion.Model, modelName())
	}

	list := monitorGET(t, "/api/model-aliases")
	if list.Status != http.StatusOK {
		t.Fatalf("list aliases: want 200, got %s", list)
	}
	var listed struct {
		Items []struct {
			ID    string `json:"id"`
			Alias string `json:"alias"`
		} `json:"items"`
	}
	decodeJSON(t, list.Body, &listed)
	found := false
	for _, item := range listed.Items {
		if item.ID == createdBody.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("created alias %s not present in /api/model-aliases", createdBody.ID)
	}

	deleted := monitorDelete(t, "/api/model-aliases/"+createdBody.ID)
	if deleted.Status != http.StatusOK && deleted.Status != http.StatusNoContent {
		t.Fatalf("delete alias: want 2xx, got %s", deleted)
	}

	after := monitorGET(t, "/api/model-aliases/"+createdBody.ID)
	if after.Status == http.StatusOK {
		var stale map[string]any
		if err := json.Unmarshal(after.Body, &stale); err == nil && len(stale) > 0 {
			t.Errorf("alias %s still readable after delete: %s", createdBody.ID, truncate(string(after.Body), 200))
		}
	}
}
