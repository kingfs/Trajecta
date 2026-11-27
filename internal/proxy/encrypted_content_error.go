package proxy

import (
	"encoding/json"
	"io"
)

type prefixedResponseBody struct {
	io.Reader
	io.Closer
}

func (f upstreamFailure) invalidEncryptedContent() bool {
	return encryptedContentErrorCode([]byte(f.rawBody), 0)
}

func encryptedContentErrorCode(body []byte, depth int) bool {
	if depth >= 8 {
		return false
	}
	var envelope struct {
		Code    string          `json:"code"`
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return false
	}
	if envelope.Code == "invalid_encrypted_content" {
		return true
	}
	return encryptedContentErrorCode(envelope.Error, depth+1) ||
		encryptedContentErrorCode([]byte(envelope.Message), depth+1)
}
