package apiv2

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/signinlinks"
)

// publicURLSettings answers the server's public URL, which sign-in links are
// built on.
type publicURLSettings struct{}

func (publicURLSettings) Get(_ context.Context, key string) (string, error) {
	if key == "server.public_url" {
		return "https://silo.example.test", nil
	}
	return "", nil
}

// signInLinkSchema is a private schema with the tables making a link
// touches, so the Owner rows don't meet another test's accounts.
func signInLinkSchema(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "sign_in_links_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.WithoutCancel(t.Context()), "DROP SCHEMA "+quoted+" CASCADE") })
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, table := range []string{"users", "access_groups", "auth_sessions", "api_keys", "abs_sessions", "jellycompat_sessions",
		"device_login_requests", "user_profiles", "activity_log", "auth_sign_in_links", "auth_app_codes", "auth_device_keys", "auth_person_keys"} {
		if _, err := pool.Exec(t.Context(), "CREATE TABLE "+table+" (LIKE public."+table+" INCLUDING ALL)"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(t.Context(), `ALTER TABLE users ALTER COLUMN id DROP IDENTITY IF EXISTS;
		CREATE SEQUENCE link_user_ids;
		ALTER TABLE users ALTER COLUMN id SET DEFAULT nextval('link_user_ids');
		CREATE TRIGGER api_key_configuration_revision BEFORE INSERT OR UPDATE ON api_keys FOR EACH ROW EXECUTE FUNCTION public.advance_api_key_configuration_revision()`); err != nil {
		t.Fatal(err)
	}
	return pool
}

// Who may make a sign-in link, through the route: exactly who may set that
// account's password as an administrator. Regression guard: a link lets its
// holder in as surely as a new password, so a looser rule here would be a
// quieter way into an account than the one that already exists.
func TestCreateSignInLinkAuthorityDB(t *testing.T) {
	pool := signInLinkSchema(t)
	ctx := t.Context()
	users := auth.NewUserRepository(pool)
	sessions := auth.NewSessionRepository(pool)
	jwt := auth.NewJWTService("synthetic-sign-in-link-jwt-secret", time.Minute, time.Hour)
	login := auth.NewService(auth.NewLocalProvider(users, sessions), jwt, sessions, users, nil, nil, nil)
	keys := auth.NewAPIKeyRepository(pool)
	links := signinlinks.NewService(auth.NewAppKeySignIn(pool, login), users, nil, publicURLSettings{}, nil)
	deps := parityDeps(false)
	deps.Auth = apimw.NewAuthMiddleware(jwt, sessions, keys, users)
	deps.SignInLinks = handlers.NewSignInLinkHandler(links)
	router := NewHandler(deps)

	const password = "a sign-in link test password"
	account := func(label, role string) *models.User {
		t.Helper()
		user, err := users.Create(ctx, models.CreateUserInput{Username: label, Email: label + "@example.test", Password: password, Role: role})
		if err != nil {
			t.Fatal(err)
		}
		return user
	}
	token := func(user *models.User) string {
		t.Helper()
		pair, _, err := login.Login(ctx, user.Username, password, "test", "")
		if err != nil {
			t.Fatal(err)
		}
		return pair.AccessToken
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	owner, admin, other := account("owner", models.RoleAdmin), account("admin", models.RoleAdmin), account("other-admin", models.RoleAdmin)
	selfOff, member, providerMember := account("admin-self-off", models.RoleAdmin), account("member", models.RoleUser), account("provider-member", models.RoleUser)
	exec(`UPDATE users SET is_owner = true WHERE id = $1`, owner.ID)
	ownerToken, adminToken, selfOffToken := token(owner), token(admin), token(selfOff)
	// Linking a sign-in provider turns the account's own password sign-in
	// off; an administrator whose own sign-in is off stays signed in.
	exec(`UPDATE users SET local_password_login_enabled = false WHERE id = ANY($1)`, []int{selfOff.ID, providerMember.ID})
	scoped, err := keys.Create(ctx, owner.ID, "integration", []string{auth.ScopeAdminUsers})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		as     string
		target *models.User
		status int
		want   ProblemType
	}{
		{"the Owner for another administrator", ownerToken, admin, http.StatusCreated, ProblemType{}},
		{"an administrator for themselves", adminToken, admin, http.StatusCreated, ProblemType{}},
		{"an administrator for another administrator", adminToken, other, http.StatusForbidden, TypePermissionDenied},
		{"an administrator for the Owner", adminToken, owner, http.StatusForbidden, TypePermissionDenied},
		{"the Owner for themselves", ownerToken, owner, http.StatusCreated, ProblemType{}},
		{"a scoped API key for an administrator", scoped.Key, other, http.StatusForbidden, TypePermissionDenied},
		{"an administrator for themselves with their own password sign-in off", selfOffToken, selfOff, http.StatusForbidden, TypePermissionDenied},
		{"an administrator for an ordinary account", adminToken, member, http.StatusCreated, ProblemType{}},
		{"an administrator for an ordinary account its sign-in provider manages", adminToken, providerMember, http.StatusCreated, ProblemType{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, router, http.MethodPost, fmt.Sprintf("%s/admin/users/%d/sign-in-links", Prefix, tc.target.ID), `{"send_email":false}`, bearer(tc.as))
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			if tc.status != http.StatusCreated {
				requireProblem(t, rec, tc.want)
				return
			}
			if !strings.HasPrefix(rec.Body.String(), `{"url":"https://silo.example.test/sign-in-link#`) {
				t.Fatalf("answer %s: the link is not on the public URL", rec.Body)
			}
		})
	}
}
