package metadata

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog/reattribute"
	"github.com/Silo-Server/silo-server/internal/recommendations"
)

// A merge that moves a profile's watch state onto the surviving item marks
// that profile's recommendations stale; a merge that moves no user state
// marks none.
func TestMergeMarksMovedProfilesStalePostgres(t *testing.T) {
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

	prefix := "merge-recs-" + uuid.NewString()[:8] + "-"
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username, role) VALUES($1, 'user') RETURNING id`, prefix+"user").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
	})
	for _, item := range []struct{ id, status string }{
		{"admin-source", "matched"}, {"admin-target", "matched"},
		{"empty-source", "matched"}, {"empty-target", "matched"},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO media_items(content_id, type, title, status) VALUES($1, 'movie', $1, $2)`, prefix+item.id, item.status); err != nil {
			t.Fatal(err)
		}
	}
	var dims int
	if err := pool.QueryRow(ctx, `
		SELECT atttypmod FROM pg_attribute
		WHERE attrelid = 'public.user_taste_profiles'::regclass AND attname = 'embedding'`).Scan(&dims); err != nil {
		t.Fatal(err)
	}
	recs := recommendations.NewRepo(pool)
	for _, profile := range []string{"admin-merge", "untouched"} {
		if err := recs.UpsertTasteProfile(ctx, userID, profile, make([]float32, dims), map[string]int{}, "", time.Now().Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_watch_progress(user_id, profile_id, media_item_id, completed) VALUES($1, 'admin-merge', $2, true)`, userID, prefix+"admin-source"); err != nil {
		t.Fatal(err)
	}
	stale := func() map[string]bool {
		t.Helper()
		profiles, err := recs.GetStaleProfiles(ctx, 10000)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, p := range profiles {
			if p.UserID == userID {
				got[p.ProfileID] = true
			}
		}
		return got
	}

	s := &MetadataService{dbPool: pool}
	s.SetRecommendationStaler(recs)
	if err := s.rebindItemToExistingItem(ctx, prefix+"empty-source", prefix+"empty-target", true); err != nil {
		t.Fatal(err)
	}
	if got := stale(); len(got) != 0 {
		t.Fatalf("stale after a merge that moved no user state = %v, want none", got)
	}
	if err := s.rebindItemToExistingItem(ctx, prefix+"admin-source", prefix+"admin-target", true); err != nil {
		t.Fatal(err)
	}
	if got := stale(); len(got) != 1 || !got["admin-merge"] {
		t.Fatalf("stale after the merge = %v, want only admin-merge", got)
	}
}

func TestMovedStateTargets(t *testing.T) {
	pairs := []reattribute.IDPair{{From: "old-e1", To: "new-e1"}}
	if got := MovedStateTargets(&reattribute.Report{HistoryMoved: 1}, "target", pairs); len(got) != 2 || got[0] != "target" || got[1] != "new-e1" {
		t.Fatalf("targets after a move = %v, want the target and its moved episode", got)
	}
	for name, report := range map[string]*reattribute.Report{
		"nil":                    nil,
		"only downloads moved":   {Downloads: 3, PlaybackSessionLog: 2},
		"only ambiguous history": {HistoryAmbiguous: 1, HistoryStayed: 4},
	} {
		if got := MovedStateTargets(report, "target", pairs); len(got) != 0 {
			t.Fatalf("%s: targets = %v, want none", name, got)
		}
	}
}
