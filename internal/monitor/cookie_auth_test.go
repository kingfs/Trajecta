package monitor

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/kingfs/Trajecta/internal/auth"
)

type staticTokenVerifier struct{}

func (staticTokenVerifier) VerifyToken(_ context.Context, token string) (auth.Principal, bool, error) {
	if token != "good" {
		return auth.Principal{}, false, nil
	}
	return auth.Principal{UserID: 1, Username: "admin", Role: auth.RoleAdmin, Scope: auth.ScopeAll}, true, nil
}

func TestMonitorCookieAuthOriginGate(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, origin, bearer string
		want                               int
	}{
		{"cookie read", "GET", "/api/traces", "", "", 200},
		{"cookie write same origin", "POST", "/api/channels", "https://monitor.test", "", 200},
		{"cookie write absent origin", "POST", "/api/channels", "", "", 403},
		{"cookie write sibling site", "POST", "/api/channels", "https://evil.monitor.test", "", 403},
		{"cookie write sibling port", "POST", "/api/channels", "https://monitor.test:9000", "", 403},
		{"cookie patch", "PATCH", "/api/settings/routing", "null", "", 403},
		{"cookie delete", "DELETE", "/api/channels/1", "", "", 403},
		{"cookie websocket absent origin", "GET", realtimeSocketPath, "", "", 403},
		{"cookie websocket same origin", "GET", realtimeSocketPath, "https://monitor.test", "", 200},
		{"explicit bearer", "POST", "/api/channels", "", "good", 200},
		{"invalid bearer does not fall back", "GET", "/api/traces", "", "bad", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			handler := monitorAuthRequired(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(200) }, staticTokenVerifier{})
			req := httptest.NewRequest(tc.method, "https://monitor.test"+tc.path, nil)
			req.AddCookie(&http.Cookie{Name: monitorSessionCookie, Value: "good"})
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.bearer != "" {
				req.Header.Set("Authorization", "Bearer "+tc.bearer)
			}
			rr := httptest.NewRecorder()
			handler(rr, req)
			if rr.Code != tc.want || called != (tc.want == 200) {
				t.Fatalf("status=%d called=%v want=%d body=%s", rr.Code, called, tc.want, rr.Body.String())
			}
		})
	}
}

func TestMonitorAuthRejectsAccessTokenInQuery(t *testing.T) {
	for _, path := range []string{"/api/events/stream", realtimeSocketPath, "/api/traces"} {
		handler := monitorAuthRequired(func(w http.ResponseWriter, r *http.Request) { t.Fatal("query token reached handler") }, staticTokenVerifier{})
		rr := httptest.NewRecorder()
		handler(rr, httptest.NewRequest("GET", path+"?access_token=good", nil))
		if rr.Code != 401 {
			t.Fatalf("%s status=%d", path, rr.Code)
		}
	}
}

func TestMonitorRequestSameOrigin(t *testing.T) {
	for _, tc := range []struct {
		name, target, origin, forwarded, site string
		want                                  bool
	}{
		{"same origin", "https://monitor.test", "https://monitor.test", "", "same-origin", true},
		{"TLS offload preserves Host", "http://monitor.test", "https://monitor.test", "", "same-origin", true},
		{"plain HTTP development", "http://monitor.test:8081", "http://monitor.test:8081", "", "same-origin", true},
		{"missing", "https://monitor.test", "", "", "", false},
		{"opaque", "https://monitor.test", "null", "", "", false},
		{"cross origin", "https://monitor.test", "https://evil.test", "", "", false},
		{"spoofed forwarded host", "https://monitor.test", "https://evil.test", "evil.test", "", false},
		{"proxy must preserve host", "http://internal:8081", "https://monitor.test", "monitor.test", "", false},
		{"scheme mismatch", "https://monitor.test", "http://monitor.test", "", "", false},
		{"same site is not same origin", "http://monitor.test", "https://monitor.test", "", "same-site", false},
		{"cross site metadata", "https://monitor.test", "https://monitor.test", "", "cross-site", false},
		{"path", "https://monitor.test", "https://monitor.test/path", "", "", false},
		{"query", "https://monitor.test", "https://monitor.test?", "", "", false},
		{"userinfo", "https://monitor.test", "https://user@monitor.test", "", "", false},
		{"foreign scheme", "https://monitor.test", "ftp://monitor.test", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", tc.target+"/api/channels", nil)
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			r.Header.Set("X-Forwarded-Host", tc.forwarded)
			r.Header.Set("Sec-Fetch-Site", tc.site)
			if got := monitorRequestSameOrigin(r); got != tc.want {
				t.Fatalf("sameOrigin=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestMonitorLoginLogoutRejectCrossOrigin(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{"login": authLoginAPIHandler(nil, nil, 0, true), "logout": authLogoutAPIHandler(true)} {
		for _, origin := range []string{"", "null", "https://evil.test", "https://monitor.test:9000"} {
			req := httptest.NewRequest("POST", "https://monitor.test/api/auth/"+name, nil)
			if origin != "" {
				req.Header.Set("Origin", origin)
			}
			rr := httptest.NewRecorder()
			handler(rr, req)
			if rr.Code != 403 || len(rr.Result().Cookies()) != 0 {
				t.Fatalf("%s origin=%q: status=%d cookies=%v", name, origin, rr.Code, rr.Result().Cookies())
			}
		}
	}
}

func TestMonitorLogoutClearsBothCookiePaths(t *testing.T) {
	req := httptest.NewRequest("POST", "https://monitor.test/api/auth/logout", nil)
	req.Header.Set("Origin", "https://monitor.test")
	rr := httptest.NewRecorder()
	authLogoutAPIHandler(true)(rr, req)
	if rr.Code != 200 || rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("logout: %d %v", rr.Code, rr.Header())
	}
	cookies := rr.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("cookies=%v", cookies)
	}
	for i, path := range []string{"/", monitorSessionCookiePath} {
		c := cookies[i]
		if c.Path != path || c.MaxAge != -1 || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
			t.Fatalf("clear cookie=%+v", c)
		}
	}
}

func TestMonitorCookieDoesNotTravelToProxyOrMCP(t *testing.T) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	monitorURL, _ := url.Parse("https://monitor.test:8081/api/auth/login")
	jar.SetCookies(monitorURL, []*http.Cookie{{Name: monitorSessionCookie, Value: "good", Path: monitorSessionCookiePath, HttpOnly: true, Secure: true}})
	for _, path := range []string{"https://monitor.test:8080/v1/chat/completions", "https://monitor.test:8081/mcp", "https://monitor.test:8081/"} {
		u, _ := url.Parse(path)
		if cookies := jar.Cookies(u); len(cookies) != 0 {
			t.Fatalf("session leaked to %s: %v", path, cookies)
		}
	}
	u, _ := url.Parse("https://monitor.test:8081/api/events/ws")
	if len(jar.Cookies(u)) != 1 {
		t.Fatal("Monitor API lost session")
	}
}
