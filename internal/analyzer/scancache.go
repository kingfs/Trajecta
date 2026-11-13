package analyzer

import (
	"context"

	"github.com/kingfs/Trajecta/pkg/secretmask"
)

// scanCacheKey carries the per-Analyze scan cache. It is created by
// Runner.Analyze and never escapes the call: detectors run sequentially over
// the same observation, so the credential and PII detectors can share one pass
// over a node text instead of each running the full union DFA.
type scanCacheKey struct{}

type scanCache struct {
	matches map[string][]secretmask.Finding
}

func withScanCache(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, scanCacheKey{}, &scanCache{matches: make(map[string][]secretmask.Finding)})
}

// scanText runs the credential/PII union DFA over text, reusing a previous
// result within the same Analyze call. A cached result is read-only; the
// detectors only iterate it.
func scanText(ctx context.Context, text string) []secretmask.Finding {
	cache, ok := ctx.Value(scanCacheKey{}).(*scanCache)
	if !ok {
		return secretmask.Scan([]byte(text))
	}
	if cached, ok := cache.matches[text]; ok {
		return cached
	}
	matches := secretmask.Scan([]byte(text))
	cache.matches[text] = matches
	return matches
}
