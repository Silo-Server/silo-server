package apiv2

import (
	"context"
	"net/http"

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
	version := catalogpkg.AdminCatalogVersion{FileID: 9007199254740993, LibraryID: 3, Resolution: "2160p", CodecVideo: "hevc", DynamicRange: "dolby_vision", CodecAudio: "truehd", AudioChannels: 8, Atmos: true, Container: "mkv", BitrateKbps: 60000, FileSize: 5_000_000_000, Edition: "Extended"}
	if filter.FilePaths {
		version.FilePath = "/media/tv/Breaking Bad/Season 01/S01E02.mkv"
	}
	return catalogpkg.AdminCatalogItemPage{HasMore: true, Items: []catalogpkg.AdminCatalogItem{
		{ContentID: "episode-tvdb-81189-1-2", Type: "episode", Title: "Cat's in the Bag", Year: 2008, TvdbID: "349232",
			SeriesID: "series-tvdb-81189", SeriesTitle: "Breaking Bad", SeasonNumber: &season, EpisodeNumber: &episode,
			LibraryIDs: []int{3}, AddedAt: fixedTime(), Versions: []catalogpkg.AdminCatalogVersion{version}},
		{ContentID: "series-tvdb-81189", Type: "series", Title: "Breaking Bad", Year: 2008, Status: "matched", TvdbID: "81189",
			LibraryIDs: []int{3}, AddedAt: fixedTime(), Versions: []catalogpkg.AdminCatalogVersion{}},
	}}, nil
}

func adminCatalogItemsFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "list_admin_catalog_items_ok", operationID: opListAdminCatalogItems, method: http.MethodGet, path: Prefix + "/admin/catalog/items?limit=2", headers: actingRequestAdmin, status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/AdminCatalogItemCollection", scenario: "Administrator catalog page carries an episode with its series placement and one file version, a series with no versions, string IDs and a scoped cursor."},
		{name: "list_admin_catalog_items_file_paths", operationID: opListAdminCatalogItems, method: http.MethodGet, path: Prefix + "/admin/catalog/items?limit=2&include_file_paths=true", headers: actingRequestAdmin, status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/AdminCatalogItemCollection", scenario: "With include_file_paths, each file version also carries its stored path."},
		{name: "admin_catalog_capabilities", operationID: "getAdminCatalogCapabilities", method: http.MethodGet, path: Prefix + "/admin/catalog/capabilities", headers: actingRequestAdmin, status: 200, schema: "#/components/schemas/AdminCatalogCapabilities", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "Catalog capabilities report the item listing and its content_ids bound."},
	}
}
