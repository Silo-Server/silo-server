package apiv2

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// keyRoutesDB serves the key and pre-approval routes through the real
// services, with one account holding a session of each kind, by token:
// tok-phone (password), tok-tv (device sign-in), tok-app (another app's
// grant-enrolled key), tok-old (no record of how it was opened) and
// tok-acting (an administrator acting as the account).
type keyRoutesDB struct {
	pool    *pgxpool.Pool
	handler http.Handler
	devices *auth.DeviceLoginService
	userID  int
	claims  map[string]*auth.Claims
}

// newKeyRoutesDB builds it; with changes the dependencies before the
// handler is built.
func newKeyRoutesDB(t *testing.T, with ...func(*Dependencies)) *keyRoutesDB {
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
	name := "keylist-" + uuid.NewString()[:8]
	var userID, adminID int
	for _, u := range []struct {
		id   *int
		name string
		role string
	}{{&userID, name, "user"}, {&adminID, name + "-admin", "admin"}} {
		if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role, permissions)
			VALUES ($1, $2, 'x', $3, '{}') RETURNING id`, u.name, u.name+"@example.test", u.role).Scan(u.id); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanup := context.WithoutCancel(ctx)
		_, _ = pool.Exec(cleanup, `DELETE FROM device_login_requests WHERE approved_by_user_id = $1`, userID)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = ANY($1)`, []int{userID, adminID})
	})
	publicKey := make([]byte, 65)
	if _, err := rand.Read(publicKey); err != nil {
		t.Fatal(err)
	}
	grantKey := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO auth_device_keys (id, user_id, public_key, key_id, kind, name, enrolled_via)
		VALUES ($1::uuid, $2, $3, $4, 'app', 'Other app', $5)`, grantKey, userID, publicKey, "key-"+grantKey, models.AppKeyEnrolledGrant); err != nil {
		t.Fatal(err)
	}

	claims := map[string]*auth.Claims{}
	session := func(token, openedVia string, deviceKey *string, impersonator *int) {
		id := uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions (id, user_id, device_name, expires_at, opened_via, device_key_id)
			VALUES ($1, $2, 'test', $3, NULLIF($4, ''), $5::uuid)`, id, userID, time.Now().Add(time.Hour), openedVia, deviceKey); err != nil {
			t.Fatal(err)
		}
		claims[token] = &auth.Claims{UserID: userID, Role: "user", SessionID: id, TokenType: auth.TokenTypeAccess, ImpersonatorUserID: impersonator}
	}
	session("tok-phone", models.SessionOpenedPassword, nil, nil)
	session("tok-tv", models.SessionOpenedDeviceLogin, nil, nil)
	session("tok-app", models.SessionOpenedAppKey, &grantKey, nil)
	session("tok-old", "", nil, nil)
	session("tok-acting", models.SessionOpenedPassword, nil, &adminID)

	jwt := auth.NewJWTService("synthetic-key-routes-secret", time.Minute, time.Hour)
	sessions := auth.NewSessionRepository(pool)
	devices := auth.NewDeviceLoginService(pool, auth.NewUserRepository(pool), jwt, sessions, nil, nil)
	handler := handlers.NewAuthHandler(nil, jwt, devices)
	handler.SetAppKeySignIn(auth.NewAppKeySignIn(pool, nil))
	deps := appKeyDeps(&fakeAppKeys{})
	deps.AppKeys = handler
	deps.Devices = handler
	// The real session lookup, so the claim saying another app's key opened
	// the session comes from the rows, as in production.
	deps.Auth = apimw.NewAuthMiddleware(fakeTokens{claims}, sessions, fakeAPIKeys{},
		fakeUsers{map[int]*models.User{userID: {ID: userID, Role: "user", Enabled: true}}})
	for _, change := range with {
		change(&deps)
	}
	return &keyRoutesDB{pool: pool, handler: NewHandler(deps), devices: devices, userID: userID, claims: claims}
}

// TestKeyRoutesRefuseSessionsDB: the key list, the list of devices waiting
// for approval and the device sign-ins approved in advance name the devices
// the person signed in (and where each key was enrolled from), so only a
// session the person opened by password (or provider, or their own key)
// lists them or withdraws one. Through the real services: a TV's session
// (device sign-in), a session another app's key opened, a session with no
// record of how it was opened, and an administrator acting as the account
// are each 403 permission_denied, and the refused withdraw leaves the
// approval waiting. Regression: the pre-approval routes checked only that
// the caller was the account's own login session, so a shared TV listed and
// withdrew them; and the key list would lose its refusal of an
// administrator acting as the account unnoticed, pinned only below the
// route.
func TestKeyRoutesRefuseSessionsDB(t *testing.T) {
	d := newKeyRoutesDB(t)
	ctx, pool, h, devices, userID, claims := t.Context(), d.pool, d.handler, d.devices, d.userID, d.claims
	// A sign-in approved in advance from the password session, for the
	// withdraw to aim at.
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	binding := sha256.Sum256(secret)
	waiting, err := devices.Preapprove(auth.WithClaims(ctx, claims["tok-phone"]), auth.DevicePreapprovalInput{
		DeviceName: "Living room TV", Binding: base64.RawURLEncoding.EncodeToString(binding[:]),
	}, userID)
	if err != nil {
		t.Fatal(err)
	}
	status := func() string {
		t.Helper()
		var s string
		if err := pool.QueryRow(ctx, `SELECT status FROM device_login_requests WHERE id = $1`, waiting.ID).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	routes := []struct{ method, path string }{
		{http.MethodGet, Prefix + "/auth/keys"},
		{http.MethodGet, Prefix + "/auth/keys/pending"},
		{http.MethodGet, Prefix + "/auth/device/preapprovals"},
		{http.MethodDelete, Prefix + "/auth/device/preapprovals/" + waiting.ID},
	}
	for _, token := range []string{"tok-tv", "tok-app", "tok-old", "tok-acting"} {
		for _, r := range routes {
			requireProblem(t, do(t, h, r.method, r.path, "", bearer(token)), TypePermissionDenied)
		}
		if got := status(); got != auth.DeviceLoginStatusApproved {
			t.Fatalf("%s: a refused withdraw left the approval %s", token, got)
		}
	}
	// The key list tells the administrator what is true for them.
	if p := requireProblem(t, do(t, h, http.MethodGet, Prefix+"/auth/keys", "", bearer("tok-acting")), TypePermissionDenied); p.Detail != handlers.ActingAsAccountDetail {
		t.Fatalf("acting-as detail = %q", p.Detail)
	}
	for _, r := range routes {
		rec := do(t, h, r.method, r.path, "", bearer("tok-phone"))
		if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
			t.Fatalf("password session %s %s: status %d: %s", r.method, r.path, rec.Code, rec.Body.String())
		}
	}
	if got := status(); got != auth.DeviceLoginStatusDenied {
		t.Fatalf("the password session's withdraw left the approval %s", got)
	}
}

// The key list carries each key's enrolled_ip_address as stored, and an
// empty one for a key with none (every key enrolled before the server
// recorded it). Regression: the list named a key only by what the device
// called itself and when it was added.
func TestKeyListCarriesEnrolledAddressDB(t *testing.T) {
	d := newKeyRoutesDB(t)
	read := func() string {
		t.Helper()
		rec := do(t, d.handler, http.MethodGet, Prefix+"/auth/keys", "", bearer("tok-phone"))
		if rec.Code != http.StatusOK {
			t.Fatalf("list: status %d: %s", rec.Code, rec.Body.String())
		}
		var list AppKeyList
		if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Devices) != 1 {
			t.Fatalf("list = %s (%v)", rec.Body.String(), err)
		}
		if !strings.Contains(rec.Body.String(), `"enrolled_ip_address":`) {
			t.Fatalf("the list has no enrolled_ip_address member: %s", rec.Body.String())
		}
		return list.Devices[0].EnrolledIPAddress
	}
	if got := read(); got != "" {
		t.Fatalf("a key with no stored address lists %q", got)
	}
	if _, err := d.pool.Exec(t.Context(), `UPDATE auth_device_keys SET enrolled_ip_address = '2001:db8::7' WHERE user_id = $1`, d.userID); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "2001:db8::7" {
		t.Fatalf("enrolled_ip_address = %q", got)
	}
}
