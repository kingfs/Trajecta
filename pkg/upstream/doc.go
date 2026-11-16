// Package upstream is the reusable upstream catalog package for Trajecta.
//
// It carries the provider presets (base URLs, auth specification, model list
// source, plan variants), the channel-plugin view derived from them, and the
// pure request rules over an assembled Resolved value: request URL construction
// (BuildURL) and auth header application (ApplyAuth). It deliberately does not
// read configuration, open a database, own a credential store, discover models
// or implement an OAuth flow; callers in internal/ inject those concerns.
// internal/upstream assembles Resolved from channel configuration and delegates
// BuildURL/ApplyAuth here.
//
// A typical flow is:
//
//	preset, ok := upstream.LookupPreset("deepseek")
//	if !ok {
//	    return errUnknownPreset
//	}
//	r := upstream.Resolved{Preset: preset, Endpoints: preset.Endpoints, Auth: preset.Auth}
//	url, err := r.BuildURL(xir.ProtocolChatCompletions, "/v1/chat/completions")
//	req, _ := http.NewRequest(http.MethodPost, url, body)
//	err = r.ApplyAuth(upstream.Credential{Mode: upstream.AuthModeAPIKey, APIKey: key}, req)
//
// OAuth and CLI-credential flows are reserved: the types and the AuthDriver
// interface exist so a future internal/credential package can implement them,
// but this package contains no flow implementation.
package upstream
