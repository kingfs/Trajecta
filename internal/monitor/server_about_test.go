package monitor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
)

func TestParseVersionTag(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		value       string
		wantBase    string
		wantParts   [3]int
		wantCommits int
		wantOK      bool
	}{
		{name: "release tag", value: "v3.3.1", wantBase: "v3.3.1", wantParts: [3]int{3, 3, 1}, wantOK: true},
		{name: "no leading v", value: "3.3.1", wantBase: "v3.3.1", wantParts: [3]int{3, 3, 1}, wantOK: true},
		{name: "git describe", value: "v3.3.1-2-g4e766d6", wantBase: "v3.3.1", wantParts: [3]int{3, 3, 1}, wantCommits: 2, wantOK: true},
		{name: "git describe dirty", value: "v3.3.1-2-g4e766d6-dirty", wantBase: "v3.3.1", wantParts: [3]int{3, 3, 1}, wantCommits: 2, wantOK: true},
		{name: "zero commits past the tag", value: "v3.4.0-0-gabc1234", wantBase: "v3.4.0", wantParts: [3]int{3, 4, 0}, wantOK: true},
		// A pre-release suffix is not a distance from the tag, so it must not be
		// read as one.
		{name: "pre-release suffix", value: "v3.4.0-rc1", wantBase: "v3.4.0", wantParts: [3]int{3, 4, 0}, wantOK: true},
		{name: "two components", value: "v3.4", wantBase: "v3.4", wantParts: [3]int{3, 4, 0}, wantOK: true},
		{name: "dev", value: "dev", wantOK: false},
		{name: "bare hash", value: "4e766d6", wantOK: false},
		{name: "empty", value: "   ", wantOK: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, parts, commits, ok := parseVersionTag(tc.value)
			if ok != tc.wantOK {
				t.Fatalf("parseVersionTag(%q) ok = %v, want %v", tc.value, ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if base != tc.wantBase {
				t.Fatalf("parseVersionTag(%q) base = %q, want %q", tc.value, base, tc.wantBase)
			}
			if parts != tc.wantParts {
				t.Fatalf("parseVersionTag(%q) parts = %v, want %v", tc.value, parts, tc.wantParts)
			}
			if commits != tc.wantCommits {
				t.Fatalf("parseVersionTag(%q) commits = %d, want %d", tc.value, commits, tc.wantCommits)
			}
		})
	}
}

func TestCompareVersionParts(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		a, b [3]int
		want int
	}{
		{a: [3]int{3, 3, 1}, b: [3]int{3, 3, 1}, want: 0},
		{a: [3]int{3, 3, 1}, b: [3]int{3, 4, 0}, want: -1},
		{a: [3]int{3, 10, 0}, b: [3]int{3, 9, 9}, want: 1},
		{a: [3]int{4, 0, 0}, b: [3]int{3, 99, 99}, want: 1},
	} {
		if got := compareVersionParts(tc.a, tc.b); got != tc.want {
			t.Fatalf("compareVersionParts(%v, %v) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// releaseTestServer answers with one release feed and counts the requests it
// served, so a test can tell a cached answer from a second lookup.
func releaseTestServer(t *testing.T, status int, body string) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if got := r.Header.Get("User-Agent"); !strings.HasPrefix(got, ProductName+"/") {
			t.Errorf("User-Agent = %q, want a %s/ prefix", got, ProductName)
		}
		w.Header().Set("Content-Type", "application/atom+xml")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

// atomFeed builds the subset of the GitHub releases feed the checker reads.
// Keeping the entries as strings lets a test make an empty feed just as easily
// as one with a release.
func atomFeed(entries ...string) string {
	return `<?xml version="1.0" encoding="UTF-8"?><feed xmlns="http://www.w3.org/2005/Atom">` +
		strings.Join(entries, "") + `</feed>`
}

func atomEntry(tag, title, updated, contentHTML string) string {
	return `<entry><updated>` + updated + `</updated>` +
		`<link rel="alternate" type="text/html" href="https://example.test/releases/tag/` + tag + `"/>` +
		`<title>` + title + `</title><content type="html">` + contentHTML + `</content></entry>`
}

func TestCheckLatestReleaseReportsAnAvailableUpdate(t *testing.T) {
	t.Parallel()

	server, hits := releaseTestServer(t, http.StatusOK, atomFeed(atomEntry(
		"v3.4.0",
		"Trajecta 3.4.0",
		"2026-10-01T00:00:00Z",
		"&lt;h3&gt;Added&lt;/h3&gt;&lt;p&gt;something like vector&lt;int&gt;&lt;/p&gt;",
	)))
	options := AboutOptions{
		Build:              BuildInfo{Name: ProductName, Version: "v3.3.1-2-g4e766d6", Commit: "4e766d6"},
		UpdateCheckEnabled: true,
	}
	checker := newReleaseChecker(server.URL, options.Build)

	got := checkLatestRelease(context.Background(), options, checker)
	if got.Status != releaseStatusUpdateAvailable {
		t.Fatalf("status = %q, want %q (error=%q)", got.Status, releaseStatusUpdateAvailable, got.Error)
	}
	if got.LatestVersion != "v3.4.0" || got.CurrentVersion != "v3.3.1-2-g4e766d6" {
		t.Fatalf("latest/current = %q/%q", got.LatestVersion, got.CurrentVersion)
	}
	if got.LatestName != "Trajecta 3.4.0" {
		t.Fatalf("latest_name = %q, want the feed title", got.LatestName)
	}
	if got.BaseVersion != "v3.3.1" || got.CommitsSinceTag != 2 {
		t.Fatalf("base/commits = %q/%d, want v3.3.1/2", got.BaseVersion, got.CommitsSinceTag)
	}
	if got.PublishedAt == nil || !got.PublishedAt.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("published_at = %v, want 2026-10-01T00:00:00Z", got.PublishedAt)
	}
	if !strings.Contains(got.Notes, "Added") || !strings.Contains(got.Notes, "something like vector<int>") {
		t.Fatalf("notes = %q, want the release body as text", got.Notes)
	}
	if strings.Contains(got.Notes, "<h3>") {
		t.Fatalf("notes = %q, want the HTML tags stripped", got.Notes)
	}
	if got.Cached {
		t.Fatal("cached = true on the first lookup")
	}

	// The second lookup is served from the cache so several operators opening
	// the page do not each ask GitHub.
	again := checkLatestRelease(context.Background(), options, checker)
	if !again.Cached {
		t.Fatal("cached = false on the second lookup")
	}
	if hits := atomic.LoadInt32(hits); hits != 1 {
		t.Fatalf("endpoint served %d requests, want 1", hits)
	}
}

func TestCheckLatestReleaseReportsTheRunningBuildAsNewest(t *testing.T) {
	t.Parallel()

	// Two cases that both mean "nothing to install": the newest release is the
	// tag this build descends from, and the build is past that tag.
	for _, tc := range []struct {
		name    string
		version string
	}{
		{name: "same tag", version: "v3.3.1"},
		{name: "commits past the tag", version: "v3.3.1-2-g4e766d6"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := releaseTestServer(t, http.StatusOK, atomFeed(atomEntry("v3.3.1", "v3.3.1", "2026-09-01T00:00:00Z", "")))
			options := AboutOptions{Build: BuildInfo{Version: tc.version}, UpdateCheckEnabled: true}
			got := checkLatestRelease(context.Background(), options, newReleaseChecker(server.URL, options.Build))
			if got.Status != releaseStatusUpToDate {
				t.Fatalf("status = %q, want %q (error=%q)", got.Status, releaseStatusUpToDate, got.Error)
			}
			if got.CommitsSinceTag != 2 && tc.version == "v3.3.1-2-g4e766d6" {
				t.Fatalf("commits_since_tag = %d, want 2", got.CommitsSinceTag)
			}
		})
	}
}

// A build that reports no comparable version still shows the newest tag, but it
// may not claim the install is current or outdated.
func TestCheckLatestReleaseStaysUnknownForADevBuild(t *testing.T) {
	t.Parallel()

	server, _ := releaseTestServer(t, http.StatusOK, atomFeed(atomEntry("v9.9.9", "v9.9.9", "2026-09-01T00:00:00Z", "")))
	options := AboutOptions{Build: BuildInfo{Version: "dev"}, UpdateCheckEnabled: true}
	got := checkLatestRelease(context.Background(), options, newReleaseChecker(server.URL, options.Build))
	if got.Status != releaseStatusUnknown {
		t.Fatalf("status = %q, want %q", got.Status, releaseStatusUnknown)
	}
	if got.LatestVersion != "v9.9.9" {
		t.Fatalf("latest_version = %q, want the tag to still be shown", got.LatestVersion)
	}
}

// A failed lookup is reported, and it is not cached: a Monitor that started
// while the network was down must recover as soon as it is up.
func TestCheckLatestReleaseReportsAFailedLookupWithoutCachingIt(t *testing.T) {
	t.Parallel()

	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, "rate limit exceeded", http.StatusForbidden)
	}))
	t.Cleanup(server.Close)

	options := AboutOptions{Build: BuildInfo{Version: "v3.3.1"}, UpdateCheckEnabled: true}
	checker := newReleaseChecker(server.URL, options.Build)
	for i := 0; i < 2; i++ {
		got := checkLatestRelease(context.Background(), options, checker)
		if got.Status != releaseStatusUnavailable {
			t.Fatalf("attempt %d: status = %q, want %q", i, got.Status, releaseStatusUnavailable)
		}
		if !strings.Contains(got.Error, "403") || !strings.Contains(got.Error, "limiting") {
			t.Fatalf("attempt %d: error = %q, want a readable rate-limit message with the status code", i, got.Error)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("endpoint served %d requests, want 2 (a failure must not be cached)", got)
	}
}

func TestCheckLatestReleaseExplainsARateLimitedEndpoint(t *testing.T) {
	t.Parallel()

	reset := time.Unix(1760000000, 0).UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1760000000")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded for 18.143.38.22.","documentation_url":"https://docs.github.com/rest"}`))
	}))
	t.Cleanup(server.Close)

	options := AboutOptions{Build: BuildInfo{Version: "v3.3.1"}, UpdateCheckEnabled: true}
	got := checkLatestRelease(context.Background(), options, newReleaseChecker(server.URL, options.Build))
	if got.Status != releaseStatusUnavailable {
		t.Fatalf("status = %q, want %q", got.Status, releaseStatusUnavailable)
	}
	if !strings.Contains(got.Error, "limiting release checks from this network (HTTP 403)") {
		t.Fatalf("error = %q, want it to explain the rate limit", got.Error)
	}
	if !strings.Contains(got.Error, reset.Format(time.RFC3339)) {
		t.Fatalf("error = %q, want it to say when the limit resets", got.Error)
	}
	// The response body is noise for the reader and can be long; the actionable
	// message replaces it rather than truncating it.
	if strings.Contains(got.Error, "18.143.38.22") || strings.Contains(got.Error, "documentation_url") {
		t.Fatalf("error = %q, want the response body omitted", got.Error)
	}
}

func TestCheckLatestReleaseReportsAnEmptyFeedAsNoRelease(t *testing.T) {
	t.Parallel()

	server, _ := releaseTestServer(t, http.StatusOK, atomFeed())
	options := AboutOptions{Build: BuildInfo{Version: "v3.3.1"}, UpdateCheckEnabled: true}
	got := checkLatestRelease(context.Background(), options, newReleaseChecker(server.URL, options.Build))
	if got.Status != releaseStatusNoRelease {
		t.Fatalf("status = %q, want %q", got.Status, releaseStatusNoRelease)
	}
	if !strings.Contains(got.Error, "has not published a GitHub release") {
		t.Fatalf("error = %q, want it to explain that the feed is empty", got.Error)
	}
}

func TestCheckLatestReleaseExplainsAMissingFeed(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`<html>not found</html>`))
	}))
	t.Cleanup(server.Close)

	options := AboutOptions{Build: BuildInfo{Version: "v3.3.1"}, UpdateCheckEnabled: true}
	got := checkLatestRelease(context.Background(), options, newReleaseChecker(server.URL, options.Build))
	if got.Status != releaseStatusUnavailable {
		t.Fatalf("status = %q, want %q", got.Status, releaseStatusUnavailable)
	}
	if !strings.Contains(got.Error, "could not find the project's release feed") {
		t.Fatalf("error = %q, want it to explain the missing feed", got.Error)
	}
	if strings.Contains(got.Error, "<html>") {
		t.Fatalf("error = %q, want the response body omitted", got.Error)
	}
}

func TestCheckLatestReleaseHonoursTheSwitch(t *testing.T) {
	t.Parallel()

	server, hits := releaseTestServer(t, http.StatusOK, atomFeed(atomEntry("v9.9.9", "v9.9.9", "2026-09-01T00:00:00Z", "")))
	options := AboutOptions{
		Build:              BuildInfo{Version: "v3.3.1"},
		UpdateCheckEnabled: false,
	}
	got := checkLatestRelease(context.Background(), options, newReleaseChecker(server.URL, options.Build))
	if got.Status != releaseStatusDisabled {
		t.Fatalf("status = %q, want %q", got.Status, releaseStatusDisabled)
	}
	if served := atomic.LoadInt32(hits); served != 0 {
		t.Fatalf("endpoint served %d requests while the check was off, want 0", served)
	}
}

func TestCheckLatestReleaseWithoutAChecker(t *testing.T) {
	t.Parallel()

	options := AboutOptions{Build: BuildInfo{Version: "v3.3.1"}, UpdateCheckEnabled: true}
	got := checkLatestRelease(context.Background(), options, nil)
	if got.Status != releaseStatusUnavailable {
		t.Fatalf("status = %q, want %q", got.Status, releaseStatusUnavailable)
	}
	if !strings.Contains(got.Error, "not available") {
		t.Fatalf("error = %q, want it to explain the missing checker", got.Error)
	}
}

func TestReleaseCacheExpires(t *testing.T) {
	t.Parallel()

	cache := newReleaseCache(time.Minute)
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	cache.now = func() time.Time { return now }
	cache.put("https://example.test/release", releaseLookup{TagName: "v3.3.1"})

	if _, ok := cache.get("https://example.test/release"); !ok {
		t.Fatal("a fresh entry was not returned")
	}
	now = now.Add(2 * time.Minute)
	if _, ok := cache.get("https://example.test/release"); ok {
		t.Fatal("an expired entry was returned")
	}
}

// The About page is readable by every signed-in role, unlike the system page.
func TestAboutAPIIsReadableByAnySignedInRole(t *testing.T) {
	t.Parallel()

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	manager := testJWTManager(t, time.Hour)
	viewerToken := systemTestToken(t, manager, 2, "viewer", "viewer")

	mux := http.NewServeMux()
	RegisterRoutes(mux, st, RouteOptions{
		MonitorAuthVerifier: manager,
		MonitorJWT:          manager,
		About: AboutOptions{
			Build:              BuildInfo{Name: ProductName, Version: "v3.3.1-2-g4e766d6", Commit: "4e766d6", Date: "2026-10-16T00:00:00Z", Branch: "main"},
			UpdateCheckEnabled: true,
			Telemetry:          TelemetryOptions{Enabled: true, Provider: "custom", Endpoint: "https://collector.test/ping", PathPrefix: "/trajecta/install", IntervalSeconds: 86400},
		},
	})

	if rr := systemTestRequest(mux, "/api/about", ""); rr.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/about without credentials = %d, want 401", rr.Code)
	}
	rr := systemTestRequest(mux, "/api/about", viewerToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/about as a viewer = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var payload aboutResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode about payload: %v", err)
	}
	if payload.Name != ProductName || payload.Version != "v3.3.1-2-g4e766d6" {
		t.Fatalf("name/version = %q/%q", payload.Name, payload.Version)
	}
	if payload.BaseVersion != "v3.3.1" || payload.CommitsSinceTag != 2 {
		t.Fatalf("base/commits = %q/%d", payload.BaseVersion, payload.CommitsSinceTag)
	}
	if payload.Repository != ProductRepo || payload.License != ProductLicenseName {
		t.Fatalf("repository/license = %q/%q", payload.Repository, payload.License)
	}
	if !payload.UpdateCheckEnabled {
		t.Fatal("update_check_enabled = false, want true")
	}
	if !payload.Telemetry.Enabled || payload.Telemetry.IntervalSeconds != 86400 {
		t.Fatalf("telemetry = %+v, want it reported verbatim", payload.Telemetry)
	}
	// The install id belongs to the deployment, so two readers - and a second
	// request - must see the same one.
	if len(payload.InstallID) != 32 {
		t.Fatalf("install_id = %q, want 32 hex characters", payload.InstallID)
	}
	var again aboutResponse
	if err := json.Unmarshal(systemTestRequest(mux, "/api/about", viewerToken).Body.Bytes(), &again); err != nil {
		t.Fatalf("decode second about payload: %v", err)
	}
	if again.InstallID != payload.InstallID {
		t.Fatalf("install_id changed between requests: %q then %q", payload.InstallID, again.InstallID)
	}
}

// The switch is the only thing that decides whether the page draws the button;
// the release feed URL is a project constant.
func TestAboutReportsTheUpdateCheckSwitch(t *testing.T) {
	t.Parallel()

	payload := buildAbout(AboutOptions{Build: BuildInfo{Version: "v3.3.1"}, UpdateCheckEnabled: true})
	if !payload.UpdateCheckEnabled {
		t.Fatal("update_check_enabled = false while the switch is on")
	}
	payload = buildAbout(AboutOptions{Build: BuildInfo{Version: "v3.3.1"}, UpdateCheckEnabled: false})
	if payload.UpdateCheckEnabled {
		t.Fatal("update_check_enabled = true while the switch is off")
	}
}

// The release check degrades to a described state rather than an HTTP error, so
// that the page has one payload shape to render.
func TestVersionCheckAPIRouteAnswersWithAPayload(t *testing.T) {
	t.Parallel()

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	manager := testJWTManager(t, time.Hour)
	token := systemTestToken(t, manager, 1, "admin", "admin")
	mux := http.NewServeMux()
	RegisterRoutes(mux, st, RouteOptions{MonitorAuthVerifier: manager, MonitorJWT: manager})

	rr := systemTestRequest(mux, "/api/version/latest", token)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/version/latest = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var payload releaseCheckResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode release payload: %v", err)
	}
	if payload.Status != releaseStatusDisabled {
		t.Fatalf("status = %q, want %q for a zero-value deployment", payload.Status, releaseStatusDisabled)
	}
}
