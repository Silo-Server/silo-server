package apiv2

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// The key list names every device the person signed in, so a TV's session
// (opened by device sign-in) is refused it through the real service, while
// a session the person opened by password reads it.
func TestKeyListRefusesDeviceSignInSessionDB(t *testing.T) {
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
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role, permissions)
		VALUES ($1, $2, 'x', 'user', '{}') RETURNING id`, name, name+"@example.test").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE id = $1`, userID) })
	claims, roles := map[string]*auth.Claims{}, map[string]string{}
	for token, via := range map[string]string{"tok-tv": models.SessionOpenedDeviceLogin, "tok-phone": models.SessionOpenedPassword} {
		id := uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions (id, user_id, device_name, expires_at, opened_via)
			VALUES ($1, $2, 'test', $3, $4)`, id, userID, time.Now().Add(time.Hour), via); err != nil {
			t.Fatal(err)
		}
		claims[token] = &auth.Claims{UserID: userID, Role: "user", SessionID: id, TokenType: auth.TokenTypeAccess}
		roles[id] = "user"
	}
	keys := handlers.NewAuthHandler(nil, nil, nil)
	keys.SetAppKeySignIn(auth.NewAppKeySignIn(pool, nil))
	deps := appKeyDeps(&fakeAppKeys{})
	deps.AppKeys = keys
	deps.Auth = apimw.NewAuthMiddleware(fakeTokens{claims}, fakeSessions{roles}, fakeAPIKeys{},
		fakeUsers{map[int]*models.User{userID: {ID: userID, Role: "user", Enabled: true}}})
	h := NewHandler(deps)
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/auth/keys", "", bearer("tok-tv")), TypePermissionDenied)
	if rec := do(t, h, http.MethodGet, Prefix+"/auth/keys", "", bearer("tok-phone")); rec.Code != http.StatusOK {
		t.Fatalf("password session: status %d: %s", rec.Code, rec.Body.String())
	}
}
