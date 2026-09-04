package apiv2

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

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
	// The handler asked for the default window plus the has_more probe row.
	if q := progress.calls[0]; q.Limit != DefaultLimit+1 || q.Offset != 0 || q.Status != "" || q.LibraryID != 0 {
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
	if q := progress.calls[1]; q.Offset != 1 || q.Limit != 2 {
		t.Fatalf("query = %+v", q)
	}
	// The cursor is bound to the filter and to the profile.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/progress?limit=1&status=completed&cursor="+url.QueryEscape(first.Page.NextCursor), "", owner), TypeInvalidCursor)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/progress?limit=1&cursor="+url.QueryEscape(first.Page.NextCursor), "", with(bearer(memberToken), "X-Profile-Id", "p-primary")), TypeInvalidCursor)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/progress?cursor=nonsense", "", owner), TypeInvalidCursor)
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
	if len(p.Errors) != 1 || p.Errors[0].Location != "header.x-profile-id" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/progress", "", with(bearer(memberToken), "X-Profile-Id", "p-locked")), TypeProfileVerificationRequired)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/progress", "", with(bearer(memberToken), "X-Profile-Id", "p-unknown")), TypeNotFound)
	// A store failure is the v1 decision (500) as a problem.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/progress", "", with(bearer(memberToken), "X-Profile-Id", "p-owner")), TypeInternalError)
}
