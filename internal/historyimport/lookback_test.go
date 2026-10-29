package historyimport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
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

// The lookback read sorts by play date, keeps items played at or after the
// cutoff (inclusive), and stops after the first page with only older plays
// without reading the rest of the history.
func TestJellyfinFetchPlayedSince_StopsAtCutoff(t *testing.T) {
	t.Parallel()

	since := time.Date(2026, 10, 14, 4, 0, 0, 0, time.UTC)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		assertJellyfinAuthorization(t, r, "token-1")
		q := r.URL.Query()
		for key, want := range map[string]string{
			"UserId": "user-1", "Filters": "IsPlayed", "IncludeItemTypes": "Movie,Episode", "Recursive": "true",
			"EnableUserData": "true", "Fields": "ProviderIds", "SortBy": "DatePlayed", "SortOrder": "Descending",
			"Limit": strconv.Itoa(jellyfinPageSize),
		} {
			if got := q.Get(key); got != want {
				t.Errorf("%s = %q, want %q", key, got, want)
			}
		}
		switch q.Get("StartIndex") {
		case "0":
			page := make([]jellyfinItem, jellyfinPageSize)
			for i := range page {
				page[i] = jellyfinItem{ID: "new-" + strconv.Itoa(i), Type: "Movie", UserData: playedAt(since.Add(time.Duration(48*60-i) * time.Minute))}
			}
			writeJellyfinItems(t, w, 100000, page)
		case strconv.Itoa(jellyfinPageSize):
			page := []jellyfinItem{
				{ID: "recent", Type: "Episode", UserData: playedAt(since.Add(time.Minute))},
				{ID: "at-cutoff", Type: "Movie", UserData: playedAt(since)},
				{ID: "older", Type: "Movie", UserData: playedAt(since.Add(-time.Second))},
				{ID: "much-older", Type: "Movie", UserData: playedAt(since.Add(-time.Hour))},
			}
			writeJellyfinItems(t, w, 100000, append(page, make([]jellyfinItem, jellyfinPageSize-len(page))...))
		case strconv.Itoa(2 * jellyfinPageSize):
			page := make([]jellyfinItem, jellyfinPageSize)
			for i := range page {
				page[i] = jellyfinItem{ID: "old-" + strconv.Itoa(i), Type: "Movie", UserData: playedAt(since.Add(-time.Duration(i+2) * time.Hour))}
			}
			writeJellyfinItems(t, w, 100000, page)
		default:
			t.Errorf("read StartIndex %s past the cutoff", q.Get("StartIndex"))
			writeJellyfinItems(t, w, 100000, nil)
		}
	}))
	defer server.Close()

	auth := jellyfinLocalAuth{BaseURL: server.URL, UserID: "user-1", AccessToken: "token-1"}
	items, sorted, err := newUnthrottledJellyfinClient().FetchPlayedSince(trustLoopback(context.Background()), auth, since)
	if err != nil || !sorted {
		t.Fatalf("FetchPlayedSince: sorted=%v err=%v", sorted, err)
	}
	if requests != 3 {
		t.Fatalf("requests = %d, want 3", requests)
	}
	if len(items) != jellyfinPageSize+2 {
		t.Fatalf("len(items) = %d, want %d", len(items), jellyfinPageSize+2)
	}
	if last := items[len(items)-1].ID; last != "at-cutoff" {
		t.Fatalf("last item = %q, want at-cutoff", last)
	}
}

// Undated items are skipped wherever the server sorts them, and an in-window
// item after an older one on the same page is still kept.
func TestJellyfinFetchPlayedSince_SkipsUndatedAndKeepsOutOfOrderPlays(t *testing.T) {
	t.Parallel()

	since := time.Date(2026, 10, 14, 4, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJellyfinItems(t, w, 4, []jellyfinItem{
			{ID: "recent", Type: "Movie", UserData: playedAt(since.Add(time.Hour))},
			{ID: "undated", Type: "Movie", UserData: jellyfinUserData{Played: true}},
			{ID: "old", Type: "Movie", UserData: playedAt(since.Add(-time.Hour))},
			{ID: "out-of-order", Type: "Movie", UserData: playedAt(since.Add(time.Minute))},
		})
	}))
	defer server.Close()

	auth := jellyfinLocalAuth{BaseURL: server.URL, UserID: "user-1", AccessToken: "token-1"}
	items, sorted, err := newUnthrottledJellyfinClient().FetchPlayedSince(trustLoopback(context.Background()), auth, since)
	if err != nil || !sorted {
		t.Fatalf("FetchPlayedSince: sorted=%v err=%v", sorted, err)
	}
	if len(items) != 2 || items[0].ID != "recent" || items[1].ID != "out-of-order" {
		t.Fatalf("items = %+v, want recent and out-of-order", items)
	}
}

// A short last page ends the read even when every item is inside the window.
func TestJellyfinFetchPlayedSince_ShortPageEnds(t *testing.T) {
	t.Parallel()

	since := time.Date(2026, 10, 14, 4, 0, 0, 0, time.UTC)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		writeJellyfinItems(t, w, 0, []jellyfinItem{{ID: "a", Type: "Movie", UserData: playedAt(since.Add(time.Hour))}})
	}))
	defer server.Close()

	auth := jellyfinLocalAuth{BaseURL: server.URL, UserID: "user-1", AccessToken: "token-1"}
	items, _, err := newUnthrottledJellyfinClient().FetchPlayedSince(trustLoopback(context.Background()), auth, since)
	if err != nil || len(items) != 1 || requests != 1 {
		t.Fatalf("items=%d requests=%d err=%v, want 1 item in 1 request", len(items), requests, err)
	}
}

func TestJellyfinFetchPlayedSince_ErrorNamesTheWindow(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	auth := jellyfinLocalAuth{BaseURL: server.URL, UserID: "user-1", AccessToken: "token-1"}
	_, _, err := newUnthrottledJellyfinClient().FetchPlayedSince(trustLoopback(context.Background()), auth, time.Date(2026, 10, 14, 4, 0, 0, 0, time.UTC))
	var httpErr *jellyfinHTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("err = %v, want the upstream 500", err)
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
	firstPages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, _ := strconv.Atoi(r.URL.Query().Get("StartIndex"))
		end := min(start+jellyfinPageSize, len(list))
		writeJellyfinItems(t, w, len(list), list[start:end])
		if start == 0 {
			firstPages++
			if firstPages == 1 {
				// item-250, not read yet, is watched again.
				moved := list[250]
				moved.UserData = playedAt(since.Add(time.Hour + time.Minute))
				list = append([]jellyfinItem{moved}, append(list[:250:250], list[251:]...)...)
			}
		}
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
	if len(items) != 300 || len(ids) != 300 || ids["item-250"] != 1 || firstPages != 2 {
		t.Fatalf("items = %d (%d distinct), item-250 = %d, first-page reads = %d; want all 300 plays once after one restart", len(items), len(ids), ids["item-250"], firstPages)
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
	// The discarded lookback pages must not stay charged on top of the full
	// read, or a history that fits a full import fails the fallback.
	var want int64
	for _, item := range full {
		want += item.retainedSize()
	}
	if got := runBudgetFrom(ctx).retained; got != want {
		t.Fatalf("retained = %d bytes, want %d for the full read alone", got, want)
	}
	if len(warnings) != 1 || PublicWarning(warnings[0]) != jellyfinLookbackUnsortedSummary {
		t.Fatalf("warnings = %v, want the unsorted-lookback warning", warnings)
	}
}

// A lookback run never issues the unsorted full played read, and still imports
// resume positions and favorites in full.
func TestJellyfinProviderFetch_LookbackReadsRecentPlaysOnly(t *testing.T) {
	t.Parallel()

	since := time.Date(2026, 10, 14, 4, 0, 0, 0, time.UTC)
	position := since.Add(-30 * 24 * time.Hour)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertJellyfinAuthorization(t, r, "token-1")
		q := r.URL.Query()
		switch {
		case r.URL.Path == "/UserItems/Resume":
			writeJellyfinItems(t, w, 1, []jellyfinItem{{ID: "half-watched", Type: "Movie", ProviderIDs: map[string]string{"Tmdb": "2"}, RunTimeTicks: 60 * 10_000_000, UserData: jellyfinUserData{PlaybackPositionTicks: 30 * 10_000_000, LastPlayedDate: &position}}})
		case r.URL.Path == "/Items" && q.Get("Filters") == "IsPlayed":
			if q.Get("SortBy") != "DatePlayed" || q.Get("SortOrder") != "Descending" {
				t.Errorf("lookback run issued the full played read: %s", r.URL.RawQuery)
			}
			writeJellyfinItems(t, w, 2, []jellyfinItem{
				{ID: "recent", Type: "Movie", ProviderIDs: map[string]string{"Tmdb": "1"}, UserData: playedAt(since.Add(time.Hour))},
				{ID: "old", Type: "Movie", ProviderIDs: map[string]string{"Tmdb": "3"}, UserData: playedAt(since.Add(-time.Hour))},
			})
		case r.URL.Path == "/Items" && q.Get("Filters") == "IsFavorite":
			writeJellyfinItems(t, w, 1, []jellyfinItem{{ID: "favorite", Type: "Movie", ProviderIDs: map[string]string{"Tmdb": "4"}, UserData: jellyfinUserData{IsFavorite: true}}})
		default:
			t.Errorf("unexpected request %s?%s", r.URL.Path, r.URL.RawQuery)
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	auth := jellyfinLocalAuth{BaseURL: server.URL, UserID: "user-1", AccessToken: "token-1"}
	records, warnings, err := NewJellyfinProvider(newUnthrottledJellyfinClient(), auth).WithPlayedSince(&since).Fetch(trustLoopback(context.Background()))
	if err != nil || len(warnings) != 0 {
		t.Fatalf("Fetch: err=%v warnings=%v", err, warnings)
	}
	got := map[string]bool{}
	for _, record := range records {
		got[record.ExternalID] = true
	}
	if !got["recent"] || !got["half-watched"] || !got["favorite"] || got["old"] || len(got) != 3 {
		t.Fatalf("records = %v, want recent, half-watched, favorite", got)
	}
}

func TestBuildAdminProvider_PassesLookbackToJellyfin(t *testing.T) {
	t.Parallel()

	s := &Service{}
	since := time.Date(2026, 10, 14, 4, 0, 0, 0, time.UTC)
	provider, err := s.buildAdminProvider(&Source{SourceType: SourceTypeJellyfin, BaseURL: "https://example.invalid"}, "token", "user", &since)
	if err != nil {
		t.Fatal(err)
	}
	jellyfin, ok := provider.(*JellyfinProvider)
	if !ok || jellyfin.playedSince == nil || !jellyfin.playedSince.Equal(since) {
		t.Fatalf("provider = %#v, want a Jellyfin provider reading since %v", provider, since)
	}
}

// The cutoff is stored with the queued run and handed back on claim, so a run
// that waits in the queue still reads the window it was queued for.
func TestQueueStoresLookbackCutoffForTheRunner(t *testing.T) {
	repo := queueRunnerRepository(t)
	ctx := t.Context()
	if _, err := repo.pool.Exec(ctx, `UPDATE history_import_sources SET source_type='jellyfin'`); err != nil {
		t.Fatal(err)
	}
	since := time.Now().Add(-72 * time.Hour).Truncate(time.Microsecond)
	queued, err := repo.EnqueueAdminRun(ctx, 1, AdminRunOptions{PlayedSince: &since})
	if err != nil {
		t.Fatal(err)
	}
	// The admin read the 202 answers with shows the cutoff too.
	if queued.PlayedSince == nil || !queued.PlayedSince.Equal(since) {
		t.Fatalf("queued PlayedSince = %v, want %v", queued.PlayedSince, since)
	}
	claimed, _, err := repo.claimAdminRun(ctx)
	if err != nil || claimed == nil {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	if claimed.PlayedSince == nil || !claimed.PlayedSince.Equal(since) {
		t.Fatalf("claimed PlayedSince = %v, want %v", claimed.PlayedSince, since)
	}

	if _, err = repo.EnqueueAdminRun(ctx, 2, AdminRunOptions{}); err != nil {
		t.Fatal(err)
	}
	full, _, err := repo.claimAdminRun(ctx)
	if err != nil || full == nil {
		t.Fatalf("claim = %+v, %v", full, err)
	}
	if full.PlayedSince != nil {
		t.Fatalf("full import PlayedSince = %v, want nil", *full.PlayedSince)
	}
}

// A completed lookback run leaves the mapping's last full import time alone.
func TestQueueLookbackRunDoesNotTouchLastImported(t *testing.T) {
	repo := queueRunnerRepository(t)
	ctx := t.Context()
	if _, err := repo.pool.Exec(ctx, `UPDATE history_import_sources SET source_type='jellyfin'`); err != nil {
		t.Fatal(err)
	}
	since := time.Now().Add(-72 * time.Hour)
	if _, err := repo.EnqueueAdminRun(ctx, 1, AdminRunOptions{PlayedSince: &since}); err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := repo.claimAdminRun(ctx)
	if err != nil || claimed == nil {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	if _, err = repo.pool.Exec(ctx, `UPDATE history_import_runs SET status='completed',completed_at=now() WHERE id=$1`, claimed.ID); err != nil {
		t.Fatal(err)
	}
	if err = repo.touchCompletedMapping(ctx, claim); err != nil {
		t.Fatal(err)
	}
	var lastImported *time.Time
	if err = repo.pool.QueryRow(ctx, `SELECT last_imported_at FROM history_import_user_mappings WHERE id=1`).Scan(&lastImported); err != nil || lastImported != nil {
		t.Fatalf("last_imported_at after a lookback run = %v (%v), want unchanged NULL", lastImported, err)
	}
}

// Emby and Plex sources refuse a lookback at admission; a bulk run refuses the
// whole batch before queueing anything.
func TestQueueRefusesLookbackOffJellyfin(t *testing.T) {
	repo := queueRunnerRepository(t)
	ctx := t.Context()
	service := &Service{repo: repo}
	since := time.Now().Add(-72 * time.Hour)
	for _, sourceType := range []string{SourceTypeEmby, SourceTypePlex} {
		if _, err := repo.pool.Exec(ctx, `UPDATE history_import_sources SET source_type=$1`, sourceType); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.EnqueueAdminRun(ctx, 1, AdminRunOptions{PlayedSince: &since}); !errors.Is(err, ErrLookbackUnsupported) {
			t.Fatalf("%s enqueue: err = %v, want ErrLookbackUnsupported", sourceType, err)
		}
		if _, err := service.BulkCreateAdminRuns(ctx, 1, AdminRunOptions{PlayedSince: &since}); !errors.Is(err, ErrLookbackUnsupported) {
			t.Fatalf("%s bulk: err = %v, want ErrLookbackUnsupported", sourceType, err)
		}
	}
	var count int
	if err := repo.pool.QueryRow(ctx, `SELECT count(*) FROM history_import_runs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("runs queued = %d (%v), want 0", count, err)
	}
}
