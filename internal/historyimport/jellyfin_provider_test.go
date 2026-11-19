package historyimport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Without a LastPlayedDate the record must not claim to be newer than local
// activity: a zero UpdatedAt sorts before any Silo progress on re-import.
func TestNormalizeJellyfinItem_MissingLastPlayedDateLeavesUpdatedAtZero(t *testing.T) {
	record := normalizeJellyfinItem(jellyfinItem{ID: "m1", Type: "Movie", UserData: jellyfinUserData{Played: true}}, jellyfinItem{})
	if !record.UpdatedAt.IsZero() || record.LastPlayedAt != nil {
		t.Fatalf("UpdatedAt = %v, LastPlayedAt = %v; want zero and nil", record.UpdatedAt, record.LastPlayedAt)
	}
}

func TestJellyfinProviderFetch_MergesDuplicates(t *testing.T) {
	first := Record{ExternalID: "1", Kind: KindMovie, Title: "Movie", Played: false, PlayCount: 1, PositionSeconds: 10, DurationSeconds: 100, UpdatedAt: time.Unix(1, 0).UTC()}
	second := Record{ExternalID: "1", Kind: KindMovie, Title: "Movie", Played: true, PlayCount: 2, PositionSeconds: 20, DurationSeconds: 120, UpdatedAt: time.Unix(2, 0).UTC()}
	merged := mergeRecords(first, second)
	if !merged.Played || merged.PlayCount != 2 || merged.PositionSeconds != 20 || merged.DurationSeconds != 120 || !merged.UpdatedAt.Equal(time.Unix(2, 0).UTC()) {
		t.Fatalf("unexpected merged record: %+v", merged)
	}
}

// Treating a resume failure as a warning must not hide a cancellation.
func TestJellyfinProviderFetch_ResumeCancellationStopsTheRun(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(trustLoopback(context.Background()))
	defer cancel()
	fetch := newJellyfinFetchHandler(t, map[string][]jellyfinItem{
		"IsPlayed":   {{ID: "matrix", Type: "Movie", Name: "The Matrix", UserData: jellyfinUserData{Played: true}}},
		"IsFavorite": {},
	}, nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/UserItems/Resume" {
			cancel()
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		fetch.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)

	records, _, err := NewJellyfinProvider(newUnthrottledJellyfinClient(), jellyfinLocalAuth{BaseURL: server.URL, UserID: "user-1", AccessToken: "token-1"}).Fetch(ctx)
	if err == nil {
		t.Fatalf("Fetch returned %d records and no error, want the run to stop on cancellation", len(records))
	}
}

// newJellyfinFetchServer serves newJellyfinFetchHandler.
func newJellyfinFetchServer(t *testing.T, byFilter map[string][]jellyfinItem, byID map[string]jellyfinItem) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(newJellyfinFetchHandler(t, byFilter, byID))
	t.Cleanup(server.Close)
	return server
}

// newJellyfinFetchHandler serves /Items by filter (a filter absent from
// byFilter answers 500), an empty /UserItems/Resume, and /Items?Ids= lookups
// from byID (a nil byID answers 500).
func newJellyfinFetchHandler(t *testing.T, byFilter map[string][]jellyfinItem, byID map[string]jellyfinItem) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertJellyfinAuthorization(t, r, "token-1")
		query := r.URL.Query()
		var items []jellyfinItem
		switch {
		case r.URL.Path == "/UserItems/Resume":
		case r.URL.Path == "/Items" && query.Get("Ids") != "":
			if byID == nil {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			for id := range strings.SplitSeq(query.Get("Ids"), ",") {
				if item, ok := byID[id]; ok {
					items = append(items, item)
				}
			}
		case r.URL.Path == "/Items":
			if query.Get("UserId") != "user-1" {
				t.Errorf("UserId = %q, want user-1", query.Get("UserId"))
			}
			found, ok := byFilter[query.Get("Filters")]
			if !ok {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			if query.Get("Filters") == "IsFavorite" && query.Get("IncludeItemTypes") != "Movie,Series,Episode" {
				t.Errorf("favorite IncludeItemTypes = %q", query.Get("IncludeItemTypes"))
			}
			items = found
		default:
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jellyfinItemsResponse{Items: items, TotalRecordCount: len(items)})
	})
}

// A Jellyfin error body can echo the request's credentials in forms the text
// redactor misses, so warning logs keep only the HTTP status.
func TestJellyfinWarningLogErrorDropsResponseBody(t *testing.T) {
	t.Parallel()

	body := `{"Authorization":"MediaBrowser Client=\"watch-importer\", Token=\"secret-token-1\""}`
	err := fmt.Errorf("fetching Jellyfin resumable items: %w", &jellyfinHTTPError{StatusCode: http.StatusBadGateway, Body: body})
	if got := jellyfinWarningLogError(err); got != "jellyfin http 502" {
		t.Fatalf("jellyfinWarningLogError = %q, want only the status", got)
	}
	err = fmt.Errorf("fetching Emby Items/Resume: %w", &embyHTTPError{StatusCode: http.StatusInternalServerError, Body: body})
	if got := warningLogError("emby", err); got != "emby http 500" {
		t.Fatalf("warningLogError(emby) = %q, want only the status", got)
	}
}
