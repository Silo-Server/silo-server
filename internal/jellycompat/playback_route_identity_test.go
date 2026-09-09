package jellycompat

import (
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"

	"github.com/go-chi/chi/v5"
)

func TestVideoStreamTokenAuthRouteIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, route, source, psid string
		seeded                    bool
		status                    int
	}{
		{"correct negotiated route", "item", "source", "server-ps", true, 200},
		{"foreign item with own session", "foreign", "source", "server-ps", true, 404},
		{"source ID is not an item route", "source", "source", "server-ps", true, 404},
		{"unknown source with own session", "item", "unknown", "server-ps", true, 400},
		{"unknown source with route reuse", "item", "unknown", "client-ps", true, 400},
		{"correct source with route reuse", "item", "source", "client-ps", true, 200},
		{"item source alias", "item", "item", "server-ps", true, 200},
		{"direct file without PlaybackInfo", "item", "source", "client-ps", false, 200},
		{"unknown source without PlaybackInfo", "item", "unknown", "client-ps", false, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, item, wantBody := newStaticDirectPlayHandler(t)
			source := h.codec.EncodeIntID(EncodedIDMediaSource, 42)
			ids := map[string]string{"item": item, "source": source, "foreign": h.codec.EncodeStringID(EncodedIDItem, "movie-2"), "unknown": "unknown"}
			if tc.seeded {
				session, _, err := h.createStaticPlaySession(t.Context(), &Session{Token: "token-1", StreamAppUserID: 1}, item, source, "")
				if err != nil {
					t.Fatal(err)
				}
				session.ID = "server-ps"
				h.playbackStore.Put(*session)
			}
			sessions := NewSessionStore(time.Hour, nil)
			if err := sessions.Put(Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"}); err != nil {
				t.Fatal(err)
			}
			router := chi.NewRouter()
			router.With(PlaybackSessionAuth(sessions, h.playbackStore, nil)).Get("/Videos/{id}/stream", h.HandleVideoStream)
			query := url.Values{"Static": {"true"}, "MediaSourceId": {ids[tc.source]}, "PlaySessionId": {tc.psid}}
			req := httptest.NewRequest("GET", "/Videos/"+ids[tc.route]+"/stream?"+query.Encode(), nil)
			req.Header.Set("X-Emby-Token", "token-1")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status %d want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.status == 200 && rec.Body.String() != wantBody {
				t.Fatalf("wrong media: %q", rec.Body.String())
			}
			if tc.status != 200 && rec.Body.String() == wantBody {
				t.Fatal("rejected route served media")
			}
			if tc.seeded && tc.status != 200 {
				session, _ := h.playbackStore.Get("server-ps")
				if session.ClientPlaySessionID != "" {
					t.Fatal("rejected route mutated client session binding")
				}
			}
			if !tc.seeded && tc.status != 200 {
				if _, _, ok := h.playbackStore.FindByRoute("token-1", item); ok {
					t.Fatal("invalid source created playback session")
				}
			}
		})
	}
}

func TestSubtitleTokenAuthRouteIdentity(t *testing.T) {
	const body = "1\n00:00:00,000 --> 00:00:01,000\nHello\n"
	path := filepath.Join(t.TempDir(), "movie.srt")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{ID: "ps", CompatToken: "token", RouteItemID: "item", MediaSources: []PlaybackMediaSource{{ID: "source", FileID: 42}}})
	h := &PlaybackHandler{playbackStore: store, fileResolver: testCompatFileResolver{file: &models.MediaFile{ID: 42, ExternalSubtitles: []models.ExternalSubtitle{{Path: path, Format: "srt"}}}}}
	sessions := NewSessionStore(time.Hour, nil)
	if err := sessions.Put(Session{Token: "token", StreamAppUserID: 1}); err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.With(PlaybackSessionAuth(sessions, store, nil)).Get("/Videos/{routeItemId}/{routeMediaSourceId}/Subtitles/{routeIndex}/stream.{routeFormat}", h.HandleSubtitleStream)
	for _, tc := range []struct {
		item, source string
		status       int
	}{{"item", "source", 200}, {"foreign", "source", 404}, {"item", "unknown", 404}, {"item", "item", 404}} {
		t.Run(tc.item+"/"+tc.source, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/Videos/"+tc.item+"/"+tc.source+"/Subtitles/1/stream.srt?PlaySessionId=ps", nil)
			req.Header.Set("X-Emby-Token", "token")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status %d want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.status == 200 && rec.Body.String() != body {
				t.Fatalf("subtitle %q", rec.Body.String())
			}
		})
	}
}
