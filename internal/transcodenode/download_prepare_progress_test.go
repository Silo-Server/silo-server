package transcodenode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/playback"
)

type recordingPrepareLogSink struct {
	mu       sync.Mutex
	sessions []string
	lines    []string
}

func (s *recordingPrepareLogSink) WriteLine(_ context.Context, sessionID string, _ playback.FFmpegLogAttrs, line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = append(s.sessions, sessionID)
	s.lines = append(s.lines, line)
}

func (s *recordingPrepareLogSink) WriteEvent(_ context.Context, sessionID string, _ playback.FFmpegLogAttrs, _ string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = append(s.sessions, sessionID)
}

func readPrepareProgress(t *testing.T, server *Server, artifactID string) downloadprepare.Progress {
	t.Helper()
	req := withNodeRouteParams(httptest.NewRequest(http.MethodGet, "/downloads/prepare/"+artifactID+"/progress", nil),
		map[string]string{"artifact_id": artifactID})
	rr := httptest.NewRecorder()
	server.handleDownloadPrepareProgress(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("progress status = %d body = %s", rr.Code, rr.Body.String())
	}
	var progress downloadprepare.Progress
	if err := json.Unmarshal(rr.Body.Bytes(), &progress); err != nil {
		t.Fatal(err)
	}
	return progress
}

func TestDownloadPrepareProgressIsReadableWhileEncoding(t *testing.T) {
	server := newTestServer(t)
	logs := &recordingPrepareLogSink{}
	server.SetFFmpegLogSink(logs)
	release := filepath.Join(t.TempDir(), "release")
	useFakeFFmpeg(t, server, "#!/bin/sh\n"+
		"printf 'out_time_us=30000000\\nspeed=2.0x\\nprogress=continue\\n'\n"+
		"printf 'encoder warning\\n' >&2\n"+
		"while [ ! -f "+release+" ]; do sleep 0.01; done\n"+
		"for last; do :; done\nprintf artifact > \"$last\"\n")
	server.watcher.Config().Playback.HWAccel = "none"
	prepare := jsonRequest(t, "/downloads/prepare", downloadprepare.Request{
		ArtifactID: "attempt-1", InputPath: "/media/movie.mkv",
		TargetCodecVideo: "copy", TargetCodecAudio: "copy", AudioTrackIndex: -1,
		TotalDuration: 120, LogSessionID: playback.DownloadPrepareLogSessionID("art-1"),
	})

	if got := readPrepareProgress(t, server, "attempt-1"); got.Running || got.ArtifactID != "attempt-1" {
		t.Fatalf("progress before prepare = %+v, want not running", got)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rr := httptest.NewRecorder()
		server.handleDownloadPrepare(rr, prepare)
		done <- rr
	}()

	deadline := time.Now().Add(10 * time.Second)
	var got downloadprepare.Progress
	for {
		got = readPrepareProgress(t, server, "attempt-1")
		if got.Running && got.EncodedSeconds == 30 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("progress never reported the running encode: %+v", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got.DurationSeconds != 120 || got.Speed != 2 {
		t.Fatalf("running progress = %+v", got)
	}

	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if rr := <-done; rr.Code != http.StatusOK {
		t.Fatalf("prepare status = %d body = %s", rr.Code, rr.Body.String())
	}
	if got := readPrepareProgress(t, server, "attempt-1"); got.Running {
		t.Fatalf("progress after prepare = %+v, want not running", got)
	}
	logs.mu.Lock()
	defer logs.mu.Unlock()
	if len(logs.lines) != 1 || logs.lines[0] != "encoder warning" {
		t.Fatalf("logged lines = %q", logs.lines)
	}
	for _, session := range logs.sessions {
		if session != "download-prepare-art-1" {
			t.Fatalf("log session = %q, want the durable job key", session)
		}
	}
}
