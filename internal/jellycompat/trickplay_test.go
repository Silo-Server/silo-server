package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
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
		"#EXTINF:1000,\n#EXT-X-TILES:RESOLUTION=300x126,LAYOUT=10x10,DURATION=10\n0.jpg?MediaSourceId=" + source + "\n" +
		"#EXTINF:1000,\n#EXT-X-TILES:RESOLUTION=300x126,LAYOUT=10x10,DURATION=10\n1.jpg?MediaSourceId=" + source + "\n" +
		"#EXTINF:500,\n#EXT-X-TILES:RESOLUTION=300x126,LAYOUT=10x10,DURATION=10\n2.jpg?MediaSourceId=" + source + "\n" +
		"#EXT-X-ENDLIST\n"
	if rec.Body.String() != want {
		t.Fatalf("playlist:\n%s\nwant:\n%s", rec.Body.String(), want)
	}
	if rec := getWithToken(router, "/Videos/"+itemID+"/Trickplay/320/tiles.m3u8"); rec.Code != http.StatusNotFound {
		t.Fatalf("other width: %d", rec.Code)
	}
}

// A playlist's sheet lines carry the PlaySessionId of the caller's live play
// of that source instead of the caller's token, and that grant alone fetches
// the sheet, but only for the source it negotiated.
func TestTrickplayPlaylistGrantsSheetsByPlaySession(t *testing.T) {
	codec := NewResourceIDCodec()
	codec.SetMediaSourceOwnerLookup(movieOwner{})
	sessions := NewSessionStore(time.Hour, time.Now)
	if err := sessions.Put(Session{Token: "trickplay-grant-token", StreamAppUserID: 1}); err != nil {
		t.Fatal(err)
	}
	itemID := codec.EncodeStringID(EncodedIDItem, "movie-1")
	wide, narrow := codec.EncodeIntID(EncodedIDMediaSource, 42), codec.EncodeIntID(EncodedIDMediaSource, 43)
	now := time.Now()
	plays := NewPlaybackSessionStore(time.Hour, func() time.Time { return now })
	plays.Put(PlaybackSession{
		ID: "play-1", CompatToken: "trickplay-grant-token", ItemID: "movie-1", RouteItemID: itemID,
		MediaSources: []PlaybackMediaSource{{ID: wide, FileID: 42}},
	})
	grid := testTrickplayGrid
	sheets := &fakeTrickplaySheets{files: map[int]bool{42: true, 43: true}}
	router := NewRouter(Dependencies{
		IDCodec: codec, SessionStore: sessions, PlaybackStore: plays, Trickplay: sheets,
		ContentService: &stubContentService{detail: &upstreamItemDetail{
			ContentID: "movie-1", Versions: []catalog.FileVersion{{FileID: 42, Trickplay: &grid}, {FileID: 43, Trickplay: &grid}},
		}},
	})
	get := func(path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			req.Header.Set("X-Emby-Token", token)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	base := "/Videos/" + itemID + "/Trickplay/300/"
	playlist := get(base+"tiles.m3u8?MediaSourceId="+wide, "trickplay-grant-token")
	if playlist.Code != http.StatusOK || strings.Contains(playlist.Body.String(), "trickplay-grant-token") {
		t.Fatalf("playlist: %d %s", playlist.Code, playlist.Body.String())
	}
	line := "0.jpg?MediaSourceId=" + wide + "&PlaySessionId=play-1"
	if !strings.Contains(playlist.Body.String(), "\n"+line+"\n") {
		t.Fatalf("playlist lines do not carry the play's grant:\n%s", playlist.Body.String())
	}
	if rec := get(base+line, ""); rec.Code != http.StatusOK || rec.Body.String() != "jpeg" {
		t.Fatalf("sheet by grant: %d %s", rec.Code, rec.Body.String())
	}
	// With no live play of that source, the lines carry no credential at all.
	narrowPlaylist := get(base+"tiles.m3u8?MediaSourceId="+narrow, "trickplay-grant-token")
	if narrowPlaylist.Code != http.StatusOK || !strings.Contains(narrowPlaylist.Body.String(), "\n0.jpg?MediaSourceId="+narrow+"\n") {
		t.Fatalf("playlist without a play: %d %s", narrowPlaylist.Code, narrowPlaylist.Body.String())
	}
	// Without a source, the grant still serves the source it negotiated, not
	// a more recent play's.
	now = now.Add(time.Minute)
	plays.Put(PlaybackSession{
		ID: "play-2", CompatToken: "trickplay-grant-token", ItemID: "movie-1", RouteItemID: itemID,
		MediaSources: []PlaybackMediaSource{{ID: narrow, FileID: 43}},
	})
	sheets.opened = nil
	if rec := get(base+"0.jpg?PlaySessionId=play-1", ""); rec.Code != http.StatusOK || len(sheets.opened) != 1 || sheets.opened[0] != 42 {
		t.Fatalf("sheet by grant without source: %d opened %v", rec.Code, sheets.opened)
	}
	for _, path := range []string{
		base + "0.jpg?MediaSourceId=" + narrow + "&PlaySessionId=play-1",
		"/Videos/" + codec.EncodeStringID(EncodedIDItem, "movie-2") + "/Trickplay/300/0.jpg?PlaySessionId=play-1",
		base + "0.jpg?MediaSourceId=" + wide,
	} {
		if rec := get(path, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", path, rec.Code)
		}
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
