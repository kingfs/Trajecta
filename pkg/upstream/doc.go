// Package upstream is the reusable upstream catalog and resolution package for
// Trajecta.
//
// It carries the provider presets (base URLs, auth specification, model list
// source, plan variants) and the pure rules that turn a channel configuration
// into an immutable Resolved value: endpoint merging, protocol derivation,
// request URL construction, auth header application, capability answers and
// optional model discovery. It deliberately does not read configuration, open a
// database, own a credential store or implement an OAuth flow; callers in
// internal/ inject those concerns.
//
// A typical flow is:
//
//	r, err := upstream.Resolve(upstream.Config{PresetID: "deepseek"})
//	if err != nil {
//	    return err
//	}
//	r = r.WithCredential(upstream.Credential{
//	    Mode:   upstream.AuthModeAPIKey,
//	    APIKey: key,
//	})
//	url, err := r.BuildURL(xir.ProtocolChatCompletions, "/v1/chat/completions")
//	req, _ := http.NewRequest(http.MethodPost, url, body)
//	err = r.ApplyAuth(r.Credential, req)
//
// OAuth and CLI-credential flows are reserved: the types and the AuthDriver
// interface exist so a future internal/credential package can implement them,
// but this package contains no flow implementation.
package upstream
