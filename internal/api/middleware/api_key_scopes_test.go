package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestAPIKeyScopesAllow(t *testing.T) {
	users := []string{auth.ScopeAdminUsers}
	groups := []string{auth.ScopeAdminAccessGroupsRead}
	both := []string{auth.ScopeAdminUsers, auth.ScopeAdminAccessGroupsRead}
	monitor := []string{auth.ScopeAdminMonitorRead}
	control := []string{auth.ScopeAdminSessionsControl}

	tests := []struct {
		name   string
		scopes []string
		method string
		path   string
		want   bool
	}{
		{"unscoped allows anything", nil, http.MethodPost, "/api/v1/admin/settings", true},
		{"empty scopes allow anything", []string{}, http.MethodGet, "/api/v1/watch/home", true},

		{"users list", users, http.MethodGet, "/api/v1/admin/users", true},
		{"users create", users, http.MethodPost, "/api/v1/admin/users", true},
		{"users get", users, http.MethodGet, "/api/v1/admin/users/42", true},
		{"users update", users, http.MethodPut, "/api/v1/admin/users/42", true},
		{"users delete", users, http.MethodDelete, "/api/v1/admin/users/42", true},
		{"users profiles", users, http.MethodGet, "/api/v1/admin/users/42/profiles", true},
		{"users v2 list", users, http.MethodGet, "/api/v2/admin/users", true},
		{"users exact identity lookup", users, http.MethodGet, "/api/v2/admin/users?identity=admin%40example.test", true},
		{"summary read", []string{auth.ScopeAdminSessionsSummaryRead}, http.MethodGet, "/api/v2/admin/sessions/summary?user_id=42&limit=5", true},
		{"summary capabilities", []string{auth.ScopeAdminSessionsSummaryRead}, http.MethodGet, "/api/v2/admin/sessions/capabilities", true},
		{"summary denies diagnostics", []string{auth.ScopeAdminSessionsSummaryRead}, http.MethodGet, "/api/v2/admin/sessions", false},
		{"summary denies control", []string{auth.ScopeAdminSessionsSummaryRead}, http.MethodDelete, "/api/v2/admin/sessions/session-a", false},
		{"summary denies account editing", []string{auth.ScopeAdminSessionsSummaryRead}, http.MethodPut, "/api/v2/admin/users/42", false},
		{"monitor sessions", monitor, http.MethodGet, "/api/v2/admin/sessions?limit=50", true},
		{"monitor history", monitor, http.MethodGet, "/api/v2/admin/playback-history", true},
		{"monitor history capabilities", monitor, http.MethodGet, "/api/v2/admin/playback-history/capabilities", true},
		{"monitor activity stats", monitor, http.MethodGet, "/api/v2/admin/stats/top-activity", true},
		{"monitor stream timeseries", monitor, http.MethodGet, "/api/v2/admin/stats/timeseries", true},
		{"monitor user detail", monitor, http.MethodGet, "/api/v2/admin/users/42", true},
		{"monitor user downloads summary", monitor, http.MethodGet, "/api/v2/admin/users/42/downloads/summary", true},
		{"monitor download entries", monitor, http.MethodGet, "/api/v2/admin/downloads/entries", true},
		{"monitor node summary", monitor, http.MethodGet, "/api/v2/admin/nodes/summary", true},
		{"monitor events capabilities", monitor, http.MethodGet, "/api/v2/events/capabilities", true},
		{"monitor catalog listing", monitor, http.MethodGet, "/api/v2/admin/catalog/items", true},
		{"monitor catalog capabilities", monitor, http.MethodGet, "/api/v2/admin/catalog/capabilities", true},
		{"monitor denies item files", monitor, http.MethodGet, "/api/v2/admin/items/movie-1/files", false},
		{"monitor denies catalog export", monitor, http.MethodPost, "/api/v2/admin/catalog/export", false},
		{"monitor events ticket", monitor, http.MethodPost, "/api/v2/events/ws-ticket", true},
		{"monitor denies log stream ticket", monitor, http.MethodPost, "/api/v2/admin/logs/ws-ticket", false},
		{"control denies events ticket", control, http.MethodPost, "/api/v2/events/ws-ticket", false},
		{"monitor denies node rows", monitor, http.MethodGet, "/api/v2/admin/nodes", false},
		{"monitor denies node sessions", monitor, http.MethodGet, "/api/v2/admin/node-sessions", false},
		{"monitor denies device setting values", monitor, http.MethodGet, "/api/v2/admin/devices/42/device-a", false},
		{"monitor denies download storage", monitor, http.MethodGet, "/api/v2/admin/downloads/storage/files", false},
		{"monitor denies user settings", monitor, http.MethodGet, "/api/v2/admin/users/42/settings/values", false},
		{"monitor denies user keys", monitor, http.MethodGet, "/api/v2/admin/users/42/api-keys", false},
		{"monitor denies login sessions", monitor, http.MethodGet, "/api/v2/admin/users/42/login-sessions", false},
		{"monitor denies account editing", monitor, http.MethodPut, "/api/v2/admin/users/42", false},
		{"monitor denies session control", monitor, http.MethodPost, "/api/v2/admin/sessions/session-a/stop", false},
		{"monitor denies event sockets", monitor, http.MethodGet, "/api/v2/events/ws", false},
		{"monitor denies stats overview", monitor, http.MethodGet, "/api/v2/admin/stats", false},
		{"control terminates", control, http.MethodPost, "/api/v2/admin/sessions/session-a/terminate", true},
		{"control messages", control, http.MethodPost, "/api/v2/admin/sessions/session-a/message", true},
		{"control command capabilities", control, http.MethodGet, "/api/v2/admin/sessions/command-capabilities", true},
		{"control denies session reads", control, http.MethodGet, "/api/v2/admin/sessions", false},
		{"control denies nested session paths", control, http.MethodPost, "/api/v2/admin/sessions/a/b/stop", false},
		{"control denies login session revocation", control, http.MethodDelete, "/api/v2/admin/users/42/login-sessions/7", false},
		{"library discovery", []string{auth.ScopeLibrariesRead}, http.MethodGet, "/api/v2/user/libraries", true},
		{"library capabilities", []string{auth.ScopeLibrariesRead}, http.MethodGet, "/api/v2/user/libraries/capabilities", true},
		{"library denies storage metadata", []string{auth.ScopeLibrariesRead}, http.MethodGet, "/api/v2/libraries", false},
		{"library denies management", []string{auth.ScopeLibrariesRead}, http.MethodPost, "/api/v2/libraries", false},
		{"library denies playback", []string{auth.ScopeLibrariesRead}, http.MethodPost, "/api/v2/playback/start", false},
		{"users v2 create", users, http.MethodPost, "/api/v2/admin/users", true},
		{"users v2 update", users, http.MethodPut, "/api/v2/admin/users/42", true},
		{"users v2 delete", users, http.MethodDelete, "/api/v2/admin/users/42", true},
		{"users v2 profiles", users, http.MethodGet, "/api/v2/admin/users/42/profiles", true},
		{"users v2 denies impersonation", users, http.MethodPost, "/api/v2/admin/users/42/impersonate", false},
		{"users v2 denies settings", users, http.MethodPut, "/api/v2/admin/users/42/settings/values/key", false},
		{"users v2 denies keys", users, http.MethodGet, "/api/v2/admin/users/42/api-keys", false},
		{"users v2 denies IP history", users, http.MethodGet, "/api/v2/admin/users/42/ips", false},
		{"groups v2 read", groups, http.MethodGet, "/api/v2/admin/access-groups/3", true},
		{"groups v2 denies write", groups, http.MethodPut, "/api/v2/admin/access-groups/3", false},
		{"users scope denies v2 profile update", users, http.MethodPatch, "/api/v2/profiles/1", false},

		{"users scope denies impersonate", users, http.MethodPost, "/api/v1/admin/users/42/impersonate", false},
		{"users scope denies settings values", users, http.MethodGet, "/api/v1/admin/users/42/settings/values", false},
		{"users scope denies user api keys", users, http.MethodGet, "/api/v1/admin/users/42/api-keys", false},
		{"users scope denies user ips", users, http.MethodGet, "/api/v1/admin/users/42/ips", false},
		{"users scope denies collection delete", users, http.MethodDelete, "/api/v1/admin/users", false},
		{"users scope denies admin settings", users, http.MethodGet, "/api/v1/admin/settings", false},
		{"users scope denies access groups", users, http.MethodGet, "/api/v1/admin/access-groups", false},
		{"users scope denies non-admin surface", users, http.MethodGet, "/api/v1/watch/home", false},
		{"users scope denies the scope catalog", users, http.MethodGet, "/api/v1/api-keys/scopes", false},
		{"users scope denies non-numeric id", users, http.MethodGet, "/api/v1/admin/users/abc", false},

		// The router reads these spellings as other routes than their cleaned
		// or decoded forms, so a scoped key is refused them outright.
		{"dot segments are refused", users, http.MethodGet, "/api/v1/admin/settings/../users", false},
		{"traversal cannot reach unlisted route", users, http.MethodGet, "/api/v1/admin/users/42/../../settings", false},
		{"double slash is refused", users, http.MethodGet, "/api/v1//admin//users", false},
		{"trailing slash is refused", users, http.MethodGet, "/api/v1/admin/users/", false},
		{"encoded slash traversal is refused", users, http.MethodPut, "/api/v1/admin/settings/..%2Fusers%2F1", false},
		{"needless escape is refused", users, http.MethodGet, "/api/v1/admin/users/%34%32", false},
		{"unscoped keys keep encoded paths", nil, http.MethodPut, "/api/v1/admin/settings/a%2Fb", true},

		{"groups read list", groups, http.MethodGet, "/api/v1/admin/access-groups", true},
		{"groups read get", groups, http.MethodGet, "/api/v1/admin/access-groups/3", true},
		{"groups scope denies create", groups, http.MethodPost, "/api/v1/admin/access-groups", false},
		{"groups scope denies update", groups, http.MethodPut, "/api/v1/admin/access-groups/3", false},
		{"groups scope denies delete", groups, http.MethodDelete, "/api/v1/admin/access-groups/3", false},
		{"groups scope denies users", groups, http.MethodGet, "/api/v1/admin/users", false},
		{"groups scope denies v2 users", groups, http.MethodGet, "/api/v2/admin/users", false},

		{"combined scopes union", both, http.MethodGet, "/api/v1/admin/access-groups", true},
		{"combined scopes still deny elsewhere", both, http.MethodPut, "/api/v1/admin/settings", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, tt.path, nil)
			if got := apiKeyScopesAllow(tt.scopes, r); got != tt.want {
				t.Fatalf("apiKeyScopesAllow(%v, %s %s) = %v, want %v", tt.scopes, tt.method, tt.path, got, tt.want)
			}
		})
	}
}

// A monitoring key is read-only. The one write this test admits is minting an
// events socket ticket (POST /api/v2/events/ws-ticket), which only opens a
// read-only stream; that row lands with key-minted socket tickets.
func TestAPIKeyMonitorScopeOnlyReads(t *testing.T) {
	const ticketRoute = `^/api/v2/events/ws-ticket$`
	for _, route := range apiKeyScopeRoutes[auth.ScopeAdminMonitorRead] {
		if route.method != http.MethodGet && (route.method != http.MethodPost || route.pattern.String() != ticketRoute) {
			t.Errorf("%s %s admits a write", route.method, route.pattern)
		}
	}
}

type fakeAPIKeyValidator struct {
	key *models.APIKey
}

func (f *fakeAPIKeyValidator) GetByKey(_ context.Context, key string) (*models.APIKey, error) {
	if f.key != nil && f.key.Key == key {
		return f.key, nil
	}
	return nil, auth.ErrAPIKeyNotFound
}

func (f *fakeAPIKeyValidator) UpdateLastUsed(context.Context, int64) error { return nil }

type fakeAPIKeyUserLoader struct {
	user *models.User
}

func (f *fakeAPIKeyUserLoader) GetByID(context.Context, int) (*models.User, error) {
	return f.user, nil
}

func TestRequireAuthEnforcesAPIKeyScopes(t *testing.T) {
	key := &models.APIKey{
		ID:     1,
		UserID: 7,
		Key:    "sa_test",
		Scopes: []string{auth.ScopeAdminUsers},
	}
	owner := &models.User{ID: 7, Role: "admin", Enabled: true}
	am := NewAuthMiddleware(nil, nil, &fakeAPIKeyValidator{key: key}, &fakeAPIKeyUserLoader{user: owner})

	handler := am.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := GetClaims(r.Context())
		if claims == nil || claims.TokenType != auth.TokenTypeAPIKey {
			t.Fatal("expected api key claims in context")
		}
		if len(claims.APIKeyScopes) != 1 || claims.APIKeyScopes[0] != auth.ScopeAdminUsers {
			t.Fatalf("claims scopes = %v", claims.APIKeyScopes)
		}
		w.WriteHeader(http.StatusOK)
	}))

	allowed := httptest.NewRequest(http.MethodGet, "/api/v1/admin/users", nil)
	allowed.Header.Set("Authorization", "Bearer sa_test")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, allowed)
	if rec.Code != http.StatusOK {
		t.Fatalf("in-scope route: status = %d, want 200", rec.Code)
	}

	denied := httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
	denied.Header.Set("Authorization", "Bearer sa_test")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, denied)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("out-of-scope route: status = %d, want 403", rec.Code)
	}
}

// A scoped key must reach exactly the handler its allowlist judged. chi
// routes on the escaped path and never cleans it, so this drives the real
// root ServeMux, RequireAuth and a chi router with the settings route the
// encoded-traversal report used: the users scope may update a user and must
// never reach the settings handler, however the path is spelled.
func TestRequireAuthScopedKeyReachesOnlyTheJudgedRoute(t *testing.T) {
	key := &models.APIKey{ID: 1, UserID: 7, Key: "sa_test", Scopes: []string{auth.ScopeAdminUsers}}
	owner := &models.User{ID: 7, Role: "admin", Enabled: true}
	am := NewAuthMiddleware(nil, nil, &fakeAPIKeyValidator{key: key}, &fakeAPIKeyUserLoader{user: owner})

	var reached []string
	router := chi.NewRouter()
	router.Group(func(r chi.Router) {
		r.Use(am.RequireAuth)
		r.Put("/api/v1/admin/users/{id}", func(w http.ResponseWriter, r *http.Request) {
			reached = append(reached, "users:"+chi.URLParam(r, "id"))
		})
		r.Put("/api/v1/admin/settings/{key}", func(w http.ResponseWriter, r *http.Request) {
			reached = append(reached, "settings:"+chi.URLParam(r, "key"))
		})
		r.Put("/api/v2/admin/users/{id}", func(w http.ResponseWriter, r *http.Request) {
			reached = append(reached, "users:"+chi.URLParam(r, "id"))
		})
		r.Put("/api/v2/admin/settings/values/{key}", func(w http.ResponseWriter, r *http.Request) {
			reached = append(reached, "settings:"+chi.URLParam(r, "key"))
		})
		// Stands in for any route whose last parameter spans segments.
		r.Put("/api/v1/admin/settings/*", func(w http.ResponseWriter, r *http.Request) {
			reached = append(reached, "settings:"+chi.URLParam(r, "*"))
		})
	})
	root := http.NewServeMux()
	root.Handle("/api/", router)

	tests := []struct {
		target string
		want   int
		reach  string
	}{
		{"/api/v1/admin/users/1", http.StatusOK, "users:1"},
		{"/api/v2/admin/users/1", http.StatusOK, "users:1"},
		{"/api/v1/admin/settings/plain.key", http.StatusForbidden, ""},
		{"/api/v1/admin/settings/..%2Fusers%2F1", http.StatusForbidden, ""},
		{"/api/v1/admin/settings/attacker.key%2F..%2F..%2Fusers%2F1", http.StatusForbidden, ""},
		{"/api/v1/admin/settings/%2E%2E%2Fusers%2F1", http.StatusForbidden, ""},
		{"/api/v2/admin/settings/values/..%2F..%2Fusers%2F1", http.StatusForbidden, ""},
	}
	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			reached = nil
			req := httptest.NewRequest(http.MethodPut, tt.target, nil)
			req.Header.Set("Authorization", "Bearer sa_test")
			rec := httptest.NewRecorder()
			root.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d (reached %v)", rec.Code, tt.want, reached)
			}
			if got := strings.Join(reached, ","); got != tt.reach {
				t.Fatalf("reached %q, want %q", got, tt.reach)
			}
		})
	}

	// The root mux redirects dot segments before the API router sees them,
	// but RequireAuth must not lean on that: chi alone routes the literal
	// segments to the settings catch-all, while the cleaned form is a users
	// route.
	for _, target := range []string{
		"/api/v1/admin/settings/../users/1",
		"/api/v1/admin/settings/./../users/1",
	} {
		t.Run("router only "+target, func(t *testing.T) {
			reached = nil
			req := httptest.NewRequest(http.MethodPut, target, nil)
			req.Header.Set("Authorization", "Bearer sa_test")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden || len(reached) != 0 {
				t.Fatalf("status = %d, reached %v; want 403 and no handler", rec.Code, reached)
			}
		})
	}
}

func TestAPIKeyScopesCannotManageV2Credentials(t *testing.T) {
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v2/admin/api-keys/capabilities"},
		{http.MethodGet, "/api/v2/admin/api-keys"},
		{http.MethodGet, "/api/v2/admin/api-keys/1"},
		{http.MethodPost, "/api/v2/admin/api-keys"},
		{http.MethodPut, "/api/v2/admin/api-keys/1/tier"},
		{http.MethodDelete, "/api/v2/admin/api-keys/1"},
	} {
		t.Run(route.method+route.path, func(t *testing.T) {
			request := httptest.NewRequest(route.method, route.path, nil)
			if apiKeyScopesAllow(auth.ValidAPIKeyScopes(), request) {
				t.Fatal("scoped key can escape its allowlist through key management")
			}
			if !apiKeyScopesAllow(nil, request) {
				t.Fatal("unscoped key lost its existing account-level access")
			}
		})
	}
}

// The monitor scope promises never to return filesystem paths, node
// addresses or raw failure text. This walks every response schema the scope
// can reach in the committed OpenAPI document, so a field added to one of
// those operations has to be withheld from scoped keys and listed here, or
// kept out of the scope. It also catches allowlist rows that match no
// operation.
func TestAPIKeyMonitorScopeResponsesCarryNoDeploymentDetails(t *testing.T) {
	raw, err := os.ReadFile("../../../contracts/api/v2/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	denied := map[string]bool{"path": true, "file_path": true, "dir": true, "url": true, "public_url": true, "origin": true, "hostname": true, "storage_key": true, "error": true, "last_stats": true, "download_artifact_dir_override": true}
	// Fields a handler withholds from scoped keys.
	withheld := map[string]bool{"AdminDiskStats.path": true, "AdminDownloadPreparation.error": true}

	var walk func(owner string, schema json.RawMessage, seen map[string]bool, report func(string))
	walk = func(owner string, schema json.RawMessage, seen map[string]bool, report func(string)) {
		var s struct {
			Ref                  string                     `json:"$ref"`
			Properties           map[string]json.RawMessage `json:"properties"`
			Items                json.RawMessage            `json:"items"`
			AdditionalProperties json.RawMessage            `json:"additionalProperties"`
			AllOf, AnyOf, OneOf  []json.RawMessage
		}
		if json.Unmarshal(schema, &s) != nil {
			return
		}
		if s.Ref != "" {
			name := strings.TrimPrefix(s.Ref, "#/components/schemas/")
			if !seen[name] {
				seen[name] = true
				walk(name, doc.Components.Schemas[name], seen, report)
			}
			return
		}
		for field, sub := range s.Properties {
			if denied[field] && !withheld[owner+"."+field] {
				report(owner + "." + field)
			}
			walk(owner, sub, seen, report)
		}
		for _, sub := range append(append(append([]json.RawMessage{s.Items, s.AdditionalProperties}, s.AllOf...), s.AnyOf...), s.OneOf...) {
			if len(sub) > 0 && sub[0] == '{' {
				walk(owner, sub, seen, report)
			}
		}
	}

	scopes := []string{auth.ScopeAdminMonitorRead}
	matched := map[string]bool{}
	for path, operations := range doc.Paths {
		concrete := regexp.MustCompile(`\{[^}]+\}`).ReplaceAllString(path, "42")
		for method, operation := range operations {
			request := httptest.NewRequest(strings.ToUpper(method), concrete, nil)
			if !apiKeyScopesAllow(scopes, request) {
				continue
			}
			for _, route := range apiKeyScopeRoutes[auth.ScopeAdminMonitorRead] {
				if route.method == request.Method && route.pattern.MatchString(concrete) {
					matched[route.method+" "+route.pattern.String()] = true
				}
			}
			var op struct {
				Responses map[string]struct {
					Content map[string]struct {
						Schema json.RawMessage `json:"schema"`
					} `json:"content"`
				} `json:"responses"`
			}
			if err := json.Unmarshal(operation, &op); err != nil {
				t.Fatal(err)
			}
			for status, response := range op.Responses {
				if !strings.HasPrefix(status, "2") {
					continue
				}
				for _, content := range response.Content {
					walk("", content.Schema, map[string]bool{}, func(field string) {
						t.Errorf("%s %s returns %s to a monitor key", strings.ToUpper(method), path, field)
					})
				}
			}
		}
	}
	for _, route := range apiKeyScopeRoutes[auth.ScopeAdminMonitorRead] {
		if !matched[route.method+" "+route.pattern.String()] {
			t.Errorf("%s %s matches no operation", route.method, route.pattern)
		}
	}
}
