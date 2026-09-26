package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPGOAuthStoreSurvivesTableRename(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	schema := fmt.Sprintf("oauth_rename_%d", time.Now().UnixNano())
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(t.Context()), "DROP SCHEMA "+schema+" CASCADE")
		pool.Close()
	})
	exec := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	exec("CREATE SCHEMA " + schema)
	apply := func(name string, down bool) {
		t.Helper()
		raw, err := migrations.FS.ReadFile("sql/" + name + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		up, rollback, _ := strings.Cut(string(raw), "-- +goose Down")
		sql := up
		if down {
			sql = rollback
		}
		exec("BEGIN;" + strings.NewReplacer("public.", schema+".", "'public'", "'"+schema+"'").Replace(sql) + ";COMMIT")
	}
	apply("126_oauth_session", false)
	exec(`CREATE TABLE users(id integer PRIMARY KEY); INSERT INTO users VALUES(1);
 CREATE TABLE playback_history_admin(session_id text PRIMARY KEY,user_id integer REFERENCES users(id) ON DELETE CASCADE,started_at timestamptz,ended_at timestamptz,profile_id text);
 CREATE INDEX idx_playback_history_admin_ended ON playback_history_admin(ended_at);
 CREATE INDEX idx_playback_history_admin_started ON playback_history_admin(started_at);
 CREATE INDEX idx_playback_history_admin_user_ended ON playback_history_admin(user_id,ended_at);
 CREATE INDEX idx_playback_history_admin_user_profile_ended ON playback_history_admin(user_id,profile_id,ended_at);
 INSERT INTO playback_history_admin(session_id,user_id) VALUES('preserved',1);`)
	store := NewPGOAuthStore(pool, []byte("synthetic-migration-secret"))
	completion := OAuthCompletion{Code: "legacy-code", AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh", ExpiresIn: 900, ExpiresAt: time.Now().Add(time.Hour), NextURL: "/me"}
	hash := oauthCompletionCodeHash(completion.Code)
	ciphertext, err := store.encryptCompletionTokens(completion, hash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), `INSERT INTO oauth_completion(code_hash,token_ciphertext,expires_in,next_url,expires_at) VALUES($1,$2,$3,$4,$5)`, hash, ciphertext, completion.ExpiresIn, completion.NextURL, completion.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO oauth_session(state,install_id,redirect_uri,expires_at) VALUES('legacy-state','1','https://example.test/callback',now()+interval '1 hour')`)
	for _, down := range []bool{false, true, false} {
		apply("20260912231314_rename_tables_for_consistency", down)
		name := "admin_playback_history"
		if down {
			name = "playback_history_admin"
		}
		var count int
		if err = pool.QueryRow(t.Context(), "SELECT count(*) FROM "+name+" WHERE session_id='preserved'").Scan(&count); err != nil || count != 1 {
			t.Fatalf("playback row lost: %d %v", count, err)
		}
		if err = pool.QueryRow(t.Context(), "SELECT count(*) FROM pg_constraint WHERE conrelid=$1::regclass AND conname=$2", name, name+"_pkey").Scan(&count); err != nil || count != 1 {
			t.Fatalf("primary-key rename failed: %d %v", count, err)
		}
	}
	out, err := store.GetAndDeleteCompletion(t.Context(), completion.Code)
	if err != nil || out.AccessToken != completion.AccessToken || out.RefreshToken != completion.RefreshToken {
		t.Fatalf("legacy completion did not survive: %v", err)
	}
	if _, err = store.GetAndDeleteCompletion(t.Context(), completion.Code); !errors.Is(err, ErrOAuthCompletionNotFound) {
		t.Fatalf("completion reused: %v", err)
	}
	sess, err := store.GetAndDelete(t.Context(), "legacy-state")
	if err != nil || sess.InstallID != "1" {
		t.Fatalf("legacy session did not survive: %v", err)
	}
	sess.State = "new-state"
	sess.ExpiresAt = time.Now().Add(-time.Hour)
	if err = store.Insert(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	if count, err := store.DeleteExpired(t.Context(), time.Now()); err != nil || count != 1 {
		t.Fatalf("session expiry: %d %v", count, err)
	}
	completion.Code = "new-code"
	completion.ExpiresAt = time.Now().Add(-time.Hour)
	if err = store.InsertCompletion(t.Context(), completion); err != nil {
		t.Fatal(err)
	}
	if count, err := store.DeleteExpiredCompletions(t.Context(), time.Now()); err != nil || count != 1 {
		t.Fatalf("completion expiry: %d %v", count, err)
	}
	exec("DELETE FROM users WHERE id=1")
	var count int
	if err = pool.QueryRow(t.Context(), "SELECT count(*) FROM admin_playback_history").Scan(&count); err != nil || count != 0 {
		t.Fatalf("FK cascade lost: %d %v", count, err)
	}
}
