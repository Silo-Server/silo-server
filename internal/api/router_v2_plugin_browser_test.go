package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/apiv2"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

type browserPluginFixture struct{ dir string }

func (browserPluginFixture) RouteDescriptors(context.Context, int) ([]*pluginv1.HttpRouteDescriptor, error) {
	return []*pluginv1.HttpRouteDescriptor{
		{Method: "GET", Path: "/", Access: "authenticated", StaticAsset: true},
		{Method: "GET", Path: "/app.js", Access: "authenticated", StaticAsset: true},
		{Method: "GET", Path: "/admin", Access: "admin", StaticAsset: true},
	}, nil
}
func (f browserPluginFixture) ResolveAssetPath(_ context.Context, _ int, path string) (string, error) {
	if path == "" || path == "admin" {
		path = "page.html"
	}
	return filepath.Join(f.dir, path), nil
}
func (browserPluginFixture) HTTPRoutesClient(context.Context, int, string) (*pluginhost.HTTPRoutesClient, error) {
	return nil, fmt.Errorf("fixture serves only static content")
}

// Follow the launch receipt with a real cookie jar, route/asset proxy, signed
// JWT and session repository. Plugin-provided absolute links are still owned
// by each plugin; this fixture exercises browser-relative page assets.
func TestV2PluginBrowserLaunchPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("plugin_browser_test_%d", time.Now().UnixNano())
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()
	if _, err := pool.Exec(t.Context(), `CREATE TABLE auth_sessions (id text PRIMARY KEY, expires_at timestamptz, revoked_at timestamptz); INSERT INTO auth_sessions(id,expires_at) VALUES ('browser-session',now()+interval '1 hour')`); err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewSessionRepository(pool)
	jwt := auth.NewJWTService("synthetic-plugin-browser-test-key", time.Minute, time.Hour)
	fixture := browserPluginFixture{dir: t.TempDir()}
	for name, body := range map[string]string{"page.html": `<script src="app.js"></script>`, "app.js": "window.pluginLoaded = true;"} {
		if err := os.WriteFile(filepath.Join(fixture.dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	proxy := plugins.NewHTTPProxyWithTypedResolver(fixture, nil)
	content := plugins.NewContentHandler(proxy, func(r *http.Request) plugins.ContentAccess {
		authenticated, admin, userID, profileID := resolveOptionalPluginAccessUser(r, jwt, sessions, nil, nil)
		return plugins.ContentAccess{Authenticated: authenticated, Admin: admin, UserID: userID, ProfileID: profileID}
	}, func(r *http.Request) plugins.ContentAccess {
		authenticated, admin := resolveOptionalPluginAccess(r, jwt, sessions)
		return plugins.ContentAccess{Authenticated: authenticated, Admin: admin}
	})
	server := httptest.NewTLSServer(apiv2.NewHandler(apiv2.Dependencies{
		Auth:          apimw.NewAuthMiddleware(jwt, sessions, nil, nil),
		ViewerAccess:  apimw.NewViewerAccessMiddleware(v2WiringViewer{}),
		PluginLaunch:  handlers.NewAuthHandler(nil, jwt, nil),
		PluginContent: content,
	}))
	defer server.Close()
	client := server.Client()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client.Jar = jar
	request := func(method, path, bearer string, status int) string {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != status {
			t.Fatalf("%s %s: %d want %d: %s", method, path, response.StatusCode, status, body)
		}
		return string(body)
	}
	page := plugins.ContentPrefix + "/plugins/1/"
	asset := plugins.ContentPrefix + "/plugin-assets/1/app.js"
	request(http.MethodGet, page, "", http.StatusUnauthorized)
	request(http.MethodGet, asset, "", http.StatusUnauthorized)
	member, err := jwt.GenerateAccessToken(1, "user", "browser-session")
	if err != nil {
		t.Fatal(err)
	}
	request(http.MethodPost, "/api/v2/auth/plugin-launch", member, http.StatusOK)
	if body := request(http.MethodGet, page, "", http.StatusOK); body != `<script src="app.js"></script>` {
		t.Fatal("plugin HTML changed", body)
	}
	for _, path := range []string{page + "app.js", asset} {
		if body := request(http.MethodGet, path, "", http.StatusOK); body != "window.pluginLoaded = true;" {
			t.Fatal("asset bytes changed", body)
		}
	}
	request(http.MethodGet, page+"admin", "", http.StatusForbidden)
	for _, path := range []string{"/api/v2/settings", "/api/v1/plugins/1/", "/"} {
		u, _ := url.Parse(server.URL + path)
		if len(jar.Cookies(u)) != 0 {
			t.Fatal("launch cookie escaped plugin content", path)
		}
	}
	admin, err := jwt.GenerateAccessToken(1, "admin", "browser-session")
	if err != nil {
		t.Fatal(err)
	}
	request(http.MethodPost, "/api/v2/auth/plugin-launch", admin, http.StatusOK)
	request(http.MethodGet, page+"admin", "", http.StatusOK)
	if err := sessions.Revoke(t.Context(), "browser-session"); err != nil {
		t.Fatal(err)
	}
	request(http.MethodGet, page, "", http.StatusUnauthorized)
	request(http.MethodGet, asset, "", http.StatusUnauthorized)
}
