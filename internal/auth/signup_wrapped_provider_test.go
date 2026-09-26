package auth_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestInvitedAccountWrappedPostgresDB(t *testing.T) {
	ctx := t.Context()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("signup_wrapper_%d", time.Now().UnixNano())
	q := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+q); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.Exec(context.WithoutCancel(ctx), "DROP SCHEMA "+q+" CASCADE") }()
	for _, table := range []string{"users", "invite_codes", "user_profiles", "user_profile_allowed_libraries"} {
		if _, err = admin.Exec(ctx, "CREATE TABLE "+q+"."+table+" (LIKE public."+table+" INCLUDING ALL EXCLUDING IDENTITY)"); err != nil {
			t.Fatal(err)
		}
	}
	// Own sequences and the real profile ownership FK stay entirely in this schema.
	for _, table := range []string{"users", "invite_codes"} {
		seq := q + "." + table + "_fixture_seq"
		if _, err = admin.Exec(ctx, "CREATE SEQUENCE "+seq+"; ALTER TABLE "+q+"."+table+" ALTER COLUMN id SET DEFAULT nextval('"+seq+"')"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = admin.Exec(ctx, "ALTER TABLE "+q+".user_profiles ADD FOREIGN KEY(user_id) REFERENCES "+q+".users(id)"); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "1500"
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var creator int
	if err = pool.QueryRow(ctx, `INSERT INTO users(username,email,password_hash,role,enabled) VALUES('creator','creator@example.invalid','x','admin',true) RETURNING id`).Scan(&creator); err != nil {
		t.Fatal(err)
	}
	for _, wrapped := range []bool{false, true} {
		name := fmt.Sprintf("account-%v", wrapped)
		code := fmt.Sprintf("invite-%v", wrapped)
		if _, err = pool.Exec(ctx, `INSERT INTO invite_codes(code,max_uses,created_by) VALUES($1,1,$2)`, code, creator); err != nil {
			t.Fatal(err)
		}
		var provider userstore.UserStoreProvider = pgstore.NewPostgresProvider(pool)
		if wrapped {
			provider = notifications.WrapUserStoreProvider(provider, &notifications.System{})
		}
		accounts := auth.NewAccountProvisioner(auth.NewUserRepository(pool), provider)
		_, err = accounts.CreateInvitedAccount(ctx, auth.CreateAccountInput{User: models.CreateUserInput{Username: name, Email: name + "@example.invalid", Password: "test-password", Role: "user"}, DefaultProfile: auth.DefaultProfileOptions{Enabled: true, Name: "Home"}}, code)
		var users, profiles, uses int
		if e := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE username=$1`, name).Scan(&users); e != nil {
			t.Fatal(e)
		}
		if e := pool.QueryRow(ctx, `SELECT count(*) FROM user_profiles p JOIN users u ON u.id=p.user_id WHERE u.username=$1`, name).Scan(&profiles); e != nil {
			t.Fatal(e)
		}
		if e := pool.QueryRow(ctx, `SELECT use_count FROM invite_codes WHERE code=$1`, code).Scan(&uses); e != nil {
			t.Fatal(e)
		}
		t.Logf("wrapped=%v err=%v users=%d profiles=%d inviteUses=%d", wrapped, err, users, profiles, uses)
		if err != nil || users != 1 || profiles != 1 || uses != 1 {
			t.Errorf("production provider must provision account/profile with one invite use")
		}
	}
}
