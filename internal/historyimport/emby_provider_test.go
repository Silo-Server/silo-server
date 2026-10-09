package historyimport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// fakeEmby answers /Users/{id}/Items and /Users/{id}/Items/Resume the way
// Emby 4.8 through 4.10 do: lists page by StartIndex and Limit, and
// ProductionYear, UserData.PlayCount, and UserData.LastPlayedDate stay empty
// unless Fields names them. The IsResumable filter returns every resumable
// item, while the Resume list leaves out the ones hidden from Continue
// Watching, shows one episode per series, and returns nothing without a type
// filter.
type fakeEmby struct {
	t         *testing.T
	played    []embyItem
	resumable []embyItem
	hidden    []string // resumable item IDs hidden from Continue Watching; Emby hides every resumable episode of a series at once
	favorites []embyItem
	series    []embyItem
	failures  map[string]int // Filters value, "Ids", or "Resume", answered with this status
	pageCap   int            // when set, pages hold at most this many items

	mu       sync.Mutex
	requests []url.Values
}

func (f *fakeEmby) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	f.mu.Lock()
	f.requests = append(f.requests, query)
	f.mu.Unlock()

	key := query.Get("Filters")
	var items []embyItem
	switch {
	case strings.HasSuffix(r.URL.Path, "/Items/Resume"):
		key = "Resume"
		if query.Get("IncludeItemTypes") == "" && query.Get("MediaTypes") == "" {
			break
		}
		// The row shows one next-up episode per series, here the first one;
		// other in-progress episodes of that series are left out.
		types := strings.Split(query.Get("IncludeItemTypes"), ",")
		nextUp := map[string]bool{}
		for _, item := range f.resumable {
			if !slices.Contains(types, item.Type) || slices.Contains(f.hidden, item.ID) {
				continue
			}
			if item.Type == "Episode" {
				if nextUp[item.SeriesID] {
					continue
				}
				nextUp[item.SeriesID] = true
			}
			items = append(items, item)
		}
	case query.Get("Ids") != "":
		key = "Ids"
		ids := strings.Split(query.Get("Ids"), ",")
		for _, item := range f.series {
			if slices.Contains(ids, item.ID) {
				items = append(items, item)
			}
		}
	case key == "IsPlayed":
		items = f.played
	case key == "IsResumable":
		items = f.resumable
	case key == "IsFavorite":
		items = f.favorites
	default:
		f.t.Errorf("unexpected Emby query %s", r.URL.RawQuery)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if status := f.failures[key]; status != 0 {
		http.Error(w, "upstream failure", status)
		return
	}

	total := len(items)
	if limit := query.Get("Limit"); limit != "" {
		start, _ := strconv.Atoi(query.Get("StartIndex"))
		n, _ := strconv.Atoi(limit)
		if f.pageCap > 0 {
			n = min(n, f.pageCap)
		}
		items = items[min(start, total):min(start+n, total)]
	}
	fields := strings.Split(query.Get("Fields"), ",")
	out := make([]embyItem, 0, len(items))
	for _, item := range items {
		if !slices.Contains(fields, "ProductionYear") {
			item.ProductionYear = 0
		}
		if !slices.Contains(fields, "UserDataPlayCount") {
			item.UserData.PlayCount = 0
		}
		if !slices.Contains(fields, "UserDataLastPlayedDate") {
			item.UserData.LastPlayedDate = nil
		}
		out = append(out, item)
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(embyItemsResponse{Items: out, TotalRecordCount: total}); err != nil {
		f.t.Errorf("encode response: %v", err)
	}
}

func (f *fakeEmby) requestsFor(key string) []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	var matched []url.Values
	for _, query := range f.requests {
		if query.Get("Filters") == key || (key == "Ids" && query.Get("Ids") != "") {
			matched = append(matched, query)
		}
	}
	return matched
}

func (f *fakeEmby) provider(t *testing.T) *EmbyProvider {
	t.Helper()
	f.t = t
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	client := NewEmbyClient()
	client.limiter = newUpstreamRateLimiter(rate.Inf, 1)
	return NewEmbyProvider(client, embyLocalAuth{BaseURL: server.URL, UserID: "emby-user", AccessToken: "token"})
}

func playedEmbyItem(item embyItem, lastPlayed time.Time, count int) embyItem {
	item.UserData.Played = true
	item.UserData.PlayCount = count
	item.UserData.LastPlayedDate = &lastPlayed
	return item
}

func fetchEmbyRecords(t *testing.T, provider *EmbyProvider) (map[string]Record, []string) {
	t.Helper()
	records, warnings, err := provider.Fetch(trustLoopback(context.Background()))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	byID := make(map[string]Record, len(records))
	for _, record := range records {
		byID[record.ExternalID] = record
	}
	return byID, warnings
}

var embySeverance = embyItem{ID: "series-1", Name: "Severance", Type: "Series", ProductionYear: 2022, ProviderIDs: map[string]string{"Tvdb": "371980", "Tmdb": "95396"}}

func TestEmbyProviderFetchImportsWatchDatesYearsAndPlayCounts(t *testing.T) {
	t.Parallel()

	moviePlayed := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	episodePlayed := time.Date(2025, 6, 4, 12, 0, 0, 0, time.UTC)
	resumed := time.Date(2026, 9, 20, 8, 30, 0, 0, time.UTC)
	fake := &fakeEmby{
		played: []embyItem{
			playedEmbyItem(embyItem{ID: "movie-1", Name: "Arrival", Type: "Movie", ProductionYear: 2016, RunTimeTicks: 18_000_000_000, ProviderIDs: map[string]string{"Tmdb": "329865"}}, moviePlayed, 3),
			playedEmbyItem(embyItem{ID: "ep-1", Name: "Good News About Hell", Type: "Episode", ProductionYear: 2022, SeriesID: "series-1", SeriesName: "Severance", ParentIndexNumber: 1, IndexNumber: 1}, episodePlayed, 1),
		},
		resumable: []embyItem{func() embyItem {
			item := embyItem{ID: "movie-2", Name: "Dune", Type: "Movie", ProductionYear: 2021, RunTimeTicks: 18_000_000_000, ProviderIDs: map[string]string{"Tmdb": "438631"}}
			item.UserData.PlaybackPositionTicks = 6_000_000_000
			item.UserData.PlayCount = 1
			item.UserData.LastPlayedDate = &resumed
			return item
		}()},
		series: []embyItem{embySeverance},
	}

	records, warnings := fetchEmbyRecords(t, fake.provider(t))
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	movie := records["movie-1"]
	if movie.LastPlayedAt == nil || !movie.LastPlayedAt.Equal(moviePlayed) || !movie.UpdatedAt.Equal(moviePlayed) {
		t.Fatalf("movie dates = last:%v updated:%v, want %v", movie.LastPlayedAt, movie.UpdatedAt, moviePlayed)
	}
	if movie.Year != 2016 || movie.PlayCount != 3 {
		t.Fatalf("movie year/count = %d/%d, want 2016/3", movie.Year, movie.PlayCount)
	}
	episode := records["ep-1"]
	if episode.LastPlayedAt == nil || !episode.LastPlayedAt.Equal(episodePlayed) {
		t.Fatalf("episode LastPlayedAt = %v, want %v", episode.LastPlayedAt, episodePlayed)
	}
	if episode.SeriesYear != 2022 || episode.SeriesTVDBID != "371980" {
		t.Fatalf("episode series year/tvdb = %d/%q, want 2022/371980", episode.SeriesYear, episode.SeriesTVDBID)
	}
	partial := records["movie-2"]
	if partial.Played || partial.PositionSeconds != 600 || !partial.UpdatedAt.Equal(resumed) {
		t.Fatalf("resumable movie = played:%v pos:%v updated:%v, want false/600/%v", partial.Played, partial.PositionSeconds, partial.UpdatedAt, resumed)
	}
}

func TestEmbyProviderFetchMarksItemsHiddenFromContinueWatching(t *testing.T) {
	t.Parallel()

	stopped := time.Date(2026, 10, 8, 19, 59, 13, 0, time.UTC)
	resumable := func(id, name, tmdb string, positionTicks int64) embyItem {
		item := embyItem{ID: id, Name: name, Type: "Movie", RunTimeTicks: 6_000_000_000, ProviderIDs: map[string]string{"Tmdb": tmdb}}
		item.UserData.PlaybackPositionTicks = positionTicks
		item.UserData.PlayCount = 1
		item.UserData.LastPlayedDate = &stopped
		return item
	}
	episode := func(id, seriesID string, number int) embyItem {
		item := resumable(id, "Episode", "", 1_000_000_000)
		item.Type, item.SeriesID, item.SeriesName, item.ParentIndexNumber, item.IndexNumber = "Episode", seriesID, "Severance", 1, number
		return item
	}
	fake := &fakeEmby{
		resumable: []embyItem{
			resumable("9", "Alien", "348", 2_700_000_000),
			resumable("8", "Heat", "949", 2_400_000_000),
			episode("ep-1", "series-1", 1),
			episode("ep-2", "series-1", 2),
			episode("ep-3", "series-2", 1),
			episode("ep-4", "series-2", 2),
			episode("ep-5", "", 1),
		},
		hidden: []string{"8", "ep-3", "ep-4", "ep-5"},
		series: []embyItem{embySeverance},
	}

	records, warnings := fetchEmbyRecords(t, fake.provider(t))
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if visible := records["9"]; visible.HiddenFromResume || visible.PositionSeconds != 270 || !visible.UpdatedAt.Equal(stopped) {
		t.Fatalf("visible resumable movie = hidden:%v pos:%v updated:%v, want false/270/%v", visible.HiddenFromResume, visible.PositionSeconds, visible.UpdatedAt, stopped)
	}
	// The hidden movie keeps its position and play date; the import dismisses
	// it from Continue Watching instead of dropping it.
	if hidden := records["8"]; !hidden.HiddenFromResume || hidden.PositionSeconds != 240 || !hidden.UpdatedAt.Equal(stopped) {
		t.Fatalf("hidden resumable movie = hidden:%v pos:%v updated:%v, want true/240/%v", hidden.HiddenFromResume, hidden.PositionSeconds, hidden.UpdatedAt, stopped)
	}

	// Emby's row shows only one episode of a series, so an episode missing
	// from it isn't hidden while its series is listed; a series missing from
	// it is hidden with every resumable episode.
	if records["ep-1"].HiddenFromResume || records["ep-2"].HiddenFromResume {
		t.Fatalf("listed series episodes marked hidden = %v/%v, want neither", records["ep-1"].HiddenFromResume, records["ep-2"].HiddenFromResume)
	}
	if !records["ep-3"].HiddenFromResume || !records["ep-4"].HiddenFromResume {
		t.Fatalf("hidden series episodes marked hidden = %v/%v, want both", records["ep-3"].HiddenFromResume, records["ep-4"].HiddenFromResume)
	}
	// Without a series an episode can't be looked up in the row.
	if records["ep-5"].HiddenFromResume {
		t.Fatal("episode without a series marked hidden, want not hidden")
	}

	// Without Emby's resume list nothing can be told apart: the import keeps
	// every position, hides nothing, and says why.
	fake.failures = map[string]int{"Resume": http.StatusInternalServerError}
	records, warnings = fetchEmbyRecords(t, fake.provider(t))
	if !slices.Equal(warnings, []string{warnEmbyResumeListUnavailable}) {
		t.Fatalf("warnings = %v, want %q", warnings, warnEmbyResumeListUnavailable)
	}
	if records["8"].HiddenFromResume || records["8"].PositionSeconds != 240 {
		t.Fatalf("hidden movie without resume list = %+v, want imported and not hidden", records["8"])
	}
	if records["ep-3"].HiddenFromResume || records["ep-4"].HiddenFromResume {
		t.Fatalf("hidden series episodes without resume list = %v/%v, want neither", records["ep-3"].HiddenFromResume, records["ep-4"].HiddenFromResume)
	}
}

func TestEmbyProviderFetchImportsEpisodeFavoritesAndReportsSeasons(t *testing.T) {
	t.Parallel()

	fake := &fakeEmby{
		played: []embyItem{
			playedEmbyItem(embyItem{ID: "movie-1", Name: "Arrival", Type: "Movie", ProviderIDs: map[string]string{"Tmdb": "329865"}}, time.Now(), 1),
		},
		favorites: []embyItem{
			{ID: "movie-1", Name: "Arrival", Type: "Movie", ProviderIDs: map[string]string{"Tmdb": "329865"}},
			{ID: "series-1", Name: "Severance", Type: "Series", ProviderIDs: map[string]string{"Tvdb": "371980"}},
			{ID: "season-1", Name: "Season 1", Type: "Season", SeriesID: "series-1", IndexNumber: 1},
			{ID: "ep-2", Name: "Half Loop", Type: "Episode", SeriesID: "series-1", SeriesName: "Severance", ParentIndexNumber: 1, IndexNumber: 2},
		},
		series: []embyItem{embySeverance},
	}

	records, warnings := fetchEmbyRecords(t, fake.provider(t))
	if got := fake.requestsFor("IsFavorite")[0].Get("IncludeItemTypes"); got != "Movie,Series,Season,Episode" {
		t.Fatalf("favorite IncludeItemTypes = %q", got)
	}
	if len(records) != 3 {
		t.Fatalf("records = %d (%v), want movie, series, and episode", len(records), records)
	}
	if movie := records["movie-1"]; !movie.Favorite || movie.FavoriteOnly || !movie.Played {
		t.Fatalf("played favorite movie = %+v, want favorite with watch state", movie)
	}
	if series := records["series-1"]; series.Kind != KindSeries || !series.FavoriteOnly || !series.PreferTMDB {
		t.Fatalf("series favorite = %+v", series)
	}
	episode := records["ep-2"]
	if episode.Kind != KindEpisode || !episode.FavoriteOnly || episode.SeriesTVDBID != "371980" || episode.EpisodeNumber != 2 {
		t.Fatalf("episode favorite = %+v, want favorite-only S01E02 with series identity", episode)
	}
	if _, ok := records["season-1"]; ok {
		t.Fatal("season favorite became a record")
	}
	if !slices.Equal(warnings, []string{fmt.Sprintf(warnEmbySeasonFavorites, 1)}) {
		t.Fatalf("warnings = %v, want the skipped season favorite", warnings)
	}
}

func TestEmbyProviderFetchContinuesWhenFavoritesFail(t *testing.T) {
	t.Parallel()

	fake := &fakeEmby{
		played:   []embyItem{playedEmbyItem(embyItem{ID: "movie-1", Name: "Arrival", Type: "Movie", ProviderIDs: map[string]string{"Tmdb": "329865"}}, time.Now(), 1)},
		failures: map[string]int{"IsFavorite": http.StatusInternalServerError},
	}
	records, warnings := fetchEmbyRecords(t, fake.provider(t))
	if len(records) != 1 || records["movie-1"].ExternalID == "" {
		t.Fatalf("records = %+v, want played movie preserved", records)
	}
	if !slices.Equal(warnings, []string{warnEmbyFavoritesUnavailable}) {
		t.Fatalf("warnings = %v, want the fixed favorites warning without the upstream body", warnings)
	}
}

func TestEmbyProviderFetchContinuesWhenSeriesMetadataFails(t *testing.T) {
	t.Parallel()

	fake := &fakeEmby{
		played: []embyItem{playedEmbyItem(embyItem{
			ID: "ep-1", Name: "Kassa", Type: "Episode", SeriesID: "series-2", SeriesName: "Andor",
			ParentIndexNumber: 1, IndexNumber: 1, ProviderIDs: map[string]string{"Tvdb": "9000101"},
		}, time.Now(), 1)},
		failures: map[string]int{"Ids": http.StatusRequestURITooLong},
	}
	records, warnings := fetchEmbyRecords(t, fake.provider(t))
	if episode := records["ep-1"]; episode.TVDBID != "9000101" || !episode.Played {
		t.Fatalf("episode = %+v, want it imported by its own ID", episode)
	}
	if !slices.Equal(warnings, []string{warnEmbySeriesUnavailable}) {
		t.Fatalf("warnings = %v, want the fixed series warning without the upstream body", warnings)
	}
}

func TestEmbyClientPagesItemLists(t *testing.T) {
	t.Parallel()

	fake := &fakeEmby{}
	for i := range 1100 {
		fake.played = append(fake.played, playedEmbyItem(embyItem{ID: fmt.Sprintf("movie-%d", i), Type: "Movie"}, time.Now(), 1))
	}
	records, _ := fetchEmbyRecords(t, fake.provider(t))
	if len(records) != 1100 {
		t.Fatalf("records = %d, want 1100", len(records))
	}
	var starts []string
	for _, query := range fake.requestsFor("IsPlayed") {
		if query.Get("Limit") != strconv.Itoa(embyPageSize) {
			t.Fatalf("Limit = %q, want %d", query.Get("Limit"), embyPageSize)
		}
		starts = append(starts, query.Get("StartIndex"))
	}
	if !slices.Equal(starts, []string{"0", "500", "1000"}) {
		t.Fatalf("StartIndex pages = %v", starts)
	}
}

func TestEmbyClientReadsPastServerPageCap(t *testing.T) {
	t.Parallel()

	fake := &fakeEmby{pageCap: 100}
	for i := range 250 {
		fake.played = append(fake.played, playedEmbyItem(embyItem{ID: fmt.Sprintf("movie-%d", i), Type: "Movie"}, time.Now(), 1))
	}
	records, _ := fetchEmbyRecords(t, fake.provider(t))
	if len(records) != 250 {
		t.Fatalf("records = %d, want 250 across capped pages", len(records))
	}
}

func TestEmbyClientChunksItemIDLookups(t *testing.T) {
	t.Parallel()

	fake := &fakeEmby{}
	ids := make([]string, 250)
	for i := range ids {
		ids[i] = fmt.Sprintf("series-%d", i)
		fake.series = append(fake.series, embyItem{ID: ids[i], Type: "Series"})
	}
	provider := fake.provider(t)
	items, err := provider.client.FetchItemsByIDs(trustLoopback(context.Background()), provider.auth, ids, "Series")
	if err != nil {
		t.Fatalf("FetchItemsByIDs: %v", err)
	}
	if len(items) != 250 {
		t.Fatalf("items = %d, want 250", len(items))
	}
	requests := fake.requestsFor("Ids")
	if len(requests) != 3 {
		t.Fatalf("requests = %d, want 3", len(requests))
	}
	for _, query := range requests {
		if n := len(strings.Split(query.Get("Ids"), ",")); n > embyIDChunkSize {
			t.Fatalf("request carried %d ids, want at most %d", n, embyIDChunkSize)
		}
	}
}

func TestEmbyWatchedRecordsExpandsPlayedEpisodeRanges(t *testing.T) {
	t.Parallel()

	file := embyItem{
		ID: "ep-1", Name: "Pilot", Type: "Episode", SeriesName: "Andor", RunTimeTicks: 36_000_000_000,
		ParentIndexNumber: 1, IndexNumber: 1, IndexNumberEnd: 3, ProviderIDs: map[string]string{"Tvdb": "9000101"},
	}
	played := playedEmbyItem(file, time.Now(), 1)
	records := embyWatchedRecords(played, embyItem{ProviderIDs: map[string]string{"Tvdb": "393189"}})
	if len(records) != 3 {
		t.Fatalf("records = %d, want 3", len(records))
	}
	for i, record := range records {
		if record.EpisodeNumber != i+1 || record.DurationSeconds != 1200 || record.SeriesTVDBID != "393189" || !record.Played {
			t.Fatalf("record %d = %+v", i, record)
		}
	}
	if records[0].TVDBID != "9000101" || records[1].TVDBID != "" || records[1].ExternalID != "ep-1#E2" {
		t.Fatalf("range IDs = %q/%q/%q, want the episode ID on the first only", records[0].TVDBID, records[1].TVDBID, records[1].ExternalID)
	}

	partial := file
	partial.UserData.PlaybackPositionTicks = 6_000_000_000
	if got := embyWatchedRecords(partial, embyItem{}); len(got) != 1 {
		t.Fatalf("partly watched range = %d records, want 1", len(got))
	}
	wide := played
	wide.IndexNumberEnd = wide.IndexNumber + maxEmbyEpisodeRange
	if got := embyWatchedRecords(wide, embyItem{}); len(got) != 1 {
		t.Fatalf("over-wide range = %d records, want 1", len(got))
	}
}

func TestEmbyProviderKeepsRangeRuntimeWhenFileIsFavorite(t *testing.T) {
	t.Parallel()

	file := playedEmbyItem(embyItem{
		ID: "ep-1", Type: "Episode", SeriesID: "series-2", RunTimeTicks: 36_000_000_000,
		ParentIndexNumber: 1, IndexNumber: 1, IndexNumberEnd: 2,
	}, time.Now(), 1)
	fake := &fakeEmby{played: []embyItem{file}, favorites: []embyItem{file}}
	records, _ := fetchEmbyRecords(t, fake.provider(t))
	first := records["ep-1"]
	if !first.Favorite || first.FavoriteOnly || first.DurationSeconds != 1800 {
		t.Fatalf("favorited range start = %+v, want a favorite keeping half the runtime", first)
	}
	if second := records["ep-1#E2"]; second.DurationSeconds != 1800 || !second.Played {
		t.Fatalf("range second episode = %+v", second)
	}
}

func TestNormalizeEmbyItemWithoutLastPlayedDateHasNoFreshnessTimestamp(t *testing.T) {
	t.Parallel()

	item := embyItem{
		ID:           "emby-episode-1",
		Name:         "Stale partial",
		Type:         "Episode",
		SeriesName:   "The Show",
		SeriesID:     "emby-series-1",
		RunTimeTicks: 3_000_000_000,
	}
	item.UserData.PlaybackPositionTicks = 1_200_000_000

	record := normalizeEmbyItem(item, embyItem{Name: "The Show"})

	if !record.UpdatedAt.IsZero() {
		t.Fatalf("UpdatedAt = %v, want zero for resumable without LastPlayedDate", record.UpdatedAt)
	}
	if record.PositionSeconds != 120 {
		t.Fatalf("PositionSeconds = %v, want 120", record.PositionSeconds)
	}
}
