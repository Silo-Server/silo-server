package watchtogether

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

type roomWatchDetail struct {
	versions []catalog.FileVersion
}

func (d roomWatchDetail) GetWatchDetail(context.Context, string, catalog.AccessFilter) (*catalog.WatchDetail, error) {
	return &catalog.WatchDetail{ContentID: "movie-1", Type: "movie", Versions: d.versions}, nil
}

func TestRoomSelectionKeepsHighestQualitySource(t *testing.T) {
	versions := []catalog.FileVersion{
		{FileID: 1, Resolution: "2160p", HDR: true, FileSize: 100, EditionKey: "theatrical"},
		{FileID: 2, Resolution: "2160p", FileSize: 500, EditionKey: "theatrical"},
		{FileID: 3, Resolution: "720p", EditionKey: "theatrical"},
		{FileID: 4, Resolution: "1080p", FileSize: 100, EditionKey: "theatrical"},
		{FileID: 5, Resolution: "1080p", FileSize: 200, EditionKey: "theatrical"},
		{FileID: 6, Resolution: "4320p", HDR: true, FileSize: 900, EditionKey: "extended"},
		{FileID: 7, Resolution: "2160p", HDR: true, FileSize: 200, EditionKey: "theatrical"},
	}
	resolver := NewCatalogSelectionResolver(roomWatchDetail{versions})
	selected, err := resolver.ResolveSelection(t.Context(), 7, "host", SelectItemInput{ContentID: "movie-1"})
	if err != nil || selected.FileID == nil || *selected.FileID != 7 {
		t.Fatalf("selection = %+v, error = %v; want highest-quality 4K HDR file in the selected edition", selected, err)
	}
	// Explicit API selections keep their existing meaning.
	selected, err = resolver.ResolveSelection(t.Context(), 7, "host", SelectItemInput{ContentID: "movie-1", FileID: new(1)})
	if err != nil || selected.FileID == nil || *selected.FileID != 1 {
		t.Fatalf("explicit selection = %+v, error = %v", selected, err)
	}
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
