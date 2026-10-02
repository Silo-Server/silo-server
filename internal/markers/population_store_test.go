package markers

import (
	"context"
	"fmt"
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
	candidates := func() []int {
		t.Helper()
		return allCandidates(t, store, providers, fixture.fileIDs[:])
	}
	if ids := candidates(); !slices.Equal(ids, []int{fixture.fileIDs[1]}) {
		t.Fatalf("candidates with a fresh miss=%v, want only the unqueried file", ids)
	}
	if _, err := fixture.pool.Exec(ctx, `UPDATE marker_fetch_state SET retry_at=now()-interval '1 second' WHERE media_file_id=$1 AND provider=$2`, fixture.fileIDs[0], fixture.provider); err != nil {
		t.Fatal(err)
	}
	if ids := candidates(); !slices.Equal(ids, []int{fixture.fileIDs[1], fixture.fileIDs[0]}) {
		t.Fatalf("candidates with a due miss=%v, want the unqueried file first", ids)
	}
	if _, err := fixture.pool.Exec(ctx, `UPDATE marker_fetch_state SET retry_at=now()+interval '1 hour' WHERE media_file_id=$1 AND provider=$2`, fixture.fileIDs[0], fixture.provider); err != nil {
		t.Fatal(err)
	}
	providers[fixture.provider] = "rev2"
	if ids := candidates(); !slices.Contains(ids, fixture.fileIDs[0]) {
		t.Fatalf("credential change candidates=%v", ids)
	}
}

// allCandidates pages through every candidate, in order, and keeps the
// fixture's files; other rows in a shared test database may sort first.
func allCandidates(t *testing.T, store *DBPopulationStore, providers map[string]string, fileIDs []int) []int {
	t.Helper()
	var ids []int
	var cursor *SyncCursor
	for {
		page, next, err := store.Candidates(t.Context(), providers, cursor, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			return ids
		}
		for _, id := range page {
			if slices.Contains(fileIDs, id) {
				ids = append(ids, id)
			}
		}
		cursor = next
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

// A daily quota smaller than the due backlog must still reach every file over
// successive runs instead of spending it on the lowest file IDs each time.
func TestPopulationCandidatesRotateThroughDueFilesUnderQuota(t *testing.T) {
	fixture := newContributionStoreFixture(t)
	store := NewPopulationStore(fixture.pool)
	ctx := t.Context()
	itemID := strconv.FormatInt(time.Now().UnixNano(), 10)
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title,tmdb_id) VALUES($1,'movie','Marker rotation fixture','42')`, itemID); err != nil {
		t.Fatal(err)
	}
	var thirdID int
	if err := fixture.pool.QueryRow(ctx, `INSERT INTO media_files(media_folder_id,file_path) VALUES($1,$2) RETURNING id`,
		fixture.folderID, fmt.Sprintf("/claim-test/%d-2.mkv", fixture.suffix)).Scan(&thirdID); err != nil {
		t.Fatal(err)
	}
	fileIDs := []int{fixture.fileIDs[0], fixture.fileIDs[1], thirdID}
	t.Cleanup(func() {
		_, _ = fixture.pool.Exec(context.Background(), `DELETE FROM media_files WHERE id=$1`, thirdID)
		_, _ = fixture.pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=$1`, itemID)
	})
	if _, err := fixture.pool.Exec(ctx, `UPDATE media_folders SET type='movies',enabled=true WHERE id=$1`, fixture.folderID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `UPDATE media_files SET content_id=$1,duration=1000 WHERE id=ANY($2)`, itemID, fileIDs); err != nil {
		t.Fatal(err)
	}
	providers := map[string]string{fixture.provider: "rev1"}
	fetch := func(fileID int) {
		t.Helper()
		claim, claimed, err := store.Claim(ctx, fileID, fixture.provider, "identity", "rev1", false)
		if err != nil || !claimed {
			t.Fatalf("claim file %d: claimed=%v err=%v", fileID, claimed, err)
		}
		if err := store.Complete(ctx, claim, FetchCompletion{Outcome: markerFetchMiss, RetryAt: time.Now().Add(markerMissTTL), Result: &Result{}}); err != nil {
			t.Fatal(err)
		}
	}
	advance := func() {
		t.Helper()
		if _, err := fixture.pool.Exec(ctx, `UPDATE marker_fetch_state SET fetched_at=fetched_at-make_interval(secs=>$1),retry_at=retry_at-make_interval(secs=>$1)
			WHERE provider=$2 AND media_file_id=ANY($3)`, markerMissTTL.Seconds()+1, fixture.provider, fileIDs); err != nil {
			t.Fatal(err)
		}
	}
	// Seed every file as an older miss, the highest ID oldest of all.
	for i := len(fileIDs) - 1; i >= 0; i-- {
		fetch(fileIDs[i])
		advance()
	}

	refreshed := make(map[int]bool)
	for run := range len(fileIDs) {
		ids := allCandidates(t, store, providers, fileIDs)
		if len(ids) == 0 {
			t.Fatalf("run %d: no due candidates", run)
		}
		// The provider quota allows one request per run.
		fetch(ids[0])
		refreshed[ids[0]] = true
		advance()
	}
	for _, id := range fileIDs {
		if !refreshed[id] {
			t.Errorf("file %d was never refreshed in %d quota-limited runs; refreshed=%v", id, len(fileIDs), refreshed)
		}
	}
}

// Each quota-limited Sync run must pick up where the previous one stopped.
func TestPopulationSyncResumesAfterQuotaAcrossRuns(t *testing.T) {
	fixture := newContributionStoreFixture(t)
	store := NewPopulationStore(fixture.pool)
	ctx := t.Context()
	itemID := strconv.FormatInt(time.Now().UnixNano(), 10)
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title,tmdb_id) VALUES($1,'movie','Marker sync quota fixture','42')`, itemID); err != nil {
		t.Fatal(err)
	}
	var thirdID int
	if err := fixture.pool.QueryRow(ctx, `INSERT INTO media_files(media_folder_id,file_path) VALUES($1,$2) RETURNING id`,
		fixture.folderID, fmt.Sprintf("/claim-test/%d-2.mkv", fixture.suffix)).Scan(&thirdID); err != nil {
		t.Fatal(err)
	}
	fileIDs := []int{fixture.fileIDs[0], fixture.fileIDs[1], thirdID}
	t.Cleanup(func() {
		_, _ = fixture.pool.Exec(context.Background(), `DELETE FROM marker_provider_cooldowns WHERE provider=$1`, fixture.provider)
		_, _ = fixture.pool.Exec(context.Background(), `DELETE FROM media_files WHERE id=$1`, thirdID)
		_, _ = fixture.pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=$1`, itemID)
	})
	if _, err := fixture.pool.Exec(ctx, `UPDATE media_folders SET type='movies',enabled=true WHERE id=$1`, fixture.folderID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `UPDATE media_files SET content_id=$1,duration=1000 WHERE id=ANY($2)`, itemID, fileIDs); err != nil {
		t.Fatal(err)
	}

	// The file ID travels to the provider as the request duration.
	var fetched []int
	quota := 0
	provider := &populationProvider{id: fixture.provider, revision: "rev1"}
	var request Request
	provider.fetch = func() (Result, error) {
		if quota == 0 {
			return Result{}, &RetryAfterError{Provider: fixture.provider, RetryAfter: time.Hour}
		}
		quota--
		fetched = append(fetched, int(request.Duration/time.Second))
		return Result{}, nil
	}
	registry := NewRegistry(nil)
	if err := registry.Register(requestRecorder{populationProvider: provider, request: &request}); err != nil {
		t.Fatal(err)
	}
	service := NewPopulationService(PopulationOptions{
		Registry: registry, Store: store,
		Settings: populationSettings{"setup.completed": "true", SettingMode: "online", SettingOnlineStorage: string(OnlineStorageStored)},
		Resolver: populationResolver{ExternalIDs{Kind: ItemKindMovie, TmdbID: "42"}},
		LoadFile: func(_ context.Context, id int) (*models.MediaFile, error) {
			if !slices.Contains(fileIDs, id) {
				return nil, fmt.Errorf("file %d is outside the fixture", id)
			}
			return &models.MediaFile{ID: id, Duration: id}, nil
		},
		Write: func(context.Context, *models.MediaFile, Result) (bool, error) { return false, nil },
	})
	nextDay := func() {
		t.Helper()
		if _, err := fixture.pool.Exec(ctx, `UPDATE marker_fetch_state SET fetched_at=fetched_at-make_interval(secs=>$1),retry_at=retry_at-make_interval(secs=>$1)
			WHERE provider=$2 AND media_file_id=ANY($3)`, markerMissTTL.Seconds()+1, fixture.provider, fileIDs); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.pool.Exec(ctx, `DELETE FROM marker_provider_cooldowns WHERE provider=$1`, fixture.provider); err != nil {
			t.Fatal(err)
		}
	}
	sync := func(allowed int) []int {
		t.Helper()
		fetched, quota = nil, allowed
		if _, err := service.Sync(ctx, nil); err != nil {
			t.Fatal(err)
		}
		return fetched
	}

	if got := sync(len(fileIDs)); !slices.Equal(got, fileIDs) {
		t.Fatalf("initial sync fetched %v, want %v", got, fileIDs)
	}
	nextDay()
	var refreshed []int
	for run := range len(fileIDs) {
		got := sync(1)
		if len(got) != 1 {
			t.Fatalf("run %d fetched %v, want one file", run, got)
		}
		refreshed = append(refreshed, got[0])
		nextDay()
	}
	slices.Sort(refreshed)
	if !slices.Equal(refreshed, fileIDs) {
		t.Fatalf("quota-limited runs refreshed %v, want each of %v once", refreshed, fileIDs)
	}
}

// requestRecorder exposes the last request to a populationProvider fetch.
type requestRecorder struct {
	*populationProvider
	request *Request
}

func (r requestRecorder) FetchMarkers(ctx context.Context, req Request) (Result, error) {
	*r.request = req
	return r.populationProvider.FetchMarkers(ctx, req)
}
