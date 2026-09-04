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
