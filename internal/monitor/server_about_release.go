package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// releaseCheckStatus values. The page switches on these instead of on an HTTP
// status, because "the check could not run" and "the check ran and there is
// nothing newer" are both successful requests that render differently.
const (
	releaseStatusUpdateAvailable = "update_available"
	releaseStatusUpToDate        = "up_to_date"
	releaseStatusUnknown         = "unknown"
	releaseStatusDisabled        = "disabled"
	releaseStatusUnavailable     = "unavailable"
)

// releaseCheckTimeout bounds the whole lookup. The button is pressed by a person
// who is looking at a spinner, so this is deliberately short: a check that has
// not answered in eight seconds is reported as unavailable and the operator can
// press it again.
const releaseCheckTimeout = 8 * time.Second

// releaseCheckCacheTTL is how long a successful lookup is reused. GitHub answers
// an unauthenticated request from a shared per-IP quota of 60 per hour, and a
// Monitor is often behind one address, so the cache is what keeps a page that
// several operators open from spending that quota.
const releaseCheckCacheTTL = 10 * time.Minute

// releaseNotesLimit caps the release body the page renders. A GitHub release
// body is arbitrary length and the page draws it as markdown; the full text is
// one click away at the release URL.
const releaseNotesLimit = 6000

// releaseCheckResponse is the payload of GET /api/version/latest.
//
// A lookup that failed is still HTTP 200 with status "unavailable" and a reason
// in Error. That follows /api/system/db, which reports an unsupported database
// the same way: the page has one shape to render and a failure is one of the
// states it draws, not an exception it has to catch.
type releaseCheckResponse struct {
	CheckedAt       time.Time  `json:"checked_at"`
	Status          string     `json:"status"`
	CurrentVersion  string     `json:"current_version"`
	BaseVersion     string     `json:"base_version"`
	CommitsSinceTag int        `json:"commits_since_tag"`
	LatestVersion   string     `json:"latest_version,omitempty"`
	LatestName      string     `json:"latest_name,omitempty"`
	PublishedAt     *time.Time `json:"published_at,omitempty"`
	ReleaseURL      string     `json:"release_url,omitempty"`
	Notes           string     `json:"notes,omitempty"`
	Cached          bool       `json:"cached"`
	Error           string     `json:"error,omitempty"`
}

// releaseLookup is the subset of a release the check reads. The endpoint is
// configurable, so a mirror only has to produce these fields; `tag_name` is the
// only one that is required.
type releaseLookup struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	HTMLURL     string    `json:"html_url"`
	Body        string    `json:"body"`
	PublishedAt time.Time `json:"published_at"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
}

// releaseCache memoizes the last successful lookup per endpoint.
//
// A failure is never cached: a Monitor that started while the network was down
// would otherwise report "unavailable" for the whole TTL after it came back.
type releaseCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]releaseCacheEntry
	now     func() time.Time
}

type releaseCacheEntry struct {
	fetchedAt time.Time
	release   releaseLookup
}

func newReleaseCache(ttl time.Duration) *releaseCache {
	return &releaseCache{ttl: ttl, entries: map[string]releaseCacheEntry{}, now: time.Now}
}

func (c *releaseCache) get(endpoint string) (releaseLookup, bool) {
	if c == nil {
		return releaseLookup{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[endpoint]
	if !ok || c.ttl <= 0 || c.now().Sub(entry.fetchedAt) >= c.ttl {
		return releaseLookup{}, false
	}
	return entry.release, true
}

func (c *releaseCache) put(endpoint string, release releaseLookup) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[endpoint] = releaseCacheEntry{fetchedAt: c.now(), release: release}
}

// releaseChecker performs the lookup. It is a struct rather than a bare function
// so tests can inject a client and a cache without touching package state.
type releaseChecker struct {
	endpoint string
	client   *http.Client
	cache    *releaseCache
	// userAgent identifies the caller. GitHub's API rejects a request without
	// one, and a mirror operator wants to know what is polling them.
	userAgent string
}

func newReleaseChecker(endpoint string, build BuildInfo) *releaseChecker {
	version := strings.TrimSpace(build.Version)
	if version == "" {
		version = "dev"
	}
	return &releaseChecker{
		endpoint:  endpoint,
		client:    &http.Client{Timeout: releaseCheckTimeout},
		cache:     newReleaseCache(releaseCheckCacheTTL),
		userAgent: ProductName + "/" + version,
	}
}

// fetch reads the release, preferring the cached copy.
func (c *releaseChecker) fetch(ctx context.Context) (releaseLookup, bool, error) {
	if cached, ok := c.cache.get(c.endpoint); ok {
		return cached, true, nil
	}
	release, err := c.request(ctx)
	if err != nil {
		return releaseLookup{}, false, err
	}
	c.cache.put(c.endpoint, release)
	return release, false, nil
}

func (c *releaseChecker) request(ctx context.Context) (releaseLookup, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return releaseLookup{}, fmt.Errorf("build request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", c.userAgent)

	response, err := c.client.Do(request)
	if err != nil {
		return releaseLookup{}, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		// Drain a little of the body so the connection can be reused, and keep
		// the server's message short enough to show in the page.
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		message := strings.TrimSpace(string(detail))
		if message == "" {
			return releaseLookup{}, fmt.Errorf("release endpoint answered HTTP %d", response.StatusCode)
		}
		return releaseLookup{}, fmt.Errorf("release endpoint answered HTTP %d: %s", response.StatusCode, truncateRunes(message, 200))
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return releaseLookup{}, fmt.Errorf("read response: %w", err)
	}
	var release releaseLookup
	if err := json.Unmarshal(body, &release); err != nil {
		return releaseLookup{}, fmt.Errorf("decode response: %w", err)
	}
	if strings.TrimSpace(release.TagName) == "" {
		return releaseLookup{}, fmt.Errorf("release endpoint returned no tag_name")
	}
	if release.Draft {
		return releaseLookup{}, fmt.Errorf("release endpoint returned a draft release")
	}
	return release, nil
}

// versionCheckAPIHandler serves GET /api/version/latest.
func versionCheckAPIHandler(options AboutOptions, checker *releaseChecker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, checkLatestRelease(r.Context(), options, checker))
	}
}

func checkLatestRelease(ctx context.Context, options AboutOptions, checker *releaseChecker) releaseCheckResponse {
	base, currentParts, commits, currentOK := parseVersionTag(options.Build.Version)
	response := releaseCheckResponse{
		CheckedAt:       time.Now().UTC(),
		CurrentVersion:  strings.TrimSpace(options.Build.Version),
		BaseVersion:     base,
		CommitsSinceTag: commits,
	}
	if !options.UpdateCheckEnabled {
		response.Status = releaseStatusDisabled
		response.Error = "release checks are turned off (update_check.enabled)"
		return response
	}
	if checker == nil || strings.TrimSpace(checker.endpoint) == "" {
		response.Status = releaseStatusUnavailable
		response.Error = "no release endpoint is configured"
		return response
	}

	release, cached, err := checker.fetch(ctx)
	if err != nil {
		response.Status = releaseStatusUnavailable
		response.Error = err.Error()
		return response
	}
	response.Cached = cached
	fillRelease(&response, release)

	_, latestParts, _, latestOK := parseVersionTag(release.TagName)
	if !currentOK || !latestOK {
		// A dev build, or a mirror whose tag is not a version. The newest tag is
		// still worth showing, but "is there something newer" has no answer.
		response.Status = releaseStatusUnknown
		if !currentOK {
			response.Error = "this build reports no comparable version"
		} else {
			response.Error = "the newest release tag is not a comparable version"
		}
		return response
	}
	// latest <= current means the running build is the newest thing there is:
	// either the same tag, or a build from commits the tag does not cover yet.
	if compareVersionParts(latestParts, currentParts) <= 0 {
		response.Status = releaseStatusUpToDate
		return response
	}
	response.Status = releaseStatusUpdateAvailable
	return response
}

func fillRelease(response *releaseCheckResponse, release releaseLookup) {
	response.LatestVersion = strings.TrimSpace(release.TagName)
	response.LatestName = strings.TrimSpace(release.Name)
	response.ReleaseURL = strings.TrimSpace(release.HTMLURL)
	if response.ReleaseURL == "" {
		response.ReleaseURL = ProductRepo + "/releases"
	}
	if !release.PublishedAt.IsZero() {
		published := release.PublishedAt.UTC()
		response.PublishedAt = &published
	}
	response.Notes = truncateRunes(strings.TrimSpace(release.Body), releaseNotesLimit)
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}
