package jellycompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/themedelivery"
	"github.com/Silo-Server/silo-server/internal/themesongs"
)

func TestThemeConversionAllowed(t *testing.T) {
	file := themesongs.File{Song: themesongs.Song{Container: "ogg", DurationSeconds: 90}, AudioCodec: "vorbis", AudioChannels: 6}
	check := func(raw, routeContainer string, universal bool) (themesongs.Conversion, float64, bool) {
		t.Helper()
		values, err := url.ParseQuery(raw)
		if err != nil {
			t.Fatal(err)
		}
		conversion, seek, _, ok := themeConversionAllowed(newCaseInsensitiveQuery(values), routeContainer, file, universal)
		return conversion, seek, ok
	}
	if conversion, _, ok := check("", "mp4", false); !ok || conversion.Channels != 2 || conversion.SourceChannels != 6 || conversion.BitrateKbps != 192 {
		t.Fatalf("stream.mp4 conversion = %+v %v", conversion, ok)
	}
	if conversion, _, ok := check("TranscodingContainer=m4a&TranscodingProtocol=http&AudioCodec=aac,mp3&MaxAudioChannels=1&MaxStreamingBitrate=96000", "", true); !ok ||
		conversion.Channels != 1 || conversion.SourceChannels != 0 || conversion.BitrateKbps != 96 {
		t.Fatalf("universal http conversion = %+v %v", conversion, ok)
	}
	if _, seek, ok := check("container=mp4&StartTimeTicks=1200000000", "", false); !ok || seek != 90 {
		t.Fatalf("seek past the end = %v %v", seek, ok)
	}
	for _, refused := range []struct {
		query, container string
		universal        bool
	}{
		{"", "", false},                   // no target container named
		{"static=true", "mp4", false},     // static asks for the original
		{"audioCodec=opus", "mp4", false}, // conversion is AAC only
		{"", "ogg", false},                // not an MP4 target
		{"TranscodingContainer=mp4&TranscodingProtocol=hls", "", true},
		{"AudioCodec=aac", "", true},            // universal without a transcoding container
		{"maxAudioBitRate=16000", "mp4", false}, // below the conversion floor
		{"audioStreamIndex=2", "mp4", false},    // themes have one audio stream
	} {
		if _, _, ok := check(refused.query, refused.container, refused.universal); ok {
			t.Fatalf("conversion accepted %+v", refused)
		}
	}
}

type compatThemePlanner struct {
	proxy    *nodepool.Node
	released int
}

func (p *compatThemePlanner) PlanRoute(req nodepool.RouteRequest) nodepool.Plan {
	if req.NeedsTranscode || !req.NeedsProxy || (req.ProxyEligible != nil && !req.ProxyEligible(p.proxy)) {
		return nodepool.Plan{}
	}
	return nodepool.Plan{ProxyNode: p.proxy}
}

func (p *compatThemePlanner) ReleaseSession(string) { p.released++ }

func TestCompatThemeAudioRoutesAndConverts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "theme.ogg")
	if err := os.WriteFile(path, []byte("ogg source"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	store := &compatThemeFixture{file: themesongs.File{Song: themesongs.Song{ID: "7", Title: "Theme", Container: "ogg"}, AudioCodec: "vorbis", AudioChannels: 2, OwnerPath: dir, Path: path, Size: info.Size(), Modified: info.ModTime().Truncate(time.Microsecond)}}
	raw, err := json.Marshal(playback.HWAccelInfo{TransportFeatures: []string{playback.TransportFeatureThemeAudioEgressV1}})
	if err != nil {
		t.Fatal(err)
	}
	planner := &compatThemePlanner{proxy: &nodepool.Node{ID: 5, URL: "http://proxy-a", Capabilities: raw}}
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg.sh")
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\nprintf compat-aac\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := &ItemsHandler{codec: NewResourceIDCodec(), themeSongs: store, themeFFmpegPath: func() string { return ffmpeg }}
	router := chi.NewRouter()
	for _, route := range []string{"/Audio/{itemId}/stream", "/Audio/{itemId}/stream.{container}", "/Audio/{itemId}/universal"} {
		router.Get(route, h.HandleThemeAudio)
		router.Head(route, h.HandleThemeAudio)
	}
	id := EncodeNumericID(EncodedIDThemeSong, 7).String()
	request := func(method, target string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, nil)
		r = r.WithContext(context.WithValue(r.Context(), compatSessionKey, &Session{StreamAppUserID: 1, ProfileID: "profile"}))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}

	// Routed original: redirect to the reserved proxy, as compat video does.
	h.themeRouter = &themedelivery.Router{Planner: planner, Secret: func() string { return "compat secret" }}
	w := request(http.MethodGet, "/Audio/"+id+"/stream")
	if w.Code != http.StatusTemporaryRedirect || !strings.HasPrefix(w.Header().Get("Location"), "http://proxy-a/stream/theme/") {
		t.Fatalf("routed theme = %d %q", w.Code, w.Header().Get("Location"))
	}
	if w = request(http.MethodHead, "/Audio/"+id+"/stream"); w.Code != http.StatusTemporaryRedirect || planner.released != 1 {
		t.Fatalf("routed HEAD = %d released=%d", w.Code, planner.released)
	}

	// A policy with no legal route refuses instead of serving from here.
	proxyOnly := config.DefaultPlaybackRoutingPolicy()
	proxyOnly.DirectPlayEgress = config.PlaybackEgressProxyOnly
	h.themeRouter = &themedelivery.Router{Policy: func() config.PlaybackRoutingPolicy { return proxyOnly }}
	if w = request(http.MethodGet, "/Audio/"+id+"/stream"); w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), compatRoutingPolicyUnsatisfiedCode) {
		t.Fatalf("proxy_only without proxies = %d %s", w.Code, w.Body.String())
	}

	// Nothing can convert: the conversion is refused like an unsupported format.
	h.themeRouter = &themedelivery.Router{}
	if w = request(http.MethodGet, "/Audio/"+id+"/stream.mp4"); w.Code != http.StatusBadRequest {
		t.Fatalf("unconvertible theme = %d %s", w.Code, w.Body.String())
	}

	// No proxy converts and this node runs the AAC recipe: convert here.
	h.themeRouter = &themedelivery.Router{LocalConversion: func(context.Context) bool { return true }}
	w = request(http.MethodGet, "/Audio/"+id+"/stream.mp4")
	if w.Code != http.StatusOK || w.Body.String() != "compat-aac" || w.Header().Get("Content-Type") != themesongs.ConvertedContentType {
		t.Fatalf("local conversion = %d %q %v", w.Code, w.Body.String(), w.Header())
	}
	// Jellyfin Web asks for HLS conversion, which themes do not offer.
	if w = request(http.MethodGet, "/Audio/"+id+"/universal?Container=mp3&TranscodingContainer=mp4&TranscodingProtocol=hls&AudioCodec=aac"); w.Code != http.StatusBadRequest {
		t.Fatalf("HLS conversion = %d %s", w.Code, w.Body.String())
	}
}
