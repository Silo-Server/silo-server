package middleware

import (
	"context"
	"net/http"
	"strings"
)

// DemoSettingsReader is the subset of ServerSettingsStore needed by DemoGuard.
type DemoSettingsReader interface {
	Get(ctx context.Context, key string) (string, error)
}

// DemoGuard blocks destructive mutations for non-admin users when demo mode
// is enabled (server setting "demo.enabled" = "true").
//
// Allowed: browsing, playback, favorites, watchlist, ratings, collections,
// profiles, playback progress, watched state.
//
// Blocked: API key management, downloads, history imports, subtitle downloads.
type DemoGuard struct {
	settings DemoSettingsReader
}

// NewDemoGuard creates a new DemoGuard.
func NewDemoGuard(settings DemoSettingsReader) *DemoGuard {
	return &DemoGuard{settings: settings}
}

// blockedRoute defines a method + path prefix combination that is blocked in demo mode.
type blockedRoute struct {
	methods []string
	prefix  string
}

// demoBlockedRoutes lists the route patterns blocked for non-admin users in demo mode.
var demoBlockedRoutes = []blockedRoute{
	{methods: []string{"POST", "DELETE"}, prefix: "/api/v1/api-keys"},
	{methods: []string{"POST", "DELETE"}, prefix: "/api/v1/downloads"},
	{methods: []string{"POST"}, prefix: "/api/v1/history-imports"},
	{methods: []string{"POST"}, prefix: "/api/v1/subtitles/download"},
	{methods: []string{"POST"}, prefix: "/api/v1/subtitles/upload"},
	{methods: []string{"DELETE"}, prefix: "/api/v1/subtitles/"},
}

// Guard is an HTTP middleware that enforces demo mode restrictions.
func (dg *DemoGuard) Guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read-only methods always pass.
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		// Check demo mode (fast path: setting not set means disabled).
		enabled, _ := dg.settings.Get(r.Context(), "demo.enabled")
		if enabled != "true" {
			next.ServeHTTP(w, r)
			return
		}

		// Admins bypass all demo restrictions.
		claims := GetClaims(r.Context())
		if claims != nil && claims.Role == "admin" {
			next.ServeHTTP(w, r)
			return
		}

		// Check if this request matches a blocked route.
		path := r.URL.Path
		for _, br := range demoBlockedRoutes {
			if !strings.HasPrefix(path, br.prefix) {
				continue
			}
			for _, m := range br.methods {
				if r.Method == m {
					writeDemoBlocked(w)
					return
				}
			}
		}

		next.ServeHTTP(w, r)
	})
}

func writeDemoBlocked(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":"demo_restricted","message":"This action is not available in demo mode."}`))
}
