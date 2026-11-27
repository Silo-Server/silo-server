package apiv2

import (
	"context"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

type fakeAdminEpisodeOrders struct {
	saved    metadata.EpisodeOrderSettings
	revision *int64
	err      error
}

func (f *fakeAdminEpisodeOrders) EpisodeOrderState(context.Context, string) (metadata.EpisodeOrderState, error) {
	return metadata.EpisodeOrderState{
		Override: true, Revision: 2, LibraryProvider: "tvdb",
		Settings: metadata.EpisodeOrderSettings{Provider: "tvdb", OrderID: "dvd", Layout: "seasons"},
		Providers: []metadata.EpisodeOrderProviderOrders{
			{Provider: "tvdb", Supported: true, Orders: []metadata.EpisodeOrder{
				{ID: "official", Name: "Aired Order", Kind: "aired", SeasonCount: 10, IsDefault: true},
				{ID: "dvd", Name: "DVD Order", Kind: "dvd", SeasonCount: 10},
			}},
			{Provider: "tmdb", Supported: true, Orders: []metadata.EpisodeOrder{{ID: "default", Name: "Seasons", Kind: "aired", IsDefault: true}}},
		},
	}, f.err
}

func (f *fakeAdminEpisodeOrders) PreviewEpisodeOrder(context.Context, string, metadata.EpisodeOrderSettings) (metadata.EpisodeOrderPreview, error) {
	return metadata.EpisodeOrderPreview{
		StructureProvider: "tvdb", EpisodeCount: 22, FileCount: 21, MatchedFiles: 20,
		UnmatchedFiles: []string{"Stargate SG-1 - S01E02.mkv"},
		Seasons:        []metadata.EpisodeOrderPreviewSeason{{Number: 1, Title: "Season 1", EpisodeCount: 22, FileCount: 20}},
	}, f.err
}

func (f *fakeAdminEpisodeOrders) SaveEpisodeOrder(_ context.Context, _ string, settings metadata.EpisodeOrderSettings, revision *int64, _ int) (int64, error) {
	f.saved, f.revision = settings, revision
	return 3, f.err
}

func adminEpisodeOrderFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "admin_episode_order_read", operationID: "getAdminEpisodeOrder", method: "GET", path: Prefix + "/admin/items/item-1/episode-order", headers: bearer(memberToken), status: 200, schema: "#/components/schemas/AdminEpisodeOrder", assertHeaders: []string{"Content-Type"}, scenario: "A curator reads a series' episode order with the orders each provider offers."},
		{name: "admin_episode_order_previewed", operationID: "previewAdminEpisodeOrder", method: "POST", path: Prefix + "/admin/items/item-1/episode-order/preview", body: `{"provider":"tvdb","order_id":"dvd","file_order":"","layout":"seasons"}`, headers: bearer(memberToken), status: 200, schema: "#/components/schemas/AdminEpisodeOrderPreview", assertHeaders: []string{"Content-Type"}, scenario: "A preview reports the order's seasons and the files it would not place."},
		{name: "admin_episode_order_saved", operationID: "setAdminEpisodeOrder", method: "PUT", path: Prefix + "/admin/items/item-1/episode-order", body: `{"provider":"tvdb","order_id":"dvd","file_order":"","layout":"seasons","revision":2}`, headers: bearer(memberToken), status: 200, schema: "#/components/schemas/AdminEpisodeOrderSaved", assertHeaders: []string{"Content-Type"}, scenario: "Saving stores the order and queues the refresh that renumbers the series."},
	}
}

// A save carries the revision it was based on to the service, and the
// service's conflict answers 409 so an admin reloads before overwriting a
// newer choice.
func TestSetAdminEpisodeOrderConflict(t *testing.T) {
	deps := fixtureDeps()
	orders := &fakeAdminEpisodeOrders{err: metadata.ErrEpisodeOrderConflict}
	deps.AdminEpisodeOrders = orders
	rec := do(t, newTestHandler(t, deps), http.MethodPut, Prefix+"/admin/items/item-1/episode-order",
		`{"provider":"tmdb","order_id":"","file_order":"tvdb","layout":"absolute","revision":2}`, bearer(memberToken))
	requireProblem(t, rec, TypeConflict)
	if orders.revision == nil || *orders.revision != 2 || orders.saved.Layout != "absolute" || orders.saved.FileOrder != "tvdb" {
		t.Fatalf("service got %+v revision %v", orders.saved, orders.revision)
	}
}
