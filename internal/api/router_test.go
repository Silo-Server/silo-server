package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/branding"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/playback"
)

type routerContractSettings struct{}

func (routerContractSettings) Get(context.Context, string) (string, error) { return "", nil }
func (routerContractSettings) Set(context.Context, string, string) error   { return nil }

func TestRouterSystemCapabilitiesRoute(t *testing.T) {
	assertRouterContractRoutes(t, "GET /api/v1/system/capabilities")
	assertSiloComplexAccess(t, http.HandlerFunc(handlers.NewSystemCapabilitiesHandler().HandleGet),
		`{"api_version":"2.2","capabilities":["branding.v1","sessions.snapshot.v2","sessions.terminate.v1","users.identity.v1"]}`+"\n")
}

func TestRouterAdminBrandingRoute(t *testing.T) {
	assertRouterContractRoutes(t, "GET /api/v1/admin/branding")
	handler := handlers.NewBrandingHandler(branding.NewService(routerContractSettings{}, nil))
	assertSiloComplexAccess(t, http.HandlerFunc(handler.HandleAdminBranding),
		`{"server_name":"Silo","logo_url":null,"logo_etag":null}`+"\n")
}

func TestRouterSessionsSnapshotRoute(t *testing.T) {
	assertRouterContractRoutes(t, "GET /api/v1/admin/sessions/snapshot")
	handler := handlers.NewAdminHandler(nil, nil, nil)
	assertSiloComplexAccess(t, http.HandlerFunc(handler.HandleGetSessionsSnapshot), "")
	protected := apimw.RequireActingAdmin(nil)(http.HandlerFunc(handler.HandleGetSessionsSnapshot))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request = request.WithContext(apimw.SetClaims(request.Context(), &auth.Claims{Role: "admin"}))
	recorder := httptest.NewRecorder()
	protected.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if body["snapshot_id"] == "" || body["generated_at"] == "" || body["complete"] != false || body["incomplete_reason"] != "snapshot_query_failed" {
		t.Fatalf("unexpected fail-closed snapshot: %#v", body)
	}
}

func assertSiloComplexAccess(t *testing.T, handler http.Handler, expectedBody string) {
	t.Helper()
	protected := apimw.RequireActingAdmin(nil)(handler)

	t.Run("unauthenticated", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		protected.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
		}
	})

	t.Run("non_admin", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request = request.WithContext(apimw.SetClaims(request.Context(), &auth.Claims{Role: "user"}))
		recorder := httptest.NewRecorder()
		protected.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
		}
	})

	t.Run("acting_admin", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request = request.WithContext(apimw.SetClaims(request.Context(), &auth.Claims{Role: "admin"}))
		recorder := httptest.NewRecorder()
		protected.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
		if body := recorder.Body.String(); expectedBody != "" && body != expectedBody {
			t.Fatalf("body = %q, want %q", body, expectedBody)
		}
	})
}

func assertRouterContractRoutes(t *testing.T, newRoute string) {
	t.Helper()

	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	pool, err := pgxpool.New(ctx, "postgres://silo:silo@127.0.0.1:1/silo?connect_timeout=1")
	if err != nil {
		t.Fatalf("create test database pool: %v", err)
	}
	t.Cleanup(pool.Close)

	router := NewRouter(Dependencies{
		AppContext: ctx,
		Config: &config.Config{
			Auth: config.AuthConfig{JWTSecret: "router-contract-test-secret"},
		},
		DB:              pool,
		BrandingService: branding.NewService(routerContractSettings{}, nil),
		SessionMgr:      playback.NewSessionManager(1, 1),
	})

	routes := make(map[string]bool)
	if err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes[method+" "+route] = true
		return nil
	}); err != nil {
		t.Fatalf("walk router: %v", err)
	}

	expected := []string{
		newRoute,
		"GET /api/v1/theme/branding",
		"GET /api/v1/admin/sessions",
		"POST /api/v1/admin/sessions/{session_id}/pause",
		"POST /api/v1/admin/sessions/{session_id}/resume",
		"POST /api/v1/admin/sessions/{session_id}/stop",
		"POST /api/v1/admin/sessions/{session_id}/terminate",
		"POST /api/v1/admin/sessions/{session_id}/message",
	}
	for _, route := range expected {
		if !routes[route] {
			t.Errorf("expected route %q to remain registered", route)
		}
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, newRoute[len("GET "):], nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated request to %q returned %d, want %d", newRoute, recorder.Code, http.StatusUnauthorized)
	}
}
