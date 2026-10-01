package sections

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/policy"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

type nextUpStatementTracer struct {
	mu      sync.Mutex
	queries []string
}

func (c *nextUpStatementTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queries = append(c.queries, strings.Join(strings.Fields(data.SQL), " "))
	return ctx
}

func (c *nextUpStatementTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (c *nextUpStatementTracer) take() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.queries
	c.queries = nil
	return out
}

// TestNextUpModeStatementBudgetPostgres counts what one NextUpMode call costs
// against Postgres. A home layout or section request calls it up to three
// times (maybeInjectNextUp, then the Continue Watching and Next Up fetchers),
// so the per-call cost multiplies across the whole Home fan-out. Inside a
// request that passed viewer access, the mode arrives with the scope and the
// calls cost nothing.
func TestNextUpModeStatementBudgetPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse test database url: %v", err)
	}
	tracer := &nextUpStatementTracer{}
	config.ConnConfig.Tracer = tracer
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	var userID int
	name := fmt.Sprintf("next-up-budget-%d", time.Now().UnixNano())
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (username, email, password_hash, role) VALUES ($1, $2, '', 'user') RETURNING id`,
		name, name+"@example.test",
	).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID) })
	provider := pgstore.NewPostgresProvider(pool)
	store, err := provider.ForUser(ctx, userID)
	if err != nil {
		t.Fatalf("ForUser: %v", err)
	}
	for _, profileID := range []string{"profile-default", "profile-separate"} {
		if err := store.CreateProfile(ctx, userstore.Profile{ID: profileID, Name: profileID}); err != nil {
			t.Fatalf("CreateProfile: %v", err)
		}
	}
	if _, err := store.UpsertSettingValue(ctx, userstore.SettingIdentity{
		Key: settingskeys.UiNextUpMode, Scope: settingscontract.ScopeProfile, ProfileID: "profile-separate",
	}, json.RawMessage(`"separate"`)); err != nil {
		t.Fatalf("store next-up mode: %v", err)
	}

	engine, err := policy.NewEngine(ctx)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	resolver := policy.NewViewerResolver(auth.NewUserRepository(pool), provider,
		access.NewProfileTokenService("next-up-budget-profile-secret", 0), policy.NewPDP(engine), access.NewGroupStore(pool))

	tests := []struct {
		name      string
		profileID string
		// withScope resolves the scope through the production viewer
		// resolver first, as RequireViewerAccess does for a Home request.
		withScope bool
		want      string
		budget    int
	}{
		{name: "no request scope", profileID: "profile-default", want: NextUpModeCombined, budget: 1},
		{name: "request scope, default", profileID: "profile-default", withScope: true, want: NextUpModeCombined, budget: 0},
		{name: "request scope, stored row", profileID: "profile-separate", withScope: true, want: NextUpModeSeparate, budget: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			callCtx := ctx
			if tt.withScope {
				scope, err := resolver.Resolve(ctx, access.ResolveInput{
					UserID: userID, ProfileID: tt.profileID, SkipPINVerification: true,
				})
				if err != nil {
					t.Fatalf("Resolve: %v", err)
				}
				callCtx = access.SetScope(ctx, scope)
			}

			tracer.take()
			if got := NextUpMode(callCtx, store, tt.profileID); got != tt.want {
				t.Fatalf("NextUpMode = %q, want %q", got, tt.want)
			}
			issued := tracer.take()
			if len(issued) != tt.budget {
				t.Fatalf("NextUpMode issued %d statements, budget %d:\n%s", len(issued), tt.budget, strings.Join(issued, "\n"))
			}
			t.Logf("NextUpMode statements: %d", len(issued))
		})
	}
}
