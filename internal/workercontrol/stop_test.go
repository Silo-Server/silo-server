//go:build unix

package workercontrol

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workersource"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

type stopObservation struct {
	domain, work, generation string
	status                   int
}

type stopStatusWriter struct {
	http.ResponseWriter
	status int
}

func (w *stopStatusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *stopStatusWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(body)
}

type stopFixture struct {
	corePrivate ed25519.PrivateKey
	runtime     *Runtime
	worker      *httptest.Server
	service     *workercontrolcore.Service
	store       *workercontrolcore.MemoryStore
	source      string
	stops       chan stopObservation
}

type stopNodes struct{ node workercontrolcore.Node }

func (n stopNodes) ByID(_ context.Context, id int) (workercontrolcore.Node, bool, error) {
	return n.node, n.node.ID == id, nil
}
func (n stopNodes) ByURL(_ context.Context, origin string) (workercontrolcore.Node, bool, error) {
	return n.node, n.node.URL == origin, nil
}

func newStopFixture(t *testing.T) *stopFixture {
	t.Helper()
	corePublic, corePrivate, _ := ed25519.GenerateKey(rand.Reader)
	nodePublic, nodePrivate, _ := ed25519.GenerateKey(rand.Reader)
	core := httptest.NewUnstartedServer(nil)
	worker := httptest.NewUnstartedServer(nil)
	t.Cleanup(core.Close)
	t.Cleanup(worker.Close)
	origin := "http://" + core.Listener.Addr().String()
	nodeURL := "http://" + worker.Listener.Addr().String()
	store := workercontrolcore.NewMemoryStore()
	var service *workercontrolcore.Service
	verifier := &workeridentity.Verifier{Origin: origin, Domain: workeridentity.DomainSourceRequest, AllowHTTP: true, ResolveKey: func(int) (ed25519.PublicKey, error) { return nodePublic, nil }}
	source, err := workersource.New(workersource.Options{Origin: origin, Store: workersource.NewMemoryStore(), PathApprover: func(context.Context, string, string) error { return nil }, Active: func(ctx context.Context, node int, work string, generation uint64) bool {
		return service.WorkActive(ctx, node, work, generation)
	}, VerifyRequest: verifier.Verify})
	if err != nil {
		t.Fatal(err)
	}
	service, err = workercontrolcore.New(workercontrolcore.Options{Origin: origin, AllowHTTP: true, PrivateKey: corePrivate, Nodes: stopNodes{workercontrolcore.Node{ID: 17, URL: nodeURL, PublicKey: nodePublic, Enabled: true, AllowHTTP: true}}, Store: store, Sources: renewalSourceAuthority{source}, SourceHandler: source.Handler(), ExecutionConfig: func(context.Context, int) (workerwire.ExecutionConfig, error) {
		return workerwire.ExecutionConfig{Revision: "r1", SegmentRetentionSeconds: 600, SubtitleSyncCapacity: 1, OutputAuthorizationEpoch: "epoch"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	core.Config.Handler = service.Handler()
	core.Start()
	program := filepath.Join(t.TempDir(), "ffmpeg")
	// The subprocess implements the real encoder's stdin lifetime. Media
	// correctness is covered by the separate real FFmpeg integration cohort.
	if err := os.WriteFile(program, []byte("#!/bin/sh\nprintf '%s\\n' \"$$\" > producer.pid\nwhile IFS= read -r line; do\n [ \"$line\" = q ] && exit 0\ndone\n"), 0700); err != nil {
		t.Fatal(err)
	}
	runtime, err := New(t.Context(), Options{Binding: workeridentity.Binding{Version: 1, NodeID: 17, CoreURL: origin, CorePublicKey: corePublic, NodePrivateKey: nodePrivate, NodeURL: nodeURL, AllowHTTP: true}, Scratch: t.TempDir(), FFmpeg: program, HWAccel: "none", MaxJobs: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	f := &stopFixture{corePrivate: corePrivate, runtime: runtime, worker: worker, service: service, store: store, stops: make(chan stopObservation, 8), source: filepath.Join(t.TempDir(), "source.bin")}
	if err := os.WriteFile(f.source, []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	worker.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodDelete {
			runtime.Handler().ServeHTTP(w, request)
			return
		}
		response := &stopStatusWriter{ResponseWriter: w}
		runtime.Handler().ServeHTTP(response, request)
		f.stops <- stopObservation{request.Header.Get(workeridentity.HeaderDomain), request.Header.Get(workeridentity.HeaderWorkID), request.Header.Get(workeridentity.HeaderGeneration), response.status}
	})
	worker.Start()
	nodehttp.Configure(service.WrapTransport)
	t.Cleanup(func() { nodehttp.Configure(nil) })
	return f
}

func (f *stopFixture) start(t *testing.T, work string, previousPID int) (uint64, int, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"session_id": work, inputPathField: f.source, "source_video_codec": "h264", "target_codec_video": "h264", "target_codec_audio": "aac", "segment_duration": 2, "total_duration": 600})
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
	a, ok, err := f.store.Get(t.Context(), 17, work)
	if err != nil || !ok {
		t.Fatalf("assignment missing: %v", err)
	}
	f.runtime.mu.Lock()
	var input string
	for _, handle := range f.runtime.handles[work] {
		input = handle.URL()
	}
	f.runtime.mu.Unlock()
	pidPattern := filepath.Join(f.runtime.options.Scratch, work+"*", "producer.pid")
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		pidPaths, err := filepath.Glob(pidPattern)
		if err != nil {
			t.Fatal(err)
		}
		for _, pidPath := range pidPaths {
			if raw, err := os.ReadFile(pidPath); err == nil {
				pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
				if err == nil && pid > 0 && pid != previousPID && syscall.Kill(pid, 0) == nil {
					// HTTP 202 and a live PID precede asynchronous tracking.
					// Begin stop assertions only after this generation is visible.
					for _, tracked := range f.runtime.tracker.scopedSnapshot() {
						if tracked.WorkID == work && tracked.Generation == a.Generation && tracked.Info.SessionID == work {
							return a.Generation, pid, input
						}
					}
				}
			}
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("encoder did not become live and tracked")
		}
	}
}

func (f *stopFixture) jobs(t *testing.T) int32 {
	t.Helper()
	response, err := http.Get(f.worker.URL + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var health transcodenode.HealthResponse
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	return health.ActiveJobs
}

func TestRemoteStopSignsGenerationAndReleasesActualWorker(t *testing.T) {
	f := newStopFixture(t)
	generation, pid, input := f.start(t, "work", 0)
	if f.jobs(t) != 1 || len(f.runtime.tracker.snapshot()) != 1 {
		t.Fatal("live producer not accounted")
	}
	manager := playback.NewTranscodeManager()
	manager.JWTSecretFn = func() string { return "legacy-test-secret" }
	if err := manager.CancelRemoteTranscode(t.Context(), "work", f.worker.URL); err != nil {
		t.Fatalf("stop acknowledgment: %v", err)
	}
	if syscall.Kill(pid, 0) != syscall.ESRCH || f.jobs(t) != 0 || len(f.runtime.tracker.snapshot()) != 0 || f.runtime.inputs.Allowed(input) {
		t.Fatal("acknowledged stop retained process/job/session/source resources")
	}
	observed := <-f.stops
	if observed.domain != workeridentity.DomainCoreCommand || observed.work != "work" || observed.generation != strconv.FormatUint(generation, 10) || observed.status != http.StatusNoContent {
		t.Fatalf("stop proof/ack=%+v", observed)
	}
	if active, _ := f.store.IsActive(t.Context(), 17, "work", generation); active {
		t.Fatal("acknowledged stop retained core authority")
	}
	// A different identity can consume the operator's one released slot.
	f.start(t, "next", 0)
	if err := manager.CancelRemoteTranscode(t.Context(), "next", f.worker.URL); err != nil {
		t.Fatal(err)
	}
}

func TestDelayedGenerationStopCannotRetireSuccessor(t *testing.T) {
	f := newStopFixture(t)
	old, oldPID, _ := f.start(t, "work", 0)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodDelete, f.worker.URL+"/transcode/work", nil)
	if err != nil {
		t.Fatal(err)
	}
	delayed, err := f.service.PrepareRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if delayed.Header.Get(workeridentity.HeaderGeneration) != strconv.FormatUint(old, 10) {
		t.Fatal("stop not pinned to observed generation")
	}
	generation, pid, input := f.start(t, "work", oldPID)
	if generation <= old {
		t.Fatal("replacement generation did not advance")
	}
	for attempt, status := range []int{http.StatusNoContent, http.StatusUnauthorized} {
		response, err := http.DefaultClient.Do(delayed.Clone(t.Context()))
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != status {
			t.Fatalf("delayed stop attempt %d status=%d", attempt, response.StatusCode)
		}
		<-f.stops
	}
	if syscall.Kill(pid, 0) != nil || f.jobs(t) != 1 || len(f.runtime.tracker.snapshot()) != 1 || !f.runtime.inputs.Allowed(input) {
		t.Fatal("old stop or replay retired successor resources")
	}
	if active, _ := f.store.IsActive(t.Context(), 17, "work", generation); !active {
		t.Fatal("old stop revoked successor authority")
	}
	manager := playback.NewTranscodeManager()
	manager.JWTSecretFn = func() string { return "legacy-test-secret" }
	if err := manager.CancelRemoteTranscode(t.Context(), "work", f.worker.URL); err != nil {
		t.Fatal(err)
	}
}

func TestStopBlockedBeforeLifecycleLockCannotRetireSuccessor(t *testing.T) {
	f := newStopFixture(t)
	_, oldPID, _ := f.start(t, "work", 0)
	entered, release := make(chan struct{}), make(chan struct{})
	releaseStop := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseStop)
	var blocked atomic.Bool
	handler := f.runtime.handler
	f.runtime.handler = http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodDelete && blocked.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
		handler.ServeHTTP(w, request)
	})
	manager := playback.NewTranscodeManager()
	manager.JWTSecretFn = func() string { return "legacy-test-secret" }
	stopped := make(chan error, 1)
	go func() { stopped <- manager.CancelRemoteTranscode(t.Context(), "work", f.worker.URL) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("stop did not reach lifecycle barrier")
	}
	// The core has revoked the old authority, but this stop has not acquired
	// the engine lifecycle lock. Install a real successor in that interval.
	generation, pid, input := f.start(t, "work", oldPID)
	releaseStop()
	if err := <-stopped; err != nil {
		t.Fatalf("old stop acknowledgment: %v", err)
	}
	if observed := <-f.stops; observed.status != http.StatusNoContent {
		t.Fatalf("old stop acknowledgment=%+v", observed)
	}
	if syscall.Kill(pid, 0) != nil || f.jobs(t) != 1 || len(f.runtime.tracker.snapshot()) != 1 || !f.runtime.inputs.Allowed(input) {
		t.Fatal("in-flight old stop retired successor resources")
	}
	if active, _ := f.store.IsActive(t.Context(), 17, "work", generation); !active {
		t.Fatal("in-flight old stop revoked successor core authority")
	}
	if err := manager.CancelRemoteTranscode(t.Context(), "work", f.worker.URL); err != nil {
		t.Fatal(err)
	}
}

func TestMissingStopScopeDoesNotTouchAnotherProducer(t *testing.T) {
	f := newStopFixture(t)
	generation, pid, input := f.start(t, "work", 0)
	manager := playback.NewTranscodeManager()
	manager.JWTSecretFn = func() string { return "legacy-test-secret" }
	if err := manager.CancelRemoteTranscode(t.Context(), "missing", f.worker.URL); err != nil {
		t.Fatal(err)
	}
	if observed := <-f.stops; observed.status != http.StatusNoContent || observed.generation != "0" {
		t.Fatalf("missing scope acknowledgment=%+v", observed)
	}
	if syscall.Kill(pid, 0) != nil || f.jobs(t) != 1 || len(f.runtime.tracker.snapshot()) != 1 || !f.runtime.inputs.Allowed(input) {
		t.Fatal("empty stop scope touched another producer")
	}
	if active, _ := f.store.IsActive(t.Context(), 17, "work", generation); !active {
		t.Fatal("empty stop scope revoked another work")
	}
	if err := manager.CancelRemoteTranscode(t.Context(), "work", f.worker.URL); err != nil {
		t.Fatal(err)
	}
}

func TestStopRetiresCapturedActiveAndPendingInputs(t *testing.T) {
	f := newStopFixture(t)
	activeGeneration, pid, activeInput := f.start(t, "work", 0)
	body, _ := json.Marshal(map[string]any{"session_id": "work", inputPathField: f.source})
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, f.worker.URL+"/transcode/start", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := f.service.PrepareRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.service.FinishRequest(candidate, nil, errors.New("test candidate finished")) })
	var assignment workerwire.Assignment
	if err := json.NewDecoder(candidate.Body).Decode(&assignment); err != nil {
		t.Fatal(err)
	}
	_ = candidate.Body.Close()
	if assignment.Generation <= activeGeneration {
		t.Fatal("candidate generation did not advance")
	}
	if err := f.runtime.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	handles, err := f.runtime.stageSources(assignment)
	if err != nil {
		t.Fatal(err)
	}
	var pendingInput string
	for _, handle := range handles {
		pendingInput = handle.URL()
	}
	if !f.runtime.inputs.Allowed(activeInput) || !f.runtime.inputs.Allowed(pendingInput) {
		t.Fatal("active and pending source leases were not simultaneously admitted")
	}
	manager := playback.NewTranscodeManager()
	manager.JWTSecretFn = func() string { return "legacy-test-secret" }
	if err := manager.CancelRemoteTranscode(t.Context(), "work", f.worker.URL); err != nil {
		t.Fatal(err)
	}
	if syscall.Kill(pid, 0) != syscall.ESRCH || f.jobs(t) != 0 || f.runtime.inputs.Allowed(activeInput) || f.runtime.inputs.Allowed(pendingInput) {
		t.Fatal("stop acknowledged before retiring captured active and pending resources")
	}
	if observed := <-f.stops; observed.status != http.StatusNoContent || observed.generation != strconv.FormatUint(assignment.Generation, 10) {
		t.Fatalf("stop did not cover latest pending cutoff: %+v", observed)
	}
}
