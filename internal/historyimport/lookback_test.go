package historyimport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

func playedAt(at time.Time) jellyfinUserData {
	return jellyfinUserData{Played: true, PlayCount: 1, LastPlayedDate: &at}
}

func writeJellyfinItems(t *testing.T, w http.ResponseWriter, total int, items []jellyfinItem) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(jellyfinItemsResponse{Items: items, TotalRecordCount: total}); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

// A page that starts with a play newer than the last play on the page before
// it shows the server is not sorting by play date, so the early stop can't be
// trusted and the read reports it instead of returning a partial window.
func TestJellyfinFetchPlayedSince_UnsortedAcrossPagesIsReported(t *testing.T) {
	t.Parallel()

	since := time.Date(2026, 10, 14, 4, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := make([]jellyfinItem, jellyfinPageSize)
		for i := range page {
			// Page 1 runs from +10h down to just above the cutoff; page 2
			// starts back near the top.
			offset := time.Duration(jellyfinPageSize-i) * time.Minute
			if r.URL.Query().Get("StartIndex") != "0" {
				offset = 9 * time.Hour
			}
			page[i] = jellyfinItem{ID: "item-" + strconv.Itoa(i), Type: "Movie", UserData: playedAt(since.Add(offset))}
		}
		writeJellyfinItems(t, w, 100000, page)
	}))
	defer server.Close()

	auth := jellyfinLocalAuth{BaseURL: server.URL, UserID: "user-1", AccessToken: "token-1"}
	items, sorted, err := newUnthrottledJellyfinClient().FetchPlayedSince(trustLoopback(context.Background()), auth, since)
	if err != nil || sorted || items != nil {
		t.Fatalf("FetchPlayedSince: items=%d sorted=%v err=%v, want an unsorted report", len(items), sorted, err)
	}
}

// A page that is out of order but still holds in-window plays doesn't end the
// read, and it must not be forgotten: when a later, sorted page of only older
// plays would stop the read, the earlier disorder means plays further on may
// still be recent, so the read reports it instead of returning a partial window.
func TestJellyfinFetchPlayedSince_EarlierDisorderBlocksEarlyStop(t *testing.T) {
	t.Parallel()

	since := time.Date(2026, 10, 14, 4, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := make([]jellyfinItem, jellyfinPageSize)
		for i := range page {
			// Page 1 descends from +10h but has one recent play out of
			// order; page 2 is sorted and wholly older than the cutoff.
			offset := 10*time.Hour - time.Duration(i)*time.Minute
			if r.URL.Query().Get("StartIndex") != "0" {
				offset = -time.Duration(i+1) * time.Minute
			}
			page[i] = jellyfinItem{ID: "item-" + strconv.Itoa(i), Type: "Movie", UserData: playedAt(since.Add(offset))}
		}
		if r.URL.Query().Get("StartIndex") == "0" {
			page[5].UserData = playedAt(since.Add(11 * time.Hour))
		}
		writeJellyfinItems(t, w, 100000, page)
	}))
	defer server.Close()

	auth := jellyfinLocalAuth{BaseURL: server.URL, UserID: "user-1", AccessToken: "token-1"}
	items, sorted, err := newUnthrottledJellyfinClient().FetchPlayedSince(trustLoopback(context.Background()), auth, since)
	if err != nil || sorted || items != nil {
		t.Fatalf("FetchPlayedSince: items=%d sorted=%v err=%v, want an unsorted report", len(items), sorted, err)
	}
}

// A play during the read moves an unread item to the top of the list, so the
// next page repeats the last item read and never returns the moved one. The
// read starts over and imports it.
func TestJellyfinFetchPlayedSince_RestartsWhenAPlayMovesTheList(t *testing.T) {
	t.Parallel()

	since := time.Date(2026, 10, 14, 4, 0, 0, 0, time.UTC)
	// 300 plays in the window, newest first, then 50 older ones.
	list := make([]jellyfinItem, 350)
	for i := range list {
		list[i] = jellyfinItem{ID: "item-" + strconv.Itoa(i), Type: "Movie", UserData: playedAt(since.Add(time.Duration(300-i)*time.Minute - time.Second))}
	}
	var mu sync.Mutex
	firstPages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, _ := strconv.Atoi(r.URL.Query().Get("StartIndex"))
		mu.Lock()
		end := min(start+jellyfinPageSize, len(list))
		page, total := list[start:end], len(list)
		if start == 0 {
			firstPages++
			if firstPages == 1 {
				// item-250, not read yet, is watched again. The list moves
				// before this page is written: the client can decode the
				// page and ask for the next one before this handler returns.
				moved := list[250]
				moved.UserData = playedAt(since.Add(time.Hour + time.Minute))
				list = append([]jellyfinItem{moved}, append(list[:250:250], list[251:]...)...)
			}
		}
		mu.Unlock()
		writeJellyfinItems(t, w, total, page)
	}))
	defer server.Close()

	auth := jellyfinLocalAuth{BaseURL: server.URL, UserID: "user-1", AccessToken: "token-1"}
	items, sorted, err := newUnthrottledJellyfinClient().FetchPlayedSince(trustLoopback(context.Background()), auth, since)
	if err != nil || !sorted {
		t.Fatalf("FetchPlayedSince: sorted=%v err=%v", sorted, err)
	}
	ids := map[string]int{}
	for _, item := range items {
		ids[item.ID]++
	}
	mu.Lock()
	reads := firstPages
	mu.Unlock()
	if len(items) != 300 || len(ids) != 300 || ids["item-250"] != 1 || reads != 2 {
		t.Fatalf("items = %d (%d distinct), item-250 = %d, first-page reads = %d; want all 300 plays once after one restart", len(items), len(ids), ids["item-250"], reads)
	}
}

// When the server ignores the sort, the lookback run reads the full played
// history and says so in a run warning, so nothing recent is silently missed.
// Here the first page holds only older plays, out of order, which would
// otherwise end the read at once with nothing imported.
func TestJellyfinProviderFetch_UnsortedLookbackFallsBackToFullRead(t *testing.T) {
	t.Parallel()

	since := time.Date(2026, 10, 14, 4, 0, 0, 0, time.UTC)
	full := []jellyfinItem{
		{ID: "old", Type: "Movie", ProviderIDs: map[string]string{"Tmdb": "1"}, UserData: playedAt(since.Add(-48 * time.Hour))},
		{ID: "recent", Type: "Movie", ProviderIDs: map[string]string{"Tmdb": "4"}, UserData: playedAt(since.Add(time.Hour))},
	}
	fullReads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case r.URL.Path == "/UserItems/Resume", r.URL.Path == "/Items" && q.Get("Filters") == "IsFavorite":
			writeJellyfinItems(t, w, 0, nil)
		case r.URL.Path == "/Items" && q.Get("Filters") == "IsPlayed" && q.Get("SortBy") == "DatePlayed":
			writeJellyfinItems(t, w, 3, []jellyfinItem{
				{ID: "old", Type: "Movie", ProviderIDs: map[string]string{"Tmdb": "1"}, UserData: playedAt(since.Add(-48 * time.Hour))},
				{ID: "older-but-newer", Type: "Movie", ProviderIDs: map[string]string{"Tmdb": "2"}, UserData: playedAt(since.Add(-time.Hour))},
				{ID: "undated", Type: "Movie", ProviderIDs: map[string]string{"Tmdb": "3"}, UserData: jellyfinUserData{Played: true}},
			})
		case r.URL.Path == "/Items" && q.Get("Filters") == "IsPlayed":
			fullReads++
			writeJellyfinItems(t, w, 2, full)
		default:
			t.Errorf("unexpected request %s?%s", r.URL.Path, r.URL.RawQuery)
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	auth := jellyfinLocalAuth{BaseURL: server.URL, UserID: "user-1", AccessToken: "token-1"}
	ctx := withRunBudget(trustLoopback(context.Background()))
	records, warnings, err := NewJellyfinProvider(newUnthrottledJellyfinClient(), auth).WithPlayedSince(&since).Fetch(ctx)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if fullReads != 1 || len(records) != 2 {
		t.Fatalf("full reads = %d, records = %d, want the full played history once", fullReads, len(records))
	}
	if len(warnings) != 1 || PublicWarning(warnings[0]) != jellyfinLookbackUnsortedSummary {
		t.Fatalf("warnings = %v, want the unsorted-lookback warning", warnings)
	}
}
