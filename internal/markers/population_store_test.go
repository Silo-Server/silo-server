package markers

import (
	"context"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestPopulationStoreLeasesFreshnessAndQuota(t *testing.T) {
	fixture := newContributionStoreFixture(t)
	store := NewPopulationStore(fixture.pool)
	ctx := t.Context()
	provider := fixture.provider
	t.Cleanup(func() {
		_, _ = fixture.pool.Exec(context.Background(), `DELETE FROM marker_provider_cooldowns WHERE provider=$1`, provider)
	})
	if _, err := fixture.pool.Exec(ctx, `UPDATE media_files SET duration=1000 WHERE id=ANY($1)`, fixture.fileIDs[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `UPDATE media_folders SET type=' TV ',enabled=true WHERE id=(SELECT media_folder_id FROM media_files WHERE id=$1)`, fixture.fileIDs[0]); err != nil {
		t.Fatal(err)
	}
	if eligible, err := store.Eligible(ctx, fixture.fileIDs[0]); err != nil || !eligible {
		t.Fatalf("normalized TV eligibility=%v err=%v", eligible, err)
	}

	var wg sync.WaitGroup
	claims := make(chan FetchClaim, 8)
	errors := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			claim, claimed, err := store.Claim(ctx, fixture.fileIDs[0], provider, "identity", "rev1", false)
			if err != nil {
				errors <- err
			} else if claimed {
				claims <- claim
			}
		})
	}
	wg.Wait()
	close(claims)
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	if len(claims) != 1 {
		t.Fatalf("concurrent request claims=%d, want 1", len(claims))
	}
	first := <-claims
	if err := store.Complete(ctx, first, FetchCompletion{Outcome: "miss", RetryAt: time.Now().Add(time.Hour), Result: &Result{}}); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := store.Claim(ctx, fixture.fileIDs[0], provider, "identity", "rev1", false); err != nil || claimed {
		t.Fatalf("cached miss refetched: claimed=%v err=%v", claimed, err)
	}
	second, claimed, err := store.Claim(ctx, fixture.fileIDs[0], provider, "identity", "rev1", true)
	if err != nil || !claimed {
		t.Fatalf("explicit refresh: claimed=%v err=%v", claimed, err)
	}
	if err := store.Complete(ctx, first, FetchCompletion{Outcome: "hit", RetryAt: time.Now().Add(7 * 24 * time.Hour), Result: &Result{ProviderID: "stale"}}); err != nil {
		t.Fatal(err)
	}
	var token string
	if err := fixture.pool.QueryRow(ctx, `SELECT lease_token::text FROM marker_fetch_state WHERE media_file_id=$1 AND provider=$2`, fixture.fileIDs[0], provider).Scan(&token); err != nil || token != second.Token {
		t.Fatalf("stale completion consumed a newer claim: %q err=%v", token, err)
	}
	if err := store.Complete(ctx, second, FetchCompletion{Outcome: "error", RetryAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := store.Claim(ctx, fixture.fileIDs[0], provider, "identity", "rev1", true); err != nil || claimed {
		t.Fatalf("refresh bypassed failure backoff: claimed=%v err=%v", claimed, err)
	}
	if err := store.Cooldown(ctx, provider, "rev1", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := store.Claim(ctx, fixture.fileIDs[1], provider, "different-file", "rev1", true); err != nil || claimed {
		t.Fatalf("quota not shared between files: claimed=%v err=%v", claimed, err)
	}
	if _, claimed, err := store.Claim(ctx, fixture.fileIDs[1], provider, "new-identity", "rev2", false); err != nil || !claimed {
		t.Fatalf("new credentials retained old cooldown: claimed=%v err=%v", claimed, err)
	}
}

func TestPopulationCandidatesPrioritizeNewFilesAndCredentialChanges(t *testing.T) {
	fixture := newContributionStoreFixture(t)
	store := NewPopulationStore(fixture.pool)
	ctx := t.Context()
	itemID := strconv.FormatInt(time.Now().UnixNano(), 10)
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title,tmdb_id) VALUES($1,'movie','Marker sync fixture','42')`, itemID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = fixture.pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=$1`, itemID)
	})
	if _, err := fixture.pool.Exec(ctx, `UPDATE media_folders SET type='movies',enabled=true WHERE id=(SELECT media_folder_id FROM media_files WHERE id=$1)`, fixture.fileIDs[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `UPDATE media_files SET content_id=$1,duration=1000 WHERE id=ANY($2)`, itemID, fixture.fileIDs[:]); err != nil {
		t.Fatal(err)
	}
	providers := map[string]string{fixture.provider: "rev1"}
	first, claimed, err := store.Claim(ctx, fixture.fileIDs[0], fixture.provider, "identity", "rev1", false)
	if err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	if err := store.Complete(ctx, first, FetchCompletion{Outcome: "miss", RetryAt: time.Now().Add(time.Hour), Result: &Result{}}); err != nil {
		t.Fatal(err)
	}
	newIDs, err := store.Candidates(ctx, providers, fixture.fileIDs[0]-1, 1000, true)
	if err != nil || !slices.Contains(newIDs, fixture.fileIDs[1]) || slices.Contains(newIDs, fixture.fileIDs[0]) {
		t.Fatalf("unqueried candidates=%v err=%v", newIDs, err)
	}
	if ids, err := store.Candidates(ctx, providers, fixture.fileIDs[0]-1, 1000, false); err != nil || slices.Contains(ids, fixture.fileIDs[0]) {
		t.Fatalf("fresh refresh candidates=%v err=%v", ids, err)
	}
	providers[fixture.provider] = "rev2"
	if ids, err := store.Candidates(ctx, providers, fixture.fileIDs[0]-1, 1000, false); err != nil || !slices.Contains(ids, fixture.fileIDs[0]) {
		t.Fatalf("credential change candidates=%v err=%v", ids, err)
	}
}

func TestMarkerResolverUsesShowIDsAndPreservesSpecials(t *testing.T) {
	fixture := newContributionStoreFixture(t)
	ctx := t.Context()
	seriesID := strconv.FormatInt(time.Now().UnixNano(), 10)
	episodeID := strconv.FormatInt(time.Now().UnixNano()+1, 10)
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title,imdb_id) VALUES($1,'series','Marker identity fixture','tt42')`, seriesID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = fixture.pool.Exec(context.Background(), `DELETE FROM episodes WHERE content_id=$1`, episodeID)
		_, _ = fixture.pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=$1`, seriesID)
	})
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO episodes(content_id,series_id,season_number,episode_number,tmdb_id,imdb_id,tvdb_id)
		VALUES($1,$2,0,2,'88','tt99','99')`, episodeID, seriesID); err != nil {
		t.Fatal(err)
	}
	resolver := NewDBExternalIDResolver(fixture.pool)
	ids, err := resolver.ResolveForFile(ctx, &models.MediaFile{ID: fixture.fileIDs[0], EpisodeID: episodeID, SeasonNumber: 9, EpisodeNumber: 9})
	if err != nil {
		t.Fatal(err)
	}
	if ids.TmdbID != "" || ids.TvdbID != "" || ids.ImdbID != "tt42" || ids.SeasonNumber != 0 || ids.EpisodeNumber != 2 {
		t.Fatalf("episode identity leaked into show lookup: %+v", ids)
	}
}
