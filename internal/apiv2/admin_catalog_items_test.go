package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
)

type fakeAdminCatalogItems struct {
	calls  int
	filter catalogpkg.AdminCatalogItemFilter
	after  string
}

func (f *fakeAdminCatalogItems) ListAdminCatalogItems(_ context.Context, filter catalogpkg.AdminCatalogItemFilter, after string, _ int) (catalogpkg.AdminCatalogItemPage, error) {
	f.calls++
	f.filter, f.after = filter, after
	if after != "" {
		return catalogpkg.AdminCatalogItemPage{Items: []catalogpkg.AdminCatalogItem{}}, nil
	}
	season, episode := 1, 2
	return catalogpkg.AdminCatalogItemPage{HasMore: true, Items: []catalogpkg.AdminCatalogItem{
		{ContentID: "episode-tvdb-81189-1-2", Type: "episode", Title: "Cat's in the Bag", Year: 2008, TvdbID: "349232",
			SeriesID: "series-tvdb-81189", SeriesTitle: "Breaking Bad", SeasonNumber: &season, EpisodeNumber: &episode,
			LibraryIDs: []int{3}, AddedAt: fixedTime(), Versions: []catalogpkg.AdminCatalogVersion{
				{FileID: 9007199254740993, LibraryID: 3, Resolution: "2160p", CodecVideo: "hevc", DynamicRange: "dolby_vision", CodecAudio: "truehd", AudioChannels: 8, Atmos: true, Container: "mkv", BitrateKbps: 60000, FileSize: 5_000_000_000, Edition: "Extended"},
			}},
		{ContentID: "series-tvdb-81189", Type: "series", Title: "Breaking Bad", Year: 2008, Status: "matched", TvdbID: "81189",
			LibraryIDs: []int{3}, AddedAt: fixedTime(), Versions: []catalogpkg.AdminCatalogVersion{}},
	}}, nil
}

func TestAdminCatalogItemsContract(t *testing.T) {
	deps := pilotDeps(nil, nil)
	fake := new(fakeAdminCatalogItems)
	deps.AdminCatalogItems = fake
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/catalog/items"
	query := "?limit=2&library_id=3&type=episode&added_after=2026-01-01T00:00:00Z&content_ids=b&content_ids=a&content_ids=b"

	first := do(t, h, http.MethodGet, path+query, "", actingRequestAdmin)
	if first.Code != 200 {
		t.Fatalf("first: %d %s", first.Code, first.Body)
	}
	if fake.filter.LibraryID != 3 || fake.filter.Type != "episode" || fake.filter.AddedAfter == nil || strings.Join(fake.filter.ContentIDs, ",") != "a,b" {
		t.Fatalf("filter: %+v", fake.filter)
	}
	body := first.Body.String()
	for _, want := range []string{`"file_id":"9007199254740993"`, `"library_ids":["3"]`, `"series_id":"series-tvdb-81189"`, `"versions":[]`, `"added_at":"2026-01-02T03:04:05.678Z"`, `"has_more":true`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}
	if strings.Contains(body, "path") {
		t.Fatalf("listing exposes a path: %s", body)
	}
	var page AdminCatalogItemCollection
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	nextPath := path + query + "&cursor=" + url.QueryEscape(page.Page.NextCursor)
	if next := do(t, h, http.MethodGet, nextPath, "", actingRequestAdmin); next.Code != 200 || fake.after != "series-tvdb-81189" {
		t.Fatalf("next: %d %s after=%q", next.Code, next.Body, fake.after)
	}
	// Repeating or reordering content_ids keeps the cursor; every other change starts over.
	if same := do(t, h, http.MethodGet, strings.Replace(nextPath, "content_ids=b&content_ids=a&content_ids=b", "content_ids=a&content_ids=b", 1), "", actingRequestAdmin); same.Code != 200 {
		t.Fatalf("reordered content_ids: %d %s", same.Code, same.Body)
	}

	calls := fake.calls
	for _, bad := range []string{
		strings.Replace(nextPath, "library_id=3", "library_id=4", 1),
		strings.Replace(nextPath, "type=episode", "type=movie", 1),
		strings.Replace(nextPath, "added_after=2026-01-01T00:00:00Z", "added_after=2026-01-01T00:00:01Z", 1),
		strings.Replace(nextPath, "content_ids=b&content_ids=a", "content_ids=c&content_ids=a", 1),
		strings.Replace(nextPath, "limit=2", "limit=3", 1),
		path + "?cursor=invalid",
	} {
		requireProblem(t, do(t, h, http.MethodGet, bad, "", actingRequestAdmin), TypeInvalidCursor)
	}
	requireProblem(t, do(t, h, http.MethodGet, nextPath, "", bearer(adminToken)), TypeInvalidCursor)
	for _, q := range []string{"?limit=201", "?type=audiobook", "?library_id=0", "?library_id=x", "?library_id=3000000000", "?added_after=yesterday", "?content_ids=%20", "?content_ids=x" + strings.Repeat("&content_ids=x", 200)} {
		requireProblem(t, do(t, h, http.MethodGet, path+q, "", actingRequestAdmin), TypeValidationFailed)
	}
	if denied := do(t, h, http.MethodGet, path, "", bearer(memberToken)); denied.Code != 403 {
		t.Fatalf("member: %d", denied.Code)
	}
	if fake.calls != calls {
		t.Fatalf("refused requests reached the store: %d -> %d", calls, fake.calls)
	}

	caps := do(t, h, http.MethodGet, Prefix+"/admin/catalog/capabilities", "", actingRequestAdmin)
	if caps.Code != 200 || !strings.Contains(caps.Body.String(), `"item_listing":true`) || !strings.Contains(caps.Body.String(), `"item_listing_max_content_ids":200`) {
		t.Fatalf("capabilities: %d %s", caps.Code, caps.Body)
	}
}

func adminCatalogItemsFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "list_admin_catalog_items_ok", operationID: opListAdminCatalogItems, method: http.MethodGet, path: Prefix + "/admin/catalog/items?limit=2", headers: actingRequestAdmin, status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/AdminCatalogItemCollection", scenario: "Administrator catalog page carries an episode with its series placement and one file version, a series with no versions, string IDs and a scoped cursor."},
		{name: "admin_catalog_capabilities", operationID: "getAdminCatalogCapabilities", method: http.MethodGet, path: Prefix + "/admin/catalog/capabilities", headers: actingRequestAdmin, status: 200, schema: "#/components/schemas/AdminCatalogCapabilities", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "Catalog capabilities report the item listing and its content_ids bound."},
	}
}
