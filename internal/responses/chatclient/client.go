// Package chatclient holds the shared chat-completions stream aggregator and
// the upstream error type. The local Responses runtime's upstream call itself
// lives in the proxy's responsesChatCompletionsAdapter, which owns routing,
// recording and masking; this package only provides the pieces that adapter
// and the HTTP surface share.
package chatclient

import "fmt"

// HTTPError reports a non-2xx upstream chat-completions answer. The proxy's
// local Responses adapter constructs it (truncating the body to its own bound)
// and internal/responses/httpapi classifies it into the client-visible error
// envelope.
type HTTPError struct {
	StatusCode    int
	Status        string
	Body          string
	BodyTruncated bool
}

func (e HTTPError) Error() string {
	if e.BodyTruncated {
		return fmt.Sprintf("chat completion request failed: status %d: %s... (truncated)", e.StatusCode, e.Body)
	}
	return fmt.Sprintf("chat completion request failed: status %d: %s", e.StatusCode, e.Body)
}
