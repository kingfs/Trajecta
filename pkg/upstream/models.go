package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/kingfs/Trajecta/pkg/xir"
)

// ModelSource describes how a preset exposes its model list. Discovery is
// always a network call made by the caller-supplied HTTP client; this package
// never caches or persists the result.
type ModelSource struct {
	// ListPath is the path appended to the selected endpoint base to list
	// models. An empty ListPath means no discovery.
	ListPath string
	// Static is a declarative model list carried by the catalog. DiscoverModels
	// does not return it and does not require it.
	Static []string
	// NoList disables model discovery explicitly.
	NoList bool
	// Protocol selects which endpoint ListPath is relative to. The zero value
	// falls back to Chat Completions, Responses, Messages and Gemini in that
	// order.
	Protocol xir.Protocol
}

const maxModelListBody = 4 << 20

// DiscoverModels lists the models the resolved upstream serves. It sends one
// GET request with the resolved credential (when one is attached with
// Resolved.WithCredential), parses the OpenAI-style {"data":[{"id":...}]} and
// Gemini-style {"models":[{"name":...}]} envelopes, and returns model names in
// upstream order with duplicates removed. Gemini names are returned as reported
// (the "models/..." resource prefix is preserved); callers that need a compact
// client ID can normalize it themselves.
//
// The function performs no persistence, no caching and no retry. A preset with
// NoList, or one whose ListPath is empty, returns an error wrapping
// ErrNoModelList.
func DiscoverModels(ctx context.Context, client *http.Client, resolved Resolved) ([]string, error) {
	if ctx == nil {
		return nil, errors.New("upstream: discover models: nil context")
	}
	if resolved.Models.NoList {
		return nil, fmt.Errorf("%w: preset %q disables discovery", ErrNoModelList, resolved.Preset.ID)
	}
	listPath := strings.TrimSpace(resolved.Models.ListPath)
	if listPath == "" {
		return nil, fmt.Errorf("%w: preset %q has no list path", ErrNoModelList, resolved.Preset.ID)
	}
	protocol, err := modelListProtocol(resolved)
	if err != nil {
		return nil, err
	}
	listURL, err := resolved.BuildURL(protocol, listPath)
	if err != nil {
		return nil, fmt.Errorf("upstream: discover models: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		return nil, fmt.Errorf("upstream: discover models: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if !resolved.Credential.IsZero() {
		if err := resolved.ApplyAuth(resolved.Credential, req); err != nil {
			return nil, fmt.Errorf("upstream: discover models: %w", err)
		}
	} else {
		resolved.applyConfiguredHeaders(Credential{}, req)
	}
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upstream: discover models: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxModelListBody))
	if err != nil {
		return nil, fmt.Errorf("upstream: discover models: read body: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, &StatusError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       truncateBody(string(body)),
		}
	}
	models, err := parseModelList(body)
	if err != nil {
		return nil, fmt.Errorf("upstream: discover models: %w", err)
	}
	return models, nil
}

// StatusError reports a non-2xx model-list response. Use errors.Is with
// ErrModelListUnauthorized, ErrModelListNotFound or ErrModelListUnavailable to
// classify it without inspecting StatusCode.
type StatusError struct {
	StatusCode int
	Status     string
	Body       string
}

// Error implements error.
func (e *StatusError) Error() string {
	if e == nil {
		return "upstream: model list request failed"
	}
	message := fmt.Sprintf("upstream: model list request failed: %s", strings.TrimSpace(e.Status))
	if e.StatusCode == 0 {
		message = "upstream: model list request failed"
	}
	if e.Body != "" {
		message += ": " + e.Body
	}
	return message
}

// Is implements errors.Is classification for the package sentinels.
func (e *StatusError) Is(target error) bool {
	if e == nil {
		return false
	}
	switch target {
	case ErrModelListUnauthorized:
		return e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden
	case ErrModelListNotFound:
		return e.StatusCode == http.StatusNotFound
	case ErrModelListUnavailable:
		return e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= http.StatusInternalServerError
	}
	return false
}

func modelListProtocol(resolved Resolved) (xir.Protocol, error) {
	if resolved.Models.Protocol != "" {
		if !resolved.Models.Protocol.Valid() {
			return "", fmt.Errorf("%w: %q", ErrUnsupportedProtocol, resolved.Models.Protocol)
		}
		return resolved.Models.Protocol, nil
	}
	for _, protocol := range []xir.Protocol{
		xir.ProtocolChatCompletions,
		xir.ProtocolResponses,
		xir.ProtocolMessages,
		xir.ProtocolGemini,
	} {
		if endpoint := resolved.Endpoints[protocol]; strings.TrimSpace(endpoint) != "" {
			return protocol, nil
		}
	}
	return "", fmt.Errorf("%w: preset %q has no endpoint", ErrNoModelList, resolved.Preset.ID)
}

type modelListPayload struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
	Models []struct {
		Name string `json:"name"`
		ID   string `json:"id"`
	} `json:"models"`
}

func parseModelList(body []byte) ([]string, error) {
	var payload modelListPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode model list: %w", err)
	}
	out := make([]string, 0, len(payload.Data)+len(payload.Models))
	seen := make(map[string]struct{}, len(payload.Data)+len(payload.Models))
	appendModel := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if len(payload.Data) > 0 {
		for _, model := range payload.Data {
			appendModel(model.ID)
		}
		return out, nil
	}
	for _, model := range payload.Models {
		name := model.Name
		if name == "" {
			name = model.ID
		}
		appendModel(name)
	}
	return out, nil
}

func truncateBody(body string) string {
	body = strings.TrimSpace(body)
	const limit = 512
	if len(body) <= limit {
		return body
	}
	return body[:limit] + "..."
}
