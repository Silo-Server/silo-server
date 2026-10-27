package pgstore

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// TestListAudioPreferencesMatchesGet: the batched read returns, for each id,
// exactly what GetAudioPreference returns, and leaves out ids without a
// stored preference.
func TestListAudioPreferencesMatchesGet(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
		fmt.Sprintf("audio-prefs-batch-%d", time.Now().UnixNano())).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM user_audio_preferences WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM user_profiles WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
	})
	store := newStore(pool, userID)
	for _, profileID := range []string{"main", "other"} {
		if err := store.CreateProfile(ctx, userstore.Profile{ID: profileID, Name: profileID}); err != nil {
			t.Fatalf("CreateProfile(%s): %v", profileID, err)
		}
	}
	for _, pref := range []userstore.AudioPreference{
		{ProfileID: "main", SeriesID: "movie-a", AudioTrackIndex: 1, AudioLanguage: "jpn",
			TrackSignature: &userstore.AudioTrackSignature{Language: "jpn", Codec: "aac", Channels: 2}},
		{ProfileID: "main", SeriesID: "series-b", AudioTrackIndex: 0, AudioLanguage: "eng"},
		// Another profile's choice for the same item must not leak in.
		{ProfileID: "other", SeriesID: "movie-c", AudioTrackIndex: 2, AudioLanguage: "fra"},
	} {
		if err := store.SetAudioPreference(ctx, pref); err != nil {
			t.Fatalf("SetAudioPreference(%+v): %v", pref, err)
		}
	}

	ids := []string{"movie-a", "series-b", "movie-c"}
	got, err := store.ListAudioPreferences(ctx, "main", ids)
	if err != nil {
		t.Fatalf("ListAudioPreferences: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListAudioPreferences returned %d preferences, want 2: %+v", len(got), got)
	}
	for _, id := range ids {
		want, err := store.GetAudioPreference(ctx, "main", id)
		if err != nil {
			t.Fatalf("GetAudioPreference(%s): %v", id, err)
		}
		if !reflect.DeepEqual(got[id], want) {
			t.Errorf("%s: batched %+v, single %+v", id, got[id], want)
		}
	}
}
