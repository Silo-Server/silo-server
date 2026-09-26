package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type fakeAdminPlaybackHistory struct {
	calls  int
	filter handlers.AdminPlaybackHistoryFilter
	after  *handlers.AdminPlaybackHistoryPageKey
	limit  int
	err    error
}

func (f *fakeAdminPlaybackHistory) ListAdminPlaybackHistoryPage(_ context.Context, filter handlers.AdminPlaybackHistoryFilter, after *handlers.AdminPlaybackHistoryPageKey, limit int) (handlers.AdminPlaybackHistoryPage, error) {
	f.calls++
	f.filter, f.after, f.limit = filter, after, limit
	if f.err != nil {
		return handlers.AdminPlaybackHistoryPage{}, f.err
	}
	duration := 5400.5
	rows := []handlers.AdminPlaybackHistoryRow{{
		SessionID: "sess-9007199254740993", UserID: 9007199254740993, Username: "laura", ProfileID: "p-owner", ProfileName: "Laura",
		MediaItemID: "movie-1", MediaFileID: 42, MediaTitle: "Synthetic Movie", MediaType: "movie", PlayMethod: "direct_play",
		StartedAt: fixedTime().Add(-time.Hour), EndedAt: fixedTime().Add(123456 * time.Nanosecond), WatchedSeconds: 3600, DurationSeconds: &duration, Completed: true,
	}}
	if after != nil {
		rows = []handlers.AdminPlaybackHistoryRow{}
	}
	return handlers.AdminPlaybackHistoryPage{Items: rows, HasMore: after == nil}, nil
}

func TestAdminPlaybackHistoryContract(t *testing.T) {
	deps := pilotDeps(nil, nil)
	fake := new(fakeAdminPlaybackHistory)
	deps.AdminPlaybackHistory = fake
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/playback-history"

	first := do(t, h, http.MethodGet, path+"?limit=1&user_id=2&profile_id=p-owner&media_item_id=movie-1&completed=false", "", actingRequestAdmin)
	if first.Code != 200 {
		t.Fatalf("first: %d %s", first.Code, first.Body)
	}
	if fake.filter.UserID != 2 || fake.filter.ProfileID != "p-owner" || fake.filter.MediaItemID != "movie-1" || fake.filter.Completed == nil || *fake.filter.Completed || fake.limit != 1 {
		t.Fatalf("filters: %+v limit=%d", fake.filter, fake.limit)
	}
	body := first.Body.String()
	for _, want := range []string{`"user_id":"9007199254740993"`, `"media_file_id":"42"`, `"duration_seconds":5400.5`, `"ended_at":"2026-01-02T03:04:05.678Z"`, `"has_more":true`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}
	if strings.Contains(body, "client_ip") {
		t.Fatalf("client address projected: %s", body)
	}
	var page AdminPlaybackHistoryCollection
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	cursor := url.QueryEscape(page.Page.NextCursor)
	query := "?limit=1&user_id=2&profile_id=p-owner&media_item_id=movie-1&completed=false"
	nextPath := path + query + "&cursor=" + cursor
	second := do(t, h, http.MethodGet, nextPath, "", actingRequestAdmin)
	if second.Code != 200 || fake.after == nil || fake.after.SessionID != "sess-9007199254740993" || !fake.after.EndedAt.Equal(fixedTime().Add(123456*time.Nanosecond)) || !strings.Contains(second.Body.String(), `"items":[]`) || !strings.Contains(second.Body.String(), `"has_more":false`) {
		t.Fatalf("next: %d %s after=%+v", second.Code, second.Body, fake.after)
	}

	// The completed filter defaults to every attempt, and "all" is the same.
	for _, q := range []string{"", "?completed=all"} {
		if rec := do(t, h, http.MethodGet, path+q, "", actingRequestAdmin); rec.Code != 200 || fake.filter.Completed != nil || fake.filter.UserID != 0 {
			t.Fatalf("default filter %q: %d %+v", q, rec.Code, fake.filter)
		}
	}

	calls := fake.calls
	// A cursor is bound to its filters, limit, account and profile.
	for _, bad := range []string{
		strings.Replace(nextPath, "completed=false", "completed=true", 1),
		strings.Replace(nextPath, "limit=1", "limit=2", 1),
		strings.Replace(nextPath, "user_id=2", "user_id=3", 1),
		path + "?cursor=invalid",
	} {
		requireProblem(t, do(t, h, http.MethodGet, bad, "", actingRequestAdmin), TypeInvalidCursor)
	}
	requireProblem(t, do(t, h, http.MethodGet, nextPath, "", bearer(adminToken)), TypeInvalidCursor)
	for _, q := range []string{"?limit=0", "?limit=201", "?offset=1", "?user_id=-1", "?user_id=0", "?user_id=abc", "?completed=maybe"} {
		requireProblem(t, do(t, h, http.MethodGet, path+q, "", actingRequestAdmin), TypeValidationFailed)
	}
	requireProblem(t, do(t, h, http.MethodGet, path, "", with(bearer(adminToken), "X-Profile-Id", "p-owner")), TypePermissionDenied)
	requireProblem(t, do(t, h, http.MethodGet, path, "", with(bearer(adminToken), "X-Profile-Id", "p-primary-locked")), TypeProfileVerificationRequired)
	if denied := do(t, h, http.MethodGet, path, "", viewerHeaders()); denied.Code != 403 {
		t.Fatalf("viewer: %d", denied.Code)
	}
	if denied := do(t, h, http.MethodGet, path, "", nil); denied.Code != 401 {
		t.Fatalf("anonymous: %d", denied.Code)
	}
	if fake.calls != calls {
		t.Fatalf("refused requests reached the store: %d -> %d", calls, fake.calls)
	}

	fake.err = errors.New("PRIVATE database details")
	failed := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	requireProblem(t, failed, TypeInternalError)
	if strings.Contains(failed.Body.String(), "PRIVATE") {
		t.Fatal("private error leaked")
	}
	deps.AdminPlaybackHistory = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, path, "", actingRequestAdmin), TypeDependencyUnavailable)
}

func adminPlaybackHistoryFixtureCases() []fixtureCase {
	return []fixtureCase{{name: "list_admin_playback_history_ok", operationID: opListAdminPlaybackHistory, method: http.MethodGet, path: Prefix + "/admin/playback-history?limit=1", headers: actingRequestAdmin, status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/AdminPlaybackHistoryCollection", scenario: "Administrator playback history page carries string IDs, instants, a nullable duration and a scoped cursor."}}
}
