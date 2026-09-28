package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/themesongs"
	"github.com/go-chi/chi/v5"
)

type compatThemeFixture struct {
	file    themesongs.File
	owner   string
	inherit bool
	err     error
}

func TestThemePlaybackReportsDoNotCreateSessions(t *testing.T) {
	mgr := &testCompatSessionManager{}
	h, _ := newActiveEncodingsHandler(mgr)
	syncer := &recordingSessionSyncer{}
	h.SessionSyncer = syncer
	for _, handle := range []http.HandlerFunc{h.HandleSessionPlaying, h.HandleSessionPlayingProgress, h.HandleSessionPlayingStopped} {
		body := strings.NewReader(`{"ItemId":"0b000000-0000-0000-0000-000000000007","MediaSourceId":"0b000000-0000-0000-0000-000000000007","PlaySessionId":"theme-session","PositionTicks":50000000}`)
		req := withCompatSession(httptest.NewRequest("POST", "/Sessions/Playing", body), "tok")
		rec := httptest.NewRecorder()
		handle(rec, req)
		if rec.Code != 204 || syncer.calls != 0 || len(mgr.sessions) != 0 || mgr.progressCalls != 0 || len(mgr.stopCalls) != 0 {
			t.Fatalf("theme report changed playback state: status=%d syncs=%d sessions=%d", rec.Code, syncer.calls, len(mgr.sessions))
		}
	}
}

func TestThemeDirectPlayFormats(t *testing.T) {
	file := themesongs.File{Song: themesongs.Song{Container: "m4a"}, AudioCodec: "aac", AudioChannels: 2, BitrateKbps: 128, SampleRate: 44100}
	for _, tc := range []struct {
		query     string
		container string
		allowed   bool
	}{
		{"container=mp4,mp3&audioCodec=aac&transcodingContainer=ts&transcodingProtocol=hls", "", true},
		{"", "mp4", true},
		{"maxAudioBitRate=128000&maxAudioChannels=2&audioSampleRate=44100&audioStreamIndex=-1", "m4a", true},
		{"audioCodec=mp3", "", false},
		{"audioStreamIndex=1", "", false},
		{"audioStreamIndex=0", "", false},
		{"maxAudioBitRate=64000", "", false},
		{"audioChannels=1", "", false},
		{"audioSampleRate=48000", "", false},
		{"container=mp4", "ogg", false},
	} {
		t.Run(tc.container+"?"+tc.query, func(t *testing.T) {
			values, err := url.ParseQuery(tc.query)
			if err != nil {
				t.Fatal(err)
			}
			if got := themeDirectPlayAllowed(newCaseInsensitiveQuery(values), tc.container, file, false); got != tc.allowed {
				t.Fatalf("allowed=%v, want %v", got, tc.allowed)
			}
		})
	}
}

func TestThemeUniversalFallbackNegotiation(t *testing.T) {
	file := themesongs.File{Song: themesongs.Song{Container: "mp3"}, AudioCodec: "mp3", AudioChannels: 2, BitrateKbps: 128, SampleRate: 44100}
	for _, tc := range []struct {
		query     string
		universal bool
		allowed   bool
	}{
		{"container=mp3&audioCodec=aac&transcodingContainer=mp4&transcodingProtocol=hls", true, true},
		{"container=mp3&audioCodec=aac&transcodingContainer=mp4&transcodingProtocol=hls", false, false},
		{"container=mp3|mp3&audioCodec=aac", true, true},
		{"container=mp3|aac&audioCodec=mp3", true, false},
		{"container=aac&audioCodec=mp3", true, false},
		{"audioCodec=aac&transcodingContainer=mp4", true, false},
		{"container=mp3&audioCodec=aac&maxStreamingBitrate=64000", true, false},
		{"container=mp3&audioCodec=aac&maxAudioChannels=1", true, false},
		{"container=mp3&audioCodec=aac&maxAudioSampleRate=22050", true, false},
		{"container=mp3&audioCodec=aac&audioBitRate=64000&transcodingAudioChannels=1", true, true},
	} {
		t.Run(tc.query, func(t *testing.T) {
			values, err := url.ParseQuery(tc.query)
			if err != nil {
				t.Fatal(err)
			}
			if got := themeDirectPlayAllowed(newCaseInsensitiveQuery(values), "", file, tc.universal); got != tc.allowed {
				t.Fatalf("allowed=%v, want %v", got, tc.allowed)
			}
		})
	}
}

func TestThemeUniversalUnknownMetadata(t *testing.T) {
	file := themesongs.File{Song: themesongs.Song{Container: "mp3"}, AudioCodec: "mp3"}
	for _, tc := range []struct {
		query   string
		allowed bool
	}{
		{"Container=mp3&MaxStreamingBitrate=40000000&MaxAudioChannels=2&MaxAudioSampleRate=44100", true},
		{"Container=mp3&MaxStreamingBitrate=39999999", false},
		{"Container=mp3&MaxAudioChannels=invalid", false},
	} {
		values, err := url.ParseQuery(tc.query)
		if err != nil {
			t.Fatal(err)
		}
		if got := themeDirectPlayAllowed(newCaseInsensitiveQuery(values), "", file, true); got != tc.allowed {
			t.Fatalf("%s: allowed=%v, want %v", tc.query, got, tc.allowed)
		}
	}
}

func (s *compatThemeFixture) Resolve(_ context.Context, id string, inherit bool, _ catalog.AccessFilter) (string, []themesongs.File, error) {
	s.inherit = inherit
	if inherit && s.owner != "" {
		return s.owner, []themesongs.File{s.file}, s.err
	}
	return id, []themesongs.File{s.file}, s.err
}
func (s *compatThemeFixture) Find(_ context.Context, id string, _ catalog.AccessFilter) (themesongs.File, error) {
	if s.err != nil {
		return themesongs.File{}, s.err
	}
	if id != s.file.ID {
		return themesongs.File{}, themesongs.ErrNotFound
	}
	return s.file, nil
}

func TestCompatThemeDiscoveryAndAudio(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "theme.mp3")
	if err := os.WriteFile(path, []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	store := &compatThemeFixture{file: themesongs.File{Song: themesongs.Song{ID: "7", Title: "Theme", Container: "mp3"}, AudioCodec: "mp3", OwnerPath: dir, Path: path, Size: info.Size(), Modified: info.ModTime().Truncate(time.Microsecond)}}
	codec := NewResourceIDCodec()
	h := &ItemsHandler{codec: codec, mapper: &mapper{serverID: "theme-server"}, content: &countingContentService{}, themeSongs: store}
	router := chi.NewRouter()
	router.Get("/Items/{id}", h.HandleItem)
	router.Get("/Users/{userId}/Items/{id}", h.HandleItem)
	router.Get("/Items/{id}/ThemeSongs", h.HandleThemeSongs)
	router.Get("/Items/{id}/ThemeMedia", h.HandleThemeMedia)
	cfg := streamtelemetry.DefaultConfig("theme-test")
	cfg.Enabled = true
	registry := streamtelemetry.NewRegistry(cfg, streamtelemetry.NewLocalStore(), nil)
	for _, path := range []string{"/Audio/{itemId}/stream", "/Audio/{itemId}/stream.{container}", "/Audio/{itemId}/universal"} {
		router.Get(path, observeCompat(registry, "GET", path, h.HandleThemeAudio))
		router.Head(path, observeCompat(registry, "HEAD", path, h.HandleThemeAudio))
	}
	request := func(method, path, rangeValue string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Range", rangeValue)
		r = r.WithContext(context.WithValue(r.Context(), compatSessionKey, &Session{StreamAppUserID: 1, ProfileID: "profile"}))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	owner := codec.EncodeStringID(EncodedIDItem, "movie")
	w := request("GET", "/Items/"+owner+"/ThemeSongs?inheritFromParent=false&sortBy=Random", "")
	var result themeMediaResultDTO
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || store.inherit || len(result.Items) != 1 || result.OwnerID != owner || result.TotalRecordCount != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	if result.Items[0].ServerID != "theme-server" {
		t.Fatal("Jellyfin Web needs the theme's server identity to select its API client")
	}
	w = request("GET", "/Items/"+owner+"/ThemeMedia", "")
	if store.inherit {
		t.Fatal("omitted inheritFromParent must default to false")
	}
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ThemeVideosResult":{"Items":[]`) {
		t.Fatal(w.Code, w.Body.String())
	}
	store.owner, store.file.OwnerType = "series-S01", "season"
	w = request("GET", "/Items/"+owner+"/ThemeSongs?inheritFromParent=true", "")
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || result.OwnerID != codec.EncodeStringID(EncodedIDSeason, "series-S01") {
		t.Fatal("synthetic season owner lost its season ID", w.Code, w.Body.String())
	}
	store.owner, store.file.OwnerType = "", ""
	id := EncodeNumericID(EncodedIDThemeSong, 7).String()
	// Jellyfin Web fetches the discovered theme's item detail before requesting
	// universal audio. Both item routes must preserve the discovery metadata.
	for _, prefix := range []string{"", "/Users/00000000-0000-0000-0000-000000000000"} {
		w = request("GET", prefix+"/Items/"+id, "")
		var item baseItemDTO
		if err := json.Unmarshal(w.Body.Bytes(), &item); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || item.ServerID != "theme-server" || item.ID != id || item.MediaType != "Audio" || item.Container != "mp3" {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	// This is the negotiation Jellyfin Web 12.1 sends for an MP3 theme. The
	// AudioCodec describes the HLS fallback, while Container allows direct MP3.
	w = request("GET", "/Audio/"+id+"/universal?UserId=00000000-0000-0000-0000-000000000000&DeviceId=theme-test&MaxStreamingBitrate=697095436&Container=opus,webm%7Copus,ts%7Cmp3,mp3,aac,m4a%7Caac,m4b%7Caac,flac,webma,webm%7Cwebma,wav,ogg&TranscodingContainer=mp4&TranscodingProtocol=hls&AudioCodec=aac&PlaySessionId=theme-test-session&StartTimeTicks=0&EnableRedirection=true&EnableRemoteMedia=false&EnableAudioVbrEncoding=true", "bytes=2-4")
	if w.Code != 206 || w.Body.String() != "234" {
		t.Fatal(w.Code, w.Body.String())
	}
	// A fresh codec must still resolve the theme ID after a restart.
	h.codec = NewResourceIDCodec()
	for _, suffix := range []string{"stream", "stream.mp3", "universal"} {
		w = request("GET", "/Audio/"+id+"/"+suffix, "bytes=2-4")
		if w.Code != 206 || w.Body.String() != "234" {
			t.Fatal(w.Code, w.Body.String())
		}
		w = request("HEAD", "/Audio/"+id+"/"+suffix, "")
		if w.Code != 200 || w.Body.Len() != 0 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, suffix := range []string{"stream.ogg", "universal?container=ogg", "stream?static=false", "universal?audioCodec=aac"} {
		w = request("GET", "/Audio/"+id+"/"+suffix, "")
		if w.Code != 400 || !strings.Contains(w.Body.String(), "PlaybackUnavailable") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	snapshot := registry.Sweep()
	if len(snapshot.Transfers) == 0 || len(snapshot.Sessions) != 0 {
		t.Fatalf("theme activity: %+v", snapshot)
	}
	for _, transfer := range snapshot.Transfers {
		if transfer.Subject != streamtelemetry.UserSubject(1) || transfer.ProfileID != "profile" {
			t.Fatalf("theme identity: %+v", transfer)
		}
	}
	h.codec = codec
	for _, tc := range []struct {
		err    error
		status int
	}{{errors.New("database failed"), 500}, {themesongs.ErrNotFound, 404}} {
		store.err = tc.err
		for _, path := range []string{"/Items/" + owner + "/ThemeSongs", "/Audio/" + id + "/stream"} {
			if got := request("GET", path, ""); got.Code != tc.status {
				t.Fatalf("lookup error status=%d want=%d", got.Code, tc.status)
			}
		}
	}
}
