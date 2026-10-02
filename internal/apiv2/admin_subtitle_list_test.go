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

type fakeAdminSubtitleList struct {
	calls  int
	filter handlers.AdminSubtitleListFilter
	after  *handlers.AdminSubtitlePageKey
	err    error
}

func (f *fakeAdminSubtitleList) ListAdminSubtitlesPage(_ context.Context, filter handlers.AdminSubtitleListFilter, after *handlers.AdminSubtitlePageKey, limit int) (handlers.AdminSubtitlePage, error) {
	f.calls++
	f.filter = filter
	f.after = after
	rows := []handlers.AdminDownloadedSubtitle{{ID: 9007199254740993, MediaFileID: 42, Provider: "upload", Language: "en", CreatedAt: fixedTime().Add(123456 * time.Nanosecond), DownloadedBy: new(2)}}
	if after != nil {
		rows = []handlers.AdminDownloadedSubtitle{}
	}
	return handlers.AdminSubtitlePage{Items: rows, Total: 3, Uploads: 1, ProviderDownloads: 2, HasMore: after == nil}, f.err
}
func TestAdminStoredSubtitleListContract(t *testing.T) {
	deps := requestDeps(fixtureRequests())
	fake := new(fakeAdminSubtitleList)
	deps.AdminSubtitleList = fake
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/subtitles"
	first := do(t, h, http.MethodGet, path+"?limit=1&provider=upload&user_id=2&media_file_id=2&q=release", "", actingRequestAdmin)
	if first.Code != 200 {
		t.Fatalf("first: %d %s", first.Code, first.Body)
	}
	if fake.filter.UserID != 2 || fake.filter.MediaFileID != 2 || fake.filter.Search != "release" {
		t.Fatalf("filters: %+v", fake.filter)
	}
	if !strings.Contains(first.Body.String(), `"id":"9007199254740993"`) || !strings.Contains(first.Body.String(), `"downloaded_by":"2"`) {
		t.Fatalf("IDs: %s", first.Body)
	}
	var body AdminStoredSubtitleCollection
	if err := json.Unmarshal(first.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	cursor := url.QueryEscape(body.Page.NextCursor)
	nextPath := path + "?limit=1&provider=upload&user_id=2&media_file_id=2&q=release&cursor=" + cursor
	second := do(t, h, http.MethodGet, nextPath, "", actingRequestAdmin)
	if second.Code != 200 || fake.after == nil || fake.after.ID != 9007199254740993 || !fake.after.CreatedAt.Equal(fixedTime().Add(123456*time.Nanosecond)) || !strings.Contains(second.Body.String(), `"items":[]`) {
		t.Fatalf("next: %d %s after=%+v", second.Code, second.Body, fake.after)
	}
	calls := fake.calls
	for _, bad := range []string{strings.Replace(nextPath, "provider=upload", "provider=other", 1), strings.Replace(nextPath, "limit=1", "limit=2", 1), path + "?cursor=invalid"} {
		requireProblem(t, do(t, h, http.MethodGet, bad, "", actingRequestAdmin), TypeInvalidCursor)
	}
	requireProblem(t, do(t, h, http.MethodGet, nextPath, "", bearer(adminToken)), TypeInvalidCursor)
	requireProblem(t, do(t, h, http.MethodGet, path, "", with(bearer(adminToken), "X-Profile-Id", "p-owner")), TypePermissionDenied)
	requireProblem(t, do(t, h, http.MethodGet, path, "", with(bearer(adminToken), "X-Profile-Id", "p-primary-locked")), TypeProfileVerificationRequired)
	for _, query := range []string{"?limit=0", "?limit=201", "?offset=1", "?user_id=-1", "?media_file_id=0", "?user_id=9223372036854775808"} {
		requireProblem(t, do(t, h, http.MethodGet, path+query, "", actingRequestAdmin), TypeValidationFailed)
	}
	if denied := do(t, h, http.MethodGet, path, "", viewerHeaders()); denied.Code != 403 {
		t.Fatalf("viewer: %d", denied.Code)
	}
	if denied := do(t, h, http.MethodGet, path, "", nil); denied.Code != 401 {
		t.Fatalf("anonymous: %d", denied.Code)
	}
	if fake.calls != calls {
		t.Fatalf("refused requests reached store: %d -> %d", calls, fake.calls)
	}
	fake.err = errors.New("PRIVATE store details")
	failed := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	requireProblem(t, failed, TypeInternalError)
	if strings.Contains(failed.Body.String(), "PRIVATE") {
		t.Fatal("private error leaked")
	}
	deps.AdminSubtitleList = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, path, "", actingRequestAdmin), TypeDependencyUnavailable)
}

func adminSubtitleListFixtureCases() []fixtureCase {
	return []fixtureCase{{name: "admin_stored_subtitle_list", operationID: opListAdminStoredSubtitles, method: http.MethodGet, path: Prefix + "/admin/subtitles?limit=1", headers: actingRequestAdmin, status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/AdminStoredSubtitleCollection", scenario: "Administrator live list carries exact string IDs, filtered counts and a scoped cursor."}}
}
