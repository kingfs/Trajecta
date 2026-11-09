package upstream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kingfs/Trajecta/pkg/xir"
)

func TestDiscoverModelsOpenAI(t *testing.T) {
	var gotPath, gotAuth, gotAccept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-a"},{"id":"gpt-b"},{"id":"gpt-a"}]}`))
	}))
	defer server.Close()

	resolved := mustResolve(t, Config{
		PresetID: "openai",
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolChatCompletions: server.URL + "/v1",
			xir.ProtocolResponses:       server.URL + "/v1",
		},
	}).WithCredential(Credential{Mode: AuthModeAPIKey, APIKey: "sk-test"})

	models, err := DiscoverModels(context.Background(), server.Client(), resolved)
	if err != nil {
		t.Fatalf("DiscoverModels: %v", err)
	}
	want := []string{"gpt-a", "gpt-b"}
	if len(models) != len(want) || models[0] != want[0] || models[1] != want[1] {
		t.Errorf("models = %v, want %v", models, want)
	}
	if gotPath != "/v1/models" {
		t.Errorf("request path = %q", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q", gotAccept)
	}
}

func TestDiscoverModelsGemini(t *testing.T) {
	var gotPath, gotKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-goog-api-key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[
			{"name":"models/gemini-2.5-flash"},
			{"name":"publishers/google/models/gemini-2.5-pro"},
			{"name":"models/gemini-2.5-flash"}
		]}`))
	}))
	defer server.Close()

	resolved := mustResolve(t, Config{
		PresetID: "google",
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolGemini:          server.URL + "/v1beta",
			xir.ProtocolChatCompletions: server.URL + "/v1beta/openai",
		},
	}).WithCredential(Credential{Mode: AuthModeAPIKey, APIKey: "google-key"})

	models, err := DiscoverModels(context.Background(), server.Client(), resolved)
	if err != nil {
		t.Fatalf("DiscoverModels: %v", err)
	}
	want := []string{"models/gemini-2.5-flash", "publishers/google/models/gemini-2.5-pro"}
	if len(models) != len(want) || models[0] != want[0] || models[1] != want[1] {
		t.Errorf("models = %v, want %v", models, want)
	}
	if gotPath != "/v1beta/models" {
		t.Errorf("request path = %q", gotPath)
	}
	if gotKey != "google-key" {
		t.Errorf("x-goog-api-key = %q", gotKey)
	}
}

func TestDiscoverModelsStaticHeadersWithoutCredential(t *testing.T) {
	var gotStatic string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotStatic = r.Header.Get("X-Static")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	resolved := mustResolve(t, Config{
		PresetID:  "openai",
		Endpoints: map[xir.Protocol]string{xir.ProtocolChatCompletions: server.URL + "/v1"},
		Headers:   map[string]string{"X-Static": "yes"},
	})
	models, err := DiscoverModels(context.Background(), server.Client(), resolved)
	if err != nil {
		t.Fatalf("DiscoverModels: %v", err)
	}
	if len(models) != 0 {
		t.Errorf("models = %v, want empty", models)
	}
	if gotStatic != "yes" {
		t.Errorf("X-Static = %q", gotStatic)
	}
}

func TestDiscoverModelsStatusErrors(t *testing.T) {
	tests := []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, ErrModelListUnauthorized},
		{http.StatusForbidden, ErrModelListUnauthorized},
		{http.StatusNotFound, ErrModelListNotFound},
		{http.StatusTooManyRequests, ErrModelListUnavailable},
		{http.StatusInternalServerError, ErrModelListUnavailable},
	}
	for _, test := range tests {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte("upstream says no"))
			}))
			defer server.Close()
			resolved := mustResolve(t, Config{
				PresetID:  "openai",
				Endpoints: map[xir.Protocol]string{xir.ProtocolChatCompletions: server.URL + "/v1"},
			})
			_, err := DiscoverModels(context.Background(), server.Client(), resolved)
			if err == nil {
				t.Fatal("DiscoverModels returned no error")
			}
			if !errors.Is(err, test.want) {
				t.Errorf("error = %v, want errors.Is(%v)", err, test.want)
			}
			var statusErr *StatusError
			if !errors.As(err, &statusErr) {
				t.Fatalf("error is not a *StatusError: %v", err)
			}
			if statusErr.StatusCode != test.status {
				t.Errorf("StatusError.StatusCode = %d, want %d", statusErr.StatusCode, test.status)
			}
		})
	}
}

func TestDiscoverModelsNoList(t *testing.T) {
	github := mustResolve(t, Config{PresetID: "github"})
	if _, err := DiscoverModels(context.Background(), nil, github); !errors.Is(err, ErrNoModelList) {
		t.Errorf("NoList error = %v", err)
	}

	openai := mustResolve(t, Config{PresetID: "openai"})
	openai.Models.ListPath = ""
	if _, err := DiscoverModels(context.Background(), nil, openai); !errors.Is(err, ErrNoModelList) {
		t.Errorf("empty ListPath error = %v", err)
	}
}

func TestDiscoverModelsInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer server.Close()
	resolved := mustResolve(t, Config{
		PresetID:  "openai",
		Endpoints: map[xir.Protocol]string{xir.ProtocolChatCompletions: server.URL + "/v1"},
	})
	if _, err := DiscoverModels(context.Background(), server.Client(), resolved); err == nil {
		t.Fatal("DiscoverModels accepted invalid JSON")
	}
}

func TestDiscoverModelsContextCanceled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	resolved := mustResolve(t, Config{
		PresetID:  "openai",
		Endpoints: map[xir.Protocol]string{xir.ProtocolChatCompletions: server.URL + "/v1"},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DiscoverModels(ctx, server.Client(), resolved); err == nil {
		t.Fatal("DiscoverModels ignored a canceled context")
	}
}
