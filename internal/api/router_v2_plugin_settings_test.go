package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/apiv2"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/plugins"
	"github.com/Silo-Server/silo-server/internal/secret"
)

type v2WiringTokens struct{}

func (v2WiringTokens) ValidateToken(string) (*auth.Claims, error) {
	return &auth.Claims{UserID: 1, Role: "user", SessionID: "s1", TokenType: auth.TokenTypeAccess}, nil
}

type v2WiringSessions struct{}

func (v2WiringSessions) IsValid(context.Context, string) (bool, error) { return true, nil }

type v2WiringViewer struct{}

func (v2WiringViewer) Resolve(context.Context, access.ResolveInput) (access.Scope, error) {
	return access.Scope{}, nil
}

// TestV2PluginSettingsWiredWhenPluginsConfigured guards the construction
// order in newChiRouter: the user-scoped plugin settings handler used to be
// built inside the /api/v1/settings route group, after the v2 dependency set
// was sealed, so every v2 plugin-settings operation answered 503
// dependency_unavailable on a server that had plugins configured.
func TestV2PluginSettingsWiredWhenPluginsConfigured(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	// A pool whose target never answers: DB-backed handlers register and
	// every query fails, which is enough to tell "not wired" from "wired".
	pool, err := pgxpool.New(context.Background(), "postgres://nobody:nobody@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	cipher, err := secret.New(bytes.Repeat([]byte{0}, secret.MinMasterKeyLen))
	if err != nil {
		t.Fatal(err)
	}
	var sealed *apiv2.Dependencies
	deps := Dependencies{
		Config:           cfg,
		AppContext:       context.Background(),
		DB:               pool,
		SecretCipher:     cipher,
		ClientIPResolver: clientip.NewResolver(nil),
		NodeID:           "v2-plugin-settings",
		PublicURL:        "https://silo.example.test",
		PluginService:    &plugins.Service{},
		PluginUserConfig: plugins.NewUserConfigStore(nil, nil),
		v2Wiring:         func(d apiv2.Dependencies) { sealed = &d },
	}
	if h := newChiRouter(deps); h == nil {
		t.Fatal("router did not build")
	}
	if sealed == nil {
		t.Fatal("v2 wiring probe never ran")
	}
	if sealed.PluginSettings == nil {
		t.Fatal("v2 PluginSettings is nil although PluginService and PluginUserConfig are configured")
	}

	// Drive the sealed set through the real v2 handler with the gates faked
	// so the request reaches the operation: the answer must come from the
	// plugin handler (here a failed query on the dead pool), never from the
	// dependency_unavailable fallback.
	v2 := *sealed
	v2.Auth = apimw.NewAuthMiddleware(v2WiringTokens{}, v2WiringSessions{}, nil, nil)
	v2.ViewerAccess = apimw.NewViewerAccessMiddleware(v2WiringViewer{})
	req := httptest.NewRequest(http.MethodGet, "/api/v2/settings/plugins", nil)
	req.Header.Set("Authorization", "Bearer member")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	apiv2.NewHandler(v2).ServeHTTP(rec, req)
	if rec.Code == http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "dependency_unavailable") {
		t.Fatalf("listPluginSettings answered as unwired: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("faked auth gate did not admit the request: %s", rec.Body.String())
	}
}
