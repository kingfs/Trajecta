package route

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// FailureClass is the normalized reason an upstream attempt failed.
type FailureClass string

const (
	FailureQuota        FailureClass = "quota"
	FailureRate         FailureClass = "rate"
	FailureAuth         FailureClass = "auth"
	FailureModelMissing FailureClass = "model_missing"
	FailureShape        FailureClass = "shape"
	FailureContext      FailureClass = "context"
	FailureUpstream     FailureClass = "upstream"
	FailureRefusal      FailureClass = "refusal"
	FailureConversion   FailureClass = "conversion"
	FailureUnknown      FailureClass = "unknown"
)

// Failure is the classification of one upstream HTTP failure.
//
// The scope flags say which part of the routing state the failure invalidates;
// they are independent and can be combined. Retryable means an attempt can
// still succeed after excluding the flagged scope (for example by trying a
// sibling target or another credential), not that the identical request should
// be replayed against the target that just failed. Cooldown is the suggested
// time to keep that scope out of selection; zero means no timed cooldown.
type Failure struct {
	Class            FailureClass
	Retryable        bool
	TargetScoped     bool
	ModelScoped      bool
	CredentialScoped bool
	Cooldown         time.Duration
	Reason           string
	StatusCode       int
}

// Cooldown defaults. Retry-After on a 429 overrides the rate default.
const (
	quotaCooldown    = 24 * time.Hour
	rateCooldown     = 60 * time.Second
	upstreamCooldown = 30 * time.Second
)

// ClassifyFailure maps one upstream status, header set and body to a Failure.
//
// Status precedence is deliberate and follows the routing design:
//
//	402                  -> quota
//	429                  -> rate
//	401, 403             -> auth
//	404                  -> model_missing
//	5xx                  -> upstream
//	400, 422             -> body heuristics, in this order:
//	                          protocol_conversion -> conversion
//	                          safety/refusal     -> refusal
//	                          context length     -> context
//	                          shape/validation   -> shape
//	                          otherwise          -> unknown
//	other statuses       -> refusal when the body carries safety words,
//	                          otherwise unknown
//
// A 400/422 with no recognized body is deliberately unknown instead of shape:
// a generic bad request does not prove that the request fields are wrong.
// Status checks run before body checks, so a 429 quota-looking body remains
// rate and a 404 remains model_missing.
//
// Body matching is a documented byte heuristic, not a provider schema. It
// lowercases the body and looks for English and common Chinese phrases because
// upstreams in this deployment commonly return either language. Header matching
// uses only Retry-After, accepting both delta-seconds and an HTTP date.
func ClassifyFailure(status int, header http.Header, body []byte) Failure {
	class := classifyFailureClass(status, body)
	return describeFailure(class, status, header, time.Now())
}

// classifyFailureClass performs the status and body heuristics.
func classifyFailureClass(status int, body []byte) FailureClass {
	text := strings.ToLower(string(body))

	switch {
	case status == http.StatusPaymentRequired:
		return FailureQuota
	case status == http.StatusTooManyRequests:
		return FailureRate
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return FailureAuth
	case status == http.StatusNotFound:
		return FailureModelMissing
	case status >= 500 && status <= 599:
		return FailureUpstream
	case status == http.StatusBadRequest || status == http.StatusUnprocessableEntity:
		switch {
		case strings.Contains(text, "protocol_conversion") || strings.Contains(text, "protocol conversion"):
			return FailureConversion
		case containsAny(text, refusalWords):
			return FailureRefusal
		case containsAny(text, contextWords):
			return FailureContext
		case containsAny(text, shapeWords):
			return FailureShape
		default:
			return FailureUnknown
		}
	default:
		if containsAny(text, refusalWords) {
			return FailureRefusal
		}
		return FailureUnknown
	}
}

// describeFailure attaches retry/scope/cooldown semantics to a class.
func describeFailure(class FailureClass, status int, header http.Header, now time.Time) Failure {
	failure := Failure{Class: class, StatusCode: status}
	switch class {
	case FailureQuota:
		// A balance/quota failure is not transient, but another target or
		// credential can usually serve the same model. Keep the target and its
		// credential out for a day; the caller may shorten this when the
		// provider reports a concrete reset.
		failure.Retryable = true
		failure.TargetScoped = true
		failure.CredentialScoped = true
		failure.Cooldown = quotaCooldown
		failure.Reason = "quota or balance exhausted"
	case FailureRate:
		failure.Retryable = true
		failure.TargetScoped = true
		failure.CredentialScoped = true
		failure.Cooldown = retryAfterCooldown(header, now)
		failure.Reason = "rate limited"
	case FailureAuth:
		// Do not replay the same credential; Retryable is false so a retry
		// loop stops rather than burning the budget on an invalid key.
		failure.TargetScoped = true
		failure.CredentialScoped = true
		failure.Reason = "authentication or authorization rejected"
	case FailureModelMissing:
		// Another target may still carry the same model, so a target-scoped
		// retry is worthwhile; the model is also marked unavailable here.
		failure.Retryable = true
		failure.TargetScoped = true
		failure.ModelScoped = true
		failure.Reason = "model not available on target"
	case FailureShape:
		failure.Reason = "request shape rejected by upstream"
	case FailureContext:
		failure.ModelScoped = true
		failure.Reason = "request exceeds the model context limit"
	case FailureUpstream:
		failure.Retryable = true
		failure.TargetScoped = true
		failure.Cooldown = upstreamCooldown
		failure.Reason = "upstream server error"
	case FailureRefusal:
		// A safety refusal is a successful model decision, not a channel fault:
		// do not cool the target down or fail over around it.
		failure.Reason = "model refused the request"
	case FailureConversion:
		// A local conversion failure should move on to the remaining native
		// candidates without consuming a cooldown budget.
		failure.Retryable = true
		failure.Reason = "protocol conversion failed"
	default:
		failure.Reason = "unclassified upstream failure"
	}
	return failure
}

// retryAfterCooldown parses Retry-After on a rate-limit response.
func retryAfterCooldown(header http.Header, now time.Time) time.Duration {
	if header == nil {
		return rateCooldown
	}
	raw := strings.TrimSpace(header.Get("Retry-After"))
	if raw == "" {
		return rateCooldown
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(raw); err == nil {
		if delay := when.Sub(now); delay > 0 {
			return delay
		}
		return 0
	}
	return rateCooldown
}

// containsAny reports whether text contains any needle.
func containsAny(text string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

// Order inside each list is irrelevant; matching stops at the first hit.
var (
	// shapeWords identify a request that upstream refused to parse or validate.
	shapeWords = []string{
		"unknown field",
		"unrecognized",
		"unexpected",
		"bad request",
		"invalid_request_error",
		"invalid request",
		"invalid value",
		"invalid parameter",
		"invalid_argument",
		"unsupported value",
		"missing required",
		"field required",
		"field is required",
		"required parameter",
		"unsupported parameter",
		"unknown parameter",
		"schema",
		"validation",
		"malformed",
		"invalid json",
		"参数错误",
		"参数无效",
		"参数不合法",
		"参数缺失",
		"无效参数",
		"缺少必填",
		"必填字段",
		"字段错误",
		"字段缺失",
		"不能为空",
		"未知字段",
		"不支持的参数",
		"请求格式",
		"格式错误",
		"无法解析",
	}

	// contextWords identify an input that is too long for the model rather than
	// malformed. They are checked before shapeWords because providers commonly
	// wrap a context error in an invalid_request_error envelope.
	contextWords = []string{
		"context_length",
		"context length",
		"maximum context",
		"max context",
		"context window",
		"too many tokens",
		"token limit",
		"prompt is too long",
		"input is too long",
		"reduce the length",
		"request too large",
		"payload too large",
		"上下文",
		"上下文超限",
		"上下文过长",
		"超过上下文",
		"长度超限",
		"超出最大",
		"超过最大",
		"最大长度",
		"长度上限",
		"提示词过长",
		"输入过长",
		"文本过长",
		"内容过长",
		"令牌数",
	}

	// refusalWords identify a safety/content-policy refusal. A refusal is not a
	// channel failure even when it arrives with a 400 status.
	refusalWords = []string{
		"safety",
		"safeguard",
		"content policy",
		"content_policy",
		"content filter",
		"content_filter",
		"moderation",
		"harmful",
		"refus",
		"policy violation",
		"not allowed",
		"blocked",
		"responsible ai",
		"sensitive",
		"unsafe",
		"安全",
		"内容安全",
		"安全策略",
		"内容政策",
		"审核",
		"违规",
		"违反",
		"违规内容",
		"拒绝",
		"拒绝回答",
		"敏感",
		"不当内容",
		"拦截",
		"有害",
	}
)
