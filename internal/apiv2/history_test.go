package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// fakeHistory stands in for handlers.PersonalDataHandler: an offset page over
// rows, cards rendered for every entry whose id it knows, and the removal
// command recorded as the seam received it.
type fakeHistory struct {
	entries []userstore.WatchHistoryEntry
	cards   map[string]handlers.CollectionItemView
	removed [][]handlers.HistoryRemovalTarget
	pages   []int
	err     error
}

func historyRows() []userstore.WatchHistoryEntry {
	return []userstore.WatchHistoryEntry{
		{ID: "h3", ProfileID: "p-owner", MediaItemID: "episode:heat-s01e02", WatchedAt: "2026-01-03T00:00:00Z", DurationSeconds: 2600, Completed: true, Source: userstore.WatchHistorySourcePlayback},
		{ID: "h2", ProfileID: "p-owner", MediaItemID: "movie:heat-1995", WatchedAt: "2026-01-02T03:04:05Z", DurationSeconds: 5400, Completed: true, Source: userstore.WatchHistorySourceManual},
		{ID: "h1", ProfileID: "p-owner", MediaItemID: "movie:hidden", WatchedAt: "2026-01-01T00:00:00Z", DurationSeconds: 100, Completed: false},
	}
}

func (f *fakeHistory) HistoryEntries(_ context.Context, _ int, profileID string, limit, offset int) ([]userstore.WatchHistoryEntry, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.pages = append(f.pages, offset)
	var rows []userstore.WatchHistoryEntry
	for _, e := range f.entries {
		if e.ProfileID == profileID {
			rows = append(rows, e)
		}
	}
	if offset >= len(rows) {
		return nil, nil
	}
	rows = rows[offset:]
	if len(rows) > limit {
		rows = rows[:limit]
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
	if len(history.pages) != 3 || history.pages[2] != 2 {
		t.Fatalf("offsets = %v", history.pages)
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
