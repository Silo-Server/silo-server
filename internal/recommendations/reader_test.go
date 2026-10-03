package recommendations

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

func TestGetBecauseYouWatchedWithSourceReturnsAnchorThatHasCache(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	const prefix = "tbcw-source-"
	const profile = "66200000-0000-4000-8000-000000000001"
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, prefix).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID) })
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles(id,user_id,name) VALUES($1,$2,'anchor test')`, profile, userID); err != nil {
		t.Fatal(err)
	}
	cleanupRecoMediaItems(t, pool, prefix)
	latest, older, rec := prefix+"latest", prefix+"older", prefix+"rec"
	for _, id := range []string{latest, older, rec} {
		seedRecoMediaItem(t, pool, id, "movie", "matched")
	}
	for i, id := range []string{latest, older} {
		if _, err := pool.Exec(ctx, `INSERT INTO user_watch_progress(user_id,profile_id,media_item_id,completed,updated_at) VALUES($1,$2,$3,true,TIMESTAMPTZ '2026-08-10 12:00:00Z' - $4 * INTERVAL '1 hour')`, userID, profile, id, i); err != nil {
			t.Fatal(err)
		}
	}
	repo := NewRepo(pool)
	// Only the older watch has a cached row; the latest watch must be skipped.
	expires := time.Now().Add(time.Hour).Format(time.RFC3339)
	if err := repo.UpsertRecommendationCache(ctx, userID, profile, RecTypeBecauseWatched, older, []ScoredItem{{MediaItemID: rec, Score: 1}}, expires); err != nil {
		t.Fatal(err)
	}

	items, sourceID, err := NewReader(repo, nil, nil, nil).GetBecauseYouWatchedWithSource(ctx, userID, profile, "", 10, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if sourceID != older {
		t.Fatalf("source = %q, want %q", sourceID, older)
	}
	if len(items) != 1 || items[0].MediaItemID != rec {
		t.Fatalf("items = %+v, want only %q", items, rec)
	}
}

func TestClusterRowTitleComesFromTheCachedRow(t *testing.T) {
	// The taste job renumbered the clusters: index 0 is now Horror, but the
	// cached row for index 0 still holds the Comedy build.
	horror := TasteCluster{ClusterIdx: 0, Label: "Horror"}
	cached := []ScoredItem{{MediaItemID: "m1", Reason: clusterTitle("Comedy")}, {MediaItemID: "m2", Reason: clusterTitle("Comedy")}}
	if row := clusterRow(horror, cached); row.Label != "Because you enjoy Comedy" || row.ClusterIndex != 0 || len(row.Items) != 2 {
		t.Fatalf("row = %+v, want the cached Comedy title", row)
	}

	// Rows whose items carry no cluster title use the cluster label.
	for _, reason := range []string{"", "cluster_match", clusterTitlePrefix} {
		row := clusterRow(horror, []ScoredItem{{MediaItemID: "m1", Reason: reason}})
		if row.Label != "Because you enjoy Horror" {
			t.Fatalf("reason %q: title = %q", reason, row.Label)
		}
	}
	if row := clusterRow(TasteCluster{}, nil); row.Label != "Because you enjoy For You" {
		t.Fatalf("unlabelled cluster title = %q", row.Label)
	}
}
