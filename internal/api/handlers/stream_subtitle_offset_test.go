package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestSubtitleTimestampOffsetExternalTrack(t *testing.T) {
	handler, sessionID := subtitleOffsetFixture(t)
	for _, tc := range []struct {
		name, method, query, track string
		status                     int
		want, absent               string
	}{
		{name: "canonical", method: http.MethodGet, track: "0.vtt", status: 200, want: "00:10:01.000 --> 00:10:02.000"},
		{name: "zero", method: http.MethodGet, query: "timestamp_offset=0", track: "0.vtt", status: 200, want: "00:10:01.000 --> 00:10:02.000"},
		{name: "receiver clock", method: http.MethodGet, query: "timestamp_offset=-600", track: "0.vtt", status: 200, want: "00:00:01.000 --> 00:00:02.000", absent: "Early cue"},
		{name: "positive offset", method: http.MethodGet, query: "timestamp_offset=0.5", track: "0.vtt", status: 200, want: "00:10:01.500 --> 00:10:02.500"},
		{name: "head", method: http.MethodHead, query: "timestamp_offset=-600", track: "0.vtt", status: 200},
		{name: "nan", method: http.MethodGet, query: "timestamp_offset=NaN", track: "0.vtt", status: 400},
		{name: "infinite", method: http.MethodGet, query: "timestamp_offset=Inf", track: "0.vtt", status: 400},
		{name: "too large", method: http.MethodGet, query: "timestamp_offset=31536001", track: "0.vtt", status: 400},
		{name: "empty", method: http.MethodGet, query: "timestamp_offset=", track: "0.vtt", status: 400},
		{name: "duplicate", method: http.MethodGet, query: "timestamp_offset=1&timestamp_offset=2", track: "0.vtt", status: 400},
		{name: "non vtt", method: http.MethodGet, query: "timestamp_offset=1", track: "0.ass", status: 400},
		{name: "error unchanged", method: http.MethodGet, query: "timestamp_offset=-600", track: "99.vtt", status: 404, want: "not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := playbackTestRequest(tc.method, "/stream/"+sessionID+"/subtitles/"+tc.track+"?"+tc.query, nil,
				map[string]string{"session_id": sessionID, "track": tc.track})
			request.Header.Set("Range", "bytes=0-5")
			request.Header.Set("If-None-Match", "*")
			response := httptest.NewRecorder()
			handler.HandleSubtitle(response, request)
			if response.Code != tc.status || !strings.Contains(response.Body.String(), tc.want) || (tc.absent != "" && strings.Contains(response.Body.String(), tc.absent)) {
				t.Fatalf("response = %d %q", response.Code, response.Body.String())
			}
			if tc.method == http.MethodHead && response.Body.Len() != 0 {
				t.Fatalf("HEAD body = %q", response.Body.String())
			}
			if tc.status == 200 && tc.query != "" && tc.query != "timestamp_offset=0" {
				if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Content-Length") != "" || response.Header().Get("Content-Range") != "" {
					t.Fatalf("transformed response retains canonical metadata: %v", response.Header())
				}
			}
			if request.Header.Get("Range") != "bytes=0-5" {
				t.Fatal("transform mutated caller request")
			}
		})
	}
}

func TestSubtitleTimestampOffsetAbortsFailedWrite(t *testing.T) {
	handler, sessionID := subtitleOffsetFixture(t)
	request := playbackTestRequest(http.MethodGet, "/stream/"+sessionID+"/subtitles/0.vtt?timestamp_offset=-600", nil,
		map[string]string{"session_id": sessionID, "track": "0.vtt"})
	defer func() {
		got := recover()
		err, ok := got.(error)
		if !ok || !errors.Is(err, http.ErrAbortHandler) {
			t.Fatalf("write failure panic = %v, want ErrAbortHandler", got)
		}
	}()
	handler.HandleSubtitle(subtitleOffsetFailedWriter{httptest.NewRecorder()}, request)
}

type subtitleOffsetFailedWriter struct{ *httptest.ResponseRecorder }

func (subtitleOffsetFailedWriter) Write([]byte) (int, error) {
	return 0, errors.New("receiver disconnected")
}

func subtitleOffsetFixture(t *testing.T) (*StreamHandler, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "movie.srt")
	if err := os.WriteFile(path, []byte("1\n00:00:01,000 --> 00:00:02,000\nEarly cue\n\n2\n00:10:01,000 --> 00:10:02,000\nSelected cue\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := &models.MediaFile{ID: 42, FilePath: "/synthetic/movie.mkv", ExternalSubtitles: []models.ExternalSubtitle{{Path: path, Format: "srt"}}}
	manager := playback.NewSessionManager(0, 0)
	session, err := manager.StartSession(1, "profile-1", 42, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	return NewStreamHandler(manager, testPlaybackFileResolver{file: file}), session.ID
}
