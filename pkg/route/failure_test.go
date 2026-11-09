package route

import (
	"net/http"
	"testing"
	"time"
)

func retryAfterHeader(value string) http.Header {
	return http.Header{"Retry-After": []string{value}}
}

func TestClassifyFailure(t *testing.T) {
	tests := []struct {
		name   string
		status int
		header http.Header
		body   string
		want   Failure
	}{
		{
			name:   "402 is quota",
			status: http.StatusPaymentRequired,
			want: Failure{
				Class:            FailureQuota,
				Retryable:        true,
				TargetScoped:     true,
				CredentialScoped: true,
				Cooldown:         quotaCooldown,
				Reason:           "quota or balance exhausted",
				StatusCode:       http.StatusPaymentRequired,
			},
		},
		{
			name:   "429 is rate with Retry-After seconds",
			status: http.StatusTooManyRequests,
			header: retryAfterHeader("7"),
			want: Failure{
				Class:            FailureRate,
				Retryable:        true,
				TargetScoped:     true,
				CredentialScoped: true,
				Cooldown:         7 * time.Second,
				Reason:           "rate limited",
				StatusCode:       http.StatusTooManyRequests,
			},
		},
		{
			name:   "429 with zero Retry-After means no cooldown",
			status: http.StatusTooManyRequests,
			header: retryAfterHeader("0"),
			want: Failure{
				Class:            FailureRate,
				Retryable:        true,
				TargetScoped:     true,
				CredentialScoped: true,
				Cooldown:         0,
				Reason:           "rate limited",
				StatusCode:       http.StatusTooManyRequests,
			},
		},
		{
			name:   "429 with an invalid Retry-After uses the default window",
			status: http.StatusTooManyRequests,
			header: retryAfterHeader("later"),
			want: Failure{
				Class:            FailureRate,
				Retryable:        true,
				TargetScoped:     true,
				CredentialScoped: true,
				Cooldown:         rateCooldown,
				Reason:           "rate limited",
				StatusCode:       http.StatusTooManyRequests,
			},
		},
		{
			name:   "401 is auth",
			status: http.StatusUnauthorized,
			want: Failure{
				Class:            FailureAuth,
				TargetScoped:     true,
				CredentialScoped: true,
				Reason:           "authentication or authorization rejected",
				StatusCode:       http.StatusUnauthorized,
			},
		},
		{
			name:   "403 is auth",
			status: http.StatusForbidden,
			want: Failure{
				Class:            FailureAuth,
				TargetScoped:     true,
				CredentialScoped: true,
				Reason:           "authentication or authorization rejected",
				StatusCode:       http.StatusForbidden,
			},
		},
		{
			name:   "404 is model missing",
			status: http.StatusNotFound,
			body:   `{"error":{"message":"The model does not exist"}}`,
			want: Failure{
				Class:        FailureModelMissing,
				Retryable:    true,
				TargetScoped: true,
				ModelScoped:  true,
				Reason:       "model not available on target",
				StatusCode:   http.StatusNotFound,
			},
		},
		{
			name:   "500 is upstream",
			status: http.StatusInternalServerError,
			want: Failure{
				Class:        FailureUpstream,
				Retryable:    true,
				TargetScoped: true,
				Cooldown:     upstreamCooldown,
				Reason:       "upstream server error",
				StatusCode:   http.StatusInternalServerError,
			},
		},
		{
			name:   "503 is upstream",
			status: http.StatusServiceUnavailable,
			want: Failure{
				Class:        FailureUpstream,
				Retryable:    true,
				TargetScoped: true,
				Cooldown:     upstreamCooldown,
				Reason:       "upstream server error",
				StatusCode:   http.StatusServiceUnavailable,
			},
		},
		{
			name:   "400 unknown field is shape",
			status: http.StatusBadRequest,
			body:   `{"error":{"type":"invalid_request_error","message":"Unrecognized request argument supplied: temperature"}}`,
			want: Failure{
				Class:      FailureShape,
				Reason:     "request shape rejected by upstream",
				StatusCode: http.StatusBadRequest,
			},
		},
		{
			name:   "422 missing required field is shape",
			status: http.StatusUnprocessableEntity,
			body:   `{"error":{"message":"missing required field: messages"}}`,
			want: Failure{
				Class:      FailureShape,
				Reason:     "request shape rejected by upstream",
				StatusCode: http.StatusUnprocessableEntity,
			},
		},
		{
			name:   "400 Chinese parameter words are shape",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"参数错误：缺少必填字段 messages"}}`,
			want: Failure{
				Class:      FailureShape,
				Reason:     "request shape rejected by upstream",
				StatusCode: http.StatusBadRequest,
			},
		},
		{
			name:   "400 context length is context",
			status: http.StatusBadRequest,
			body:   `{"error":{"type":"invalid_request_error","message":"This model's maximum context length is 128000 tokens"}}`,
			want: Failure{
				Class:       FailureContext,
				ModelScoped: true,
				Reason:      "request exceeds the model context limit",
				StatusCode:  http.StatusBadRequest,
			},
		},
		{
			name:   "400 Chinese context words are context",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"上下文长度超出模型限制"}}`,
			want: Failure{
				Class:       FailureContext,
				ModelScoped: true,
				Reason:      "request exceeds the model context limit",
				StatusCode:  http.StatusBadRequest,
			},
		},
		{
			name:   "context is checked before the invalid request envelope",
			status: http.StatusBadRequest,
			body:   `{"error":{"type":"invalid_request_error","message":"context_length_exceeded: reduce the length of the messages"}}`,
			want: Failure{
				Class:       FailureContext,
				ModelScoped: true,
				Reason:      "request exceeds the model context limit",
				StatusCode:  http.StatusBadRequest,
			},
		},
		{
			name:   "400 protocol_conversion code is conversion",
			status: http.StatusBadRequest,
			body:   `{"error":{"type":"protocol_conversion","code":"protocol_conversion_unsupported","message":"field cannot be carried"}}`,
			want: Failure{
				Class:      FailureConversion,
				Retryable:  true,
				Reason:     "protocol conversion failed",
				StatusCode: http.StatusBadRequest,
			},
		},
		{
			name:   "400 safety words are refusal",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"Your request was blocked by our safety system"}}`,
			want: Failure{
				Class:      FailureRefusal,
				Reason:     "model refused the request",
				StatusCode: http.StatusBadRequest,
			},
		},
		{
			name:   "400 Chinese safety words are refusal",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"内容安全审核未通过，请求已被拦截"}}`,
			want: Failure{
				Class:      FailureRefusal,
				Reason:     "model refused the request",
				StatusCode: http.StatusBadRequest,
			},
		},
		{
			name:   "refusal is checked before shape envelope words",
			status: http.StatusBadRequest,
			body:   `{"error":{"type":"invalid_request_error","message":"content policy violation"}}`,
			want: Failure{
				Class:      FailureRefusal,
				Reason:     "model refused the request",
				StatusCode: http.StatusBadRequest,
			},
		},
		{
			name:   "400 with no recognized words is unknown",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"something went wrong"}}`,
			want: Failure{
				Class:      FailureUnknown,
				Reason:     "unclassified upstream failure",
				StatusCode: http.StatusBadRequest,
			},
		},
		{
			name:   "empty 400 is unknown",
			status: http.StatusBadRequest,
			want: Failure{
				Class:      FailureUnknown,
				Reason:     "unclassified upstream failure",
				StatusCode: http.StatusBadRequest,
			},
		},
		{
			name:   "non 400 refusal words still classify as refusal",
			status: 418,
			body:   `the model refused this request`,
			want: Failure{
				Class:      FailureRefusal,
				Reason:     "model refused the request",
				StatusCode: 418,
			},
		},
		{
			name:   "unrecognized status without words is unknown",
			status: 418,
			want: Failure{
				Class:      FailureUnknown,
				Reason:     "unclassified upstream failure",
				StatusCode: 418,
			},
		},
		{
			name:   "status takes precedence over quota body words",
			status: http.StatusTooManyRequests,
			body:   `{"error":{"code":"insufficient_quota","message":"You exceeded your current quota"}}`,
			want: Failure{
				Class:            FailureRate,
				Retryable:        true,
				TargetScoped:     true,
				CredentialScoped: true,
				Cooldown:         rateCooldown,
				Reason:           "rate limited",
				StatusCode:       http.StatusTooManyRequests,
			},
		},
		{
			name:   "status 404 wins over body context words",
			status: http.StatusNotFound,
			body:   `{"error":{"message":"context length limit reached for model"}}`,
			want: Failure{
				Class:        FailureModelMissing,
				Retryable:    true,
				TargetScoped: true,
				ModelScoped:  true,
				Reason:       "model not available on target",
				StatusCode:   http.StatusNotFound,
			},
		},
		{
			name:   "5xx wins over conversion body words",
			status: http.StatusBadGateway,
			body:   `{"error":{"code":"protocol_conversion_unsupported"}}`,
			want: Failure{
				Class:        FailureUpstream,
				Retryable:    true,
				TargetScoped: true,
				Cooldown:     upstreamCooldown,
				Reason:       "upstream server error",
				StatusCode:   http.StatusBadGateway,
			},
		},
		{
			name:   "uppercase English body words are recognized",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"UNKNOWN FIELD temperature"}}`,
			want: Failure{
				Class:      FailureShape,
				Reason:     "request shape rejected by upstream",
				StatusCode: http.StatusBadRequest,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ClassifyFailure(test.status, test.header, []byte(test.body))
			if got != test.want {
				t.Fatalf("ClassifyFailure() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestClassifyFailureRetryAfterHTTPDate(t *testing.T) {
	when := time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)
	got := ClassifyFailure(http.StatusTooManyRequests, retryAfterHeader(when), nil)
	if got.Class != FailureRate {
		t.Fatalf("Class = %q, want %q", got.Class, FailureRate)
	}
	if got.Cooldown < 80*time.Second || got.Cooldown > 90*time.Second {
		t.Fatalf("Cooldown = %v, want about 90s", got.Cooldown)
	}
}

func TestClassifyFailureNilHeader(t *testing.T) {
	got := ClassifyFailure(http.StatusTooManyRequests, nil, nil)
	if got.Cooldown != rateCooldown {
		t.Fatalf("Cooldown = %v, want %v", got.Cooldown, rateCooldown)
	}
}
