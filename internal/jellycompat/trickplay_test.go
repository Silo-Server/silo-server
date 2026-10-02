package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

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

// trickplayRouter serves the trickplay routes for a movie with a wide
// default version (file 42, with previews) and a narrower one (file 43).
func trickplayRouter(t *testing.T, sheets *fakeTrickplaySheets) (http.Handler, *ResourceIDCodec) {
	t.Helper()
	codec := NewResourceIDCodec()
	codec.SetMediaSourceOwnerLookup(movieOwner{})
	wide, narrow := testTrickplayGrid, testTrickplayGrid
	handler := &PlaybackHandler{codec: codec, Trickplay: sheets, content: &stubContentService{detail: &upstreamItemDetail{
		ContentID: "movie-1",
		Versions: []catalog.FileVersion{
			{FileID: 42, Trickplay: &wide},
			{FileID: 43, Trickplay: &narrow},
		},
	}}}
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Emby-Token") != "" {
				r = r.WithContext(context.WithValue(r.Context(), compatSessionKey, &Session{Token: "token", StreamAppUserID: 1}))
			}
			next.ServeHTTP(w, r)
		})
	})
	router.Get(compatTrickplaySheetRoute, handler.HandleTrickplaySheet)
	router.Get(compatTrickplayPlaylistRoute, handler.HandleTrickplayPlaylist)
	return router, codec
}

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

func TestTrickplaySheetRoute(t *testing.T) {
	sheets := &fakeTrickplaySheets{files: map[int]bool{42: true, 43: true}}
	router, codec := trickplayRouter(t, sheets)
	itemID := codec.EncodeStringID(EncodedIDItem, "movie-1")
	narrowSource := codec.EncodeIntID(EncodedIDMediaSource, 43)

	// Without a media source the default (widest) version's sheets serve.
	rec := getWithToken(router, "/Videos/"+itemID+"/Trickplay/300/1.jpg")
	if rec.Code != http.StatusOK || rec.Body.String() != "jpeg" || rec.Header().Get("Content-Type") != "image/jpeg" ||
		rec.Header().Get("ETag") != `"7-1"` || rec.Header().Get("Cache-Control") != "private, no-cache" || sheets.opened[0] != 42 {
		t.Fatalf("default sheet: %d %q %v opened %v", rec.Code, rec.Body.String(), rec.Header(), sheets.opened)
	}
	// The mediaSourceId query picks the version, in either spelling.
	for _, param := range []string{"mediaSourceId", "MediaSourceId"} {
		sheets.opened = nil
		if rec := getWithToken(router, "/Videos/"+itemID+"/Trickplay/300/0.jpg?"+param+"="+narrowSource); rec.Code != http.StatusOK || sheets.opened[0] != 43 {
			t.Fatalf("%s: %d opened %v", param, rec.Code, sheets.opened)
		}
	}
	// A media source id in the item position picks that version.
	sheets.opened = nil
	if rec := getWithToken(router, "/Videos/"+narrowSource+"/Trickplay/300/0.jpg"); rec.Code != http.StatusOK || sheets.opened[0] != 43 {
		t.Fatalf("media source route: %d opened %v", rec.Code, sheets.opened)
	}
	// Revalidation.
	if rec := getWithToken(router, "/Videos/"+itemID+"/Trickplay/300/1.jpg", "If-None-Match", `"7-1"`); rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Fatalf("revalidation: %d", rec.Code)
	}
	// Findroid asks for one sheet past the end; other widths and foreign
	// sources do not exist.
	for _, path := range []string{
		"/Videos/" + itemID + "/Trickplay/300/3.jpg",
		"/Videos/" + itemID + "/Trickplay/320/0.jpg",
		"/Videos/" + itemID + "/Trickplay/300/0.jpg?MediaSourceId=" + codec.EncodeIntID(EncodedIDMediaSource, 99),
		"/Videos/" + itemID + "/Trickplay/abc/0.jpg",
	} {
		if rec := getWithToken(router, path); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", path, rec.Code)
		}
	}
	unauthenticated := httptest.NewRecorder()
	router.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/Videos/"+itemID+"/Trickplay/300/0.jpg", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d", unauthenticated.Code)
	}
	sheets.err = errors.New("storage down")
	if rec := getWithToken(router, "/Videos/"+itemID+"/Trickplay/300/0.jpg"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("storage failure: %d", rec.Code)
	}
}

func TestTrickplayRouterDoesNotRequireSubtitles(t *testing.T) {
	codec := NewResourceIDCodec()
	store := NewSessionStore(time.Hour, time.Now)
	if err := store.Put(Session{Token: "trickplay-router-test", StreamAppUserID: 1}); err != nil {
		t.Fatal(err)
	}
	sheets := &fakeTrickplaySheets{files: map[int]bool{42: true}}
	grid := testTrickplayGrid
	router := NewRouter(Dependencies{
		IDCodec: codec, SessionStore: store, Trickplay: sheets,
		ContentService: &stubContentService{detail: &upstreamItemDetail{
			ContentID: "movie-1", Versions: []catalog.FileVersion{{FileID: 42, Trickplay: &grid}},
		}},
	})
	itemID := codec.EncodeStringID(EncodedIDItem, "movie-1")
	req := httptest.NewRequest(http.MethodGet, "/Videos/"+itemID+"/Trickplay/300/0.jpg", nil)
	req.Header.Set("X-Emby-Token", "trickplay-router-test")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "jpeg" {
		t.Fatalf("independent trickplay dependency: %d %q", rec.Code, rec.Body.String())
	}
}

func TestTrickplayPlaylistRoute(t *testing.T) {
	router, codec := trickplayRouter(t, &fakeTrickplaySheets{files: map[int]bool{42: true}})
	itemID := codec.EncodeStringID(EncodedIDItem, "movie-1")
	rec := getWithToken(router, "/Videos/"+itemID+"/Trickplay/300/tiles.m3u8")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/x-mpegURL" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	source := codec.EncodeIntID(EncodedIDMediaSource, 42)
	want := "#EXTM3U\n#EXT-X-TARGETDURATION:1000\n#EXT-X-VERSION:7\n#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-IMAGES-ONLY\n" +
		"#EXTINF:1000,\n#EXT-X-TILES:RESOLUTION=300x126,LAYOUT=10x10,DURATION=10\n0.jpg?ApiKey=token&MediaSourceId=" + source + "\n" +
		"#EXTINF:1000,\n#EXT-X-TILES:RESOLUTION=300x126,LAYOUT=10x10,DURATION=10\n1.jpg?ApiKey=token&MediaSourceId=" + source + "\n" +
		"#EXTINF:500,\n#EXT-X-TILES:RESOLUTION=300x126,LAYOUT=10x10,DURATION=10\n2.jpg?ApiKey=token&MediaSourceId=" + source + "\n" +
		"#EXT-X-ENDLIST\n"
	if rec.Body.String() != want {
		t.Fatalf("playlist:\n%s\nwant:\n%s", rec.Body.String(), want)
	}
	if rec := getWithToken(router, "/Videos/"+itemID+"/Trickplay/320/tiles.m3u8"); rec.Code != http.StatusNotFound {
		t.Fatalf("other width: %d", rec.Code)
	}
}

// TestTrickplayMember maps versions with previews to Jellyfin's Trickplay
// member, keyed by media source and width, only when the field is wanted.
func TestTrickplayMember(t *testing.T) {
	codec := NewResourceIDCodec()
	m := newMapper(codec, nil)
	grid := testTrickplayGrid
	detail := upstreamItemDetail{ContentID: "movie-1", Type: "movie", Versions: []catalog.FileVersion{{FileID: 42, Trickplay: &grid}, {FileID: 43}}}

	dto := m.itemFromDetail(detail, false, nil)
	raw, err := json.Marshal(dto.Trickplay)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"` + codec.EncodeIntID(EncodedIDMediaSource, 42) + `":{"300":{"Width":300,"Height":126,"TileWidth":10,"TileHeight":10,"ThumbnailCount":250,"Interval":10000,"Bandwidth":1600}}}`
	if string(raw) != want {
		t.Fatalf("trickplay %s, want %s", raw, want)
	}
	if listed := m.itemFromDetailWithFields(detail, false, nil, map[string]bool{"chapters": true}); listed.Trickplay != nil {
		t.Fatal("trickplay mapped without the field")
	}
	if listed := m.itemFromDetailWithFields(detail, false, nil, map[string]bool{"trickplay": true}); len(listed.Trickplay) != 1 {
		t.Fatal("trickplay not mapped with the field")
	}
	none := upstreamItemDetail{ContentID: "movie-2", Type: "movie", Versions: []catalog.FileVersion{{FileID: 44}}}
	full, _ := json.Marshal(m.itemFromDetail(none, false, nil))
	if strings.Contains(string(full), "Trickplay") {
		t.Fatalf("an item without previews carries Trickplay: %s", full)
	}
}

func TestTrickplayRoutesSkipLoggingAndCompression(t *testing.T) {
	if !skipCompatActivityLog(compatTrickplaySheetRoute) || !skipCompatActivityLog(compatTrickplayPlaylistRoute) {
		t.Fatal("trickplay routes are activity-logged")
	}
	if !skipCompatMediaCompression(httptest.NewRequest(http.MethodGet, "/Videos/abc/Trickplay/300/0.jpg", nil)) {
		t.Fatal("trickplay sheets are compressed")
	}
}

func TestPlayingFileUsesDurableSelectedSource(t *testing.T) {
	now := time.Now()
	original := PlaybackSession{ID: "play", CompatToken: "token", ItemID: "movie-1", UpstreamSessionID: "upstream", UpstreamMediaFileID: 43, UpdatedAt: now, ExpiresAt: now.Add(time.Hour)}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var restored PlaybackSession
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	store := NewPlaybackSessionStore(time.Hour, func() time.Time { return now })
	store.PutNegotiated(restored)
	// A fresh API process has durable compat state and no native session.
	handler := &PlaybackHandler{playbackStore: store}
	if got := handler.playingFile(t.Context(), &Session{Token: "token"}, "movie-1"); got != 43 {
		t.Fatalf("selected file=%d want43", got)
	}
	if got := handler.playingFile(t.Context(), &Session{Token: "other-token"}, "movie-1"); got != 0 {
		t.Fatalf("another token's file=%d", got)
	}
	if got := handler.playingFile(t.Context(), &Session{Token: "token"}, "movie-2"); got != 0 {
		t.Fatalf("another item's file=%d", got)
	}
}

func TestPlayingFileSurvivesAPIReplicaDB(t *testing.T) {
	pool := newCompatTestPool(t)
	id := fmt.Sprintf("trickplay-replica-%d", time.Now().UnixNano())
	token := id + "-token"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM jellycompat_playback_sessions WHERE id=$1`, id)
	})
	owner := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	owner.Put(PlaybackSession{ID: id, CompatToken: token, ItemID: "movie-1", UserID: "viewer", UpstreamSessionID: "native", UpstreamMediaFileID: 43})
	replica := &PlaybackHandler{playbackStore: NewDurableCompatPlaybackStore(pool, time.Hour, nil)}
	if got := replica.playingFile(t.Context(), &Session{Token: token}, "movie-1"); got != 43 {
		t.Fatalf("fresh replica selected file=%d want43", got)
	}
}
