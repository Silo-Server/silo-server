package transcodenode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTrickplayExtractionAppearsInHealthUntilItEnds(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the FFmpeg fixture uses a POSIX shell")
	}
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	ffmpeg := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\ntouch '"+started+"'\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := newTrickplayTestServer(t, ffmpeg)
	ctx, cancel := context.WithCancel(t.Context())
	req := httptest.NewRequest(http.MethodPost, "/trickplay/extract", strings.NewReader(sheetsRequestJSON(t, "/media/movie.mkv"))).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.handleTrickplayExtract(rec, req)
		close(done)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("canceled extraction did not finish")
		}
	}()
	waitCtx, stopWaiting := context.WithTimeout(t.Context(), 2*time.Second)
	defer stopWaiting()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		select {
		case <-waitCtx.Done():
			t.Fatal("FFmpeg did not start")
		case <-ticker.C:
		}
	}
	health := httptest.NewRecorder()
	server.handleHealth(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	var response HealthResponse
	if err := json.Unmarshal(health.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ActiveJobs != 1 {
		t.Fatalf("health active_jobs=%d during extraction", response.ActiveJobs)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("canceled extraction did not finish")
	}
	if jobs := server.activeJobs.Load(); jobs != 0 {
		t.Fatalf("finished extraction retained %d jobs", jobs)
	}
}
