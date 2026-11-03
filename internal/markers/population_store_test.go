package markers

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// An abandoned request hands its claim back without touching what earlier
// requests recorded, and never leaves a new identity waiting out an old
// result's freshness with nothing cached.
func TestPopulationStoreReleaseKeepsFetchState(t *testing.T) {
	fixture := newContributionStoreFixture(t)
	store := NewPopulationStore(fixture.pool)
	ctx := t.Context()
	fileID, provider := fixture.fileIDs[0], fixture.provider
	type fetchState struct {
		outcome, lastError, result string
		failures                   int
		retryAt                    time.Time
		leased                     bool
	}
	read := func() fetchState {
		t.Helper()
		var state fetchState
		if err := fixture.pool.QueryRow(ctx, `SELECT outcome,COALESCE(last_error,''),COALESCE(result::text,''),failures,retry_at,lease_token IS NOT NULL
			FROM marker_fetch_state WHERE media_file_id=$1 AND provider=$2`, fileID, provider).
			Scan(&state.outcome, &state.lastError, &state.result, &state.failures, &state.retryAt, &state.leased); err != nil {
			t.Fatal(err)
		}
		return state
	}
	claim := func(identity string, force bool) FetchClaim {
		t.Helper()
		c, claimed, err := store.Claim(ctx, fileID, provider, identity, "rev1", force)
		if err != nil || !claimed {
			t.Fatalf("claim %s: claimed=%v err=%v", identity, claimed, err)
		}
		return c
	}

	// A cached answer, then a failed request whose backoff has run out.
	if err := store.Complete(ctx, claim("identity", false), FetchCompletion{Outcome: "hit", RetryAt: time.Now().Add(-time.Second), Result: &Result{ProviderID: "cached"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, claim("identity", false), FetchCompletion{Outcome: "error", RetryAt: time.Now().Add(-time.Second), Error: "provider down"}); err != nil {
		t.Fatal(err)
	}
	before := read()
	abandoned := claim("identity", false)
	if abandoned.Failures != 1 {
		t.Fatalf("claim failures=%d, want 1", abandoned.Failures)
	}
	stale := abandoned
	stale.Token = "00000000-0000-0000-0000-000000000000"
	if err := store.Release(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if !read().leased {
		t.Fatal("release with another token freed the lease")
	}
	if err := store.Release(ctx, abandoned); err != nil {
		t.Fatal(err)
	}
	after := read()
	if after.leased || after.outcome != before.outcome || after.lastError != before.lastError || after.result != before.result ||
		after.failures != before.failures || !after.retryAt.Equal(before.retryAt) {
		t.Fatalf("release changed fetch state:\nbefore %+v\nafter  %+v", before, after)
	}

	// A fresh answer for the current identity, then an abandoned request for
	// a new one: the new identity must be asked again at once.
	if err := store.Complete(ctx, claim("identity", false), FetchCompletion{Outcome: "hit", RetryAt: time.Now().Add(24 * time.Hour), Result: &Result{ProviderID: "cached"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Release(ctx, claim("new-identity", false)); err != nil {
		t.Fatal(err)
	}
	var due bool
	if err := fixture.pool.QueryRow(ctx, `SELECT result IS NULL AND retry_at<=now() FROM marker_fetch_state WHERE media_file_id=$1 AND provider=$2`,
		fileID, provider).Scan(&due); err != nil || !due {
		t.Fatalf("released new identity kept the old freshness: %+v err=%v", read(), err)
	}
	claim("new-identity", false)
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

// requestRecorder exposes the last request to a populationProvider fetch.
type requestRecorder struct {
	*populationProvider
	request *Request
}

func (r requestRecorder) FetchMarkers(ctx context.Context, req Request) (Result, error) {
	*r.request = req
	return r.populationProvider.FetchMarkers(ctx, req)
}
