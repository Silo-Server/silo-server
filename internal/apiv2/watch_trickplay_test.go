package apiv2

import (
	"context"
	"errors"
	"net/http"
	"time"

	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/trickplay"
)

// fakeTrickplay publishes previews for file 42 only.
type fakeTrickplay struct {
	err   error
	calls []int
}

func (f *fakeTrickplay) SignedManifest(_ context.Context, fileID int) (trickplay.SignedManifest, bool, error) {
	f.calls = append(f.calls, fileID)
	if f.err != nil {
		return trickplay.SignedManifest{}, false, f.err
	}
	if fileID != 42 {
		return trickplay.SignedManifest{}, false, nil
	}
	return trickplay.SignedManifest{
		Manifest: trickplay.Manifest{FileID: 42, Revision: 9913, Width: 300, Height: 126, TileColumns: 10, TileRows: 10,
			IntervalMS: 10000, ThumbnailCount: 1020, SheetCount: 11, Bandwidth: 1600},
		SheetURLs: []string{
			"/api/v2/artwork/trickplay/42/9913/0.9913.jpg?exp=1767409445&sig=a", "/api/v2/artwork/trickplay/42/9913/1.9913.jpg?exp=1767409445&sig=b",
			"/api/v2/artwork/trickplay/42/9913/2.9913.jpg?exp=1767409445&sig=c", "/api/v2/artwork/trickplay/42/9913/3.9913.jpg?exp=1767409445&sig=d",
			"/api/v2/artwork/trickplay/42/9913/4.9913.jpg?exp=1767409445&sig=e", "/api/v2/artwork/trickplay/42/9913/5.9913.jpg?exp=1767409445&sig=f",
			"/api/v2/artwork/trickplay/42/9913/6.9913.jpg?exp=1767409445&sig=g", "/api/v2/artwork/trickplay/42/9913/7.9913.jpg?exp=1767409445&sig=h",
			"/api/v2/artwork/trickplay/42/9913/8.9913.jpg?exp=1767409445&sig=i", "/api/v2/artwork/trickplay/42/9913/9.9913.jpg?exp=1767409445&sig=j",
			"/api/v2/artwork/trickplay/42/9913/10.9913.jpg?exp=1767409445&sig=k",
		},
		ExpiresAt: time.Date(2026, 1, 3, 3, 4, 5, 0, time.UTC),
	}, true, nil
}

type watchWithUnavailableArtwork struct{ fakeWatch }

func (f *watchWithUnavailableArtwork) WatchDetail(ctx context.Context, userID int, profileID, contentID string, filter catalogpkg.AccessFilter) (*catalogpkg.WatchDetail, error) {
	if !filter.OmitWatchArtwork {
		return nil, errors.New("artwork provider unavailable")
	}
	return f.fakeWatch.WatchDetail(ctx, userID, profileID, contentID, filter)
}

func watchTrickplayFixtureCases() []fixtureCase {
	problem := "#/components/schemas/Problem"
	return []fixtureCase{
		{name: "get_watch_trickplay_ok", operationID: "getWatchTrickplay",
			scenario: "The seek-bar previews of a movie's file: eleven sheets of 10x10 thumbnails, one every ten seconds, each sheet URL signed.",
			method:   http.MethodGet, path: "/api/v2/watch/movie:heat-1995/trickplay?file_id=42", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/WatchTrickplay"},
		{name: "get_watch_trickplay_not_found", operationID: "getWatchTrickplay",
			scenario: "A file the item does not list, or one without published previews: not found.",
			method:   http.MethodGet, path: "/api/v2/watch/movie:heat-1995/trickplay?file_id=7", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusNotFound, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
	}
}
