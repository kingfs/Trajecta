package monitor

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
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
	releaseStatusNoRelease       = "no_release"
	releaseStatusDisabled        = "disabled"
	releaseStatusUnavailable     = "unavailable"
)

// releaseFeedURL is the project's fixed release feed. The repository and its
// releases are facts about Trajecta, not deployment settings, so there is no
// configuration key for this URL.
//
// It is the Atom feed rather than the REST API on purpose: api.github.com gives
// an unauthenticated caller 60 requests per hour per egress IP, and a Monitor
// behind a shared proxy exhausts that without doing anything wrong. The feed is
// the public release list, does not carry that quota, and states plainly when
// the project has not published a release yet.
const releaseFeedURL = ProductRepo + "/releases.atom"

// errNoPublishedRelease is a successful lookup that found an empty feed. It is
// a state, not a failure, so checkLatestRelease reports it separately.
var errNoPublishedRelease = errors.New("no published release")

// releaseCheckTimeout bounds the whole lookup. The button is pressed by a person
// who is looking at a spinner, so this is deliberately short: a check that has
// not answered in eight seconds is reported as unavailable and the operator can
// press it again.
const releaseCheckTimeout = 8 * time.Second

// releaseCheckCacheTTL is how long a successful lookup is reused. The feed is
// public and cheap, but a console that several operators share should not ask
// GitHub once per click; ten minutes is short enough to notice a release.
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

// releaseLookup is the subset of a release the check reads. It is filled from
// the project's Atom feed; TagName is the only required field.
type releaseLookup struct {
	TagName     string
	Name        string
	HTMLURL     string
	Body        string
	PublishedAt time.Time
}

// releaseFeed is the part of the GitHub releases Atom feed this endpoint reads.
// XML namespaces are ignored by encoding/xml, so the local tag names are enough.
type releaseFeed struct {
	Entries []releaseFeedEntry `xml:"entry"`
}

type releaseFeedEntry struct {
	Updated string            `xml:"updated"`
	Title   string            `xml:"title"`
	Links   []releaseFeedLink `xml:"link"`
	Content string            `xml:"content"`
}

type releaseFeedLink struct {
	Rel  string `xml:"rel,attr"`
	Href string `xml:"href,attr"`
}

// releaseCache memoizes the last successful lookup per feed URL.
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

func (c *releaseCache) get(feedURL string) (releaseLookup, bool) {
	if c == nil {
		return releaseLookup{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[feedURL]
	if !ok || c.ttl <= 0 || c.now().Sub(entry.fetchedAt) >= c.ttl {
		return releaseLookup{}, false
	}
	return entry.release, true
}

func (c *releaseCache) put(feedURL string, release releaseLookup) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[feedURL] = releaseCacheEntry{fetchedAt: c.now(), release: release}
}

// releaseChecker performs the lookup. It is a struct rather than a bare function
// so tests can inject a client and a cache without touching package state.
type releaseChecker struct {
	feedURL string
	client  *http.Client
	cache   *releaseCache
	// userAgent identifies the caller. GitHub asks for one, and the feed
	// operator wants to know what is polling them.
	userAgent string
}

func newReleaseChecker(feedURL string, build BuildInfo) *releaseChecker {
	version := strings.TrimSpace(build.Version)
	if version == "" {
		version = "dev"
	}
	return &releaseChecker{
		feedURL:   feedURL,
		client:    &http.Client{Timeout: releaseCheckTimeout},
		cache:     newReleaseCache(releaseCheckCacheTTL),
		userAgent: ProductName + "/" + version,
	}
}

// fetch reads the release feed, preferring the cached copy.
func (c *releaseChecker) fetch(ctx context.Context) (releaseLookup, bool, error) {
	if cached, ok := c.cache.get(c.feedURL); ok {
		return cached, true, nil
	}
	release, err := c.request(ctx)
	if err != nil {
		return releaseLookup{}, false, err
	}
	c.cache.put(c.feedURL, release)
	return release, false, nil
}

func (c *releaseChecker) request(ctx context.Context) (releaseLookup, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.feedURL, nil)
	if err != nil {
		return releaseLookup{}, errors.New("the release feed URL is invalid")
	}
	request.Header.Set("Accept", "application/atom+xml, application/xml;q=0.9")
	request.Header.Set("User-Agent", c.userAgent)

	response, err := c.client.Do(request)
	if err != nil {
		return releaseLookup{}, fmt.Errorf("could not reach GitHub to check for a release: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		// Read a little of the body so the connection can be reused and the
		// rate-limit detector has something to inspect. The body is never shown:
		// an Atom error page or GitHub HTML is noise in the About card.
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		if message := releaseFeedStatusMessage(response, string(detail)); message != "" {
			return releaseLookup{}, errors.New(message)
		}
		return releaseLookup{}, fmt.Errorf("GitHub returned HTTP %d while reading the release feed", response.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return releaseLookup{}, errors.New("could not read the release feed from GitHub")
	}
	var feed releaseFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return releaseLookup{}, errors.New("GitHub returned an unreadable release feed; try again later")
	}
	if len(feed.Entries) == 0 {
		return releaseLookup{}, errNoPublishedRelease
	}
	release, err := releaseFromFeedEntry(feed.Entries[0])
	if err != nil {
		return releaseLookup{}, err
	}
	return release, nil
}

// releaseFromFeedEntry picks the human-facing link and tag out of one entry.
// The feed's newest entry is first; its alternate link is
// https://github.com/<owner>/<repo>/releases/tag/<tag>.
func releaseFromFeedEntry(entry releaseFeedEntry) (releaseLookup, error) {
	href := ""
	for _, link := range entry.Links {
		if link.Rel == "alternate" && strings.TrimSpace(link.Href) != "" {
			href = strings.TrimSpace(link.Href)
			break
		}
	}
	if href == "" {
		for _, link := range entry.Links {
			if strings.TrimSpace(link.Href) != "" {
				href = strings.TrimSpace(link.Href)
				break
			}
		}
	}
	if href == "" {
		return releaseLookup{}, errors.New("GitHub's release feed has no link for the newest release")
	}
	tag := releaseTagFromURL(href)
	if tag == "" {
		return releaseLookup{}, errors.New("GitHub's release feed did not name a version; try again later")
	}
	published, _ := time.Parse(time.RFC3339, strings.TrimSpace(entry.Updated))
	return releaseLookup{
		TagName:     tag,
		Name:        strings.TrimSpace(entry.Title),
		HTMLURL:     href,
		Body:        releaseNotesFromHTML(entry.Content),
		PublishedAt: published,
	}, nil
}

func releaseTagFromURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	tag, err := url.PathUnescape(path.Base(parsed.Path))
	if err != nil {
		return ""
	}
	tag = strings.TrimSpace(tag)
	if tag == "." || tag == "/" {
		return ""
	}
	return tag
}

// Only known HTML tags are removed. A release note may contain code such as
// `vector<int>`, and a generic "<...>" pattern would eat the code along with
// the markup.
var releaseHTMLTag = regexp.MustCompile(`(?i)</?(?:a|blockquote|br|code|dd|del|details|div|dl|dt|em|h[1-6]|hr|img|input|li|ol|p|pre|s|span|strong|sub|summary|sup|table|tbody|td|th|thead|tr|ul)(?:\s[^>]*)?/?>`)

// releaseNotesFromHTML turns the feed's escaped release body into readable
// plain text. The page renders notes as markdown, so keeping HTML tags would
// show markup rather than the notes; block boundaries become newlines.
func releaseNotesFromHTML(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.NewReplacer(
		"<br>", "\n",
		"<br/>", "\n",
		"<br />", "\n",
		"</p>", "\n\n",
		"</li>", "\n",
		"</h1>", "\n\n",
		"</h2>", "\n\n",
		"</h3>", "\n\n",
		"</h4>", "\n\n",
		"</blockquote>", "\n\n",
		"</pre>", "\n\n",
	).Replace(value)
	value = releaseHTMLTag.ReplaceAllString(value, "")
	value = html.UnescapeString(value)
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// releaseFeedStatusMessage turns a non-200 feed response into a sentence the
// About card can show. GitHub's HTML error body never reaches the reader.
func releaseFeedStatusMessage(response *http.Response, body string) string {
	switch response.StatusCode {
	case http.StatusNotFound:
		return "GitHub could not find the project's release feed (HTTP 404)"
	case http.StatusForbidden, http.StatusTooManyRequests:
		if response.StatusCode != http.StatusTooManyRequests && !looksRateLimited(response, body) {
			return "GitHub refused the release check (HTTP 403); try again later"
		}
		message := fmt.Sprintf("GitHub is limiting release checks from this network (HTTP %d)", response.StatusCode)
		if reset := strings.TrimSpace(response.Header.Get("X-RateLimit-Reset")); reset != "" {
			if epoch, err := strconv.ParseInt(reset, 10, 64); err == nil && epoch > 0 {
				message += fmt.Sprintf("; the limit resets at %s", time.Unix(epoch, 0).UTC().Format(time.RFC3339))
			}
		} else if retryAfter := strings.TrimSpace(response.Header.Get("Retry-After")); retryAfter != "" {
			message += fmt.Sprintf("; retry after %s", retryAfter)
		}
		return message + "; try again later"
	}
	if response.StatusCode >= http.StatusInternalServerError {
		return fmt.Sprintf("GitHub is having trouble (HTTP %d); try again later", response.StatusCode)
	}
	return ""
}

func looksRateLimited(response *http.Response, body string) bool {
	if strings.TrimSpace(response.Header.Get("X-RateLimit-Remaining")) == "0" {
		return true
	}
	if strings.TrimSpace(response.Header.Get("Retry-After")) != "" {
		return true
	}
	lowered := strings.ToLower(body)
	return strings.Contains(lowered, "rate limit") || strings.Contains(lowered, "abuse")
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
		response.Error = "release checks are turned off in the server configuration"
		return response
	}
	if checker == nil {
		response.Status = releaseStatusUnavailable
		response.Error = "release checks are not available in this build"
		return response
	}

	release, cached, err := checker.fetch(ctx)
	if err != nil {
		if errors.Is(err, errNoPublishedRelease) {
			response.Status = releaseStatusNoRelease
			response.Error = "this project has not published a GitHub release yet"
			return response
		}
		response.Status = releaseStatusUnavailable
		response.Error = err.Error()
		return response
	}
	response.Cached = cached
	fillRelease(&response, release)

	_, latestParts, _, latestOK := parseVersionTag(release.TagName)
	if !currentOK || !latestOK {
		// A dev build, or a tag that is not a version. The newest tag is still
		// worth showing, but "is there something newer" has no answer.
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
