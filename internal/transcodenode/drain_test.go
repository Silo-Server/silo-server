package transcodenode

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/chapterthumbs"
	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

func healthStatusOf(t *testing.T, s *Server) HealthResponse {
	t.Helper()
	rr := httptest.NewRecorder()
	s.handleHealth(rr, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("health status code = %d, want 200 in every drain phase", rr.Code)
	}
	var health HealthResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	return health
}

func newLoopingFFmpegServer(t *testing.T) *Server {
	t.Helper()
	s := newTestServer(t)
	s.tracker = nodesessions.NewLocalTracker()
	useFakeFFmpeg(t, s, "#!/bin/sh\nwhile :; do sleep 0.1; done\n")
	return s
}

func postStart(t *testing.T, s *Server, sessionID string) *httptest.ResponseRecorder {
	t.Helper()
	return postStartRequest(t, s, TranscodeStartRequest{
		SessionID: sessionID, InputPath: "/media/movie.mkv", TargetCodecVideo: "h264",
		TargetCodecAudio: "aac", SegmentDuration: 2,
	})
}

func signed(t *testing.T, r *http.Request) *http.Request {
	t.Helper()
	nodeauthtest.Sign(t, r, testNodeKey)
	return r
}

// A draining node refuses a new transcode with the 503 both API retry loops
// read as "try the next node", but replaces one it runs: a Jellyfin audio-track
// switch re-posts the same session id, and refusing it would end the stream.
func TestDrainingNodeRefusesNewStartsButReplacesItsOwn(t *testing.T) {
	s := newLoopingFFmpegServer(t)
	if rr := postStart(t, s, "kept"); rr.Code != http.StatusAccepted {
		t.Fatalf("start before drain: %d %s", rr.Code, rr.Body.String())
	}
	// Shutdown ends the sessions, which also ends the drain's watch goroutine.
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })

	s.BeginShutdownDrain()

	rr := postStart(t, s, "new")
	if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), "draining") {
		t.Fatalf("new start while draining: %d %q, want 503 node is draining", rr.Code, rr.Body.String())
	}
	if s.hostsSession("new") {
		t.Fatal("a refused start registered a session")
	}
	if rr := postStart(t, s, "kept"); rr.Code != http.StatusAccepted {
		t.Fatalf("replacing a hosted session while draining: %d %s", rr.Code, rr.Body.String())
	}
	if got := s.activeJobs.Load(); got != 1 {
		t.Fatalf("active jobs = %d after a replacement, want 1", got)
	}
}

// Health never reports drained while a rebuild or remux that counted itself in
// flight goes ahead: the work check and the latch are one step against
// reportedDrained, so the admission is either seen by the check or refused.
func TestDrainedLatchFencesAdmission(t *testing.T) {
	s := newLoopingFFmpegServer(t)
	s.BeginShutdownDrain()
	for range 2000 {
		s.drainedReported.Store(false)
		var admitted bool
		var status string
		healthDone := make(chan struct{})
		var wg sync.WaitGroup
		wg.Go(func() {
			release := s.trackInFlight()
			admitted = !s.reportedDrained()
			<-healthDone
			release()
		})
		wg.Go(func() {
			status = s.healthStatus()
			close(healthDone)
		})
		wg.Wait()
		if status == HealthStatusDrained && admitted {
			t.Fatal("health reported drained while a counted admission went ahead")
		}
	}
}

// An administrator's drain request, read from the node's own row, drains the
// node the same way; lifting it puts the node back to work. A SIGTERM drain
// cannot be lifted.
func TestAdminDrainRequestFollowsTheNodeRow(t *testing.T) {
	s := newTestServer(t)
	var announced atomic.Int32
	s.SetDrainAnnouncer(func() { announced.Add(1) })

	runtimeOf(s).SetNodeDrainRequestedForTest(true)
	if !s.draining() || healthStatusOf(t, s).Status != HealthStatusDrained {
		t.Fatalf("draining=%v status=%q after a drain request", s.draining(), healthStatusOf(t, s).Status)
	}
	if rr := postStart(t, s, "new"); rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("start while admin-drained: %d, want 503", rr.Code)
	}

	runtimeOf(s).SetNodeDrainRequestedForTest(false)
	if s.draining() || healthStatusOf(t, s).Status != HealthStatusOK {
		t.Fatal("lifting the request did not put the node back to work")
	}
	if got := announced.Load(); got < 2 {
		t.Fatalf("announced %d drain changes, want one for each", got)
	}

	s.BeginShutdownDrain()
	runtimeOf(s).SetNodeDrainRequestedForTest(true)
	runtimeOf(s).SetNodeDrainRequestedForTest(false)
	if !s.draining() {
		t.Fatal("lifting an admin request ended a SIGTERM drain")
	}
}

// Draining announces at the start and again once the node has finished, so
// the API servers see "drained" without waiting for a sweep.
func TestDrainAnnouncesStartAndCompletion(t *testing.T) {
	s := newTestServer(t)
	s.activeJobs.Add(1)
	announcements := make(chan struct{}, 4)
	s.SetDrainAnnouncer(func() { announcements <- struct{}{} })

	s.BeginShutdownDrain()
	s.BeginShutdownDrain()
	select {
	case <-announcements:
	case <-time.After(5 * time.Second):
		t.Fatal("no announcement when the drain began")
	}
	s.activeJobs.Add(-1)
	select {
	case <-announcements:
	case <-time.After(5 * time.Second):
		t.Fatal("no announcement when the drain finished")
	}
	if len(announcements) != 0 {
		t.Fatalf("%d extra announcements; a repeated SIGTERM must not announce again", len(announcements))
	}
}

func TestWaitDrainedEndsWithTheWorkOrTheDeadline(t *testing.T) {
	s := newTestServer(t)
	s.activeJobs.Add(1)
	s.BeginShutdownDrain()

	ctx, cancel := context.WithTimeout(context.Background(), 2*drainPollInterval)
	err := s.WaitDrained(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitDrained with work left = %v, want the deadline", err)
	}

	done := make(chan error, 1)
	go func() { done <- s.WaitDrained(context.Background()) }()
	s.activeJobs.Add(-1)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitDrained = %v once the work ended", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitDrained did not return once the work ended")
	}
}

// Every other kind of new work is refused with the shape its API-side client
// already treats as "node unavailable".
func TestDrainingNodeRefusesNewJobs(t *testing.T) {
	s := newTestServer(t)
	handler := s.Handler()
	s.BeginShutdownDrain()
	prepare, err := json.Marshal(downloadprepare.Request{ArtifactID: strings.Repeat("a", 32), InputPath: "/media/movie.mkv"})
	if err != nil {
		t.Fatal(err)
	}
	chapter, err := json.Marshal(chapterthumbs.RemoteExtractRequest{InputPath: "/media/movie.mkv"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		path   string
		body   string
		reason string
	}{
		{name: "download prepare", path: "/downloads/prepare", body: string(prepare), reason: "draining"},
		{name: "chapter thumbnail", path: "/chapter-thumbnails/extract", body: string(chapter), reason: chapterthumbs.RemoteNodeUnavailableReason},
		{name: "media sample", path: mediasample.RemotePath, body: string(speechRequestBody(t, nil)), reason: string(mediasample.ReasonNodeUnavailable)},
		{name: "trickplay", path: "/trickplay/extract", body: sheetsRequestJSON(t, "/media/movie.mkv"), reason: "node_unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, signed(t, httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))))
			if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), test.reason) {
				t.Fatalf("status %d body %q, want 503 carrying %q", rr.Code, rr.Body.String(), test.reason)
			}
			if test.path == "/chapter-thumbnails/extract" {
				assertChapterFallsBackToLocal(t, rr.Body.Bytes())
			}
		})
	}
	if got := s.activeJobs.Load(); got != 0 {
		t.Fatalf("active jobs = %d after refusals", got)
	}
}

// A drain request the node read before its announcer was set (its first config
// load at boot) is announced when the announcer is set, and so is its end.
func TestDrainReadAtBootIsAnnounced(t *testing.T) {
	s := newTestServer(t)
	runtimeOf(s).SetNodeDrainRequestedForTest(true)
	announcements := make(chan struct{}, 4)
	s.SetDrainAnnouncer(func() { announcements <- struct{}{} })
	for _, what := range []string{"the drain read at boot", "the drained node"} {
		select {
		case <-announcements:
		case <-time.After(5 * time.Second):
			t.Fatalf("no announcement for %s", what)
		}
	}
}

// A fresh progressive remux GET (a reconnect or seek) is refused once the node
// has reported itself drained, since whoever read that may stop it.
func TestDrainedNodeRefusesFreshRemux(t *testing.T) {
	server := newTestServer(t)
	server.nodeRowID = func() (int, bool) { return 11, true }
	mediaPath := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(mediaPath, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	claims := streamtoken.Claims{
		SessionID: "playback-drained-remux", MediaPath: mediaPath, PlayMethod: string(playback.PlayRemux),
		TranscodeNode: "http://node", TranscodeTransportID: "transport-drained-remux",
		RoutingWorkload: string(noderouting.WorkloadRemux), RoutingExecution: string(noderouting.ExecutionTranscode),
		RoutingExecutionNodeID: 11, RoutingEgress: string(noderouting.EgressProxy),
	}
	coreOf(server).resolve(claims.TranscodeTransportID, resolutionOf(playback.RecipeCardFromClaims(&claims)))
	server.BeginShutdownDrain()
	if got := healthStatusOf(t, server).Status; got != HealthStatusDrained {
		t.Fatalf("status = %q, want drained", got)
	}

	request := httptest.NewRequest(http.MethodGet, "/remux/"+claims.TranscodeTransportID+"?seek=2", nil)
	request.Header.Set("X-Silo-Stream-Token", "relay-token")
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("session_id", claims.TranscodeTransportID)
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
	rr := httptest.NewRecorder()
	server.handleRemux(rr, request)
	if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), "draining") {
		t.Fatalf("remux after drained = %d %q, want 503 node is draining", rr.Code, rr.Body.String())
	}
	if got := server.activeJobs.Load(); got != 0 {
		t.Fatalf("active jobs = %d after a refused remux", got)
	}
}

// assertChapterFallsBackToLocal checks a chapter extraction refusal carries a
// reason the core's chapter client treats as the node's failure, so under
// prefer_transcode_nodes it extracts locally instead of recording the chapter
// as failed.
func assertChapterFallsBackToLocal(t *testing.T, body []byte) {
	t.Helper()
	var refusal chapterthumbs.RemoteExtractErrorResponse
	if err := json.Unmarshal(body, &refusal); err != nil {
		t.Fatalf("chapter refusal %q: %v", body, err)
	}
	if !chapterthumbs.IsInfrastructureRemoteFailure(refusal.Reason) {
		t.Fatalf("chapter refusal reason %q: the core would record the chapter as failed instead of extracting locally", refusal.Reason)
	}
}

func TestDrainingNodeReconstructsUntilItReportsDrained(t *testing.T) {
	const sessionID = "sess-drain-1"
	const transportID = sessionID + "-plan1234-abcd1234"
	s := newLoopingFFmpegServer(t)
	coreOf(s).resolve(transportID, resolutionOf(*nativeTransportCard(sessionID, transportID)))
	s.BeginShutdownDrain()

	session, err := s.reconstructLost(requestWithToken(transportID, ""), transportID, 5)
	if err != nil || session == nil {
		t.Fatalf("reconstruct while draining = %v, %v; want the session rebuilt", session, err)
	}
	_ = session.CloseProcess()
	// The viewer pauses long enough for the idle reaper to close the job. A
	// stop would fence the transport instead, and the drain check would never
	// run.
	s.reapIdleSessions(0)

	if got := healthStatusOf(t, s).Status; got != HealthStatusDrained {
		t.Fatalf("status = %q, want drained", got)
	}
	session, err = s.reconstructLost(requestWithToken(transportID, ""), transportID, 5)
	if err != nil || session != nil {
		if session != nil {
			_ = session.CloseProcess()
		}
		t.Fatalf("reconstruct after reporting drained = %v, %v; want no session", session, err)
	}
}
