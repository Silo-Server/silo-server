package jellycompat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/profileedits"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// TestProfileEditActorThroughJellyfinRouterDB: a Jellyfin-compatible app's
// configuration save, through the real router, is recorded as the session
// account's edit, and saving the same configuration again records nothing.
// It catches the router's edits middleware or the session's actor going
// missing, which silently stops recording, and an echoed value counted as an
// edit.
func TestProfileEditActorThroughJellyfinRouterDB(t *testing.T) {
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
	name := fmt.Sprintf("edits-compat-%d", time.Now().UnixNano())
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role) VALUES ($1, $2, 'x', 'user') RETURNING id`,
		name, name+"@example.invalid").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM profile_set_edits WHERE user_id = $1`, userID)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1`, userID)
	})
	provider := pgstore.NewPostgresProvider(pool)
	store, err := provider.ForUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	profileID := pgstore.NewProfileID()
	if err := store.CreateProfile(ctx, userstore.Profile{ID: profileID, Name: "Main", IsPrimary: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE user_profiles SET sync_id = gen_random_uuid(), sync_position = 0 WHERE user_id = $1 AND id = $2`,
		userID, profileID); err != nil {
		t.Fatal(err)
	}
	sessions := NewSessionStore(time.Hour, nil)
	if err := sessions.Put(Session{Token: "compat-token", StreamAppUserID: userID, ProfileID: profileID}); err != nil {
		t.Fatal(err)
	}
	router := NewRouter(Dependencies{Config: &config.Config{}, SessionStore: sessions, UserStoreProvider: provider})

	save := func(t *testing.T, body string) []string {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/Users/Configuration", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Emby-Token", "compat-token")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		edits, err := profileedits.List(ctx, pool, userID)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range edits {
			out = append(out, fmt.Sprintf("%s:%s:%d", e.By, e.Field, e.ActorUserID))
		}
		return out
	}
	// An unset language echoed as empty and the default autoplay are no
	// change; a chosen language is the account's edit.
	body := `{"AudioLanguagePreference":"fra","SubtitleLanguagePreference":"","EnableNextEpisodeAutoPlay":true}`
	want := fmt.Sprintf("account:settings.%s:%d", settingskeys.PlaybackAudioLanguage, userID)
	if got := save(t, body); len(got) != 1 || got[0] != want {
		t.Fatalf("after the first save, edits = %v, want [%s]", got, want)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM profile_set_edits WHERE user_id = $1`, userID); err != nil {
		t.Fatal(err)
	}
	if got := save(t, body); len(got) != 0 {
		t.Fatalf("saving the same configuration again recorded %v", got)
	}
}
