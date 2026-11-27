package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/demomode"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestDemoLoginEndpoint(t *testing.T) {
	t.Run("success returns token pair with no-store", func(t *testing.T) {
		deps := pilotDeps(nil, nil)
		deps.Sessions = &fakeSessionService{}
		h := newTestHandler(t, deps)

		rec := do(t, h, http.MethodPost, Prefix+"/auth/demo", `{}`, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d %s, want 200", rec.Code, rec.Body.String())
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
			t.Fatalf("Cache-Control = %q, want no-store", cc)
		}
		var body TokenPair
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.AccessToken != "demo-acc" || body.RefreshToken != "demo-ref" || body.User.Username != "demo" {
			t.Fatalf("unexpected body: %+v", body)
		}
	})

	t.Run("disabled returns 403 demo_login_disabled", func(t *testing.T) {
		deps := pilotDeps(nil, nil)
		deps.Sessions = &fakeSessionService{
			demoLoginErr: &handlers.APIError{Status: http.StatusForbidden, Code: "demo_login_disabled", Message: "Demo login is not available on this server"},
		}
		h := newTestHandler(t, deps)

		rec := do(t, h, http.MethodPost, Prefix+"/auth/demo", `{}`, nil)
		requireProblem(t, rec, TypeDemoLoginDisabled)
	})

	t.Run("store error returns 500 internal_error", func(t *testing.T) {
		deps := pilotDeps(nil, nil)
		deps.Sessions = &fakeSessionService{demoLoginErr: errors.New("db down")}
		h := newTestHandler(t, deps)

		rec := do(t, h, http.MethodPost, Prefix+"/auth/demo", `{}`, nil)
		requireProblem(t, rec, TypeInternalError)
	})

	t.Run("uses dedicated demo_login rate-limit bucket", func(t *testing.T) {
		var seenBuckets []string
		deps := pilotDeps(nil, nil)
		deps.Sessions = &fakeSessionService{}
		deps.BucketRateLimit = func(bucket string) func(http.Handler) http.Handler {
			return func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					seenBuckets = append(seenBuckets, bucket)
					if bucket == demoLoginBucket {
						w.Header().Set("Content-Type", "application/json")
						w.Header().Set("Retry-After", "7")
						w.WriteHeader(http.StatusTooManyRequests)
						_, _ = w.Write([]byte(`{"error":"rate_limit_exceeded","message":"Too many requests"}`))
						return
					}
					next.ServeHTTP(w, r)
				})
			}
		}
		h := newTestHandler(t, deps)

		rec := do(t, h, http.MethodPost, Prefix+"/auth/demo", `{}`, nil)
		requireProblem(t, rec, TypeRateLimited)
		if len(seenBuckets) != 1 || seenBuckets[0] != demoLoginBucket {
			t.Fatalf("buckets = %v, want [%s]", seenBuckets, demoLoginBucket)
		}
	})
}

type purposeFakeSessions struct {
	roles    map[string]string
	purposes map[string]string
}

func (f purposeFakeSessions) ActiveSession(_ context.Context, id string, _ int) (auth.SessionAuthority, bool, error) {
	role, ok := f.roles[id]
	if !ok {
		return auth.SessionAuthority{}, false, nil
	}
	purpose := f.purposes[id]
	if purpose == "" {
		purpose = models.SessionPurposeAccount
	}
	return auth.SessionAuthority{Role: role, Purpose: purpose}, true, nil
}

func TestDemoPurposeSessionRestrictions(t *testing.T) {
	const demoTok = "tok-demo-purpose"
	users := map[int]*models.User{
		1: {ID: 1, Role: models.RoleUser, Enabled: true},
	}
	claims := map[string]*auth.Claims{
		demoTok: {UserID: 1, Role: models.RoleUser, SessionID: "s-demo-1", TokenType: auth.TokenTypeAccess, SessionPurpose: models.SessionPurposeDemo},
	}
	sessions := purposeFakeSessions{
		roles:    map[string]string{"s-demo-1": models.RoleUser},
		purposes: map[string]string{"s-demo-1": models.SessionPurposeDemo},
	}

	deps := externalSignInDeps(&fakeExternalSignIn{allowUnlink: true})
	deps.Auth = apimw.NewAuthMiddleware(fakeTokens{claims}, sessions, nil, fakeUsers{users})
	deps.Sessions = &fakeSessionService{}
	// Even with DemoSettings reporting off or failing, a SessionPurposeDemo
	// session stays restricted on DemoRestricted mutations and cannot approve
	// device sign-ins.
	deps.DemoSettings = failingSettings{}
	h := newTestHandler(t, deps)
	hdr := with(bearer(demoTok), "X-Profile-Id", "p-primary")

	// Mutation gate must refuse with demo refusal despite failing DemoSettings.
	rec := do(t, h, http.MethodPost, Prefix+"/account/password", `{"current_password":"pw","new_password":"margin fossil quench"}`, hdr)
	if !isDemoRefusal(rec) {
		t.Fatalf("demo session password change = %d %s, want demo refusal", rec.Code, rec.Body.String())
	}

	// Device sign-in approval / denial require IsOwnLoginSession, so demo
	// sessions cannot mint regular account sessions via device approval.
	for _, tc := range []struct {
		name, path, body string
	}{
		{"approve device login", "/auth/device/approve", `{"code":"ABCD-EFGH"}`},
		{"approve device handoff", "/auth/device/approve-handoff", `{"code":"ABCD-EFGH"}`},
		{"deny device login", "/auth/device/deny", `{"code":"ABCD-EFGH"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, h, http.MethodPost, Prefix+tc.path, tc.body, hdr)
			requireProblem(t, rec, TypePermissionDenied)
		})
	}
}

type poolDemoSettings struct{ pool *pgxpool.Pool }

func (p poolDemoSettings) Get(ctx context.Context, key string) (string, error) {
	var value string
	err := p.pool.QueryRow(ctx, `SELECT value FROM server_settings WHERE key = $1`, key).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func setupDemoIsolationDB(t *testing.T) (http.Handler, *auth.JWTService, *auth.SessionRepository, int) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	demoUser := "apiv2-demo-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	saved := map[string]*string{}
	for _, key := range []string{demomode.SettingKey, demomode.UsernameKey} {
		var val string
		if err := pool.QueryRow(ctx, `SELECT value FROM server_settings WHERE key = $1`, key).Scan(&val); err == nil {
			v := val
			saved[key] = &v
		}
	}
	t.Cleanup(func() {
		cleanCtx := context.WithoutCancel(ctx)
		for _, key := range []string{demomode.SettingKey, demomode.UsernameKey} {
			if saved[key] == nil {
				_, _ = pool.Exec(cleanCtx, `DELETE FROM server_settings WHERE key = $1`, key)
			} else {
				_, _ = pool.Exec(cleanCtx, `INSERT INTO server_settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, *saved[key])
			}
		}
		_, _ = pool.Exec(cleanCtx, `DELETE FROM auth_sessions WHERE user_id IN (SELECT id FROM users WHERE username = $1)`, demoUser)
		_, _ = pool.Exec(cleanCtx, `DELETE FROM users WHERE username = $1`, demoUser)
	})

	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role, enabled)
		VALUES ($1, $2, 'synthetic-hash', 'user', true) RETURNING id`, demoUser, demoUser+"@example.test").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{demomode.SettingKey: demomode.OnValue, demomode.UsernameKey: demoUser} {
		if _, err := pool.Exec(ctx, `INSERT INTO server_settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, k, v); err != nil {
			t.Fatal(err)
		}
	}

	users := auth.NewUserRepository(pool)
	sessions := auth.NewSessionRepository(pool)
	jwt := auth.NewJWTService("apiv2-demo-isolation-secret-0123456", 15*time.Minute, 24*time.Hour)
	settings := poolDemoSettings{pool: pool}
	svc := auth.NewService(auth.NewLocalProvider(users, sessions), jwt, sessions, users, nil, settings, nil)

	deps := parityDeps(true)
	deps.DemoSettings = settings
	deps.Auth = apimw.NewAuthMiddleware(jwt, sessions, nil, users)
	deps.Sessions = handlers.NewAuthHandler(svc, jwt, nil)
	return newTestHandler(t, deps), jwt, sessions, userID
}

func TestDemoLoginEndpointAndIsolationDB(t *testing.T) {
	ctx := t.Context()
	h, jwt, sessions, userID := setupDemoIsolationDB(t)

	issue := func(deviceName string) (TokenPair, string) {
		t.Helper()
		rec := do(t, h, http.MethodPost, Prefix+"/auth/demo", `{}`, map[string]string{"X-Silo-Device-Name": deviceName})
		if rec.Code != http.StatusOK {
			t.Fatalf("POST /auth/demo = %d %s", rec.Code, rec.Body.String())
		}
		var pair TokenPair
		if err := json.Unmarshal(rec.Body.Bytes(), &pair); err != nil {
			t.Fatal(err)
		}
		claims, err := jwt.ValidateToken(pair.AccessToken)
		if err != nil {
			t.Fatal(err)
		}
		return pair, claims.SessionID
	}

	pair1, sid1 := issue("Visitor One Roku")
	pair2, sid2 := issue("Visitor Two Roku")
	if sid1 == sid2 {
		t.Fatal("two demo logins returned the same session id")
	}

	// Visitor 1 lists sessions: sees ONLY its own session, never visitor 2's ID or count.
	listRec := do(t, h, http.MethodGet, Prefix+"/auth/sessions", "", bearer(pair1.AccessToken))
	if listRec.Code != http.StatusOK {
		t.Fatalf("GET /auth/sessions = %d %s", listRec.Code, listRec.Body.String())
	}
	if strings.Contains(listRec.Body.String(), sid2) {
		t.Fatalf("visitor 1 session list leaked visitor 2 session id %s: %s", sid2, listRec.Body.String())
	}
	var page reachPage
	if err := json.Unmarshal(listRec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || string(page.Items[0]["id"]) != `"`+sid1+`"` || page.Page.HasMore || page.Page.NextCursor != "" {
		t.Fatalf("unexpected demo session list: %s", listRec.Body.String())
	}

	// Non-empty cursor on demo session list is invalid_cursor.
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/auth/sessions?cursor=bogus", "", bearer(pair1.AccessToken)), TypeInvalidCursor)

	// Visitor 1 cannot revoke visitor 2's session.
	requireProblem(t, do(t, h, http.MethodDelete, Prefix+"/auth/sessions/"+sid2, "", bearer(pair1.AccessToken)), TypePermissionDenied)
	if _, active, err := sessions.ActiveSession(ctx, sid2, userID); err != nil || !active {
		t.Fatalf("visitor 2 session should remain active: (%v, %v)", active, err)
	}

	// Visitor 1 can revoke its own session.
	if rec := do(t, h, http.MethodDelete, Prefix+"/auth/sessions/"+sid1, "", bearer(pair1.AccessToken)); rec.Code != http.StatusNoContent {
		t.Fatalf("revoking own demo session = %d %s", rec.Code, rec.Body.String())
	}
	if _, active, err := sessions.ActiveSession(ctx, sid1, userID); err != nil || active {
		t.Fatalf("visitor 1 session should be revoked: (%v, %v)", active, err)
	}
	// Visitor 2 still works after visitor 1 logs out.
	if rec := do(t, h, http.MethodGet, Prefix+"/auth/sessions", "", bearer(pair2.AccessToken)); rec.Code != http.StatusOK {
		t.Fatalf("visitor 2 after visitor 1 logout = %d %s", rec.Code, rec.Body.String())
	}
}
