package audiobooks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

const absTestPassword = "correct horse battery"

// absTestAccount connects to the test database and creates an account with
// a local password and no profiles, deleted when the test ends.
func absTestAccount(t *testing.T) (*pgxpool.Pool, *auth.UserRepository, *models.User) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	users := auth.NewUserRepository(pool)
	suffix := time.Now().UnixNano()
	user, err := users.Create(context.Background(), models.CreateUserInput{Username: fmt.Sprintf("abssignout-%d", suffix),
		Email: fmt.Sprintf("abssignout-%d@example.test", suffix), Password: absTestPassword, Role: models.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, user.ID) })
	return pool, users, user
}

// signOutEverywhere locks the account and revokes every sign-in, as a
// password reset or an admin's sign-out everywhere does.
func signOutEverywhere(ctx context.Context, pool *pgxpool.Pool, userID int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, userID); err != nil {
		return err
	}
	if err := auth.RevokeSignInsInTransaction(ctx, tx, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func liveABSTokens(t *testing.T, pool *pgxpool.Pool, userID int) int {
	t.Helper()
	var live int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM abs_sessions WHERE user_id = $1 AND revoked_at IS NULL`, userID).Scan(&live); err != nil {
		t.Fatal(err)
	}
	return live
}

// signOutConfig is the Audiobookshelf handlers' config. Sign-in, refresh and
// /authorize all read AccessTTL after checking the request's credential and
// before storing the tokens it mints. Once armed, that read first signs the
// account out everywhere, as a password reset committing in that window would.
type signOutConfig struct {
	t      *testing.T
	pool   *pgxpool.Pool
	userID int
	armed  atomic.Bool
}

func (c *signOutConfig) JWTSecret(context.Context) ([]byte, error) {
	return []byte("abs-signout-test-secret-0123456789"), nil
}

func (c *signOutConfig) AccessTTL(ctx context.Context) (time.Duration, error) {
	if c.armed.Swap(false) {
		if err := signOutEverywhere(ctx, c.pool, c.userID); err != nil {
			c.t.Errorf("sign out everywhere: %v", err)
		}
	}
	return 0, nil
}

func (c *signOutConfig) RefreshTTL(context.Context) (time.Duration, error) { return 0, nil }

func (c *signOutConfig) StandaloneLoginEnabled(context.Context) (bool, error) { return true, nil }

// TestABSTokensMintedDuringSignOutEverywhereDB: a sign-out everywhere that
// commits after a sign-in, refresh or /authorize checked its credential and
// before the minted tokens are stored leaves none of them working; the
// request is refused instead.
func TestABSTokensMintedDuringSignOutEverywhereDB(t *testing.T) {
	pool, users, user := absTestAccount(t)
	sessions := auth.NewSessionRepository(pool)
	svc := auth.NewService(auth.NewLocalProvider(users, sessions), auth.NewJWTService("abs-signout-test-jwt-secret-0123456789", time.Minute, time.Hour),
		sessions, users, nil, nil, nil)
	cfg := &signOutConfig{t: t, pool: pool, userID: user.ID}
	limiter := abs.NewLoginLimiter()
	t.Cleanup(limiter.Stop)
	router := chi.NewRouter()
	abs.New(abs.Dependencies{
		MediaStore:    &ABSMediaStore{},
		TokenStore:    &ABSSessionStore{Pool: pool},
		CredValidator: &SiloCredValidator{Auth: svc, Pool: pool, Stores: pgstore.NewPostgresProvider(pool)},
		Config:        cfg,
		LoginLimiter:  limiter,
	}).Mount(router)

	send := func(path string, header map[string]string, body string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		for k, v := range header {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		var resp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp
	}
	login := func() (int, map[string]any) {
		return send("/login", map[string]string{"Content-Type": "application/json", "x-return-tokens": "true"},
			fmt.Sprintf(`{"username":%q,"password":%q}`, user.Username, absTestPassword))
	}
	// signIn returns a working access and refresh token for the account.
	signIn := func(t *testing.T) (access, refresh string) {
		t.Helper()
		status, resp := login()
		access, _ = resp["accessToken"].(string)
		refresh, _ = resp["refreshToken"].(string)
		if status != http.StatusOK || access == "" || refresh == "" {
			t.Fatalf("sign-in = %d %v, want 200 with both tokens", status, resp)
		}
		return access, refresh
	}

	for _, tc := range []struct {
		name string
		// request arms the sign-out once it holds a credential, and sends.
		request func(t *testing.T) int
	}{
		{"sign-in", func(*testing.T) int {
			cfg.armed.Store(true)
			status, _ := login()
			return status
		}},
		{"refresh", func(t *testing.T) int {
			_, refresh := signIn(t)
			cfg.armed.Store(true)
			status, _ := send("/auth/refresh", map[string]string{"x-refresh-token": refresh}, "")
			return status
		}},
		{"authorize", func(t *testing.T) int {
			access, _ := signIn(t)
			cfg.armed.Store(true)
			status, _ := send("/api/authorize", map[string]string{"Authorization": "Bearer " + access}, "")
			return status
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status := tc.request(t)
			if cfg.armed.Load() {
				t.Fatalf("status %d: the request never reached the sign-out", status)
			}
			if live := liveABSTokens(t, pool, user.ID); live != 0 {
				t.Fatalf("%d Audiobookshelf tokens outlived the sign-out (status %d)", live, status)
			}
			if status != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", status)
			}
		})
	}
}
