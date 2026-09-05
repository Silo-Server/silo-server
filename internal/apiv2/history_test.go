package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// fakeHistory stands in for handlers.PersonalDataHandler: a keyset page over
// rows kept in (watched_at DESC, id DESC) order, cards rendered for every
// entry whose id it knows, and the removal command recorded as the seam
// received it.
type fakeHistory struct {
	entries []userstore.WatchHistoryEntry
	cards   map[string]handlers.CollectionItemView
	removed [][]handlers.HistoryRemovalTarget
	pages   []*userstore.HistoryKey
	err     error
}

func historyRows() []userstore.WatchHistoryEntry {
	return []userstore.WatchHistoryEntry{
		{ID: "h3", ProfileID: "p-owner", MediaItemID: "episode:heat-s01e02", WatchedAt: "2026-01-03T00:00:00Z", DurationSeconds: 2600, Completed: true, Source: userstore.WatchHistorySourcePlayback},
		{ID: "h2", ProfileID: "p-owner", MediaItemID: "movie:heat-1995", WatchedAt: "2026-01-02T03:04:05Z", DurationSeconds: 5400, Completed: true, Source: userstore.WatchHistorySourceManual},
		{ID: "h1", ProfileID: "p-owner", MediaItemID: "movie:hidden", WatchedAt: "2026-01-01T00:00:00Z", DurationSeconds: 100, Completed: false},
	}
}

func (f *fakeHistory) HistoryPage(_ context.Context, _ int, profileID string, after *userstore.HistoryKey, limit int) ([]userstore.WatchHistoryEntry, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.pages = append(f.pages, after)
	sorted := append([]userstore.WatchHistoryEntry(nil), f.entries...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].WatchedAt != sorted[j].WatchedAt {
			return sorted[i].WatchedAt > sorted[j].WatchedAt
		}
		return sorted[i].ID > sorted[j].ID
	})
	var rows []userstore.WatchHistoryEntry
	for _, e := range sorted {
		if e.ProfileID != profileID {
			continue
		}
		if after != nil && (e.WatchedAt > after.WatchedAt || (e.WatchedAt == after.WatchedAt && e.ID >= after.ID)) {
			continue
		}
		rows = append(rows, e)
		if len(rows) == limit {
			break
		}
	}
	return rows, nil
}

func (f *fakeHistory) HistoryCards(_ context.Context, _ handlers.SectionViewer, entries []userstore.WatchHistoryEntry) ([]handlers.HistoryCardView, error) {
	var out []handlers.HistoryCardView
	for _, e := range entries {
		if card, ok := f.cards[e.MediaItemID]; ok {
			out = append(out, handlers.HistoryCardView{Item: card, Entry: e})
		}
	}
	return out, nil
}

func (f *fakeHistory) RemoveHistory(_ context.Context, _ int, _ string, _ catalogpkg.AccessFilter, targets []handlers.HistoryRemovalTarget) error {
	if f.err != nil {
		return f.err
	}
	for _, t := range targets {
		if t.ContentID == "missing" {
			return &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "History target not found"}
		}
	}
	f.removed = append(f.removed, targets)
	return nil
}

func newFakeHistory() *fakeHistory {
	return &fakeHistory{
		entries: historyRows(),
		cards: map[string]handlers.CollectionItemView{
			"episode:heat-s01e02": {ContentID: "series:heat", Type: "series", Title: "Heat: The Series", Genres: []string{"Crime"}, Status: "matched"},
			"movie:heat-1995":     {ContentID: "movie:heat-1995", Type: "movie", Title: "Heat", Year: 1995, Genres: []string{"Crime"}, Status: "matched"},
		},
	}
}

func historyDeps(history *fakeHistory) Dependencies {
	deps := pilotDeps(nil, nil)
	deps.History = history
	return deps
}

type historyPage struct {
	Items []struct {
		ContentID string `json:"content_id"`
		Type      string `json:"type"`
		Watch     struct {
			MediaItemID string  `json:"media_item_id"`
			WatchedAt   string  `json:"watched_at"`
			Duration    float64 `json:"duration_seconds"`
			Completed   bool    `json:"completed"`
			Source      string  `json:"source"`
		} `json:"watch"`
	} `json:"items"`
	Page struct {
		NextCursor string `json:"next_cursor"`
		HasMore    bool   `json:"has_more"`
	} `json:"page"`
}

func decodeHistory(t *testing.T, body string) historyPage {
	t.Helper()
	var page historyPage
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatalf("decode: %v: %s", err, body)
	}
	return page
}

func TestListHistory(t *testing.T) {
	history := newFakeHistory()
	h := newTestHandler(t, historyDeps(history))
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")

	rec := do(t, h, http.MethodGet, "/api/v2/history", "", owner)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	page := decodeHistory(t, rec.Body.String())
	// Three rows, two of which resolve to a card; the hidden one is omitted
	// without hiding the rows behind it.
	if len(page.Items) != 2 || page.Page.HasMore || page.Page.NextCursor != "" {
		t.Fatalf("page = %s", rec.Body.String())
	}
	first := page.Items[0]
	if first.ContentID != "series:heat" || first.Type != "series" || first.Watch.MediaItemID != "episode:heat-s01e02" ||
		first.Watch.WatchedAt != "2026-01-03T00:00:00.000Z" || !first.Watch.Completed || first.Watch.Source != "playback" || first.Watch.Duration != 2600 {
		t.Fatalf("item = %+v", first)
	}
	if page.Items[1].ContentID != "movie:heat-1995" || page.Items[1].Watch.Source != "manual" {
		t.Fatalf("item = %+v", page.Items[1])
	}

	// Paging: a limit of 2 leaves one row behind; the cursor resumes at it.
	rec = do(t, h, http.MethodGet, "/api/v2/history?limit=2", "", owner)
	page = decodeHistory(t, rec.Body.String())
	if rec.Code != 200 || len(page.Items) != 2 || !page.Page.HasMore || page.Page.NextCursor == "" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/api/v2/history?limit=2&cursor="+page.Page.NextCursor, "", owner)
	page = decodeHistory(t, rec.Body.String())
	if rec.Code != 200 || len(page.Items) != 0 || page.Page.HasMore {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if len(history.pages) != 3 || history.pages[2] == nil || history.pages[2].ID != "h2" || history.pages[2].WatchedAt != "2026-01-02T03:04:05Z" {
		t.Fatalf("keys = %+v", history.pages)
	}
}

// The cursor is a keyset, not an offset: rows inserted or removed between
// pages neither repeat nor skip a row, and equal watched_at values are
// ordered by id.
func TestListHistoryKeysetSurvivesChurn(t *testing.T) {
	history := newFakeHistory()
	// Five rows, two sharing a timestamp; every one resolves to a card.
	history.entries = []userstore.WatchHistoryEntry{
		{ID: "h5", ProfileID: "p-owner", MediaItemID: "movie:heat-1995", WatchedAt: "2026-01-05T00:00:00Z", Completed: true},
		{ID: "h4a", ProfileID: "p-owner", MediaItemID: "movie:heat-1995", WatchedAt: "2026-01-04T00:00:00Z", Completed: true},
		{ID: "h4b", ProfileID: "p-owner", MediaItemID: "movie:heat-1995", WatchedAt: "2026-01-04T00:00:00Z", Completed: true},
		{ID: "h3", ProfileID: "p-owner", MediaItemID: "movie:heat-1995", WatchedAt: "2026-01-03T00:00:00Z", Completed: true},
		{ID: "h2", ProfileID: "p-owner", MediaItemID: "movie:heat-1995", WatchedAt: "2026-01-02T00:00:00Z", Completed: true},
	}
	h := newTestHandler(t, historyDeps(history))
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")

	watchedAts := func(page historyPage) []string {
		out := make([]string, 0, len(page.Items))
		for _, item := range page.Items {
			out = append(out, item.Watch.WatchedAt)
		}
		return out
	}
	equal := func(got, want []string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	rec := do(t, h, http.MethodGet, "/api/v2/history?limit=2", "", owner)
	page1 := decodeHistory(t, rec.Body.String())
	if rec.Code != 200 || !page1.Page.HasMore || !equal(watchedAts(page1), []string{"2026-01-05T00:00:00.000Z", "2026-01-04T00:00:00.000Z"}) {
		t.Fatalf("page 1: %d %s", rec.Code, rec.Body.String())
	}
	// The tie on 2026-01-04 breaks by id: h4b (larger) first, h4a next page.
	if history.pages[0] != nil {
		t.Fatalf("first page key = %+v, want nil", history.pages[0])
	}

	// A new watch lands at the top after page 1: it must not push the last
	// row of page 1 onto page 2.
	history.entries = append(history.entries, userstore.WatchHistoryEntry{ID: "h6", ProfileID: "p-owner", MediaItemID: "movie:heat-1995", WatchedAt: "2026-01-06T00:00:00Z", Completed: true})
	rec = do(t, h, http.MethodGet, "/api/v2/history?limit=2&cursor="+page1.Page.NextCursor, "", owner)
	page2 := decodeHistory(t, rec.Body.String())
	if rec.Code != 200 || !page2.Page.HasMore || !equal(watchedAts(page2), []string{"2026-01-04T00:00:00.000Z", "2026-01-03T00:00:00.000Z"}) {
		t.Fatalf("page 2: %d %s", rec.Code, rec.Body.String())
	}
	if key := history.pages[1]; key == nil || key.ID != "h4b" || key.WatchedAt != "2026-01-04T00:00:00Z" {
		t.Fatalf("page 2 key = %+v, want the tie broken by id", key)
	}

	// A row already emitted disappears after page 2: page 3 still resumes
	// at h2 rather than skipping it.
	kept := history.entries[:0]
	for _, e := range history.entries {
		if e.ID != "h4a" {
			kept = append(kept, e)
		}
	}
	history.entries = kept
	rec = do(t, h, http.MethodGet, "/api/v2/history?limit=2&cursor="+page2.Page.NextCursor, "", owner)
	page3 := decodeHistory(t, rec.Body.String())
	if rec.Code != 200 || page3.Page.HasMore || page3.Page.NextCursor != "" || !equal(watchedAts(page3), []string{"2026-01-02T00:00:00.000Z"}) {
		t.Fatalf("page 3: %d %s", rec.Code, rec.Body.String())
	}
	if key := history.pages[2]; key == nil || key.ID != "h3" {
		t.Fatalf("page 3 key = %+v, want h3", key)
	}
}

func TestListHistoryRejectsBadInput(t *testing.T) {
	h := newTestHandler(t, historyDeps(newFakeHistory()))
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")

	p := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/history?image_size=huge", "", owner), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "query.image_size" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/history?offset=10", "", owner), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/history?cursor=nope", "", owner), TypeInvalidCursor)
	// A cursor minted for another profile is rejected.
	rec := do(t, h, http.MethodGet, "/api/v2/history?limit=1", "", owner)
	cursor := decodeHistory(t, rec.Body.String()).Page.NextCursor
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/history?limit=1&cursor="+cursor, "", with(bearer(memberToken), "X-Profile-Id", "p-primary")), TypeInvalidCursor)
}

func TestListHistoryDeniesWithoutProfile(t *testing.T) {
	h := newTestHandler(t, historyDeps(newFakeHistory()))
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/history", "", bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/history", "", nil), TypeAuthenticationRequired)
	off := newTestHandler(t, pilotDeps(nil, nil))
	requireProblem(t, do(t, off, http.MethodGet, "/api/v2/history", "", with(bearer(memberToken), "X-Profile-Id", "p-owner")), TypeDependencyUnavailable)
}

func TestRemoveHistoryEntries(t *testing.T) {
	history := newFakeHistory()
	h := newTestHandler(t, historyDeps(history))
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")

	rec := do(t, h, http.MethodPost, "/api/v2/history/remove", `{"targets":[{"content_id":"episode:heat-s01e02","scope":"show"},{"content_id":"movie:heat-1995"}]}`, owner)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if len(history.removed) != 1 || len(history.removed[0]) != 2 || history.removed[0][0].Scope != "show" || history.removed[0][1].ContentID != "movie:heat-1995" {
		t.Fatalf("removed = %+v", history.removed)
	}

	p := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/history/remove", `{"targets":[]}`, owner), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.targets" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	p = requireProblem(t, do(t, h, http.MethodPost, "/api/v2/history/remove", `{"targets":[{"content_id":"x","scope":"season"}]}`, owner), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.targets[0].scope" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/history/remove", `{"targets":[{"content_id":"x","extra":1}]}`, owner), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/history/remove", `{"targets":[{"content_id":"missing"}]}`, owner), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/history/remove", `{"targets":[{"content_id":"x"}]}`, bearer(memberToken)), TypeValidationFailed)

	// A seam rejecting a member is a validation failure naming it.
	history.err = &handlers.APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "content_id is required", Field: "targets"}
	p = requireProblem(t, do(t, h, http.MethodPost, "/api/v2/history/remove", `{"targets":[{"content_id":" "}]}`, owner), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.targets" {
		t.Fatalf("errors = %+v", p.Errors)
	}
}
