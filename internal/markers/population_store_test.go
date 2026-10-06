package markers

import (
	"context"
	"errors"
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

// seedMovieFiles adds a third file to the fixture, attaches all three to one
// matched movie in an enabled movies library, and returns their IDs.
func (f contributionStoreFixture) seedMovieFiles(t *testing.T) (string, []int) {
	t.Helper()
	ctx := t.Context()
	itemID := strconv.FormatInt(time.Now().UnixNano(), 10)
	if _, err := f.pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title,tmdb_id) VALUES($1,'movie','Marker sync fixture','42')`, itemID); err != nil {
		t.Fatal(err)
	}
	var thirdID int
	if err := f.pool.QueryRow(ctx, `INSERT INTO media_files(media_folder_id,file_path) VALUES($1,$2) RETURNING id`,
		f.folderID, fmt.Sprintf("/claim-test/%d-2.mkv", f.suffix)).Scan(&thirdID); err != nil {
		t.Fatal(err)
	}
	fileIDs := []int{f.fileIDs[0], f.fileIDs[1], thirdID}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM marker_provider_cooldowns WHERE provider=$1`, f.provider)
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM media_files WHERE id=$1`, thirdID)
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=$1`, itemID)
	})
	if _, err := f.pool.Exec(ctx, `UPDATE media_folders SET type='movies',enabled=true WHERE id=$1`, f.folderID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE media_files SET content_id=$1,duration=1000 WHERE id=ANY($2)`, itemID, fileIDs); err != nil {
		t.Fatal(err)
	}
	return itemID, fileIDs
}

func TestPopulationCandidatesPrioritizeNewFilesAndCredentialChanges(t *testing.T) {
	fixture := newContributionStoreFixture(t)
	store := NewPopulationStore(fixture.pool)
	ctx := t.Context()
	_, fileIDs := fixture.seedMovieFiles(t)
	providers := map[string]string{fixture.provider: "rev1"}
	first, claimed, err := store.Claim(ctx, fileIDs[0], fixture.provider, "identity", "rev1", false)
	if err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	if err := store.Complete(ctx, first, FetchCompletion{Outcome: "miss", RetryAt: time.Now().Add(time.Hour), Result: &Result{}}); err != nil {
		t.Fatal(err)
	}
	candidates := func() []int {
		t.Helper()
		return allCandidates(t, store, providers, fileIDs)
	}
	if ids := candidates(); !slices.Equal(ids, fileIDs[1:]) {
		t.Fatalf("candidates with a fresh miss=%v, want only the unqueried files", ids)
	}
	if _, err := fixture.pool.Exec(ctx, `UPDATE marker_fetch_state SET retry_at=now()-interval '1 second' WHERE media_file_id=$1 AND provider=$2`, fileIDs[0], fixture.provider); err != nil {
		t.Fatal(err)
	}
	if ids := candidates(); !slices.Equal(ids, []int{fileIDs[1], fileIDs[2], fileIDs[0]}) {
		t.Fatalf("candidates with a due miss=%v, want the unqueried files first", ids)
	}
	if _, err := fixture.pool.Exec(ctx, `UPDATE marker_fetch_state SET retry_at=now()+interval '1 hour' WHERE media_file_id=$1 AND provider=$2`, fileIDs[0], fixture.provider); err != nil {
		t.Fatal(err)
	}
	providers[fixture.provider] = "rev2"
	if ids := candidates(); !slices.Contains(ids, fileIDs[0]) {
		t.Fatalf("credential change candidates=%v", ids)
	}
}

// allCandidates keeps the fixture's files, in order; other rows in a shared
// test database may also be due.
func allCandidates(t *testing.T, store *DBPopulationStore, providers map[string]string, fileIDs []int) []int {
	t.Helper()
	all, err := store.Candidates(t.Context(), providers)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int
	for _, id := range all {
		if slices.Contains(fileIDs, id) {
			ids = append(ids, id)
		}
	}
	return ids
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
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO episodes(content_id,series_id,season_number,episode_number,tmdb_id,imdb_id,tvdb_id,air_date)
		VALUES($1,$2,0,2,'88','tt99','99','2026-09-30')`, episodeID, seriesID); err != nil {
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
	if !ids.Released.Equal(time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("released=%v, want the episode air date", ids.Released)
	}
}

// quotaSync runs Sync over the fixture's movie files with a provider that
// answers a fixed number of requests per run and then rate-limits.
type quotaSync struct {
	fixture contributionStoreFixture
	service *PopulationService
	fileIDs []int
	// limit is the provider's cooldown once the quota runs out; refill is the
	// quota it grants after that cooldown.
	limit   time.Duration
	refill  int
	quota   int
	fetched []int
	write   func() error
}

func newQuotaSync(t *testing.T) *quotaSync {
	t.Helper()
	fixture := newContributionStoreFixture(t)
	_, fileIDs := fixture.seedMovieFiles(t)
	q := &quotaSync{fixture: fixture, fileIDs: fileIDs, limit: time.Hour}
	// The file ID travels to the provider as the request duration.
	provider := &populationProvider{id: fixture.provider, revision: "rev1"}
	var request Request
	provider.fetch = func() (Result, error) {
		if q.quota == 0 {
			q.quota = q.refill
			return Result{}, &RetryAfterError{Provider: fixture.provider, RetryAfter: q.limit}
		}
		q.quota--
		q.fetched = append(q.fetched, int(request.Duration/time.Second))
		return Result{}, nil
	}
	registry := NewRegistry(nil)
	if err := registry.Register(requestRecorder{populationProvider: provider, request: &request}); err != nil {
		t.Fatal(err)
	}
	q.service = NewPopulationService(PopulationOptions{
		Registry: registry, Store: NewPopulationStore(fixture.pool),
		Settings: populationSettings{"setup.completed": "true", SettingMode: "online", SettingOnlineStorage: string(OnlineStorageStored)},
		Resolver: populationResolver{ExternalIDs{Kind: ItemKindMovie, TmdbID: "42"}},
		LoadFile: func(_ context.Context, id int) (*models.MediaFile, error) {
			if !slices.Contains(fileIDs, id) {
				return nil, fmt.Errorf("file %d is outside the fixture", id)
			}
			return &models.MediaFile{ID: id, Duration: id}, nil
		},
		Write: func(context.Context, *models.MediaFile, Result) (bool, error) {
			if q.write != nil {
				return false, q.write()
			}
			return false, nil
		},
	})
	return q
}

// run starts a Sync with allowed requests and returns the files it fetched.
func (q *quotaSync) run(t *testing.T, allowed int) (SyncSummary, []int) {
	t.Helper()
	q.fetched, q.quota = nil, allowed
	summary, err := q.service.Sync(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return summary, q.fetched
}

// nextDay ages every stored result past its retry time and ends the cooldown.
func (q *quotaSync) nextDay(t *testing.T) {
	t.Helper()
	if _, err := q.fixture.pool.Exec(t.Context(), `UPDATE marker_fetch_state SET fetched_at=fetched_at-make_interval(secs=>$1),retry_at=retry_at-make_interval(secs=>$1)
		WHERE provider=$2 AND media_file_id=ANY($3)`, markerMissTTL.Seconds()+1, q.fixture.provider, q.fileIDs); err != nil {
		t.Fatal(err)
	}
	if _, err := q.fixture.pool.Exec(t.Context(), `DELETE FROM marker_provider_cooldowns WHERE provider=$1`, q.fixture.provider); err != nil {
		t.Fatal(err)
	}
}

// Each quota-limited Sync run must pick up where the previous one stopped,
// and end once the quota is spent instead of walking the rest of the library.
func TestPopulationSyncResumesAfterQuotaAcrossRuns(t *testing.T) {
	q := newQuotaSync(t)
	if _, got := q.run(t, len(q.fileIDs)); !slices.Equal(got, q.fileIDs) {
		t.Fatalf("initial sync fetched %v, want %v", got, q.fileIDs)
	}
	q.nextDay(t)
	var refreshed []int
	for run := range len(q.fileIDs) {
		summary, got := q.run(t, 1)
		if len(got) != 1 {
			t.Fatalf("run %d fetched %v, want one file", run, got)
		}
		if summary.Considered != 2 {
			t.Fatalf("run %d considered %d files, want it to stop at the rate-limited one", run, summary.Considered)
		}
		refreshed = append(refreshed, got[0])
		q.nextDay(t)
	}
	slices.Sort(refreshed)
	if !slices.Equal(refreshed, q.fileIDs) {
		t.Fatalf("quota-limited runs refreshed %v, want each of %v once", refreshed, q.fileIDs)
	}
}

// A short rate limit pauses the run; the files after it are still fetched.
func TestPopulationSyncWaitsOutShortRateLimit(t *testing.T) {
	q := newQuotaSync(t)
	q.limit, q.refill = 200*time.Millisecond, len(q.fileIDs)
	if _, got := q.run(t, 1); !slices.Equal(got, []int{q.fileIDs[0], q.fileIDs[2]}) {
		t.Fatalf("sync fetched %v, want every file but the rate-limited one", got)
	}
	// A cooldown left by another lookup must not end the next run before it starts.
	q.nextDay(t)
	if err := NewPopulationStore(q.fixture.pool).Cooldown(t.Context(), q.fixture.provider, "rev1", time.Now().Add(200*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	_, got := q.run(t, len(q.fileIDs))
	if slices.Sort(got); !slices.Equal(got, q.fileIDs) {
		t.Fatalf("sync after an existing cooldown fetched %v, want %v", got, q.fileIDs)
	}
}

// A metadata refresh that leaves a file's IDs unchanged must not keep the file
// in every daily sync until its cached result expires.
func TestPopulationSyncClearsUnchangedMetadataEdits(t *testing.T) {
	q := newQuotaSync(t)
	store := NewPopulationStore(q.fixture.pool)
	providers := map[string]string{q.fixture.provider: "rev1"}
	q.run(t, len(q.fileIDs))
	if _, err := q.fixture.pool.Exec(t.Context(), `UPDATE media_items SET updated_at=now()
		WHERE content_id=(SELECT content_id FROM media_files WHERE id=$1)`, q.fileIDs[0]); err != nil {
		t.Fatal(err)
	}
	if ids := allCandidates(t, store, providers, q.fileIDs); !slices.Equal(ids, q.fileIDs) {
		t.Fatalf("candidates after a metadata edit=%v, want %v", ids, q.fileIDs)
	}
	// A pass that could not store the markers has not finished the check.
	q.write = func() error { return errors.New("write unavailable") }
	q.run(t, len(q.fileIDs))
	if ids := allCandidates(t, store, providers, q.fileIDs); !slices.Equal(ids, q.fileIDs) {
		t.Fatalf("candidates after a failed check=%v, want %v", ids, q.fileIDs)
	}
	q.write = nil
	if _, got := q.run(t, len(q.fileIDs)); len(got) != 0 {
		t.Fatalf("unchanged identities were refetched: %v", got)
	}
	if ids := allCandidates(t, store, providers, q.fileIDs); len(ids) != 0 {
		t.Fatalf("candidates after the identity check=%v, want none", ids)
	}
	// An edit that lands while a pass is checking the file stays due.
	edit := func() error {
		_, err := q.fixture.pool.Exec(t.Context(), `UPDATE media_items SET updated_at=now()
			WHERE content_id=(SELECT content_id FROM media_files WHERE id=$1)`, q.fileIDs[0])
		return err
	}
	if err := edit(); err != nil {
		t.Fatal(err)
	}
	q.write = edit
	q.run(t, len(q.fileIDs))
	// Each file's own check saw the edit made while it ran, the last file's included.
	if ids := allCandidates(t, store, providers, q.fileIDs); !slices.Equal(ids, q.fileIDs) {
		t.Fatalf("candidates after edits during the identity check=%v, want %v", ids, q.fileIDs)
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
