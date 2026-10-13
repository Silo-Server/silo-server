//go:build unix

package transcodenode

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSourceRevocationCancelsBeforeLifecycleAndPreservesReplacement(t *testing.T) {
	s := ceilingServer(t, 1)
	if response := postStart(t, s, "work"); response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	s.mu.RLock()
	old := s.sessions["work"]
	s.mu.RUnlock()
	oldInput, oldDir := old.Opts().InputPath, old.Opts().OutputDir
	marker := filepath.Join(oldDir, "ownership-marker")
	if err := os.WriteFile(marker, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	unlock := sync.OnceFunc(s.lockSessionLifecycle("work"))
	t.Cleanup(unlock)
	done := make(chan struct{})
	go func() {
		s.StopWorkInputs(context.Background(), "work", []string{oldInput})
		close(done)
	}()
	// Even a producer that never reads its input again is canceled promptly,
	// while its map entry and directory remain owned until lifecycle teardown.
	select {
	case <-old.Done():
	case <-time.After(time.Second):
		t.Fatal("revoked buffered producer waited for lifecycle cancellation")
	}
	s.mu.RLock()
	retained := s.sessions["work"] == old
	s.mu.RUnlock()
	if !retained || s.activeJobs.Load() != 1 {
		t.Fatal("revocation dropped directory/accounting ownership before lifecycle lock")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("revocation removed the directory before lifecycle teardown")
	}
	unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("revocation did not complete after releasing lifecycle")
	}
	if s.activeJobs.Load() != 0 {
		t.Fatal("revoked producer retained its job")
	}
	body, _ := json.Marshal(TranscodeStartRequest{SessionID: "work", InputPath: "/media/replacement.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2})
	response := httptest.NewRecorder()
	s.handleStart(response, httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(body)))
	if response.Code != http.StatusAccepted {
		t.Fatalf("replacement start=%d %s", response.Code, response.Body)
	}
	s.mu.RLock()
	next := s.sessions["work"]
	s.mu.RUnlock()
	marker = filepath.Join(next.Opts().OutputDir, "ownership-marker")
	if err := os.WriteFile(marker, []byte("next"), 0600); err != nil {
		t.Fatal(err)
	}
	s.StopWorkInputs(t.Context(), "work", []string{oldInput})
	if !next.IsRunning() || s.activeJobs.Load() != 1 {
		t.Fatal("old source callback canceled or recounted replacement")
	}
	if raw, err := os.ReadFile(marker); err != nil || string(raw) != "next" {
		t.Fatal("old source callback deleted replacement output")
	}
	if response := postStart(t, s, "other"); response.Code != http.StatusServiceUnavailable {
		t.Fatal("old source callback released the replacement's capacity")
	}
	stopSession(t, s, "work")
}
