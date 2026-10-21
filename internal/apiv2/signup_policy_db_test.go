package apiv2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
)

// Model a preflight read that won before the administrator's disable. The
// durable transaction must judge the current database policy instead.
type staleSignupSettings struct{}

func (staleSignupSettings) Get(context.Context, string) (string, error) { return "true", nil }

func TestSignupCommitPolicyErrorsDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "signup_policy_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.WithoutCancel(ctx), "DROP SCHEMA "+quoted+" CASCADE") })
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, table := range []string{"users", "invite_codes", "auth_sessions", "server_settings", "user_profiles"} {
		if _, err := pool.Exec(ctx, "CREATE TABLE "+table+" (LIKE public."+table+" INCLUDING ALL)"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO server_settings (key,value) VALUES ('signup.enabled','false')`); err != nil {
		t.Fatal(err)
	}
	var creatorID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username,email,password_hash,role)
		VALUES ('inviter','inviter@example.test','synthetic-hash','admin') RETURNING id`).Scan(&creatorID); err != nil {
		t.Fatal(err)
	}
	users, sessions := auth.NewUserRepository(pool), auth.NewSessionRepository(pool)
	jwt := auth.NewJWTService("synthetic-signup-mapping-secret", time.Minute, time.Hour)
	svc := auth.NewService(auth.NewLocalProvider(users, sessions), jwt, sessions, users, auth.NewInviteCodeRepository(pool), staleSignupSettings{}, nil)
	registration := handlers.NewAuthHandler(svc, jwt, nil)
	deps := parityDeps(false)
	deps.Sessions = registration
	router := chi.NewRouter()
	router.Post("/api/v1/auth/signup", registration.HandleSignup)
	router.Handle(Prefix+"/*", NewHandler(deps))
	for _, version := range []string{"v1", "v2"} {
		t.Run(version, func(t *testing.T) {
			name, code := "signup-"+version, "invite-"+version
			if _, err := pool.Exec(ctx, `INSERT INTO invite_codes (code,max_uses,created_by) VALUES ($1,1,$2)`, code, creatorID); err != nil {
				t.Fatal(err)
			}
			body := fmt.Sprintf(`{"username":%q,"email":%q,"password":"synthetic-password","invite_code":%q}`, name, name+"@example.test", code)
			rec := do(t, router, http.MethodPost, "/api/"+version+"/auth/signup", body, nil)
			if rec.Code != http.StatusForbidden {
				t.Errorf("stale preflight signup status = %d, want 403", rec.Code)
			} else if version == "v1" {
				var response struct {
					Error string `json:"error"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.Error != "signup_disabled" {
					t.Fatalf("signup denial = %s (%v)", rec.Body, err)
				}
			} else {
				requireProblem(t, rec, TypePermissionDenied)
			}
			var accounts, profiles, uses int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE username=$1`, name).Scan(&accounts); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_profiles`).Scan(&profiles); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `SELECT use_count FROM invite_codes WHERE code=$1`, code).Scan(&uses); err != nil {
				t.Fatal(err)
			}
			t.Logf("status=%d accounts=%d profiles=%d invite uses=%d", rec.Code, accounts, profiles, uses)
			if accounts != 0 || profiles != 0 || uses != 0 {
				t.Fatalf("refused signup persisted accounts=%d profiles=%d invite uses=%d", accounts, profiles, uses)
			}
		})
	}
}
