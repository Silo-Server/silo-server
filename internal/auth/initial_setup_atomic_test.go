package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/Silo-Server/silo-server/internal/api"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/database"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/Silo-Server/silo-server/migrations"
)

// These tests prove the database-wide first-setup boundary. They need an
// owned, ephemeral, loopback Postgres server (SILO_INITIAL_SETUP_ATOMIC_DSN)
// whose database name starts with silo_identity_setup_; each case gets its own
// freshly created database so no case observes another's rows.

const (
	atomicSetupPassword = "initial-fixture-password"
	atomicJWTSecret     = "initial-setup-atomic-fixture-jwt-secret-000000000000"
	atomicMasterKey     = "initial-setup-atomic-fixture-master-key-0000000000"
)

type atomicSetupDB struct {
	pool *pgxpool.Pool
	cfg  *config.Config
}

// atomicSetupDatabase creates and migrates a virgin database for one case on
// the owned server and drops it on cleanup.
func atomicSetupDatabase(t *testing.T, name string) *atomicSetupDB {
	t.Helper()
	raw := os.Getenv("SILO_INITIAL_SETUP_ATOMIC_DSN")
	if raw == "" {
		t.Skip("SILO_INITIAL_SETUP_ATOMIC_DSN is not set")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "postgres" || u.Hostname() != "127.0.0.1" || u.Port() == "" ||
		slices.Contains([]string{"55441", "55442", "55443", "55444", "55445", "55446", "55447", "55448"}, u.Port()) ||
		!strings.HasPrefix(u.Path, "/silo_identity_setup_") {
		t.Fatal("initial setup proof requires its owned ephemeral loopback database")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	caseDB := strings.TrimPrefix(u.Path, "/") + "_" + name
	ident := pgx.Identifier{caseDB}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+ident); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.WithoutCancel(ctx), "DROP DATABASE "+ident+" WITH (FORCE)"); err != nil {
			t.Error("drop case database:", err)
		}
	})
	cu := *u
	cu.Path = "/" + caseDB
	pool, err := pgxpool.New(ctx, cu.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var occupied int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname NOT LIKE 'pg_%' AND n.nspname <> 'information_schema'`).Scan(&occupied); err != nil {
		t.Fatal(err)
	}
	if occupied != 0 {
		t.Fatal("case database is not virgin before migration")
	}
	if err := database.RunMigrations(ctx, pool, migrations.FS, "sql"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFromDB(map[string]string{
		"auth.jwt_secret":             atomicJWTSecret,
		"jellyfin_compat.server_name": "Silo Fixture",
		"jellyfin_compat.server_id":   "00000000-0000-4000-8000-0000000000f2",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &atomicSetupDB{pool: pool, cfg: cfg}
}

func (d *atomicSetupDB) router(t *testing.T, stores userstore.UserStoreProvider) *httptest.Server {
	t.Helper()
	cipher, err := secret.New([]byte(atomicMasterKey))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api.NewRouter(api.Dependencies{
		Config:            d.cfg,
		AppContext:        t.Context(),
		DB:                d.pool,
		SecretCipher:      cipher,
		ClientIPResolver:  clientip.NewResolver(nil),
		NodeID:            "fixture-node",
		PublicURL:         "https://silo.example.test",
		UserStoreProvider: stores,
	}))
	t.Cleanup(server.Close)
	return server
}

func (d *atomicSetupDB) jwt() *auth.JWTService {
	return auth.NewJWTService(d.cfg.Auth.JWTSecret, d.cfg.Auth.AccessTokenExpiry, d.cfg.Auth.RefreshTokenExpiry)
}

func (d *atomicSetupDB) service(t *testing.T, stores userstore.UserStoreProvider) *auth.Service {
	t.Helper()
	users := auth.NewUserRepository(d.pool)
	sessions := auth.NewSessionRepository(d.pool)
	return auth.NewService(auth.NewLocalProvider(users, sessions), d.jwt(), sessions, users, auth.NewInviteCodeRepository(d.pool), nil, stores)
}

type atomicCounts struct{ users, admins, profiles, sessions int }

func (d *atomicSetupDB) counts(t *testing.T) atomicCounts {
	t.Helper()
	var c atomicCounts
	if err := d.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM users),(SELECT count(*) FROM users WHERE role='admin'),(SELECT count(*) FROM user_profiles),(SELECT count(*) FROM auth_sessions)`).Scan(&c.users, &c.admins, &c.profiles, &c.sessions); err != nil {
		t.Fatal(err)
	}
	return c
}

type setupReply struct {
	status int
	body   map[string]any
}

func postSetup(ctx context.Context, base, transport, username string, profile bool) (setupReply, error) {
	payload := map[string]any{"username": username, "email": username + "@silo.example.test", "password": atomicSetupPassword}
	if profile {
		payload["create_default_profile"] = true
		payload["default_profile_name"] = "Home"
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/"+transport+"/auth/setup", bytes.NewReader(body))
	if err != nil {
		return setupReply{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "SiloAtomicSetupProof/1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return setupReply{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	var doc map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&doc)
	return setupReply{status: resp.StatusCode, body: doc}, nil
}

// TestInitialSetupCompetingCallersDB drives two concurrent real-router setup
// requests per transport into the corrected admission boundary. A held
// session-level advisory lock on the setup key parks BOTH callers at the
// boundary (observable as two ungranted waiters), so neither has yet passed
// the in-transaction emptiness check. After release exactly one 201 is
// issued; the other caller re-checks under the lock and gets the existing
// refusal (401 setup_complete on v1, 409 on v2) with no extra rows.
func TestInitialSetupCompetingCallersDB(t *testing.T) {
	for _, transport := range []string{"v1", "v2"} {
		t.Run(transport, func(t *testing.T) {
			d := atomicSetupDatabase(t, "competing_"+transport)
			server := d.router(t, pgstore.NewPostgresProvider(d.pool))
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()

			held, err := d.pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer held.Release()
			if _, err := held.Exec(ctx, "SELECT pg_advisory_lock($1)", auth.InitialSetupAdvisoryLock); err != nil {
				t.Fatal(err)
			}
			released := false
			release := func() {
				if released {
					return
				}
				released = true
				if _, err := held.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", auth.InitialSetupAdvisoryLock); err != nil {
					t.Error("release setup barrier:", err)
				}
			}
			defer release()

			type outcome struct {
				name  string
				reply setupReply
				err   error
			}
			results := make(chan outcome, 2)
			for i := range 2 {
				name := fmt.Sprintf("contender%d", i)
				go func() {
					reply, err := postSetup(ctx, server.URL, transport, name, true)
					results <- outcome{name: name, reply: reply, err: err}
				}()
			}

			// The corrected boundary is one bigint advisory key: pg_locks
			// exposes it as (classid = high 32 bits, objid = low 32 bits).
			classID := auth.InitialSetupAdvisoryLock >> 32
			objID := auth.InitialSetupAdvisoryLock & 0xffffffff
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				var waiters int
				if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND classid=$1 AND objid=$2 AND NOT granted`, classID, objID).Scan(&waiters); err != nil {
					t.Fatal(err)
				}
				if waiters == 2 {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("two requests did not park at the setup admission boundary")
				case <-ticker.C:
				}
			}
			if c := d.counts(t); c != (atomicCounts{}) {
				t.Fatalf("rows written before the boundary was released: %+v", c)
			}
			release()

			outcomes := []outcome{<-results, <-results}
			var statuses []int
			var winner *outcome
			for i := range outcomes {
				if outcomes[i].err != nil {
					t.Fatal(outcomes[i].err)
				}
				statuses = append(statuses, outcomes[i].reply.status)
				if outcomes[i].reply.status == http.StatusCreated {
					winner = &outcomes[i]
				}
			}
			slices.Sort(statuses)
			expected := []int{http.StatusCreated, http.StatusUnauthorized}
			if transport == "v2" {
				expected = []int{http.StatusCreated, http.StatusConflict}
			}
			c := d.counts(t)
			t.Logf("%s statuses=%v counts=%+v", transport, statuses, c)
			if !slices.Equal(statuses, expected) || winner == nil {
				t.Fatalf("statuses = %v, want %v", statuses, expected)
			}
			if c != (atomicCounts{users: 1, admins: 1, profiles: 1, sessions: 1}) {
				t.Fatalf("counts = %+v, want one admin, one profile, one session", c)
			}
			for _, o := range outcomes {
				if o.reply.status == http.StatusCreated {
					continue
				}
				if transport == "v1" && o.reply.body["error"] != "setup_complete" {
					t.Fatalf("v1 loser body = %v, want setup_complete", o.reply.body)
				}
				if transport == "v2" && o.reply.body["status"] != float64(http.StatusConflict) {
					t.Fatalf("v2 loser body = %v, want conflict problem", o.reply.body)
				}
			}

			var storedUser string
			if err := d.pool.QueryRow(ctx, `SELECT username FROM users`).Scan(&storedUser); err != nil {
				t.Fatal(err)
			}
			if storedUser != winner.name {
				t.Fatalf("stored administrator %q, want winner %q", storedUser, winner.name)
			}
			d.assertIssuedSession(t, winner.reply.body, server.URL)
		})
	}
}

// assertIssuedSession checks the returned token pair against the one stored
// session: claims bind the user, role, and session id; the refresh token is a
// refresh token; the stored expiry follows the refresh TTL; and the access
// token authorizes the winner on the real router.
func (d *atomicSetupDB) assertIssuedSession(t *testing.T, body map[string]any, base string) {
	t.Helper()
	ctx := t.Context()
	access, _ := body["access_token"].(string)
	refresh, _ := body["refresh_token"].(string)
	if access == "" || refresh == "" {
		t.Fatalf("token pair missing in %v", body)
	}
	j := d.jwt()
	accessClaims, err := j.ValidateToken(access)
	if err != nil {
		t.Fatal(err)
	}
	refreshClaims, err := j.ValidateToken(refresh)
	if err != nil {
		t.Fatal(err)
	}
	if accessClaims.TokenType != auth.TokenTypeAccess || refreshClaims.TokenType != auth.TokenTypeRefresh {
		t.Fatalf("token types = %q/%q", accessClaims.TokenType, refreshClaims.TokenType)
	}
	if accessClaims.SessionID == "" || accessClaims.SessionID != refreshClaims.SessionID || accessClaims.UserID != refreshClaims.UserID {
		t.Fatalf("claims disagree: %+v vs %+v", accessClaims, refreshClaims)
	}
	var (
		sessionID, role, hash string
		userID                int
		expiresAt, createdAt  time.Time
		revoked               *time.Time
	)
	if err := d.pool.QueryRow(ctx, `SELECT s.id, s.user_id, s.expires_at, s.created_at, s.revoked_at, u.role, u.password_hash FROM auth_sessions s JOIN users u ON u.id=s.user_id`).Scan(&sessionID, &userID, &expiresAt, &createdAt, &revoked, &role, &hash); err != nil {
		t.Fatal(err)
	}
	if sessionID != accessClaims.SessionID || userID != accessClaims.UserID || role != "admin" || accessClaims.Role != "admin" || revoked != nil {
		t.Fatalf("stored session (%s,%d,%s,revoked=%v) does not match claims %+v", sessionID, userID, role, revoked, accessClaims)
	}
	if ttl := expiresAt.Sub(createdAt); ttl < j.RefreshExpiry()-time.Minute || ttl > j.RefreshExpiry()+time.Minute {
		t.Fatalf("session ttl %s, want refresh expiry %s", ttl, j.RefreshExpiry())
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(atomicSetupPassword)); err != nil {
		t.Fatal("stored password hash does not verify:", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte("wrong-"+atomicSetupPassword)); err == nil {
		t.Fatal("stored password hash verifies a wrong password")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /auth/me with issued access token = %d, want 200", resp.StatusCode)
	}
}

// failingProfileStore fails transactional profile creation (Postgres store path).
type failingProfileStore struct {
	userstore.UserStoreProvider
	err error
}

func (f failingProfileStore) CreateProfileInTransaction(context.Context, pgx.Tx, int, userstore.Profile) error {
	return f.err
}

// bridgeStore has no transactional profile writer, like the SQLite bridge,
// and fails on ForUser.
type bridgeStore struct{ err error }

func (b bridgeStore) ForUser(context.Context, int) (userstore.UserStore, error) { return nil, b.err }
func (bridgeStore) Close() error                                                { return nil }

// TestInitialSetupProfileFailureRollsBackDB proves a failed default profile
// leaves no account and no session, keeps setup open, and that a later setup
// on the same database succeeds. Both the transactional Postgres path and the
// bridge fallback path are covered.
func TestInitialSetupProfileFailureRollsBackDB(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stores func(*atomicSetupDB) userstore.UserStoreProvider
	}{
		{"postgres_transactional", func(d *atomicSetupDB) userstore.UserStoreProvider {
			return failingProfileStore{UserStoreProvider: pgstore.NewPostgresProvider(d.pool), err: errors.New("fixture profile failure")}
		}},
		{"bridge_fallback", func(*atomicSetupDB) userstore.UserStoreProvider {
			return bridgeStore{err: errors.New("fixture bridge failure")}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := atomicSetupDatabase(t, "rollback_"+tc.name)
			ctx := t.Context()
			svc := d.service(t, tc.stores(d))
			_, _, err := svc.SetupInitialUser(ctx, "owner", "owner@silo.example.test", atomicSetupPassword, true, "Home", "fixture", "")
			if err == nil || errors.Is(err, auth.ErrSetupAlreadyComplete) {
				t.Fatalf("setup with failing profile err = %v, want profile failure", err)
			}
			if c := d.counts(t); c != (atomicCounts{}) {
				t.Fatalf("rows survived rollback: %+v", c)
			}
			needs, err := svc.NeedsSetup(ctx)
			if err != nil || !needs {
				t.Fatalf("NeedsSetup after rollback = %v, %v; want true", needs, err)
			}
			healthy := d.service(t, pgstore.NewPostgresProvider(d.pool))
			pair, user, err := healthy.SetupInitialUser(ctx, "owner", "owner@silo.example.test", atomicSetupPassword, true, "Home", "fixture", "")
			if err != nil {
				t.Fatal(err)
			}
			if user.Role != "admin" || pair.AccessToken == "" {
				t.Fatalf("retry produced %+v / %+v", user, pair)
			}
			if c := d.counts(t); c != (atomicCounts{users: 1, admins: 1, profiles: 1, sessions: 1}) {
				t.Fatalf("counts after retry = %+v", c)
			}
			if _, _, err := healthy.SetupInitialUser(ctx, "later", "later@silo.example.test", atomicSetupPassword, false, "", "fixture", ""); !errors.Is(err, auth.ErrSetupAlreadyComplete) {
				t.Fatalf("sequential second setup err = %v, want ErrSetupAlreadyComplete", err)
			}
			if c := d.counts(t); c != (atomicCounts{users: 1, admins: 1, profiles: 1, sessions: 1}) {
				t.Fatalf("sequential refusal mutated rows: %+v", c)
			}
		})
	}
}

// TestInitialSetupNoProfileDB covers the no-profile shape and the loser
// leaving no session behind when it is refused without a barrier.
func TestInitialSetupNoProfileDB(t *testing.T) {
	d := atomicSetupDatabase(t, "noprofile")
	server := d.router(t, pgstore.NewPostgresProvider(d.pool))
	ctx := t.Context()
	first, err := postSetup(ctx, server.URL, "v2", "solo", false)
	if err != nil {
		t.Fatal(err)
	}
	if first.status != http.StatusCreated {
		t.Fatalf("setup = %d %v", first.status, first.body)
	}
	if c := d.counts(t); c != (atomicCounts{users: 1, admins: 1, profiles: 0, sessions: 1}) {
		t.Fatalf("counts = %+v", c)
	}
	d.assertIssuedSession(t, first.body, server.URL)
	second, err := postSetup(ctx, server.URL, "v1", "second", true)
	if err != nil {
		t.Fatal(err)
	}
	if second.status != http.StatusUnauthorized || second.body["error"] != "setup_complete" {
		t.Fatalf("v1 sequential refusal = %d %v", second.status, second.body)
	}
	if c := d.counts(t); c != (atomicCounts{users: 1, admins: 1, profiles: 0, sessions: 1}) {
		t.Fatalf("refusal mutated rows: %+v", c)
	}
}
