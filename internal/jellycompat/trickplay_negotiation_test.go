package jellycompat

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func negotiatedTrickplayRouter(handler *PlaybackHandler) http.Handler {
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.WithContext(context.WithValue(r.Context(), compatSessionKey, &Session{Token: "token-1"}))
			next.ServeHTTP(w, r)
		})
	})
	router.Get(compatTrickplaySheetRoute, handler.HandleTrickplaySheet)
	router.Get(compatTrickplayPlaylistRoute, handler.HandleTrickplayPlaylist)
	return router
}

func TestTrickplayUsesNegotiatedSourceBeforeStreaming(t *testing.T) {
	for _, inRoute := range []bool{false, true} {
		t.Run(map[bool]string{false: "body source", true: "route source"}[inRoute], func(t *testing.T) {
			handler, content := newTwoVersionPlaybackInfoHandler(t)
			for i := range content.detail.Versions {
				content.detail.Versions[i].Trickplay = new(testTrickplayGrid)
			}
			sheets := &fakeTrickplaySheets{files: map[int]bool{42: true, 43: true}}
			handler.Trickplay = sheets
			itemID := handler.codec.EncodeStringID(EncodedIDItem, "movie-1")
			sourceID := handler.codec.EncodeIntID(EncodedIDMediaSource, 43)
			routeID, body := itemID, `{"MediaSourceId":"`+sourceID+`"}`
			if inRoute {
				routeID, body = sourceID, `{}`
			}
			resp := postPlaybackInfo(t, handler, routeID, body)
			play, ok := handler.playbackStore.Get(resp.PlaySessionID)
			if !ok || play.UpstreamSessionID != "" || len(play.MediaSources) != 1 || play.MediaSources[0].FileID != 43 {
				t.Fatalf("negotiated play=%+v found=%v", play, ok)
			}
			router := negotiatedTrickplayRouter(handler)
			if rec := getWithToken(router, "/Videos/"+itemID+"/Trickplay/300/0.jpg"); rec.Code != http.StatusOK || len(sheets.opened) != 1 || sheets.opened[0] != 43 {
				t.Errorf("pre-stream sheet status=%d opened=%v", rec.Code, sheets.opened)
			}
			if rec := getWithToken(router, "/Videos/"+itemID+"/Trickplay/300/tiles.m3u8"); rec.Code != http.StatusOK || strings.Count(rec.Body.String(), "MediaSourceId="+sourceID) != 3 {
				t.Errorf("pre-stream playlist status=%d body=%s", rec.Code, rec.Body.String())
			}
			sheets.opened = nil
			defaultID := handler.codec.EncodeIntID(EncodedIDMediaSource, 42)
			if rec := getWithToken(router, "/Videos/"+itemID+"/Trickplay/300/0.jpg?MediaSourceId="+defaultID); rec.Code != http.StatusOK || len(sheets.opened) != 1 || sheets.opened[0] != 42 {
				t.Fatalf("explicit source override status=%d opened=%v", rec.Code, sheets.opened)
			}
		})
	}
}

type versionedTrickplaySheets struct{}

func (versionedTrickplaySheets) OpenSheet(_ context.Context, fileID, _, index int) (io.ReadCloser, string, bool, error) {
	return io.NopCloser(strings.NewReader(fmt.Sprint(fileID))), fmt.Sprintf(`"%d-%d"`, fileID, index), true, nil
}

func TestTrickplaySheetRevalidatesAfterVersionSwitch(t *testing.T) {
	handler, _ := newTwoVersionPlaybackInfoHandler(t)
	handler.Trickplay = versionedTrickplaySheets{}
	now := time.Now()
	store := NewPlaybackSessionStore(time.Hour, func() time.Time { return now })
	handler.playbackStore = store
	router := negotiatedTrickplayRouter(handler)
	itemID := handler.codec.EncodeStringID(EncodedIDItem, "movie-1")
	path := "/Videos/" + itemID + "/Trickplay/300/0.jpg"
	etag := ""
	for _, fileID := range []int{42, 43} {
		now = now.Add(time.Minute)
		store.PutNegotiated(PlaybackSession{
			ID: fmt.Sprint(fileID), CompatToken: "token-1", ItemID: "movie-1", UpstreamMediaFileID: fileID,
		})
		rec := getWithToken(router, path, "If-None-Match", etag)
		if rec.Code != http.StatusOK || rec.Body.String() != fmt.Sprint(fileID) {
			t.Fatalf("selected file %d: status=%d body=%s", fileID, rec.Code, rec.Body.String())
		}
		if cache := rec.Header().Get("Cache-Control"); cache != "private, no-cache" {
			t.Fatalf("selected file %d permits stale sheet reuse: %s", fileID, cache)
		}
		if next := rec.Header().Get("ETag"); next == "" || next == etag {
			t.Fatalf("selected file %d did not change the validator: %s", fileID, next)
		} else {
			etag = next
		}
	}
	rec := getWithToken(router, path, "If-None-Match", etag)
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 || rec.Header().Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("unchanged source revalidation: status=%d body=%s headers=%v", rec.Code, rec.Body.String(), rec.Header())
	}
}

func TestTrickplayNegotiatedSourceSurvivesAPIReplicaDB(t *testing.T) {
	pool := newCompatTestPool(t)
	handler, content := newTwoVersionPlaybackInfoHandler(t)
	for i := range content.detail.Versions {
		content.detail.Versions[i].Trickplay = new(testTrickplayGrid)
	}
	handler.playbackStore = NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	sourceID := handler.codec.EncodeIntID(EncodedIDMediaSource, 43)
	resp := postPlaybackInfo(t, handler, handler.codec.EncodeStringID(EncodedIDItem, "movie-1"), `{"MediaSourceId":"`+sourceID+`"}`)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(t.Context()), `DELETE FROM jellycompat_playback_sessions WHERE id=$1`, resp.PlaySessionID)
	})
	// The replica has no native playback session and no cached negotiation.
	handler.playbackStore = NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	handler.sessionMgr = nil
	sheets := &fakeTrickplaySheets{files: map[int]bool{43: true}}
	handler.Trickplay = sheets
	itemID := handler.codec.EncodeStringID(EncodedIDItem, "movie-1")
	router := negotiatedTrickplayRouter(handler)
	if rec := getWithToken(router, "/Videos/"+itemID+"/Trickplay/300/0.jpg"); rec.Code != http.StatusOK || len(sheets.opened) != 1 || sheets.opened[0] != 43 {
		t.Fatalf("replica pre-stream sheet status=%d opened=%v", rec.Code, sheets.opened)
	}
	if rec := getWithToken(router, "/Videos/"+itemID+"/Trickplay/300/tiles.m3u8"); rec.Code != http.StatusOK || strings.Count(rec.Body.String(), "MediaSourceId="+sourceID) != 3 {
		t.Fatalf("replica pre-stream playlist status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPlayingFileUsesLatestUnambiguousNegotiation(t *testing.T) {
	now := time.Now()
	store := NewPlaybackSessionStore(time.Hour, func() time.Time { return now })
	store.PutNegotiated(PlaybackSession{ID: "playing", CompatToken: "token", ItemID: "movie-1", UpstreamSessionID: "native", UpstreamMediaFileID: 43})
	now = now.Add(time.Minute)
	store.PutNegotiated(PlaybackSession{ID: "selected", CompatToken: "token", ItemID: "movie-1", MediaSources: []PlaybackMediaSource{{FileID: 42}}})
	now = now.Add(time.Minute)
	store.PutNegotiated(PlaybackSession{ID: "unselected", CompatToken: "token", ItemID: "movie-1", MediaSources: []PlaybackMediaSource{{FileID: 43}, {FileID: 44}}})
	store.PutNegotiated(PlaybackSession{ID: "other-token", CompatToken: "another-token", ItemID: "movie-1", MediaSources: []PlaybackMediaSource{{FileID: 44}}})
	store.PutNegotiated(PlaybackSession{ID: "other-item", CompatToken: "token", ItemID: "movie-2", MediaSources: []PlaybackMediaSource{{FileID: 44}}})
	handler := &PlaybackHandler{playbackStore: store}
	if got := handler.playingFile(t.Context(), &Session{Token: "token"}, "movie-1"); got != 42 {
		t.Fatalf("latest selected file=%d want42", got)
	}
	if got := handler.playingFile(t.Context(), &Session{Token: "missing-token"}, "movie-1"); got != 0 {
		t.Fatalf("unrelated token selected file=%d", got)
	}
	if got := handler.playingFile(t.Context(), &Session{Token: "token"}, "missing-item"); got != 0 {
		t.Fatalf("unrelated item selected file=%d", got)
	}
	store = NewPlaybackSessionStore(time.Hour, func() time.Time { return now })
	store.PutNegotiated(PlaybackSession{ID: "ambiguous", CompatToken: "token", ItemID: "movie-1", MediaSources: []PlaybackMediaSource{{FileID: 43}, {FileID: 44}}})
	handler.playbackStore = store
	if got := handler.playingFile(t.Context(), &Session{Token: "token"}, "movie-1"); got != 0 {
		t.Fatalf("guessed a file from an unselected negotiation: %d", got)
	}
}
