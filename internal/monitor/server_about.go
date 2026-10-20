package monitor

import (
	"context"
	"log/slog"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
)

// Product identity. These are facts about the project rather than about a
// deployment, so they are constants instead of configuration: a fork that wants
// its own name on the About page changes them here, the way it already changes
// the module path.
const (
	ProductName        = "Trajecta"
	ProductRepo        = "https://github.com/kingfs/Trajecta"
	ProductIssues      = ProductRepo + "/issues"
	ProductDocs        = ProductRepo + "/tree/main/docs"
	ProductLicenseName = "MIT"
	ProductLicenseURL  = ProductRepo + "/blob/main/LICENSE"
)

// BuildInfo is the artifact's own build identity.
//
// It is passed in rather than read here because the values are injected at link
// time into `package main` (`-X main.Version=...` in the Taskfile) and
// internal/monitor cannot import a package main. cmd/server hands its own
// variables down through RouteOptions.
type BuildInfo struct {
	Name    string
	Version string
	Commit  string
	Date    string
	Branch  string
}

// aboutResponse is the payload of GET /api/about.
//
// Everything in it is a fact about the binary or about the project, so the page
// that renders it is the one page every signed-in role may open. It carries no
// recording, no configuration and no operator data.
type aboutResponse struct {
	GeneratedAt time.Time `json:"generated_at"`
	Name        string    `json:"name"`
	// Version is the raw build version, which for a build made by the Taskfile
	// is `git describe --tags --always --dirty` - so "v3.3.1-2-g4e766d6" on a
	// commit two past the tag, and "dev" for a build outside a checkout.
	Version string `json:"version"`
	// BaseVersion is Version with the `-<n>-g<hash>` suffix removed. It is what
	// the release check compares against, and an empty value means the version
	// could not be read as one (`dev`, a bare hash).
	BaseVersion string `json:"base_version"`
	// CommitsSinceTag is the `<n>` of `git describe`: how far the build is past
	// the tag BaseVersion names. A build two commits ahead of the newest release
	// is not an outdated install, and the page needs the number to say so.
	CommitsSinceTag int    `json:"commits_since_tag"`
	Commit          string `json:"commit"`
	BuildDate       string `json:"build_date"`
	Branch          string `json:"branch"`
	GoVersion       string `json:"go_version"`
	GOOS            string `json:"goos"`
	GOARCH          string `json:"goarch"`
	Repository      string `json:"repository"`
	IssuesURL       string `json:"issues_url"`
	ReleasesURL     string `json:"releases_url"`
	DocsURL         string `json:"docs_url"`
	License         string `json:"license"`
	LicenseURL      string `json:"license_url"`
	// UpdateCheckEnabled tells the page whether to draw the button. The page
	// hides it rather than showing a control that always answers "turned off".
	UpdateCheckEnabled bool             `json:"update_check_enabled"`
	Telemetry          TelemetryOptions `json:"telemetry"`
	// InstallID identifies this deployment, not this browser, so that the several
	// people who share one Monitor are counted once. It is empty when there is no
	// database to store it in, and the page then reports nothing rather than
	// falling back to a per-browser id that would inflate the count.
	InstallID string `json:"install_id,omitempty"`
}

// aboutAPIHandler serves the build identity and the feature switches the About
// page renders.
//
// It is registered with monitorAuthRequired rather than monitorAdminRequired:
// every signed-in role may see which version is running.
func aboutAPIHandler(options AboutOptions, identity *installIdentity) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		payload := buildAbout(options)
		payload.InstallID = identity.get(r.Context())
		writeJSON(w, http.StatusOK, payload)
	}
}

// installIdentity resolves the deployment's install id once and remembers it.
//
// It is per-mux state rather than a package global: RegisterRoutes is called by
// every test that builds a Monitor, and a shared global would let one test's id
// answer another test's request. A failure is not remembered, so a deployment
// whose database was briefly unavailable generates the id on the next request
// instead of reporting nothing until it restarts.
type installIdentity struct {
	mu    sync.Mutex
	store *store.Store
	value string
}

func (i *installIdentity) get(ctx context.Context) string {
	if i == nil || i.store == nil {
		return ""
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.value != "" {
		return i.value
	}
	id, err := i.store.LoadOrCreateInstallID(ctx)
	if err != nil {
		slog.Warn("could not resolve the anonymous install id; the About page reports no install id", "error", err)
		return ""
	}
	i.value = id
	return id
}

func buildAbout(options AboutOptions) aboutResponse {
	build := options.Build
	version := strings.TrimSpace(build.Version)
	base, _, commits, _ := parseVersionTag(version)
	name := strings.TrimSpace(build.Name)
	if name == "" {
		name = ProductName
	}
	return aboutResponse{
		GeneratedAt:        time.Now().UTC(),
		Name:               name,
		Version:            version,
		BaseVersion:        base,
		CommitsSinceTag:    commits,
		Commit:             strings.TrimSpace(build.Commit),
		BuildDate:          strings.TrimSpace(build.Date),
		Branch:             strings.TrimSpace(build.Branch),
		GoVersion:          runtime.Version(),
		GOOS:               runtime.GOOS,
		GOARCH:             runtime.GOARCH,
		Repository:         ProductRepo,
		IssuesURL:          ProductIssues,
		ReleasesURL:        ProductRepo + "/releases",
		DocsURL:            ProductDocs,
		License:            ProductLicenseName,
		LicenseURL:         ProductLicenseURL,
		UpdateCheckEnabled: options.UpdateCheckEnabled,
		Telemetry:          options.Telemetry,
	}
}

// parseVersionTag splits a build version into the release tag it descends from,
// that tag's numeric components, and how many commits past it the build is.
//
// The shapes it has to read are the ones the toolchain produces: a tagged build
// is "v3.3.1", a build made by the Taskfile is git describe's
// "v3.3.1-2-g4e766d6" (or "...-dirty"), and a build outside a checkout is
// "dev" or a bare short hash. Only the first two are versions; the rest report
// ok = false, and the caller renders "unknown" rather than guessing.
func parseVersionTag(value string) (base string, parts [3]int, commits int, ok bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", parts, 0, false
	}
	head := trimmed
	if dash := strings.IndexByte(head, '-'); dash >= 0 {
		head = head[:dash]
		// git describe puts the commit count between the tag and the `g<hash>`
		// segment. A pre-release suffix ("v3.4.0-rc1") lands here too and simply
		// fails to parse, which leaves commits at zero - correct, because a
		// pre-release tag is not a distance from the tag.
		if rest := trimmed[dash+1:]; rest != "" {
			if next := strings.IndexByte(rest, '-'); next >= 0 {
				rest = rest[:next]
			}
			if count, err := strconv.Atoi(rest); err == nil && count >= 0 {
				commits = count
			}
		}
	}
	head = strings.TrimPrefix(strings.TrimPrefix(head, "v"), "V")
	if head == "" {
		return "", parts, commits, false
	}
	fields := strings.Split(head, ".")
	if len(fields) > 3 {
		fields = fields[:3]
	}
	for i, field := range fields {
		number, err := strconv.Atoi(field)
		if err != nil || number < 0 {
			return "", [3]int{}, 0, false
		}
		parts[i] = number
	}
	// A single component ("3") is a version; it just sorts below any x.y.z.
	return "v" + strings.Join(fields, "."), parts, commits, true
}

// compareVersionParts orders two parsed versions. It returns a negative number
// when a is older than b, zero when they are equal and a positive number when a
// is newer.
func compareVersionParts(a, b [3]int) int {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}
