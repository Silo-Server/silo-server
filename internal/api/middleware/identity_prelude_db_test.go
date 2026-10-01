package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/policy"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// preludeStatementTracer records every statement pgx sends while it is armed.
type preludeStatementTracer struct {
	mu      sync.Mutex
	armed   bool
	queries []string
}

func (c *preludeStatementTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.armed {
		c.queries = append(c.queries, strings.Join(strings.Fields(data.SQL), " "))
	}
	return ctx
}

func (c *preludeStatementTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (c *preludeStatementTracer) arm() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.armed = true
	c.queries = nil
}

func (c *preludeStatementTracer) disarm() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.armed = false
	return append([]string(nil), c.queries...)
}

// TestProfileScopedPreludeStatementBudget counts the Postgres statements the
// production auth prelude (RequireAuth, RequireViewerAccess, RequireProfile)
// issues before a profile-scoped handler runs. Every statement is a separate
// pool acquire and round trip on the hot path of every native request, so the
// budget is exact: a change that adds a read fails here, and one that removes
// a read lowers the budget in the same commit.
func TestProfileScopedPreludeStatementBudget(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse test database url: %v", err)
	}
	tracer := &preludeStatementTracer{}
	config.ConnConfig.Tracer = tracer
	// One connection keeps the trace deterministic: a second connection would
	// replay session setup statements into the count.
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	jwt := auth.NewJWTService("prelude-budget-secret-0123456789abcdef", time.Hour, time.Hour)
	sessions := auth.NewSessionRepository(pool)
	users := auth.NewUserRepository(pool)
	groups := access.NewGroupStore(pool)
	provider := pgstore.NewPostgresProvider(pool)
	engine, err := policy.NewEngine(ctx)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	resolver := policy.NewViewerResolver(users, provider,
		access.NewProfileTokenService("prelude-budget-profile-secret", 0), policy.NewPDP(engine), groups)
	authMW := NewAuthMiddleware(jwt, sessions, auth.NewAPIKeyRepository(pool), users)
	viewerMW := NewViewerAccessMiddleware(resolver)

	var scope access.Scope
	chain := authMW.RequireAuth(viewerMW.RequireViewerAccess(RequireProfile(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scope, _ = access.GetScope(r.Context())
			w.WriteHeader(http.StatusNoContent)
		}))))

	const profileID = "prelude-profile"
	seed := func(t *testing.T, grouped bool) (token string, store userstore.UserStore) {
		t.Helper()
		var userID int
		name := fmt.Sprintf("prelude-%d", time.Now().UnixNano())
		if err := pool.QueryRow(ctx,
			`INSERT INTO users (username, email, password_hash, role) VALUES ($1, $2, '', 'user') RETURNING id`,
			name, name+"@example.test",
		).Scan(&userID); err != nil {
			t.Fatalf("seed user: %v", err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID) })
		if grouped {
			group, err := groups.Create(ctx, access.CreateGroupInput{
				Name:             fmt.Sprintf("prelude-group-%d", userID),
				TranscodeAllowed: true,
				RequestsAllowed:  true,
			})
			if err != nil {
				t.Fatalf("create access group: %v", err)
			}
			t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM access_groups WHERE id = $1`, group.ID) })
			if _, err := pool.Exec(ctx, `UPDATE users SET access_group_id = $1 WHERE id = $2`, group.ID, userID); err != nil {
				t.Fatalf("assign access group: %v", err)
			}
		}
		store, err := provider.ForUser(ctx, userID)
		if err != nil {
			t.Fatalf("ForUser: %v", err)
		}
		if err := store.CreateProfile(ctx, userstore.Profile{ID: profileID, Name: "Main"}); err != nil {
			t.Fatalf("CreateProfile: %v", err)
		}
		session := models.AuthSession{
			ID:        fmt.Sprintf("prelude-session-%d", userID),
			UserID:    userID,
			ExpiresAt: time.Now().Add(time.Hour),
		}
		if err := sessions.Create(ctx, session); err != nil {
			t.Fatalf("create session: %v", err)
		}
		token, err = jwt.GenerateAccessToken(userID, models.RoleUser, session.ID)
		if err != nil {
			t.Fatalf("GenerateAccessToken: %v", err)
		}
		return token, store
	}

	serve := func(t *testing.T, token string) []string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v2/home", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Profile-Id", profileID)
		rec := httptest.NewRecorder()
		tracer.arm()
		chain.ServeHTTP(rec, req)
		issued := tracer.disarm()
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusNoContent, rec.Body.String())
		}
		return issued
	}

	tests := []struct {
		name    string
		grouped bool
		// storedHiddenLibraries writes a canonical ui.disabled_library_ids
		// row for the profile before the request.
		storedHiddenLibraries bool
		budget                int
	}{
		// A profile that never saved hidden libraries: the common case. The
		// statements are the session check, the users row, the profile row,
		// its allowed libraries, and one settings resolution. The legacy
		// user_settings fallback read is gone, so a stored row no longer
		// changes the count.
		{name: "default profile", budget: 5},
		{name: "profile with stored hidden libraries", storedHiddenLibraries: true, budget: 5},
		// A non-admin account in an access group adds the group policy read.
		{name: "access group member", grouped: true, budget: 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, store := seed(t, tt.grouped)
			if tt.storedHiddenLibraries {
				if _, err := store.UpsertSettingValue(ctx, userstore.SettingIdentity{
					Key: settingskeys.UiDisabledLibraryIds, Scope: settingscontract.ScopeProfile, ProfileID: profileID,
				}, json.RawMessage(`[4]`)); err != nil {
					t.Fatalf("store hidden libraries: %v", err)
				}
			}

			issued := serve(t, token)
			if len(issued) != tt.budget {
				t.Fatalf("prelude issued %d statements, budget %d:\n%s",
					len(issued), tt.budget, strings.Join(issued, "\n"))
			}
			t.Logf("prelude statements: %d", len(issued))
			if scope.ProfileID != profileID {
				t.Fatalf("scope profile = %q, want %q", scope.ProfileID, profileID)
			}
		})
	}
}
