package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestSubtitleRouteIndexRejectsInvalidPins(t *testing.T) {
	file := &models.MediaFile{ExternalSubtitles: []models.ExternalSubtitle{{Path: "/media/selected.srt"}},
		SubtitleTracks: []models.SubtitleTrack{{Index: 4}, {Index: 7}, {Index: 7}},
	}
	for _, query := range []string{
		"embedded_stream_index=",
		"embedded_stream_index=-1",
		"embedded_stream_index=4&embedded_stream_index=7",
		"embedded_stream_index=4&downloaded_subtitle_id=2",
		"external_subtitle_key=invalid",
	} {
		values, err := url.ParseQuery(query)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := subtitleRouteIndex(file, 0, values); !errors.Is(err, errSubtitleIdentityInvalid) {
			t.Errorf("query %q error = %v, want invalid identity", query, err)
		}
	}
	values := url.Values{playback.EmbeddedSubtitleStreamIndexParamV3: {"7"}}
	if _, err := subtitleRouteIndex(file, 0, values); !errors.Is(err, errSubtitleIdentityUnavailable) {
		t.Fatalf("ambiguous stream index must not select either track: %v", err)
	}
	if index, err := subtitleRouteIndex(file, 2, nil); err != nil || index != 2 {
		t.Fatalf("legacy ordinal changed: index=%d err=%v", index, err)
	}
}

func TestSubtitleEmbeddedIdentitySurvivesExternalInsertion(t *testing.T) {
	dir := t.TempDir()
	sidecar := filepath.Join(dir, "new.srt")
	if err := os.WriteFile(sidecar, []byte("1\n00:00:01,000 --> 00:00:02,000\nNew external\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf 'WEBVTT\\n\\n'; printf '%s\\n' \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	file := &models.MediaFile{ID: 42, FilePath: "/synthetic/movie.mkv",
		ExternalSubtitles: []models.ExternalSubtitle{{Path: sidecar, Format: "srt"}},
		SubtitleTracks:    []models.SubtitleTrack{{Index: 4, Codec: "subrip"}, {Index: 7, Codec: "subrip"}},
	}
	manager := playback.NewSessionManager(0, 0)
	session, err := manager.StartSession(1, "profile-1", 42, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewStreamHandler(manager, testPlaybackFileResolver{file: file})
	handler.PlaybackConfig = func() config.PlaybackConfig { return config.PlaybackConfig{FFmpegPath: bin} }
	response := httptest.NewRecorder()
	handler.HandleSubtitle(response, playbackTestRequest(http.MethodGet,
		"/stream/"+session.ID+"/subtitles/1.vtt?file_id=42&embedded_stream_index=7", nil,
		map[string]string{"session_id": session.ID, "track": "1.vtt"}))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "0:s:1\n") {
		t.Fatalf("frozen embedded identity selected wrong track: %d %q", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.HandleSubtitle(response, playbackTestRequest(http.MethodGet,
		"/stream/"+session.ID+"/subtitles/1.vtt?file_id=42&embedded_stream_index=99", nil,
		map[string]string{"session_id": session.ID, "track": "1.vtt"}))
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing embedded identity silently rebound: %d %q", response.Code, response.Body.String())
	}
}

func TestSubtitleFontIdentitySurvivesExternalInsertion(t *testing.T) {
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(mediaPath, []byte("synthetic fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ffprobe"), []byte("#!/bin/sh\nprintf '{\"streams\":[]}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	file := &models.MediaFile{ID: 42, FilePath: mediaPath,
		ExternalSubtitles: []models.ExternalSubtitle{{Path: "/synthetic/new.srt", Format: "srt"}},
		SubtitleTracks:    []models.SubtitleTrack{{Index: 4, Codec: "ass"}},
	}
	manager := playback.NewSessionManager(0, 0)
	session, err := manager.StartSession(1, "profile-1", 42, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewStreamHandler(manager, testPlaybackFileResolver{file: file})
	handler.PlaybackConfig = func() config.PlaybackConfig { return config.PlaybackConfig{FFmpegPath: filepath.Join(dir, "ffmpeg")} }
	response := httptest.NewRecorder()
	handler.HandleSubtitleFonts(response, playbackTestRequest(http.MethodGet,
		"/stream/"+session.ID+"/subtitles/0/fonts?file_id=42&embedded_stream_index=4", nil,
		map[string]string{"session_id": session.ID, "track": "0"}))
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != "[]" {
		t.Fatalf("frozen font identity failed after scan insertion: %d %q", response.Code, response.Body.String())
	}
	query := url.Values{"file_id": {"42"}, playback.EmbeddedSubtitleStreamIndexParamV3: {"4"}}
	fonts, err := handler.SubtitleFonts(newAuthorizedPlaybackContext(), SubtitleFontRequest{SessionID: session.ID, Track: "0", Query: query})
	if err != nil || fonts == nil || len(fonts) != 0 {
		t.Fatalf("shared font service failed after scan insertion: fonts=%v err=%v", fonts, err)
	}
	query[playback.EmbeddedSubtitleStreamIndexParamV3] = []string{"4", "7"}
	_, err = handler.SubtitleFonts(newAuthorizedPlaybackContext(), SubtitleFontRequest{SessionID: session.ID, Track: "0", Query: query})
	if failure, ok := errors.AsType[*APIError](err); !ok || failure.Status != http.StatusBadRequest {
		t.Fatalf("conflicting identity pins were accepted: %v", err)
	}
}

func TestSubtitleExternalIdentitySurvivesReordering(t *testing.T) {
	dir := t.TempDir()
	selectedPath, otherPath := filepath.Join(dir, "selected.srt"), filepath.Join(dir, "other.srt")
	for path, text := range map[string]string{selectedPath: "Selected subtitle", otherPath: "Other subtitle"} {
		if err := os.WriteFile(path, []byte("1\n00:00:01,000 --> 00:00:02,000\n"+text+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The selected file formerly occupied ordinal 1. A scan reordered it to 0.
	file := &models.MediaFile{ID: 42, FilePath: "/synthetic/movie.mkv",
		ExternalSubtitles: []models.ExternalSubtitle{{Path: selectedPath, Format: "srt"}, {Path: otherPath, Format: "srt"}},
	}
	manager := playback.NewSessionManager(0, 0)
	session, err := manager.StartSession(1, "profile-1", 42, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewStreamHandler(manager, testPlaybackFileResolver{file: file})
	for _, removed := range []bool{false, true} {
		if removed {
			file.ExternalSubtitles = file.ExternalSubtitles[1:]
		}
		response := httptest.NewRecorder()
		handler.HandleSubtitle(response, playbackTestRequest(http.MethodGet,
			"/stream/"+session.ID+"/subtitles/1.vtt?file_id=42&external_subtitle_key="+playback.ExternalSubtitlePathKeyV3(selectedPath), nil,
			map[string]string{"session_id": session.ID, "track": "1.vtt"}))
		if removed {
			if response.Code != http.StatusNotFound {
				t.Fatalf("removed external identity silently rebound: %d %q", response.Code, response.Body.String())
			}
		} else if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Selected subtitle") {
			t.Fatalf("frozen external identity selected wrong track: %d %q", response.Code, response.Body.String())
		}
	}
}

// The published .srt?original=1 URL on /api/v2 answers an external SRT with the original
// bytes, which keep {\an8} for a client that parses SubRip. .vtt, and a bare
// .srt as the frozen v1 route has always done, answer with the WebVTT
// conversion.
func TestSubtitleExternalSRTServesOriginalOrConvertedRepresentation(t *testing.T) {
	const srtFile = "1\n00:00:01,000 --> 00:00:02,000\n{\\an8}Top line\n"
	path := filepath.Join(t.TempDir(), "movie.ar.srt")
	if err := os.WriteFile(path, []byte(srtFile), 0o600); err != nil {
		t.Fatal(err)
	}
	file := &models.MediaFile{ID: 42, FilePath: "/synthetic/movie.mkv",
		ExternalSubtitles: []models.ExternalSubtitle{{Path: path, Format: "srt"}},
		SubtitleTracks:    []models.SubtitleTrack{{Index: 2, Codec: "subrip"}},
	}
	manager := playback.NewSessionManager(0, 0)
	session, err := manager.StartSession(1, "profile-1", 42, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewStreamHandler(manager, testPlaybackFileResolver{file: file})
	request := func(method, track, query string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		req := playbackTestRequest(method,
			"/stream/"+session.ID+"/subtitles/"+track+"?file_id=42"+query, nil,
			map[string]string{"session_id": session.ID, "track": track})
		handler.HandleSubtitle(response, req.WithContext(WithNativeAPIV2(req.Context())))
		return response
	}
	const original = "&" + playback.SubtitleOriginalParamV3 + "=1"

	srt := request(http.MethodGet, "0.srt", original)
	if srt.Code != http.StatusOK || srt.Body.String() != srtFile ||
		!strings.HasPrefix(srt.Header().Get("Content-Type"), "application/x-subrip") {
		t.Fatalf(".srt must serve the original file: %d %q %q", srt.Code, srt.Header().Get("Content-Type"), srt.Body.String())
	}
	// The frozen /api/v1 route shares the handler and keeps WebVTT for the
	// same URL.
	v1 := httptest.NewRecorder()
	handler.HandleSubtitle(v1, playbackTestRequest(http.MethodGet,
		"/stream/"+session.ID+"/subtitles/0.srt?file_id=42"+original, nil,
		map[string]string{"session_id": session.ID, "track": "0.srt"}))
	if v1.Code != http.StatusOK || !strings.HasPrefix(v1.Header().Get("Content-Type"), "text/vtt") {
		t.Fatalf("/api/v1 .srt?original=1 must keep WebVTT: %d %q", v1.Code, v1.Header().Get("Content-Type"))
	}

	for _, converted := range []struct{ track, query string }{{"0.vtt", ""}, {"0.srt", ""}, {"0.vtt", original}} {
		vtt := request(http.MethodGet, converted.track, converted.query)
		if vtt.Code != http.StatusOK || !strings.HasPrefix(vtt.Header().Get("Content-Type"), "text/vtt") ||
			!strings.HasPrefix(vtt.Body.String(), "WEBVTT") || !strings.Contains(vtt.Body.String(), "00:00:01.000 --> 00:00:02.000") {
			t.Fatalf("%s%s must serve the WebVTT conversion: %d %q", converted.track, converted.query, vtt.Code, vtt.Body.String())
		}
	}

	for _, tc := range []struct{ track, query, want string }{
		{"0.srt", original, "application/x-subrip"},
		{"0.srt", "", "text/vtt"},
		{"0.vtt", "", "text/vtt"},
		// Embedded text streams as WebVTT whatever the extension.
		{"1.srt", original, "text/vtt"},
	} {
		head := request(http.MethodHead, tc.track, tc.query)
		if head.Code != http.StatusOK || !strings.HasPrefix(head.Header().Get("Content-Type"), tc.want) {
			t.Errorf("HEAD %s%s = %d %q, want %s", tc.track, tc.query, head.Code, head.Header().Get("Content-Type"), tc.want)
		}
	}
}

// SRT declares no encoding, so original bytes that are not UTF-8 are served
// without a UTF-8 charset label.
func TestOriginalSubRipDeclaresUTF8OnlyForUTF8Bytes(t *testing.T) {
	for name, tc := range map[string]struct {
		data []byte
		want string
	}{
		"utf-8":        {[]byte("1\n00:00:01,000 --> 00:00:02,000\nمرحبا\n"), "application/x-subrip; charset=utf-8"},
		"windows-1256": {[]byte("1\n00:00:01,000 --> 00:00:02,000\n\xe3\xd1\xcd\xc8\xc7\n"), "application/x-subrip"},
	} {
		t.Run(name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			serveOriginalSubRip(rr, tc.data)
			if got := rr.Header().Get("Content-Type"); got != tc.want || rr.Body.String() != string(tc.data) {
				t.Fatalf("content type = %q, want %q; body changed: %v", got, tc.want, rr.Body.String() != string(tc.data))
			}
		})
	}
}
