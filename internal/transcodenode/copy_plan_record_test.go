package transcodenode

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// A session's keyframe-plan record lives outside its output directory: an
// idle reap leaves it for the rebuild, while a stop (of a live or a reaped
// transport) and a force reload remove it.
func TestCopyPlanRecordOutlivesAnIdleReapOnly(t *testing.T) {
	s := newTestServer(t)
	record := func(id string) string {
		t.Helper()
		path := s.copyPlanRecordPath(id)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{"planned":true,"durations":[2]}`), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	register := func(id string, lastAccess time.Time) {
		t.Helper()
		if err := os.MkdirAll(s.sessionOutputDir(id), 0o755); err != nil {
			t.Fatal(err)
		}
		s.mu.Lock()
		s.sessions[id] = playback.NewTranscodeSessionForTest(s.sessionOutputDir(id))
		s.lastAccess[id] = lastAccess
		s.mu.Unlock()
		s.activeJobs.Add(1)
	}
	exists := func(path string) bool {
		_, err := os.Stat(path)
		return err == nil
	}

	reaped := record("reaped")
	register("reaped", time.Now().Add(-sessionIdleTTL-time.Minute))
	s.reapIdleSessions(sessionIdleTTL)
	if exists(s.sessionOutputDir("reaped")) || !exists(reaped) {
		t.Fatalf("idle reap: output kept %t, record kept %t; want the record only", exists(s.sessionOutputDir("reaped")), exists(reaped))
	}

	// A stop after the reap ends the transport for good.
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, signed(t, httptest.NewRequest(http.MethodDelete, "/transcode/reaped", nil)))
	if exists(reaped) {
		t.Fatalf("stop of a reaped transport (%d) kept its record", rec.Code)
	}

	live := record("live")
	register("live", time.Now())
	stopSession(t, s, "live")
	if exists(live) {
		t.Fatal("stop kept the live session's record")
	}

	reloaded := record("reloaded")
	register("reloaded", time.Now())
	if err := s.teardownForForceReload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if exists(reloaded) {
		t.Fatal("force reload kept the record")
	}
}

// A rebuild whose recorded plan belongs to another revision of the source can
// never succeed, so the node answers 410 and the client stops, rather than
// 404 or 503, which it would retry.
func TestReconstructFailureForAChangedSourceIsGone(t *testing.T) {
	rec := httptest.NewRecorder()
	writeReconstructFailure(rec, fmt.Errorf("plan rebuilt keyframe playlist: %w", playback.ErrCopyPlanSourceChanged))
	if rec.Code != http.StatusGone {
		t.Fatalf("changed source = %d, want 410", rec.Code)
	}
	rec = httptest.NewRecorder()
	writeReconstructFailure(rec, errors.New("plan rebuilt keyframe playlist: read failed"))
	if rec.Code == http.StatusGone {
		t.Fatal("a retryable rebuild failure answered 410")
	}
}

// The orphan sweep spares the record directory and removes only records no
// live session uses that are older than the token lifetime.
func TestOrphanSweepKeepsTheRecordDirectory(t *testing.T) {
	s := newTestServer(t)
	stale := s.copyPlanRecordPath("gone")
	live := s.copyPlanRecordPath("live")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-playback.MaxTokenTTL - time.Hour)
	for _, path := range []string{stale, live} {
		if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(filepath.Dir(stale), old, old); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.sessions["live"] = playback.NewTranscodeSessionForTest(s.sessionOutputDir("live"))
	s.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s.StartOrphanSweeper(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, staleErr := os.Stat(stale)
		if errors.Is(staleErr, os.ErrNotExist) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the sweep kept a stale record")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("the sweep removed a live session's record: %v", err)
	}
}

// A rebuild whose start fails for a reason a later request may not meet (its
// keyframe plan could not be read now, or the subtitle staging is busy)
// answers 503, which the client retries, not 404; a changed source stays 410.
func TestReconstructStartFailureThatMayPassIsUnavailable(t *testing.T) {
	for name, err := range map[string]error{
		"plan read": fmt.Errorf("plan rebuilt keyframe playlist: %w: %w", playback.ErrCopyPlanUnavailable, context.DeadlineExceeded),
		"staging":   fmt.Errorf("%w: waiting for a staging slot", playback.ErrSubtitleStagingBusy),
	} {
		rec := httptest.NewRecorder()
		writeReconstructFailure(rec, reconstructStartError(err))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: rebuild failure = %d, want 503", name, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	writeReconstructFailure(rec, reconstructStartError(fmt.Errorf("plan rebuilt keyframe playlist: %w", playback.ErrCopyPlanSourceChanged)))
	if rec.Code != http.StatusGone {
		t.Fatalf("changed source = %d, want 410", rec.Code)
	}
}
