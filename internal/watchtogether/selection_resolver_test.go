package watchtogether

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

type roomWatchDetail struct {
	versions []catalog.FileVersion
}

type unavailableArtworkWatchDetail struct{}

func (unavailableArtworkWatchDetail) GetWatchDetail(_ context.Context, _ string, filter catalog.AccessFilter) (*catalog.WatchDetail, error) {
	if !filter.OmitWatchArtwork {
		return nil, errors.New("artwork provider unavailable")
	}
	return &catalog.WatchDetail{ContentID: "movie-1", Type: "movie", Versions: []catalog.FileVersion{
		{FileID: 1, Resolution: "2160p", EditionKey: "theatrical"},
		{FileID: 2, Resolution: "1080p", EditionKey: "theatrical"},
	}}, nil
}

func (d roomWatchDetail) GetWatchDetail(context.Context, string, catalog.AccessFilter) (*catalog.WatchDetail, error) {
	return &catalog.WatchDetail{ContentID: "movie-1", Type: "movie", Versions: d.versions}, nil
}

func TestRoomSelectionDoesNotImposeResolutionOrRangeLimits(t *testing.T) {
	for _, versions := range [][]catalog.FileVersion{
		{{FileID: 1, Resolution: "1080p", HDR: true}, {FileID: 2, Resolution: "2160p"}},
		{{FileID: 1, Resolution: "1080p"}, {FileID: 2, Resolution: "1080p", HDR: true}},
		{{FileID: 1, Resolution: "2160p", HDR: true}, {FileID: 2, Resolution: "4320p", HDR: true}},
		{{FileID: 2, Resolution: "2160p", HDR: true}},
	} {
		resolver := NewCatalogSelectionResolver(roomWatchDetail{versions})
		selected, err := resolver.ResolveSelection(t.Context(), 7, "host", SelectItemInput{ContentID: "movie-1"})
		if err != nil || selected.FileID == nil || *selected.FileID != 2 {
			t.Fatalf("selection = %+v, error = %v", selected, err)
		}
	}
}
