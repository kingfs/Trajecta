package secretmask

// contextWindow is how many bytes on either side of a match are searched for
// credential vocabulary. It has to cover a header line ("Authorization:
// Bearer "), an assignment ("OPENAI_API_KEY = \"") and a JSON member.
const contextWindow = 96

// globalKeywords is the credential vocabulary that counts as context evidence
// for every rule. Env var names such as DEEPSEEK_API_KEY and OPENAI_API_KEY are
// covered by the api_key entry, and header names such as X-Goog-Api-Key by the
// api-key entry, so the list stays short on purpose: every extra entry widens
// what may be rewritten automatically.
var globalKeywords = []string{
	"api_key",
	"api-key",
	"apikey",
	"api key",
	"access_key",
	"access-key",
	"accesskey",
	"secret_key",
	"secret-key",
	"secretkey",
	"client_secret",
	"client-secret",
	"clientsecret",
	"private_key",
	"authorization",
	"bearer ",
	"x-api-key",
	"x-goog-api-key",
	"token",
	"password",
	"passwd",
	"credential",
}

// hasContext reports whether credential vocabulary appears in the window
// around [start,end). Rule-specific keywords are checked in addition to the
// global list.
func hasContext(src []byte, start, end int, extra []string) bool {
	lo := start - contextWindow
	if lo < 0 {
		lo = 0
	}
	hi := end + contextWindow
	if hi > len(src) {
		hi = len(src)
	}
	window := src[lo:hi]
	for _, kw := range globalKeywords {
		if containsFold(window, kw) {
			return true
		}
	}
	for _, kw := range extra {
		if containsFold(window, kw) {
			return true
		}
	}
	return false
}

// containsFold reports whether hay contains needle, comparing ASCII letters
// case-insensitively. It allocates nothing and is only used on short windows
// with short needles.
func containsFold(hay []byte, needle string) bool {
	n := len(needle)
	if n == 0 {
		return true
	}
	if n > len(hay) {
		return false
	}
	lo := lowerASCII(needle[0])
	hi := upperASCII(needle[0])
	for i := 0; i+n <= len(hay); i++ {
		c := hay[i]
		if c != lo && c != hi {
			continue
		}
		if equalFoldAt(hay[i:], needle) {
			return true
		}
	}
	return false
}

func equalFoldAt(hay []byte, needle string) bool {
	for j := 0; j < len(needle); j++ {
		if lowerASCII(hay[j]) != lowerASCII(needle[j]) {
			return false
		}
	}
	return true
}

func lowerASCII(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

func upperASCII(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c - ('a' - 'A')
	}
	return c
}
