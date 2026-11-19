package historyimport

import (
	"context"
	"encoding/json"
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
// filter. Since Emby 4.6 merged Next Up into it, the list also shows the next
// unstarted episode of a show between episodes (nextUp).
type fakeEmby struct {
	t         *testing.T
	played    []embyItem
	resumable []embyItem
	nextUp    []embyItem // unstarted next episodes of shows between episodes
	hidden    []string   // resumable or next-up item IDs hidden from Continue Watching; Emby hides every episode of a series at once
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
		for _, item := range f.nextUp {
			if !slices.Contains(types, item.Type) || slices.Contains(f.hidden, item.ID) || nextUp[item.SeriesID] {
				continue
			}
			nextUp[item.SeriesID] = true
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
