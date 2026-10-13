package transcodenode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

func ceilingServer(t *testing.T, limit int) *Server {
	t.Helper()
	s := newLoopingFFmpegServer(t)
	if err := s.SetJobCeiling(limit); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	return s
}

func TestJobCeilingSessionReplacementAndStop(t *testing.T) {
	s := ceilingServer(t, 1)
	if rec := postStart(t, s, "kept"); rec.Code != http.StatusAccepted {
		t.Fatal(rec.Body.String())
	}
	if rec := postStart(t, s, "other"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("different work exceeded ceiling: status=%d", rec.Code)
	}
	if rec := postStart(t, s, "kept"); rec.Code != http.StatusAccepted {
		t.Fatalf("same-work replacement refused: %d %s", rec.Code, rec.Body)
	}
	s.mu.RLock()
	healthy := s.sessions["kept"]
	s.mu.RUnlock()
	ffmpeg := s.watcher.Config().Playback.FFmpegPath
	s.watcher.Config().Playback.FFmpegPath = "/missing-job-ceiling-fixture"
	if rec := postStart(t, s, "kept"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("failed candidate status=%d", rec.Code)
	}
	s.watcher.Config().Playback.FFmpegPath = ffmpeg
	s.mu.RLock()
	current := s.sessions["kept"]
	s.mu.RUnlock()
	if current != healthy || !healthy.IsRunning() {
		t.Fatal("failed candidate stopped healthy work")
	}
	if rec := postStart(t, s, "other"); rec.Code != http.StatusServiceUnavailable {
		t.Fatal("failed replacement released healthy work slot")
	}
	stopSession(t, s, "kept")
	if rec := postStart(t, s, "next"); rec.Code != http.StatusAccepted {
		t.Fatalf("stopped work retained slot: %d %s", rec.Code, rec.Body)
	}
}

func TestJobCeilingConcurrentIndependentStartsAreAtomic(t *testing.T) {
	s := ceilingServer(t, 2)
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := postStart(t, s, fmt.Sprintf("work-%d", i))
			if rec.Code == http.StatusAccepted {
				accepted.Add(1)
			} else if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("start status=%d: %s", rec.Code, rec.Body)
			}
		}(i)
	}
	wg.Wait()
	if accepted.Load() != 2 {
		t.Fatalf("ceiling2 admitted %d live processes", accepted.Load())
	}
}

func TestJobCeilingReconstructionUsesSessionLifetime(t *testing.T) {
	s := ceilingServer(t, 1)
	if rec := postStart(t, s, "kept"); rec.Code != http.StatusAccepted {
		t.Fatal(rec.Body.String())
	}
	card := playback.RecipeCard{SessionID: "rebuilt", InputPath: "/media/movie.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2}
	req := httptest.NewRequest(http.MethodGet, "/transcode/rebuilt/manifest.m3u8", nil)
	if session, err := s.spawnReconstruct(req, "rebuilt", 0, card); err != nil || session != nil {
		t.Fatalf("full ceiling reconstructed work: session=%v err=%v", session, err)
	}
	stopSession(t, s, "kept")
	if session, err := s.spawnReconstruct(req, "rebuilt", 0, card); err != nil || session == nil {
		t.Fatalf("free ceiling failed reconstruction: %v", err)
	}
	if rec := postStart(t, s, "other"); rec.Code != http.StatusServiceUnavailable {
		t.Fatal("reconstructed session did not retain slot")
	}
	stopSession(t, s, "rebuilt")
	if rec := postStart(t, s, "other"); rec.Code != http.StatusAccepted {
		t.Fatal("closed reconstruction retained slot")
	}
}

func TestJobCeilingDefaultUnlimitedAndInvalidLimit(t *testing.T) {
	s := ceilingServer(t, 0)
	if s.SetJobCeiling(-1) == nil {
		t.Fatal("negative operator ceiling accepted")
	}
	for _, id := range []string{"a", "b", "c"} {
		if rec := postStart(t, s, id); rec.Code != http.StatusAccepted {
			t.Fatalf("zero ceiling refused %s", id)
		}
	}
}

func TestJobCeilingBlocksColdProbeAndComputeRoutesAtCapacity(t *testing.T) {
	s := ceilingServer(t, 1)
	if rec := postStart(t, s, "kept"); rec.Code != http.StatusAccepted {
		t.Fatal(rec.Body.String())
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := s.buildCapabilitySnapshot(ctx); !errors.Is(err, ErrCapabilityBuildBusy) {
		t.Fatalf("cold probe bypassed ceiling: %v", err)
	}
	for _, test := range []struct{ name, path, body string }{
		{"prepared download", "/downloads/prepare", `{"artifact_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","input_path":"/media/movie.mkv"}`},
		{"chapter thumbnail", "/chapter-thumbnails/extract", `{"input_path":"/media/movie.mkv","seek_seconds":1}`},
		{"media sample", mediasample.RemotePath, string(speechRequestBody(t, nil))},
		{"trickplay", "/trickplay/extract", sheetsRequestJSON(t, "/media/movie.mkv")},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, signed(t, httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))))
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("compute route bypassed ceiling: %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestJobCeilingAuthenticatedWorkIdentitySharesReplacementSlot(t *testing.T) {
	s := ceilingServer(t, 1)
	for _, sessionID := range []string{"old", "candidate"} {
		body, _ := json.Marshal(TranscodeStartRequest{SessionID: sessionID, InputPath: "/media/movie.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2})
		req := httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(body))
		req = req.WithContext(WithJobIdentity(req.Context(), "authenticated-work"))
		rec := httptest.NewRecorder()
		s.handleStart(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("same assignment work refused: %d %s", rec.Code, rec.Body)
		}
	}
	stopSession(t, s, "candidate")
	if rec := postStart(t, s, "other"); rec.Code != http.StatusServiceUnavailable {
		t.Fatal("failed/released candidate freed healthy work slot")
	}
	stopSession(t, s, "old")
	if rec := postStart(t, s, "other"); rec.Code != http.StatusAccepted {
		t.Fatal("last work reference retained capacity")
	}
}

func TestJobCeilingRemuxThemeAndHeadRespectComputeLifetime(t *testing.T) {
	s := ceilingServer(t, 1)
	s.nodeRowID = func() (int, bool) { return 21, true }
	if rec := postStart(t, s, "kept"); rec.Code != http.StatusAccepted {
		t.Fatal(rec.Body.String())
	}
	for _, theme := range []bool{false, true} {
		claims := streamtoken.Claims{SessionID: "remux", MediaPath: "/media/movie.mkv", PlayMethod: string(playback.PlayRemux), TranscodeNode: "http://node", TranscodeTransportID: "remux", RoutingWorkload: string(noderouting.WorkloadRemux), RoutingExecution: string(noderouting.ExecutionTranscode), RoutingExecutionNodeID: 21, RoutingEgress: string(noderouting.EgressProxy), RoutingEgressNodeID: 11}
		if theme {
			path, size, modified := writeNodeTheme(t)
			claims.MediaPath = path
			claims.PlayMethod = streamtoken.PlayMethodThemeAAC
			claims.AudioOnly = true
			claims.TranscodeAudio = true
			claims.TargetCodecAudio = "aac"
			claims.TargetAudioChannels = 2
			claims.ThemeID = 9
			claims.ThemeSize = size
			claims.ThemeModifiedUnixNano = modified.UnixNano()
			s.SetThemeInputAuthorizer(NewThemeInputAuthorizer(stubThemeSource{id: 9, path: path}))
		}
		card := playback.RecipeCardFromClaims(&claims)
		s.SetRecipeStore(&stubRecipeStore{card: &card, ok: true})
		token, err := streamtoken.Sign(claims, testSecret, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		request := themeRemuxRequest(t, token, "remux")
		request.Method = http.MethodHead
		head := httptest.NewRecorder()
		s.handleRemux(head, request)
		if head.Code != http.StatusOK {
			t.Fatalf("HEAD consumed compute capacity: %d %s", head.Code, head.Body)
		}
		get := httptest.NewRecorder()
		s.handleRemux(get, themeRemuxRequest(t, token, "remux"))
		if get.Code != http.StatusServiceUnavailable || !strings.Contains(get.Body.String(), errJobCeiling) {
			t.Fatalf("remux/theme bypassed ceiling: %d %s", get.Code, get.Body)
		}
	}
}

func TestJobCeilingCanceledExtractionReleasesCapacity(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	ffmpeg := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\ntouch '"+started+"'\nexec sleep 30\n"), 0755); err != nil {
		t.Fatal(err)
	}
	s := newTrickplayTestServer(t, ffmpeg)
	if err := s.SetJobCeiling(1); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	req := httptest.NewRequest(http.MethodPost, "/trickplay/extract", strings.NewReader(sheetsRequestJSON(t, "/media/movie.mkv"))).WithContext(ctx)
	go func() { s.handleTrickplayExtract(httptest.NewRecorder(), req); close(done) }()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("extraction process did not start")
		case <-tick.C:
		}
	}
	if rec := postStart(t, s, "other"); rec.Code != http.StatusServiceUnavailable {
		t.Fatal("extraction did not hold compute capacity")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("canceled extraction did not stop")
	}
	if release, ok := s.acquireJob(t.Context(), "other"); !ok {
		t.Fatal("canceled extraction retained capacity")
	} else {
		release()
	}
}

func TestJobCeilingRetainsCanceledCallerUntilDetachedProbeExits(t *testing.T) {
	s := newTestServer(t)
	if err := s.SetJobCeiling(1); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	finish := filepath.Join(dir, "finish")
	ffmpeg := filepath.Join(dir, "ffmpeg")
	script := "#!/bin/sh\ntouch '" + started + "'\nwhile [ ! -f '" + finish + "' ]; do sleep 0.01; done\nexit 0\n"
	if err := os.WriteFile(ffmpeg, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(finish, nil, 0600) })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx, release, ok := s.acquireJobContext(ctx, "probe")
	if !ok {
		t.Fatal("probe not admitted")
	}
	done := make(chan error, 1)
	go func() { _, err := tonemap.Probe(ctx, ffmpeg, playback.HWAccelNone, ""); done <- err }()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("detached probe process did not start")
		case <-ticker.C:
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("caller cancellation changed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("probe ignored caller cancellation")
	}
	release()
	if other, ok := s.acquireJob(t.Context(), "other"); ok {
		other()
		t.Fatal("canceled caller released capacity while detached FFmpeg was still running")
	}
	if err := os.WriteFile(finish, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for {
		if other, ok := s.acquireJob(t.Context(), "other"); ok {
			other()
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("completed detached probe retained capacity")
		case <-ticker.C:
		}
	}
}
