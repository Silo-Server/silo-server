package jellycompat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// TestPasswordlessAccountSignsInWithQuickConnectDB: an account with no
// password (password_hash NULL, local password sign-in off, as an account
// that signs in with app keys has) signs a Jellyfin app in with Quick
// Connect approved by one of its own login sessions, and the user it gets
// says it has no password, while a password account's user says it has one.
// A failed password sign-in answers the same bytes for a name with no
// account, a wrong password and the passwordless account, so the answer does
// not tell a stranger which names exist or have a password.
func TestPasswordlessAccountSignsInWithQuickConnectDB(t *testing.T) {
	pool, suffix := jellyfin12CompatPool(t)
	ctx := context.Background()
	users := auth.NewUserRepository(pool)
	sessions := auth.NewSessionRepository(pool)
	jwt := auth.NewJWTService("jellycompat-passwordless-test-secret-0123", time.Minute, time.Hour)
	svc := auth.NewService(auth.NewLocalProvider(users, sessions), jwt, sessions, users, nil, nil, nil)
	stores := pgstore.NewPostgresProvider(pool)
	devices := auth.NewDeviceLoginService(pool, users, jwt, sessions, stores, nil)
	const password = "correct horse battery"

	account := func(username string) (*models.User, string) {
		t.Helper()
		user, err := users.Create(ctx, models.CreateUserInput{Username: username, Email: username + "@example.test",
			Password: password, Role: models.RoleUser})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanup := context.WithoutCancel(ctx)
			_, _ = pool.Exec(cleanup, `DELETE FROM device_login_requests WHERE approved_by_user_id = $1`, user.ID)
			_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = $1`, user.ID)
		})
		store, err := stores.ForUser(ctx, user.ID)
		if err != nil {
			t.Fatal(err)
		}
		profileID := uuid.NewString()
		if err := store.CreateProfile(ctx, userstore.Profile{ID: profileID, Name: "Main", IsPrimary: true}); err != nil {
			t.Fatal(err)
		}
		return user, profileID
	}
	withPassword, _ := account(fmt.Sprintf("jfpw-%d", suffix))
	keysOnly, keysOnlyProfile := account(fmt.Sprintf("jfnopw-%d", suffix))
	jellyfin12Exec(t, pool, `UPDATE users SET password_hash = NULL, local_password_login_enabled = false WHERE id = $1`, keysOnly.ID)

	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouter(Dependencies{Config: cfg, SessionStore: NewSessionStore(time.Hour, nil), AuthService: svc,
		UserStoreProvider: stores, DeviceLogin: devices})
	header := map[string]string{"Content-Type": "application/json", "Authorization": quickConnectAuthHeader}
	signIn := func(name, pw string) (int, []byte) {
		body, _ := json.Marshal(map[string]string{"Username": name, "Pw": pw})
		rec := serveQuickConnect(router, http.MethodPost, "/Users/AuthenticateByName", string(body), header)
		return rec.Code, rec.Body.Bytes()
	}
	userOf := func(status int, body []byte) authenticateByNameResponse {
		t.Helper()
		var out authenticateByNameResponse
		if status != http.StatusOK || json.Unmarshal(body, &out) != nil {
			t.Fatalf("sign-in = %d %s", status, body)
		}
		return out
	}

	if out := userOf(signIn(withPassword.Username, password)); !out.User.HasPassword || !out.User.HasConfiguredPassword {
		t.Fatalf("password account: HasPassword = %v, HasConfiguredPassword = %v, want true", out.User.HasPassword, out.User.HasConfiguredPassword)
	}

	refusedStatus, refused := signIn(fmt.Sprintf("jfnobody-%d", suffix), password)
	if refusedStatus != http.StatusUnauthorized || !bytes.Contains(refused, []byte("Quick Connect")) {
		t.Fatalf("unknown name = %d %s, want 401 naming Quick Connect", refusedStatus, refused)
	}
	for name, pw := range map[string]string{withPassword.Username: "wrong password", keysOnly.Username: password} {
		if status, body := signIn(name, pw); status != refusedStatus || !bytes.Equal(body, refused) {
			t.Fatalf("%s = %d %s, want the unknown name's %d %s", name, status, body, refusedStatus, refused)
		}
	}

	start := decodeQuickConnectResult(t, serveQuickConnect(router, http.MethodPost, "/QuickConnect/Initiate", "", header))
	approver := models.AuthSession{ID: uuid.NewString(), UserID: keysOnly.ID, DeviceName: "approver", ExpiresAt: time.Now().Add(time.Hour)}
	if err := sessions.Create(ctx, approver); err != nil {
		t.Fatal(err)
	}
	accessToken, err := jwt.GenerateAccessToken(keysOnly.ID, keysOnly.Role, approver.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := devices.ApproveQuickConnectAsSession(ctx, start.Code, accessToken, keysOnlyProfile); err != nil {
		t.Fatalf("approve: %v", err)
	}
	rec := serveQuickConnect(router, http.MethodPost, "/Users/AuthenticateWithQuickConnect", `{"Secret":"`+start.Secret+`"}`, header)
	out := userOf(rec.Code, rec.Body.Bytes())
	if out.User.HasPassword || out.User.HasConfiguredPassword {
		t.Fatalf("passwordless account: HasPassword = %v, HasConfiguredPassword = %v, want false", out.User.HasPassword, out.User.HasConfiguredPassword)
	}
	me := serveQuickConnect(router, http.MethodGet, "/Users/Me", "", map[string]string{"X-Emby-Token": out.AccessToken})
	if me.Code != http.StatusOK || !strings.Contains(me.Body.String(), `"HasPassword":false`) {
		t.Fatalf("/Users/Me = %d %s", me.Code, me.Body.String())
	}
}
