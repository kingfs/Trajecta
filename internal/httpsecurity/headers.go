// Package httpsecurity adds browser-facing security response headers without
// changing application response bodies or cache semantics.
package httpsecurity

import "net/http"

const monitorContentSecurityPolicy = "default-src 'self'; base-uri 'self'; object-src 'none'; frame-ancestors 'none'; form-action 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self' https: ws: wss:; manifest-src 'self'; worker-src 'self' blob:"

// Common wraps an HTTP handler with headers that are safe for the proxy and
// management surfaces. It intentionally does not add a Content-Security-Policy
// to proxied provider responses.
func Common(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), geolocation=(), microphone=()")
		next.ServeHTTP(w, r)
	})
}

// Monitor adds a restrictive same-origin CSP to the Monitor UI and APIs, as
// well as the common response headers. Inline scripts are not allowed; the
// small theme bootstrap is served as a same-origin asset. Inline styles remain
// allowed because the React UI uses computed style attributes for charts and
// timeline layout.
func Monitor(next http.Handler) http.Handler {
	return Common(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", monitorContentSecurityPolicy)
		next.ServeHTTP(w, r)
	}))
}
