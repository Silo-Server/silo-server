package jellycompat

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

var testTrickplayGrid = catalog.TrickplayGrid{Width: 300, Height: 126, TileColumns: 10, TileRows: 10, ThumbnailCount: 250, IntervalMS: 10000, Bandwidth: 1600}

// fakeTrickplaySheets holds three sheets of width 300 for each file in files.
type fakeTrickplaySheets struct {
	files  map[int]bool
	err    error
	opened []int
}

func (f *fakeTrickplaySheets) OpenSheet(_ context.Context, fileID, width, index int) (io.ReadCloser, string, bool, error) {
	if f.err != nil {
		return nil, "", false, f.err
	}
	f.opened = append(f.opened, fileID)
	if !f.files[fileID] || width != 300 || index < 0 || index > 2 {
		return nil, "", false, nil
	}
	return io.NopCloser(strings.NewReader("jpeg")), `"7-` + string(rune('0'+index)) + `"`, true, nil
}

// movieOwner says every media source belongs to movie-1.
type movieOwner struct{}

func (movieOwner) PlayableContentID(context.Context, int) (string, error) { return "movie-1", nil }

func getWithToken(router http.Handler, path string, header ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Emby-Token", "token")
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// listFailingPlayStore fails every listing of a token's plays.
type listFailingPlayStore struct{ CompatPlaybackStore }

func (listFailingPlayStore) ListActiveForToken(context.Context, string) ([]PlaybackSession, error) {
	return nil, errors.New("database unavailable")
}
