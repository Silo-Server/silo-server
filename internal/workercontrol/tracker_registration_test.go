//go:build unix

package workercontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodehttp"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
)

type delayedReportTracker struct {
	transcodenode.SessionTracker
	entered  chan struct{}
	release  chan struct{}
	finished chan struct{}
	first    atomic.Bool
}

func (t *delayedReportTracker) waitTrack(info nodesessions.SessionInfo, run func()) {
	if info.SessionID == "work" && t.first.CompareAndSwap(false, true) {
		close(t.entered)
		<-t.release
		defer close(t.finished)
	}
	run()
}
func (t *delayedReportTracker) Track(ctx context.Context, info nodesessions.SessionInfo) {
	t.waitTrack(info, func() { t.SessionTracker.Track(ctx, info) })
}

type delayedRegistration struct {
	transcodenode.SessionTracking
	tracker *delayedReportTracker
	info    nodesessions.SessionInfo
}

func (p *delayedRegistration) Track(ctx context.Context) {
	p.tracker.waitTrack(p.info, func() { p.SessionTracking.Track(ctx) })
}
func (t *delayedReportTracker) PrepareSessionTracking(ctx context.Context, info nodesessions.SessionInfo) transcodenode.SessionTracking {
	registration := t.SessionTracker.(transcodenode.SessionTrackingPreparer).PrepareSessionTracking(ctx, info)
	if registration == nil {
		return nil
	}
	return &delayedRegistration{SessionTracking: registration, tracker: t, info: info}
}
func installDelayedTracker(t *testing.T, f *stopFixture) *delayedReportTracker {
	t.Helper()
	tracker := &delayedReportTracker{SessionTracker: f.runtime.tracker, entered: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
	r := f.runtime
	engine, err := transcodenode.NewRuntimeServer(transcodenode.RuntimeOptions{Config: r.config, Tracker: tracker, VerifyCommand: r.verifyCommand, VerifyToken: r.verifyToken, Denied: r.denied, ExecuteHandler: http.HandlerFunc(r.execute)})
	if err != nil {
		t.Fatal(err)
	}
	engine.SetInputPathAuthorizer(inputAuthorizer{r.inputs})
	engine.SetThemeInputAuthorizer(inputAuthorizer{r.inputs})
	engine.SetRecipeStore(recipeAdapter{r})
	if err := engine.SetJobCeiling(r.options.MaxJobs); err != nil {
		t.Fatal(err)
	}
	_ = r.observations.registry.Stop(t.Context())
	r.server = engine
	r.handler = engine.Handler()
	r.initObservations()
	return tracker
}

// startBeforeReport waits only for signed acceptance and the observable producer
// receipt. This test intentionally holds the separate monitoring registration.
func (f *stopFixture) startBeforeReport(t *testing.T, previousPID int) (uint64, int, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"session_id": "work", inputPathField: f.source, "source_video_codec": "h264", "target_codec_video": "h264", "target_codec_audio": "aac", "segment_duration": 2, "total_duration": 600})
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, f.worker.URL+"/transcode/start", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if err := nodeauth.Sign(request, f.worker.URL, "legacy-test-secret"); err != nil {
		t.Fatal(err)
	}
	response, err := nodehttp.Client(&http.Client{}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("start status=%d body=%s", response.StatusCode, raw)
	}
	assignment, ok, err := f.store.Get(t.Context(), 17, "work")
	if err != nil || !ok {
		t.Fatal("missing signed assignment", err)
	}
	f.runtime.mu.Lock()
	var input string
	for _, handle := range f.runtime.handles["work"] {
		input = handle.URL()
	}
	f.runtime.mu.Unlock()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		paths, err := filepath.Glob(filepath.Join(f.runtime.options.Scratch, "work*", "producer.pid"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			if raw, err := os.ReadFile(path); err == nil {
				pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
				if err == nil && pid > 0 && pid != previousPID && syscall.Kill(pid, 0) == nil {
					return assignment.Generation, pid, input
				}
			}
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("producer did not become live")
		}
	}
}
func TestDelayedTrackerCannotResurrectStoppedOrOverwriteNewGeneration(t *testing.T) {
	for _, successor := range []bool{false, true} {
		t.Run(strconv.FormatBool(successor), func(t *testing.T) {
			f := newStopFixture(t)
			tracker := installDelayedTracker(t, f)
			release := sync.OnceFunc(func() { close(tracker.release) })
			defer release()
			old, oldPID, oldInput := f.startBeforeReport(t, 0)
			select {
			case <-tracker.entered:
			case <-time.After(time.Second):
				t.Fatal("monitoring Track did not reach gate")
			}
			manager := playback.NewTranscodeManager()
			manager.JWTSecretFn = func() string { return "legacy-test-secret" }
			if err := manager.CancelRemoteTranscode(t.Context(), "work", f.worker.URL); err != nil {
				t.Fatal(err)
			}
			if syscall.Kill(oldPID, 0) != syscall.ESRCH || f.jobs(t) != 0 || f.runtime.inputs.Allowed(oldInput) || f.service.WorkActive(t.Context(), 17, "work", old) {
				t.Fatal("STOP did not retire old execution/authority")
			}
			if len(f.runtime.tracker.scopedSnapshot()) != 0 {
				t.Fatal("old monitoring report remains before delayed Track")
			}
			var current uint64
			var pid int
			var input string
			if successor {
				current, pid, input = f.start(t, "work", oldPID)
				if current <= old {
					t.Fatal("new generation did not advance")
				}
			}
			release()
			select {
			case <-tracker.finished:
			case <-time.After(time.Second):
				t.Fatal("delayed Track failed to finish")
			}
			if err := f.runtime.refresh(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.runtime.revoked("work", old)
			reports := f.runtime.tracker.scopedSnapshot()
			if successor {
				if syscall.Kill(pid, 0) != nil || f.jobs(t) != 1 || !f.runtime.inputs.Allowed(input) || !f.service.WorkActive(t.Context(), 17, "work", current) {
					t.Fatal("live successor execution/authority changed")
				}
				if len(reports) != 1 || reports[0].Generation != current {
					t.Fatalf("delayed Track overwrote live generation %d: %+v", current, reports)
				}
			} else if len(reports) != 0 {
				t.Fatalf("delayed Track resurrected stopped receipt: %+v", reports)
			}
		})
	}
}

//nolint:nestif // The delayed retirement must cross reconstruction on the same generation without erasing its successor tracker.
func TestDelayedTrackerRetirementPreservesSameGenerationReconstruction(t *testing.T) {
	for _, reconstruct := range []bool{false, true} {
		t.Run(strconv.FormatBool(reconstruct), func(t *testing.T) {
			f := newStopFixture(t)
			tracker := installDelayedTracker(t, f)
			release := sync.OnceFunc(func() { close(tracker.release) })
			defer release()
			generation, oldPID, input := f.startBeforeReport(t, 0)
			select {
			case <-tracker.entered:
			case <-time.After(time.Second):
				t.Fatal("missing old Track gate")
			}
			f.runtime.tracker.mu.Lock()
			old := f.runtime.tracker.registrations["work"]
			f.runtime.tracker.mu.Unlock()
			if old == nil {
				t.Fatal("producer instance was not reserved before asynchronous Track")
			}
			// This exact producer retirement retains the existing signed generation and
			// sources, as an idle reap does. It must still retire its queued registration.
			f.runtime.server.StopWorkInputs(t.Context(), "work", []string{input})
			if syscall.Kill(oldPID, 0) != syscall.ESRCH || f.jobs(t) != 0 || !f.service.WorkActive(t.Context(), 17, "work", generation) {
				t.Fatal("idle retirement changed signed authority or retained producer")
			}
			var current *reportRegistration
			if reconstruct {
				assignment, ok, err := f.store.Get(t.Context(), 17, "work")
				if err != nil || !ok {
					t.Fatal(err)
				}
				raw, err := json.Marshal(assignment)
				if err != nil {
					t.Fatal(err)
				}
				request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, f.worker.URL+"/worker/execute", bytes.NewReader(raw))
				if err != nil {
					t.Fatal(err)
				}
				if err := workeridentity.Sign(request, f.worker.URL, workeridentity.DomainCoreCommand, f.corePrivate, 17, true); err != nil {
					t.Fatal(err)
				}
				response, err := http.DefaultClient.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if response.StatusCode != http.StatusAccepted {
					t.Fatalf("same-generation reconstruction: %d %s", response.StatusCode, body)
				}
				deadline := time.NewTimer(time.Second)
				defer deadline.Stop()
				tick := time.NewTicker(time.Millisecond)
				defer tick.Stop()
				for {
					f.runtime.tracker.mu.Lock()
					current = f.runtime.tracker.registrations["work"]
					_, tracked := f.runtime.tracker.sessions["work"]
					f.runtime.tracker.mu.Unlock()
					if current != nil && current != old && tracked {
						break
					}
					select {
					case <-tick.C:
					case <-deadline.C:
						t.Fatal("reconstructed same-generation instance did not track")
					}
				}
			}
			release()
			select {
			case <-tracker.finished:
			case <-time.After(time.Second):
				t.Fatal("old Track failed to finish")
			}
			old.Remove(t.Context()) // a delayed old cleanup has no right over the new instance
			reports := f.runtime.tracker.scopedSnapshot()
			if reconstruct {
				f.runtime.tracker.mu.Lock()
				retained := f.runtime.tracker.registrations["work"]
				f.runtime.tracker.mu.Unlock()
				if retained != current || len(reports) != 1 || reports[0].Generation != generation || f.jobs(t) != 1 {
					t.Fatal("old instance Track/Remove erased same-generation reconstruction")
				}
			} else if len(reports) != 0 {
				t.Fatal("idle retired Track resurrected a report")
			}
		})
	}
}

func TestTrackerRegistrationRevokePromotionAndCleanupAreExact(t *testing.T) {
	r := raceRuntime(t)
	r.mu.Lock()
	r.generations["work"] = 1
	r.mu.Unlock()
	ctx := context.WithValue(t.Context(), workContextKey{}, "work")
	ctx = context.WithValue(ctx, generationContextKey{}, uint64(1))
	first := r.tracker.PrepareSessionTracking(ctx, nodesessions.SessionInfo{SessionID: "viewer"})
	if first == nil {
		t.Fatal("current generation rejected")
	}
	replacement := r.tracker.PrepareSessionTracking(ctx, nodesessions.SessionInfo{SessionID: "viewer", Resolution: "replacement"})
	replacement.Track(ctx)
	first.Track(ctx)
	first.Remove(ctx)
	if reports := r.tracker.scopedSnapshot(); len(reports) != 1 || reports[0].Info.Resolution != "replacement" {
		t.Fatal("stale same-generation receipt overwrote/retired successor")
	}
	r.mu.Lock()
	r.generations["work"] = 3
	r.tracker.retireGenerationLocked("work", 3, true)
	r.mu.Unlock()
	first.Track(ctx)
	replacement.Track(ctx)
	if len(r.tracker.scopedSnapshot()) != 0 {
		t.Fatal("promotion retained old tracking")
	}
	next := context.WithValue(ctx, generationContextKey{}, uint64(3))
	live := r.tracker.PrepareSessionTracking(next, nodesessions.SessionInfo{SessionID: "viewer"})
	if live == nil {
		t.Fatal("new current generation rejected")
	}
	live.Track(next)
	r.revoked("work", 1)
	if len(r.tracker.scopedSnapshot()) != 1 {
		t.Fatal("old generation revoke erased live successor")
	}
	r.revoked("work", 3)
	live.Track(next)
	r.tracker.mu.Lock()
	remaining := len(r.tracker.registrations)
	r.tracker.mu.Unlock()
	if remaining != 0 || len(r.tracker.scopedSnapshot()) != 0 {
		t.Fatal("revoked pending tickets/reports retained")
	}
	if rejected := r.tracker.PrepareSessionTracking(next, nodesessions.SessionInfo{SessionID: "unowned"}); rejected != nil {
		t.Fatal("missing runtime generation accepted")
	}
	r.mu.Lock()
	r.generations["work"] = 4
	r.mu.Unlock()
	pending := r.tracker.PrepareSessionTracking(context.WithValue(ctx, generationContextKey{}, uint64(4)), nodesessions.SessionInfo{SessionID: "pending"})
	if pending == nil {
		t.Fatal("pending ticket missing")
	}
	r.tracker.Cleanup(t.Context())
	pending.Track(t.Context())
	r.tracker.mu.Lock()
	remaining = len(r.tracker.registrations)
	r.tracker.mu.Unlock()
	if remaining != 0 || len(r.tracker.scopedSnapshot()) != 0 {
		t.Fatal("cleanup allowed delayed registration")
	}
}
