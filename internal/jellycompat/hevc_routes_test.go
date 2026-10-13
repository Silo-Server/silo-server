package jellycompat

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/go-chi/chi/v5"
)

func TestHEVCPlaybackURLsRejectOlderAPIRouters(t *testing.T) {
	legacy := chi.NewRouter()
	reached := false
	for _, prefix := range []string{"", "/audio-v2", "/remux-v1", "/remux-ts-v1", "/remux-dv-v1"} {
		for _, suffix := range []string{"/master.m3u8", "/hls/{playlistId}/stream.m3u8", "/hls/{playlistId}/{segmentId}.{segmentContainer}"} {
			legacy.Get("/Videos/{id}"+prefix+suffix, func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			})
		}
	}
	for _, channels := range []int{2, 6} {
		source := testCompatSource(NewResourceIDCodec(), testCompatVersion())
		source.TargetVideoCodec = "hevc"
		for i := range source.Version.AudioTracks {
			source.Version.AudioTracks[i].Channels = channels
		}
		dto := (&PlaybackHandler{}).mediaSourceDTO("item", "play", "token", source)
		urls := []string{dto.TranscodingURL}
		for _, resource := range []string{"stream.m3u8", "init.mp4", "seg_00001.m4s"} {
			urls = append(urls, buildSegmentProxyPath("item", "play", source.ID, resource, compatHLSRoutePathSegment(source)))
		}
		for _, resourceURL := range urls {
			reached = false
			rec := httptest.NewRecorder()
			legacy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, resourceURL, nil))
			if rec.Code != http.StatusNotFound || reached {
				t.Errorf("%d-channel HEVC URL %q reached an older H.264 handler: status %d", channels, resourceURL, rec.Code)
			}
			if !strings.Contains(resourceURL, "/hevc-v1/") {
				t.Errorf("HEVC resource %q lacks its recipe route", resourceURL)
			}
		}
	}
}

func TestHEVCHLSRoutesEnforceNegotiatedRecipe(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		codec    string
		channels int
		remux    bool
		prefix   string
	}{
		{name: "legacy route rejects HEVC stereo", codec: "hevc", channels: 2},
		{name: "audio-v2 route rejects HEVC surround", codec: "hevc", channels: 6, prefix: "/audio-v2"},
		{name: "HEVC route rejects H264", codec: "h264", channels: 2, prefix: "/hevc-v1"},
		{name: "HEVC route rejects video copy", codec: "hevc", channels: 2, remux: true, prefix: "/hevc-v1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := testCompatSource(NewResourceIDCodec(), testCompatVersion())
			source.ID = "source"
			source.TargetVideoCodec = test.codec
			source.HLSRemux = test.remux
			for i := range source.Version.AudioTracks {
				source.Version.AudioTracks[i].Channels = test.channels
			}
			store := NewPlaybackSessionStore(time.Hour, nil)
			store.Put(PlaybackSession{
				ID: "play", CompatToken: "token", RouteItemID: "item", UpstreamSessionID: "upstream",
				MediaSources: []PlaybackMediaSource{source},
			})
			sessions := NewSessionStore(time.Hour, nil)
			if err := sessions.Put(Session{Token: "token", StreamAppUserID: 1}); err != nil {
				t.Fatal(err)
			}
			router := NewRouter(Dependencies{Config: cfg, SessionStore: sessions, PlaybackStore: store})
			for _, suffix := range []string{"/master.m3u8", "/hls/play/stream.m3u8", "/hls/play/init.mp4", "/hls/play/seg_00001.m4s"} {
				req := httptest.NewRequest(http.MethodGet, "/Videos/item"+test.prefix+suffix+"?PlaySessionId=play&MediaSourceId=source", nil)
				req.Header.Set("X-Emby-Token", "token")
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				if rec.Code != http.StatusNotFound {
					t.Fatalf("%s status = %d, want 404 before starting playback: %s", suffix, rec.Code, rec.Body.String())
				}
			}
			stored, _ := store.Get("play")
			if stored == nil || stored.TranscodeStarted || stored.Recipe != nil || stored.UpstreamSessionID != "upstream" {
				t.Fatalf("mismatched route changed the session: %+v", stored)
			}
		})
	}
}

func TestHEVCHLSChildRoutesAcceptMatchingRecipeAndCheckIdentity(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	for _, channels := range []int{2, 6} {
		source := testCompatSource(NewResourceIDCodec(), testCompatVersion())
		source.ID, source.TargetVideoCodec = "source", "hevc"
		for i := range source.Version.AudioTracks {
			source.Version.AudioTracks[i].Channels = channels
		}
		store := NewPlaybackSessionStore(time.Hour, nil)
		store.Put(PlaybackSession{ID: "play", CompatToken: "token", RouteItemID: "item", UpstreamSessionID: "upstream", MediaSources: []PlaybackMediaSource{source}})
		sessions := NewSessionStore(time.Hour, nil)
		if err := sessions.Put(Session{Token: "token", StreamAppUserID: 1}); err != nil {
			t.Fatal(err)
		}
		router := NewRouter(Dependencies{Config: cfg, SessionStore: sessions, PlaybackStore: store})
		for _, routeItem := range []string{"item", "other-item"} {
			for _, resource := range []string{"stream.m3u8", "init.mp4", "seg_00001.m4s"} {
				req := httptest.NewRequest(http.MethodGet, "/Videos/"+routeItem+"/hevc-v1/hls/play/"+resource+"?PlaySessionId=play&MediaSourceId=source", nil)
				req.Header.Set("X-Emby-Token", "token")
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				want := http.StatusNotFound
				if routeItem == "item" {
					// The matching HEVC handler accepts its recipe and reaches
					// the requirement to request the master before child bytes.
					want = http.StatusConflict
					if !strings.Contains(rec.Body.String(), compatPlaybackRouteUnboundCode) {
						t.Errorf("matching HEVC route did not reach route binding: %s", rec.Body.String())
					}
				}
				if rec.Code != want {
					t.Errorf("%d-channel %s/%s status = %d, want %d: %s", channels, routeItem, resource, rec.Code, want, rec.Body.String())
				}
			}
		}
	}
}
