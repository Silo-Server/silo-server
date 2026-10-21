package historyimport

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
)

// These tests drive whole personal import runs, from the queue through the
// provider to Postgres, against fake Jellyfin, Emby, and Plex servers.

const (
	// limitsSeriesCount spreads the filler episodes over enough series that
	// every ID lookup runs in several chunks.
	limitsSeriesCount = 250
	// limitsMatchedSeries is the series whose S01E01 exists in the catalog.
	limitsMatchedSeries = 7
	// limitsUnrequestedPerLookup is how many items a lookup answers with
	// that nobody asked for.
	limitsUnrequestedPerLookup = 500
)

// limitsPadding makes every listed item heavy with fields the importer skips.
var limitsPadding = func() string {
	var streams []string
	for i := range 10 {
		streams = append(streams, fmt.Sprintf(`{"Index":%d,"Type":"Subtitle","Codec":"srt","Language":"eng","DisplayTitle":"English","IsDefault":false,"IsForced":false,"IsExternal":true}`, i))
	}
	return `"Overview":"` + strings.Repeat("o", 4<<10) + `","MediaStreams":[` + strings.Join(streams, ",") + `]`
}()

// limitsObserved records the shape of the requests one fake upstream saw.
type limitsObserved struct {
	mu         sync.Mutex
	maxPage    int
	maxLookup  int
	lookedUp   map[string]bool
	listings   int
	unexpected []string
}

func newLimitsObserved() *limitsObserved {
	return &limitsObserved{lookedUp: map[string]bool{}}
}

func (o *limitsObserved) page(size int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.listings++
	o.maxPage = max(o.maxPage, size)
}

func (o *limitsObserved) lookup(ids []string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.maxLookup = max(o.maxLookup, len(ids))
	for _, id := range ids {
		o.lookedUp[id] = true
	}
}

func (o *limitsObserved) unexpectedRequest(r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.unexpected = append(o.unexpected, r.Method+" "+r.URL.Path)
}

// pageOf returns items[start:start+limit] clamped to the slice.
func pageOf(items []string, start, limit int) []string {
	start = min(max(start, 0), len(items))
	return items[start:min(start+limit, len(items))]
}

// limitsEpisodes returns n filler episodes plus the one episode that matches
// the catalog, which comes last so it only arrives with the final page.
func limitsEpisodes(n int, episode func(id, seriesID string, season, number int) string) []string {
	items := make([]string, 0, n+1)
	for i := range n {
		items = append(items, episode(
			"ep-"+strconv.Itoa(i), "series-"+strconv.Itoa(i%limitsSeriesCount), 5, i+1))
	}
	return append(items, episode("ep-matched", "series-"+strconv.Itoa(limitsMatchedSeries), 1, 1))
}

// limitsLookupAnswer answers an ID lookup with each requested item twice and
// a batch of items nobody asked for.
func limitsLookupAnswer(ids []string, item func(id string) string) string {
	var parts []string
	for _, id := range ids {
		parts = append(parts, item(id), item(id))
	}
	for i := range limitsUnrequestedPerLookup {
		parts = append(parts, item("unrequested-"+strconv.Itoa(i)))
	}
	return strings.Join(parts, ",")
}

func limitsSeriesTVDB(id string) string {
	if id == "series-"+strconv.Itoa(limitsMatchedSeries) {
		return "9000"
	}
	return "1" + strings.TrimPrefix(id, "series-")
}

func jellyfinLimitsEpisode(id, seriesID string, season, number int) string {
	return fmt.Sprintf(`{"Id":%q,"Name":"Episode","Type":"Episode","SeriesId":%q,"SeriesName":"Show","ParentIndexNumber":%d,"IndexNumber":%d,"RunTimeTicks":1000000000,"UserData":{"Played":true,"PlayCount":1,"LastPlayedDate":"2026-09-02T00:00:00Z"},%s}`,
		id, seriesID, season, number, limitsPadding)
}

const limitsMovie = `{"Id":"movie-1","Name":"Movie","Type":"Movie","ProviderIds":{"Imdb":"tt1234567"},"RunTimeTicks":1000000000,"UserData":{"Played":true,"PlayCount":1,"LastPlayedDate":"2026-09-01T00:00:00Z"}}`

// embyJellyfinLimitsHandler serves the Emby and Jellyfin item APIs, which share
// their wire format. played is the IsPlayed listing; lookups by Ids answer
// with series ids that the matcher resolves.
func embyJellyfinLimitsHandler(t *testing.T, observed *limitsObserved, pageSize int, played []string, playedBody []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		query := r.URL.Query()
		switch {
		case strings.HasSuffix(r.URL.Path, "/Users/AuthenticateByName"):
			_, _ = w.Write([]byte(`{"AccessToken":"server-token","User":{"Id":"external"}}`))
		case query.Get("Ids") != "":
			ids := strings.Split(query.Get("Ids"), ",")
			observed.lookup(ids)
			_, _ = w.Write([]byte(`{"Items":[` + limitsLookupAnswer(ids, func(id string) string {
				return fmt.Sprintf(`{"Id":%q,"Name":"Show","Type":"Series","ProviderIds":{"Tvdb":%q},%s}`, id, limitsSeriesTVDB(id), limitsPadding)
			}) + `]}`))
		case query.Get("Filters") == "IsPlayed":
			if playedBody != nil {
				_, _ = w.Write(playedBody)
				return
			}
			limit, err := strconv.Atoi(query.Get("Limit"))
			if err != nil || limit > pageSize {
				t.Errorf("played listing Limit = %q, want at most %d", query.Get("Limit"), pageSize)
			}
			start, _ := strconv.Atoi(query.Get("StartIndex"))
			observed.page(limit)
			_, _ = fmt.Fprintf(w, `{"TotalRecordCount":%d,"Items":[%s]}`, len(played), strings.Join(pageOf(played, start, limit), ","))
		case strings.HasSuffix(r.URL.Path, "/Items") || strings.HasSuffix(r.URL.Path, "/Resume"):
			// Favorites and resume positions are empty.
			_, _ = w.Write([]byte(`{"TotalRecordCount":0,"Items":[]}`))
		default:
			observed.unexpectedRequest(r)
			http.NotFound(w, r)
		}
	})
}

func plexLimitsEpisode(id, seriesID string, season, number int) string {
	return fmt.Sprintf(`{"ratingKey":%q,"key":"/library/metadata/%s","type":"episode","title":"Episode","grandparentTitle":"Show","grandparentRatingKey":%q,"parentIndex":%d,"index":%d,"duration":100000,"viewCount":1,"lastViewedAt":1788307200,%s}`,
		id, id, seriesID, season, number, limitsPadding)
}

const plexLimitsMovie = `{"ratingKey":"movie-1","type":"movie","title":"Movie","year":2026,"Guid":[{"id":"imdb://tt1234567"}],"duration":100000,"viewCount":1,"lastViewedAt":1788220800}`

func plexLimitsHandler(t *testing.T, observed *limitsObserved, episodes []string, playedBody []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		query := r.URL.Query()
		switch {
		case r.URL.Path == "/library/sections":
			_, _ = w.Write([]byte(`{"MediaContainer":{"Directory":[{"key":"1","type":"movie","title":"Movies"},{"key":"2","type":"show","title":"Shows"}]}}`))
		case r.URL.Path == "/library/sections/1/all" || r.URL.Path == "/library/sections/2/all":
			if query.Get("inProgress") == "1" {
				_, _ = w.Write([]byte(`{"MediaContainer":{"totalSize":0,"Metadata":[]}}`))
				return
			}
			if r.URL.Path == "/library/sections/1/all" {
				_, _ = w.Write([]byte(`{"MediaContainer":{"totalSize":1,"Metadata":[` + plexLimitsMovie + `]}}`))
				return
			}
			if playedBody != nil {
				_, _ = w.Write(playedBody)
				return
			}
			limit, err := strconv.Atoi(query.Get("X-Plex-Container-Size"))
			if err != nil || limit > plexPageSize {
				t.Errorf("section page size = %q, want at most %d", query.Get("X-Plex-Container-Size"), plexPageSize)
			}
			start, _ := strconv.Atoi(query.Get("X-Plex-Container-Start"))
			observed.page(limit)
			_, _ = fmt.Fprintf(w, `{"MediaContainer":{"totalSize":%d,"Metadata":[%s]}}`, len(episodes), strings.Join(pageOf(episodes, start, limit), ","))
		case strings.HasPrefix(r.URL.Path, "/library/metadata/"):
			keys := strings.Split(strings.TrimPrefix(r.URL.Path, "/library/metadata/"), ",")
			observed.lookup(keys)
			_, _ = w.Write([]byte(`{"MediaContainer":{"Metadata":[` + limitsLookupAnswer(keys, func(key string) string {
				return fmt.Sprintf(`{"ratingKey":%q,"type":"show","title":"Show","Guid":[{"id":"tvdb://%s"}],"Role":[{"tag":"Actor","role":"Role"}]}`, key, limitsSeriesTVDB(key))
			}) + `]}}`))
		default:
			observed.unexpectedRequest(r)
			http.NotFound(w, r)
		}
	})
}

// limitsRepository is the personal-run schema plus one movie and one episode
// of a series in the catalog.
func limitsRepository(t *testing.T) *Repository {
	t.Helper()
	repo := personalEffectRepository(t)
	makeUserOneAdmin(t, repo)
	if _, err := repo.pool.Exec(t.Context(), `INSERT INTO media_items VALUES('series','series','Show',2020,'matched',NULL,NULL,'9000');
 CREATE TABLE episodes(content_id text PRIMARY KEY,series_id text,title text,season_number integer,episode_number integer,imdb_id text,tmdb_id text,tvdb_id text);
 INSERT INTO episodes VALUES('episode','series','Pilot',1,1,NULL,NULL,NULL)`); err != nil {
		t.Fatal(err)
	}
	return repo
}

// runLimitsImport queues one personal run with input and waits for it to end.
func runLimitsImport(t *testing.T, repo *Repository, input CreateRunInput, discoverURL string) Run {
	t.Helper()
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	service := NewService(ctx, repo, pgstore.NewPostgresProvider(repo.pool))
	service.SetLocalNetworkAccess(NewLocalNetworkAccess(nil, repo))
	service.plex.discoverBaseURL = discoverURL
	service.SetStableIdentityResolver(watchstate.NewStableIdentityResolver(startupIdentityItems{}, nil, startupIdentityProviders{}))
	observed := make(chan Run, 64)
	service.AddObserver(queueObserverFunc(func(run Run) {
		select {
		case observed <- run:
		default:
		}
	}))
	service.StartBackgroundWork()
	input.ProfileID = "p"
	run, err := service.CreateRun(t.Context(), 1, input)
	if err != nil {
		t.Fatal(err)
	}
	timeout := time.NewTimer(60 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case got := <-observed:
			if got.ID == run.ID && (got.Status == RunStatusCompleted || got.Status == RunStatusFailed || got.Status == RunStatusCancelled) {
				return got
			}
		case <-timeout.C:
			t.Fatal("import run never ended")
		}
	}
}

func limitsHistory(t *testing.T, repo *Repository) []string {
	t.Helper()
	rows, err := repo.pool.Query(t.Context(), `SELECT media_item_id||'@'||to_char(watched_at AT TIME ZONE 'UTC','YYYY-MM-DD') FROM user_watch_history WHERE user_id=1 AND profile_id='p' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var history []string
	for rows.Next() {
		var entry string
		if err := rows.Scan(&entry); err != nil {
			t.Fatal(err)
		}
		history = append(history, entry)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return history
}

type limitsSource struct {
	name       string
	pageSize   int
	lookupSize int
	// episodes is how many filler episodes the played listing holds.
	episodes int
	// expanding is a played listing within the byte limit that decodes into
	// millions of values.
	expanding []byte
	// start serves played (or, when non-nil, playedBody) and returns the run
	// input that imports from it.
	start func(t *testing.T, repo *Repository, observed *limitsObserved, played []string, playedBody []byte) (CreateRunInput, string)
}

var limitsSources = []limitsSource{
	{
		name: "jellyfin", pageSize: jellyfinPageSize, lookupSize: jellyfinIDChunkSize, episodes: 3*jellyfinPageSize + 50,
		expanding: expandingBody(`{"TotalRecordCount":1,"Items":[`, `{}`, `]}`),
		start: func(t *testing.T, _ *Repository, observed *limitsObserved, played []string, playedBody []byte) (CreateRunInput, string) {
			upstream := httptest.NewServer(embyJellyfinLimitsHandler(t, observed, jellyfinPageSize, played, playedBody))
			t.Cleanup(upstream.Close)
			return CreateRunInput{Source: SourceTypeJellyfin, JellyfinBaseURL: upstream.URL, JellyfinUsername: "user", JellyfinPassword: "password"}, ""
		},
	},
	{
		name: "emby", pageSize: embyPageSize, lookupSize: embyIDChunkSize, episodes: 2*embyPageSize + 100,
		expanding: expandingBody(`{"TotalRecordCount":1,"Items":[`, `0`, `]}`),
		start: func(t *testing.T, repo *Repository, observed *limitsObserved, played []string, playedBody []byte) (CreateRunInput, string) {
			upstream := httptest.NewServer(embyJellyfinLimitsHandler(t, observed, embyPageSize, played, playedBody))
			t.Cleanup(upstream.Close)
			if _, err := repo.pool.Exec(t.Context(), `UPDATE history_import_sources SET base_url=$1,source_type='emby' WHERE id=1`, upstream.URL); err != nil {
				t.Fatal(err)
			}
			return CreateRunInput{Source: SourceTypeEmby, SourceID: 1, Username: "user", Password: "password"}, ""
		},
	},
	{
		name: "plex", pageSize: plexPageSize, lookupSize: plexMetadataBatchSize, episodes: 2*plexPageSize + 100,
		expanding: expandingBody(`{"MediaContainer":{"totalSize":1,"Metadata":[{"ratingKey":"1","type":"episode","Guid":[`, `{}`, `]}]}}`),
		start: func(t *testing.T, _ *Repository, observed *limitsObserved, played []string, playedBody []byte) (CreateRunInput, string) {
			upstream := httptest.NewServer(plexLimitsHandler(t, observed, played, playedBody))
			t.Cleanup(upstream.Close)
			discover := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"MediaContainer":{"totalSize":0,"Metadata":[]}}`))
			}))
			t.Cleanup(discover.Close)
			return CreateRunInput{Source: SourceTypePlex, PlexBaseURL: upstream.URL, PlexToken: "server-token", PlexAccountToken: "account-token"}, discover.URL
		},
	},
}

// A large, heavy, but legitimate library imports completely: every page is
// read at the client's page size, every series lookup is chunked and keeps
// only what it asked for, and the right watch history reaches Postgres.
func TestImportRunReadsLargeSourcesWithinLimits(t *testing.T) {
	for _, source := range limitsSources {
		t.Run(source.name, func(t *testing.T) {
			repo := limitsRepository(t)
			observed := newLimitsObserved()
			var played []string
			if source.name == "plex" {
				played = limitsEpisodes(source.episodes, plexLimitsEpisode)
			} else {
				played = append(limitsEpisodes(source.episodes, jellyfinLimitsEpisode), limitsMovie)
			}
			input, discoverURL := source.start(t, repo, observed, played, nil)

			run := runLimitsImport(t, repo, input, discoverURL)
			if run.Status != RunStatusCompleted {
				t.Fatalf("run = %s (%s), want completed", run.Status, run.ErrorMessage)
			}
			// Every filler episode, the matched episode, and the movie.
			if want := source.episodes + 2; run.Fetched != want {
				t.Fatalf("fetched = %d, want %d", run.Fetched, want)
			}
			if got, want := limitsHistory(t, repo), []string{"episode@2026-09-02", "movie@2026-09-01"}; !slices.Equal(got, want) {
				t.Fatalf("history = %v, want %v", got, want)
			}

			observed.mu.Lock()
			defer observed.mu.Unlock()
			if len(observed.unexpected) > 0 {
				t.Fatalf("unexpected requests: %v", observed.unexpected)
			}
			if wantPages := (len(played) + source.pageSize - 1) / source.pageSize; observed.listings < wantPages || observed.maxPage != source.pageSize {
				t.Fatalf("listing pages = %d at most %d items, want %d of %d", observed.listings, observed.maxPage, wantPages, source.pageSize)
			}
			if observed.maxLookup != source.lookupSize {
				t.Fatalf("largest lookup = %d ids, want chunks of %d", observed.maxLookup, source.lookupSize)
			}
			for i := range limitsSeriesCount {
				if id := "series-" + strconv.Itoa(i); !observed.lookedUp[id] {
					t.Fatalf("series %s was never looked up", id)
				}
			}
		})
	}
}

// A source whose played listing fits the byte limit but decodes into millions
// of values fails the run with the size-limit message, writes nothing, and
// does not make the server allocate the decoded listing. Not parallel,
// because it measures allocations.
func TestImportRunFailsOnExpandingSourceListing(t *testing.T) {
	for _, source := range limitsSources {
		t.Run(source.name, func(t *testing.T) {
			repo := limitsRepository(t)
			observed := newLimitsObserved()
			input, discoverURL := source.start(t, repo, observed, nil, source.expanding)

			var run Run
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			run = runLimitsImport(t, repo, input, discoverURL)
			runtime.ReadMemStats(&after)

			if run.Status != RunStatusFailed || run.ErrorMessage != RunErrorSourceTooLarge {
				t.Fatalf("run = %s (%q), want failed with %q", run.Status, run.ErrorMessage, RunErrorSourceTooLarge)
			}
			if history := limitsHistory(t, repo); len(history) != 0 {
				t.Fatalf("history = %v, want none", history)
			}
			// Decoding the listing used to allocate several GiB.
			if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 256<<20 {
				t.Fatalf("run allocated %d MiB for a %d MiB listing", allocated>>20, len(source.expanding)>>20)
			}
		})
	}
}
