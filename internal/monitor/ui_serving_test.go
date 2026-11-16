package monitor

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The console white-screened after a UI rebuild when a browser held a cached
// pre-rebuild index.html: the old hashed asset names no longer exist in the
// new embed, and the SPA fallback answered those module-script requests with
// index.html (200, text/html), which the browser rejects on MIME type. These
// tests pin the serving contract that prevents it: the entry point is always
// revalidated, hashed assets are immutable, and a missing asset is a 404.

func TestEmbeddedIndexIsNotCacheable(t *testing.T) {
	t.Parallel()

	handler := appHandler()
	for _, route := range []string{"/", "/overview", "/traces/example-trace"} {
		req := httptest.NewRequest(http.MethodGet, route, nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", route, rr.Code)
		}
		if got := rr.Header().Get("Cache-Control"); got != "no-cache" {
			t.Fatalf("%s Cache-Control = %q, want no-cache", route, got)
		}
	}
}

func TestEmbeddedHashedAssetsAreImmutable(t *testing.T) {
	t.Parallel()

	entries, err := fs.ReadDir(uiFS, "ui/dist/assets")
	if err != nil {
		t.Fatalf("read embedded assets: %v", err)
	}
	var name string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".js") {
			name = e.Name()
			break
		}
	}
	if name == "" {
		t.Fatal("no embedded .js asset found")
	}

	handler := appHandler()
	req := httptest.NewRequest(http.MethodGet, "/assets/"+name, nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("/assets/%s status = %d, want 200", name, rr.Code)
	}
	if got := rr.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("/assets/%s Cache-Control = %q, want immutable", name, got)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("/assets/%s Content-Type = %q, want javascript", name, ct)
	}
}

func TestMissingAssetIs404NotIndexFallback(t *testing.T) {
	t.Parallel()

	handler := appHandler()
	// A plausible stale hash from a previous build, and a plain missing file.
	for _, path := range []string{"/assets/index-00000000.js", "/assets/index-00000000.css"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404 (SPA fallback must not answer missing assets with HTML)", path, rr.Code)
		}
		if ct := rr.Header().Get("Content-Type"); strings.Contains(ct, "text/html") {
			t.Fatalf("%s Content-Type = %q, want non-HTML 404", path, ct)
		}
	}
}

func TestSPAFallbackStillServesIndexForRoutes(t *testing.T) {
	t.Parallel()

	handler := appHandler()
	// Extensionless client routes keep the index fallback, including model
	// names with dots and escaped slashes.
	for _, route := range []string{"/channels/abc", "/models/dev%2Fgpt-5.5", "/upstreams/42"} {
		req := httptest.NewRequest(http.MethodGet, route, nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", route, rr.Code)
		}
		if !strings.Contains(rr.Body.String(), `<div id="root"></div>`) {
			t.Fatalf("%s did not return the monitor index", route)
		}
	}
}
