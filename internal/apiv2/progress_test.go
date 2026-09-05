package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func progressRows() []userstore.WatchProgress {
	return []userstore.WatchProgress{
		{ProfileID: "p-owner", MediaItemID: "movie-8f2c1a", PositionSeconds: 1325.5, DurationSeconds: 5400, UpdatedAt: "2026-01-02T03:04:05Z"},
		{ProfileID: "p-owner", MediaItemID: "episode-1b2c3d", PositionSeconds: 0, DurationSeconds: 2600, Completed: true, UpdatedAt: "2026-01-01T00:00:00Z"},
		{ProfileID: "p-other", MediaItemID: "movie-ffffff", PositionSeconds: 1, UpdatedAt: "2026-01-01T00:00:00Z"},
	}
}

type progressPage struct {
	Items []struct {
		MediaItemID string  `json:"media_item_id"`
		Position    float64 `json:"position_seconds"`
		UpdatedAt   string  `json:"updated_at"`
	} `json:"items"`
	Page struct {
		NextCursor string `json:"next_cursor"`
		HasMore    bool   `json:"has_more"`
	} `json:"page"`
}

func decodeProgress(t *testing.T, rec interface{ String() string }) progressPage {
	t.Helper()
	var page progressPage
	if err := json.Unmarshal([]byte(rec.String()), &page); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.String())
	}
	return page
}

func TestListProgress(t *testing.T) {
	progress := &fakeProgress{entries: progressRows()}
	h := newTestHandler(t, pilotDeps(progress, nil))
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")

	rec := do(t, h, http.MethodGet, "/api/v2/progress", "", owner)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	page := decodeProgress(t, rec.Body)
	if len(page.Items) != 2 || page.Page.HasMore || page.Page.NextCursor != "" {
		t.Fatalf("page = %s", rec.Body.String())
	}
	if page.Items[0].MediaItemID != "movie-8f2c1a" || page.Items[0].Position != 1325.5 || page.Items[0].UpdatedAt != "2026-01-02T03:04:05.000Z" {
		t.Fatalf("item = %+v", page.Items[0])
	}
	// The handler asked for the default window from the newest row.
	if q := progress.calls[0]; q.Limit != DefaultLimit || q.After != nil || q.Status != "" || q.LibraryID != 0 {
		t.Fatalf("query = %+v", q)
	}

	rec = do(t, h, http.MethodGet, "/api/v2/progress?status=completed&library_id=3", "", owner)
	page = decodeProgress(t, rec.Body)
	if rec.Code != 200 || len(page.Items) != 1 || page.Items[0].MediaItemID != "episode-1b2c3d" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if q := progress.calls[1]; q.Status != "completed" || q.LibraryID != 3 {
		t.Fatalf("query = %+v", q)
	}
}

// listProgressIDs walks every page of the query and returns the item ids in
// order, along with the number of pages.
func listProgressIDs(t *testing.T, h http.Handler, query string, headers map[string]string) ([]string, int) {
	t.Helper()
	var ids []string
	cursor := ""
	for pages := 1; ; pages++ {
		q := query
		if cursor != "" {
			q += "&cursor=" + url.QueryEscape(cursor)
		}
		rec := do(t, h, http.MethodGet, "/api/v2/progress?"+q, "", headers)
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		page := decodeProgress(t, rec.Body)
		for _, it := range page.Items {
			ids = append(ids, it.MediaItemID)
		}
		if page.Page.HasMore != (page.Page.NextCursor != "") {
			t.Fatalf("has_more and next_cursor disagree: %s", rec.Body.String())
		}
		if !page.Page.HasMore {
			return ids, pages
		}
		cursor = page.Page.NextCursor
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
	}
}

// TestListProgressFilteredHasMore proves a library filter that drops rows
// still reports has_more while older matching rows exist, and that the
// second page continues with no gap and no duplicate. Library 3 holds every
// other row; a raw window of limit+1 = 3 rows sees only one or two matches.
func TestListProgressFilteredHasMore(t *testing.T) {
	var rows []userstore.WatchProgress
	libraries := map[string]int{}
	for i := 0; i < 8; i++ {
		id := "item-" + string(rune('a'+i))
		rows = append(rows, userstore.WatchProgress{ProfileID: "p-owner", MediaItemID: id, PositionSeconds: 1, UpdatedAt: "2026-01-0" + string(rune('1'+i)) + "T00:00:00Z"})
		if i%2 == 0 {
			libraries[id] = 3
		} else {
			libraries[id] = 4
		}
	}
	progress := &fakeProgress{entries: rows, libraries: libraries}
	h := newTestHandler(t, pilotDeps(progress, nil))
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")

	rec := do(t, h, http.MethodGet, "/api/v2/progress?limit=2&library_id=3", "", owner)
	first := decodeProgress(t, rec.Body)
	if rec.Code != 200 || len(first.Items) != 2 || !first.Page.HasMore || first.Page.NextCursor == "" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if first.Items[0].MediaItemID != "item-g" || first.Items[1].MediaItemID != "item-e" {
		t.Fatalf("first page = %+v", first.Items)
	}
	ids, pages := listProgressIDs(t, h, "limit=2&library_id=3", owner)
	want := []string{"item-g", "item-e", "item-c", "item-a"}
	if pages != 2 || strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("pages = %d ids = %v, want %v", pages, ids, want)
	}
	// The resumed query asked the seam to start strictly after the last emitted row.
	if q := progress.calls[len(progress.calls)-1]; q.After == nil || q.After.MediaItemID != "item-e" || q.After.UpdatedAt != "2026-01-05T00:00:00Z" || q.LibraryID != 3 || q.Limit != 2 {
		t.Fatalf("query = %+v after = %+v", q, q.After)
	}
}

// TestListProgressEqualTimestamps proves rows sharing an updated_at paginate
// deterministically across a page boundary by the media_item_id tiebreaker.
func TestListProgressEqualTimestamps(t *testing.T) {
	const at = "2026-01-02T03:04:05Z"
	rows := []userstore.WatchProgress{
		{ProfileID: "p-owner", MediaItemID: "movie-b", PositionSeconds: 1, UpdatedAt: at},
		{ProfileID: "p-owner", MediaItemID: "movie-d", PositionSeconds: 1, UpdatedAt: at},
		{ProfileID: "p-owner", MediaItemID: "movie-a", PositionSeconds: 1, UpdatedAt: at},
		{ProfileID: "p-owner", MediaItemID: "movie-c", PositionSeconds: 1, UpdatedAt: at},
		{ProfileID: "p-owner", MediaItemID: "movie-z", PositionSeconds: 1, UpdatedAt: "2026-01-01T00:00:00Z"},
	}
	h := newTestHandler(t, pilotDeps(&fakeProgress{entries: rows}, nil))
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	ids, pages := listProgressIDs(t, h, "limit=2", owner)
	want := []string{"movie-d", "movie-c", "movie-b", "movie-a", "movie-z"}
	if pages != 3 || strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("pages = %d ids = %v, want %v", pages, ids, want)
	}
}

// TestListProgressRowMovesAhead proves a row whose updated_at advances between
// two pages (playback) appears on neither the first page it was not yet on
// nor the second, and does not push an older unseen row off the second page.
func TestListProgressRowMovesAhead(t *testing.T) {
	rows := []userstore.WatchProgress{
		{ProfileID: "p-owner", MediaItemID: "movie-1", PositionSeconds: 1, UpdatedAt: "2026-01-05T00:00:00Z"},
		{ProfileID: "p-owner", MediaItemID: "movie-2", PositionSeconds: 1, UpdatedAt: "2026-01-04T00:00:00Z"},
		{ProfileID: "p-owner", MediaItemID: "movie-3", PositionSeconds: 1, UpdatedAt: "2026-01-03T00:00:00Z"},
		{ProfileID: "p-owner", MediaItemID: "movie-4", PositionSeconds: 1, UpdatedAt: "2026-01-02T00:00:00Z"},
	}
	progress := &fakeProgress{entries: rows}
	h := newTestHandler(t, pilotDeps(progress, nil))
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")

	rec := do(t, h, http.MethodGet, "/api/v2/progress?limit=2", "", owner)
	first := decodeProgress(t, rec.Body)
	if rec.Code != 200 || len(first.Items) != 2 || first.Items[0].MediaItemID != "movie-1" || first.Items[1].MediaItemID != "movie-2" || !first.Page.HasMore {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// movie-3 is played between the pages and becomes the newest row.
	progress.entries[2].UpdatedAt = "2026-01-06T00:00:00Z"

	rec = do(t, h, http.MethodGet, "/api/v2/progress?limit=2&cursor="+url.QueryEscape(first.Page.NextCursor), "", owner)
	second := decodeProgress(t, rec.Body)
	if rec.Code != 200 || len(second.Items) != 1 || second.Items[0].MediaItemID != "movie-4" || second.Page.HasMore {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestListProgressCursor(t *testing.T) {
	progress := &fakeProgress{entries: progressRows()}
	h := newTestHandler(t, pilotDeps(progress, nil))
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")

	rec := do(t, h, http.MethodGet, "/api/v2/progress?limit=1", "", owner)
	first := decodeProgress(t, rec.Body)
	if rec.Code != 200 || len(first.Items) != 1 || !first.Page.HasMore || first.Page.NextCursor == "" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/api/v2/progress?limit=1&cursor="+url.QueryEscape(first.Page.NextCursor), "", owner)
	second := decodeProgress(t, rec.Body)
	if rec.Code != 200 || len(second.Items) != 1 || second.Items[0].MediaItemID != "episode-1b2c3d" || second.Page.HasMore {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if q := progress.calls[1]; q.After == nil || q.After.MediaItemID != "movie-8f2c1a" || q.After.UpdatedAt != "2026-01-02T03:04:05Z" || q.Limit != 1 {
		t.Fatalf("query = %+v", q)
	}
	// The cursor is bound to the filter and to the profile.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/progress?limit=1&status=completed&cursor="+url.QueryEscape(first.Page.NextCursor), "", owner), TypeInvalidCursor)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/progress?limit=1&cursor="+url.QueryEscape(first.Page.NextCursor), "", with(bearer(memberToken), "X-Profile-Id", "p-primary")), TypeInvalidCursor)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/progress?cursor=nonsense", "", owner), TypeInvalidCursor)
}

// policyResolver is fakeResolver with a mutable access policy, so a test can
// change what the viewer may see between two pages.
type policyResolver struct{ scope *access.Scope }

func (r policyResolver) Resolve(ctx context.Context, in access.ResolveInput) (access.Scope, error) {
	scope, err := fakeResolver{}.Resolve(ctx, in)
	if err != nil {
		return scope, err
	}
	scope.AllowedLibraryIDs = r.scope.AllowedLibraryIDs
	scope.LibrariesRestricted = r.scope.LibrariesRestricted
	scope.DisabledLibraryIDs = r.scope.DisabledLibraryIDs
	scope.MaxContentRating = r.scope.MaxContentRating
	scope.PolicyRevision = r.scope.PolicyRevision
	return scope, nil
}

// TestListProgressCursorBoundToViewerPolicy: the listing is access-filtered,
// so a cursor minted under one policy is refused once the policy changes;
// otherwise rows that became visible before the key would be skipped.
func TestListProgressCursorBoundToViewerPolicy(t *testing.T) {
	policy := &access.Scope{AllowedLibraryIDs: []int{3, 1}, PolicyRevision: 7, MaxContentRating: "PG-13"}
	deps := pilotDeps(&fakeProgress{entries: progressRows()}, nil)
	deps.ViewerAccess = apimw.NewViewerAccessMiddleware(policyResolver{scope: policy})
	h := newTestHandler(t, deps)
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")

	rec := do(t, h, http.MethodGet, "/api/v2/progress?limit=1", "", owner)
	first := decodeProgress(t, rec.Body)
	if rec.Code != 200 || !first.Page.HasMore || first.Page.NextCursor == "" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	next := "/api/v2/progress?limit=1&cursor=" + url.QueryEscape(first.Page.NextCursor)

	// Same policy (order of the allowlist is irrelevant): the cursor works.
	policy.AllowedLibraryIDs = []int{1, 3}
	if rec := do(t, h, http.MethodGet, next, "", owner); rec.Code != 200 {
		t.Fatalf("unchanged policy: %d %s", rec.Code, rec.Body.String())
	}
	for name, change := range map[string]func(){
		"allowed libraries":  func() { policy.AllowedLibraryIDs = []int{1, 3, 5} },
		"disabled libraries": func() { policy.DisabledLibraryIDs = []int{2} },
		"content rating":     func() { policy.MaxContentRating = "R" },
		"policy revision":    func() { policy.PolicyRevision = 8 },
	} {
		change()
		requireProblem(t, do(t, h, http.MethodGet, next, "", owner), TypeInvalidCursor)
		// A fresh first page under the new policy paginates again.
		rec := do(t, h, http.MethodGet, "/api/v2/progress?limit=1", "", owner)
		page := decodeProgress(t, rec.Body)
		if rec.Code != 200 || page.Page.NextCursor == "" {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
		if rec := do(t, h, http.MethodGet, "/api/v2/progress?limit=1&cursor="+url.QueryEscape(page.Page.NextCursor), "", owner); rec.Code != 200 {
			t.Fatalf("%s: reissued cursor: %d %s", name, rec.Code, rec.Body.String())
		}
	}

	// Widening from "restricted to nothing" to "unrestricted" is a policy
	// change even when the store keeps the PolicyRevision: an empty
	// allowlist and a nil one are different policies, as is the restriction
	// flag on its own.
	for name, tc := range map[string]struct{ before, after access.Scope }{
		"restricted-empty to unrestricted": {
			before: access.Scope{AllowedLibraryIDs: []int{}, LibrariesRestricted: true},
			after:  access.Scope{AllowedLibraryIDs: nil, LibrariesRestricted: false},
		},
		"empty versus nil allowlist": {
			before: access.Scope{AllowedLibraryIDs: []int{}},
			after:  access.Scope{AllowedLibraryIDs: nil},
		},
		"restriction flag alone": {
			before: access.Scope{AllowedLibraryIDs: []int{1}, LibrariesRestricted: true},
			after:  access.Scope{AllowedLibraryIDs: []int{1}, LibrariesRestricted: false},
		},
	} {
		apply := func(s access.Scope) {
			policy.AllowedLibraryIDs, policy.LibrariesRestricted = s.AllowedLibraryIDs, s.LibrariesRestricted
			policy.DisabledLibraryIDs, policy.MaxContentRating, policy.PolicyRevision = nil, "PG-13", 7
		}
		apply(tc.before)
		rec := do(t, h, http.MethodGet, "/api/v2/progress?limit=1", "", owner)
		page := decodeProgress(t, rec.Body)
		if rec.Code != 200 || page.Page.NextCursor == "" {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
		next := "/api/v2/progress?limit=1&cursor=" + url.QueryEscape(page.Page.NextCursor)
		if rec := do(t, h, http.MethodGet, next, "", owner); rec.Code != 200 {
			t.Fatalf("%s: same policy: %d %s", name, rec.Code, rec.Body.String())
		}
		apply(tc.after)
		requireProblem(t, do(t, h, http.MethodGet, next, "", owner), TypeInvalidCursor)
	}
}

func TestListProgressValidation(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	for _, tc := range []struct{ query, location, code string }{
		{"offset=10", "query.offset", codeUnknownParameter},
		{"since=abc", "query.since", codeUnknownParameter},
		{"status=watched", "query.status", codeInvalidEnum},
		{"limit=0", "query.limit", codeOutOfRange},
		{"limit=201", "query.limit", codeOutOfRange},
		{"library_id=abc", "query.library_id", codeInvalid},
	} {
		p := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/progress?"+tc.query, "", owner), TypeValidationFailed)
		if len(p.Errors) != 1 || p.Errors[0].Location != tc.location || p.Errors[0].Code != tc.code {
			t.Errorf("%s: errors = %+v", tc.query, p.Errors)
		}
	}
}

func TestListProgressDenied(t *testing.T) {
	deps := pilotDeps(&fakeProgress{err: errStore}, nil)
	h := newTestHandler(t, deps)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/progress", "", nil), TypeAuthenticationRequired)
	// Profile scoped: the header is required, and a locked profile must be verified.
	p := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/progress", "", bearer(memberToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != locationProfileHeader {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/progress", "", with(bearer(memberToken), "X-Profile-Id", "p-locked")), TypeProfileVerificationRequired)
	// An unknown profile is the viewer-access 404 as a not_found problem, which
	// the class implies without a path parameter (ImpliedStatuses).
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/progress", "", with(bearer(memberToken), "X-Profile-Id", "p-unknown")), TypeNotFound)
	// A store failure is the v1 decision (500) as a problem.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/progress", "", with(bearer(memberToken), "X-Profile-Id", "p-owner")), TypeInternalError)
}

func TestSyncProgress(t *testing.T) {
	progress := &fakeProgress{failID: "movie-broken"}
	h := newTestHandler(t, pilotDeps(progress, nil))
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")

	body := `{"items":[{"media_item_id":"movie-8f2c1a","position_ms":1325500,"duration_ms":5400000,"updated_at":"2026-01-02T03:04:05.250Z"},{"media_item_id":"movie-broken","position_ms":10,"duration_ms":0,"force_overwrite":true}]}`
	rec := do(t, h, http.MethodPost, "/api/v2/sync/progress", body, owner)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var out struct {
		Results []struct {
			MediaItemID string `json:"media_item_id"`
			Status      string `json:"status"`
			Error       string `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != 2 || out.Results[0].Status != "ok" || out.Results[0].Error != "" ||
		out.Results[1].MediaItemID != "movie-broken" || out.Results[1].Status != "error" || out.Results[1].Error != "failed to update progress" {
		t.Fatalf("results = %+v", out.Results)
	}
	// Milliseconds on the wire became the seconds the store keeps; the
	// instant reached the seam in UTC.
	if len(progress.synced) != 1 || len(progress.synced[0]) != 2 {
		t.Fatalf("synced = %+v", progress.synced)
	}
	first, second := progress.synced[0][0], progress.synced[0][1]
	if first.Position != 1325.5 || first.Duration != 5400 || first.ForceOverwrite || first.UpdatedAt == nil || first.UpdatedAt.Format("2006-01-02T15:04:05.000Z07:00") != "2026-01-02T03:04:05.250Z" {
		t.Fatalf("first = %+v", first)
	}
	if second.Position != 0.01 || !second.ForceOverwrite || second.UpdatedAt != nil {
		t.Fatalf("second = %+v", second)
	}
}

func TestSyncProgressRejectsBadInput(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")

	p := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/sync/progress", `{"items":[]}`, owner), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.items" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	p = requireProblem(t, do(t, h, http.MethodPost, "/api/v2/sync/progress", `{"items":[{"media_item_id":"","position_ms":-1,"duration_ms":0}]}`, owner), TypeValidationFailed)
	if len(p.Errors) != 2 || p.Errors[0].Location != "body.items[0].media_item_id" || p.Errors[1].Location != "body.items[0].position_ms" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	// v1's seconds member and a malformed instant are rejected, not silently accepted.
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/sync/progress", `{"items":[{"media_item_id":"m","position":5,"duration":10}]}`, owner), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/sync/progress", `{"items":[{"media_item_id":"m","position_ms":5,"duration_ms":10,"updated_at":"yesterday"}]}`, owner), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/sync/progress", `{"items":[{"media_item_id":"m","position_ms":5,"duration_ms":10}]}`, bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/sync/progress", `{"items":[{"media_item_id":"m","position_ms":5,"duration_ms":10}]}`, nil), TypeAuthenticationRequired)

	off := newTestHandler(t, parityDeps(false))
	requireProblem(t, do(t, off, http.MethodPost, "/api/v2/sync/progress", `{"items":[{"media_item_id":"m","position_ms":5,"duration_ms":10}]}`, owner), TypeDependencyUnavailable)
}
