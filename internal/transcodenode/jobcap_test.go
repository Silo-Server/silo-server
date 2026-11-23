package transcodenode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// assertAtCapacity checks a refusal the core can tell from a draining one.
func assertAtCapacity(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	if rr.Code != http.StatusServiceUnavailable || rr.Header().Get(nodepool.NodeRefusalHeader) != nodepool.RefusalAtCapacity ||
		rr.Header().Get("Retry-After") == "" {
		t.Fatalf("status %d, refusal %q, Retry-After %q: want 503 at_capacity with Retry-After",
			rr.Code, rr.Header().Get(nodepool.NodeRefusalHeader), rr.Header().Get("Retry-After"))
	}
}

// A node at its own job cap refuses a new start, but not one that continues a
// job it holds: a same-id replacement (an audio switch) or a new transport for
// a playback session it serves (a quality switch).
func TestJobCapRefusesNewStartsButContinuesHeldWork(t *testing.T) {
	s := newLoopingFFmpegServer(t)
	s.SetJobCap(1)
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	start := func(sessionID, playbackSessionID string) *httptest.ResponseRecorder {
		return postStartRequest(t, s, TranscodeStartRequest{
			SessionID: sessionID, PlaybackSessionID: playbackSessionID, InputPath: "/media/movie.mkv",
			TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
		})
	}
	if rr := start("first", "playback-1"); rr.Code != http.StatusAccepted {
		t.Fatalf("first start: %d %s", rr.Code, rr.Body.String())
	}
	rr := start("second", "playback-2")
	assertAtCapacity(t, rr)
	if s.hostsSession("second") {
		t.Fatal("a refused start registered a session")
	}
	if rr := start("first", "playback-1"); rr.Code != http.StatusAccepted {
		t.Fatalf("replacing a hosted session at the cap: %d %s", rr.Code, rr.Body.String())
	}
	if rr := start("first-replan", "playback-1"); rr.Code != http.StatusAccepted {
		t.Fatalf("a new transport for a served playback session at the cap: %d %s", rr.Code, rr.Body.String())
	}
	// One transport may overlap the one it replaces; a third for the same
	// playback session is new work.
	assertAtCapacity(t, start("first-replan-again", "playback-1"))
}

// Starts arriving together at the cap less one admit exactly one: a slot
// admitted but not yet counted in activeJobs counts against the cap.
func TestJobCapAdmissionIsRaceFree(t *testing.T) {
	s := newTestServer(t)
	s.SetJobCap(3)
	s.activeJobs.Store(2)
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		admitted []*jobSlot
	)
	for range 64 {
		wg.Go(func() {
			if slot, ok := s.admitJob(); ok {
				mu.Lock()
				admitted = append(admitted, slot)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if len(admitted) != 1 {
		t.Fatalf("%d starts admitted at the cap less one, want 1", len(admitted))
	}
	s.countJob(admitted[0])
	admitted[0].release()
	if s.activeJobs.Load() != 3 || s.admitted != 0 {
		t.Fatalf("after counting the slot: active %d, admitted %d", s.activeJobs.Load(), s.admitted)
	}
	if _, ok := s.admitJob(); ok {
		t.Fatal("a job was admitted at the cap")
	}
}

// Starts arriving together for one playback session the node serves share one
// overlap allowance: the one already running plus the allowances granted and
// not yet registered are counted together.
func TestContinuationAllowanceIsRaceFree(t *testing.T) {
	s := newTestServer(t)
	s.mu.Lock()
	s.identities = map[string]sessionIdentity{"running": {playbackSessionID: "playback-1"}}
	s.mu.Unlock()
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		granted []*continuation
	)
	for range 64 {
		wg.Go(func() {
			if overlap, ok := s.claimContinuation("playback-1", time.Now()); ok {
				mu.Lock()
				granted = append(granted, overlap)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if len(granted) != 1 {
		t.Fatalf("%d transports allowed to overlap one running session, want 1", len(granted))
	}
	granted[0].release()
	if overlap, ok := s.claimContinuation("playback-1", time.Now()); !ok {
		t.Fatal("a released allowance was not granted again")
	} else {
		overlap.release()
	}
	if len(s.continuing) != 0 {
		t.Fatalf("allowances left held: %v", s.continuing)
	}
}

// The other routes that take a job slot refuse at the cap with the shape
// their core-side clients already read as "node unavailable" or busy, plus
// the capacity refusal header.
func TestJobCapRefusesOtherNewJobs(t *testing.T) {
	s := newTestServer(t)
	s.SetJobCap(1)
	s.activeJobs.Store(1)
	handler := s.Handler()
	prepare, err := json.Marshal(downloadprepare.Request{ArtifactID: strings.Repeat("b", 32), InputPath: "/media/movie.mkv"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, path, body, reason string }{
		{name: "download prepare", path: "/downloads/prepare", body: string(prepare), reason: "job limit"},
		{name: "trickplay", path: "/trickplay/extract", body: sheetsRequestJSON(t, "/media/movie.mkv"), reason: "node_busy"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, signed(t, httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))))
			assertAtCapacity(t, rr)
			if !strings.Contains(rr.Body.String(), test.reason) {
				t.Fatalf("body %q, want %q", rr.Body.String(), test.reason)
			}
		})
	}
	if got := s.activeJobs.Load(); got != 1 || s.admitted != 0 {
		t.Fatalf("active jobs %d, admitted %d after refusals", got, s.admitted)
	}
}

// A declared acceleration holds against the backend the core names from the
// node's row; the core may still ask for none, and only a node that declares
// auto lets a hardware tone map name its backend.
func TestDeclaredHWAccelHoldsAgainstTheRequest(t *testing.T) {
	for _, tc := range []struct {
		declared, requested string
		toneMap             bool
		want                string
	}{
		{"", "nvenc", false, "nvenc"},
		{"vaapi", "nvenc", false, "vaapi"},
		{"vaapi", playback.HWAccelNone, false, playback.HWAccelNone},
		{"vaapi", "vaapi", true, "vaapi"},
		{"vaapi", "qsv", true, "vaapi"},
		{playback.HWAccelAuto, "vaapi", true, "vaapi"},
		{playback.HWAccelNone, "nvenc", true, playback.HWAccelNone},
		{playback.HWAccelAuto, "qsv", false, playback.HWAccelAuto},
	} {
		s := newTestServer(t)
		s.SetDeclaredHWAccel(tc.declared)
		if got := s.jobHWAccel(tc.requested, tc.toneMap); got != tc.want {
			t.Errorf("declared %q, requested %q, tone map %v: %q, want %q", tc.declared, tc.requested, tc.toneMap, got, tc.want)
		}
	}
}
