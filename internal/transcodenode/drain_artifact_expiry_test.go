package transcodenode

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/workerdrain"
)

type artifactExpiryFixture struct {
	server   *Server
	listener *httptest.Server
	preparer downloadprepare.HTTPPreparer
	request  downloadprepare.Request
	initial  time.Time
	clock    atomic.Int64
	fence    atomic.Value
}

func newArtifactExpiryFixture(t *testing.T) *artifactExpiryFixture {
	t.Helper()
	fixture := &artifactExpiryFixture{server: newTestServer(t), initial: time.Now().UTC(), request: downloadprepare.Request{ArtifactID: "bounded", InputPath: "/media/movie.mkv", TargetCodecVideo: "copy", TargetCodecAudio: "copy", AudioTrackIndex: -1}}
	fixture.clock.Store(fixture.initial.UnixNano())
	fixture.fence.Store("")
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg.sh")
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\nfor last; do :; done\nprintf artifact > \"$last\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	fixture.server.watcher.Config().Playback.FFmpegPath = ffmpeg
	fixture.server.watcher.Config().Playback.HWAccel = "none"
	fixture.server.drain = workerdrain.NewWithClock(func(context.Context) (int, string, string, error) {
		return 7, fixture.fence.Load().(string), "synthetic-realm", nil
	}, fixture.server.drainActiveJobs, nil, func() time.Time { return time.Unix(0, fixture.clock.Load()).UTC() })
	fixture.listener = httptest.NewServer(fixture.server.Handler())
	t.Cleanup(fixture.listener.Close)
	// Cleanup remains independent of the expiry observer, including mutation RED.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 3*time.Second)
		defer cancel()
		if err := fixture.preparer.Delete(ctx, fixture.listener.URL, testSecret, fixture.request.ArtifactID); err != nil {
			t.Errorf("exact fixture deletion: %v", err)
		}
		path := filepath.Join(fixture.server.artifactRoot, fixture.request.ArtifactID+".mp4")
		for _, candidate := range []string{path, path + ".part"} {
			if _, err := os.Stat(candidate); !os.IsNotExist(err) {
				t.Errorf("fixture file cleanup: %v", err)
			}
		}
		status, err := fixture.server.drain.Observe(ctx)
		if err != nil || status.ActiveRequests != 0 || status.ActiveJobs != 0 || status.ActiveReservations != 0 {
			t.Errorf("fixture reservation cleanup: %+v %v", status, err)
		}
	})
	return fixture
}

func (f *artifactExpiryFixture) advance(elapsed time.Duration) {
	f.clock.Store(f.initial.Add(elapsed).UnixNano())
}

func (f *artifactExpiryFixture) open(t *testing.T, method string, expected int) {
	t.Helper()
	response, err := f.preparer.Open(t.Context(), f.listener.URL, testSecret, f.request.ArtifactID, method, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || response.StatusCode != expected {
		t.Fatalf("actual %s: status=%d body=%q read=%v close=%v", method, response.StatusCode, body, readErr, closeErr)
	}
	if method == http.MethodGet && expected == http.StatusOK && string(body) != "artifact" {
		t.Fatalf("actual artifact changed: %q", body)
	}
}

func (f *artifactExpiryFixture) proof(t *testing.T) workerdrain.Status {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, f.listener.URL+"/admin/drain", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testSecret)
	response, err := f.listener.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var status workerdrain.Status
	if response.StatusCode != http.StatusOK {
		t.Fatalf("actual drain status: %d", response.StatusCode)
	}
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func TestTranscodeDrainArtifactPollingCannotExtendBoundedPermit(t *testing.T) {
	for _, admission := range []string{"prepared artifact", "initial delivery", "cached reuse"} {
		t.Run(admission, func(t *testing.T) {
			fixture := newArtifactExpiryFixture(t)
			path := filepath.Join(fixture.server.artifactRoot, fixture.request.ArtifactID+".mp4")
			if admission == "initial delivery" {
				if err := os.MkdirAll(fixture.server.artifactRoot, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("artifact"), 0600); err != nil {
					t.Fatal(err)
				}
				fixture.open(t, http.MethodHead, http.StatusOK)
			} else {
				result, err := fixture.preparer.Prepare(t.Context(), fixture.listener.URL, testSecret, fixture.request)
				if err != nil || result.FileSize != 8 {
					t.Fatalf("actual preparation: %+v %v", result, err)
				}
			}
			original, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			deadline := playback.MaxTokenTTL
			// Even before fencing, delivery must not renew existing ownership.
			fixture.advance(time.Hour)
			fixture.open(t, http.MethodHead, http.StatusOK)
			fixture.advance(5 * time.Hour)
			fixture.open(t, http.MethodGet, http.StatusOK)
			if admission == "cached reuse" {
				fixture.advance(12 * time.Hour)
				result, err := fixture.preparer.Prepare(t.Context(), fixture.listener.URL, testSecret, fixture.request)
				if err != nil || result.FileSize != 8 {
					t.Fatalf("actual cached reuse: %+v %v", result, err)
				}
				deadline += 12 * time.Hour
				fixture.advance(13 * time.Hour)
				fixture.open(t, http.MethodHead, http.StatusOK)
				fixture.open(t, http.MethodGet, http.StatusOK)
			}
			fixture.fence.Store("durable-fence")
			for _, elapsed := range []time.Duration{deadline - time.Hour, deadline - time.Second} {
				fixture.advance(elapsed)
				fixture.open(t, http.MethodHead, http.StatusOK)
				fixture.open(t, http.MethodGet, http.StatusOK)
				status := fixture.proof(t)
				if !status.Fenced || status.Drained || status.ActiveReservations != 1 || status.ActiveRequests != 0 || status.ActiveJobs != 0 {
					t.Fatalf("live bounded artifact: %+v", status)
				}
			}
			fixture.advance(deadline)
			// Ask the actual HEAD route first: it must not renew/resurrect at expiry.
			fixture.open(t, http.MethodHead, http.StatusServiceUnavailable)
			fixture.open(t, http.MethodGet, http.StatusServiceUnavailable)
			status := fixture.proof(t)
			if !status.Drained || status.ActiveReservations != 0 || status.ActiveRequests != 0 || status.ActiveJobs != 0 {
				t.Fatalf("polling defeated bounded retirement: %+v", status)
			}
			data, err := os.ReadFile(path)
			after, statErr := os.Stat(path)
			if err != nil || statErr != nil || string(data) != "artifact" || !after.ModTime().Equal(original.ModTime()) {
				t.Fatalf("delivery/expiry mutated artifact: bytes=%q read=%v stat=%v", data, err, statErr)
			}
			if err := fixture.preparer.Delete(t.Context(), fixture.listener.URL, testSecret, fixture.request.ArtifactID); err != nil {
				t.Fatal(err)
			}
			for _, candidate := range []string{path, path + ".part"} {
				if _, err := os.Stat(candidate); !os.IsNotExist(err) {
					t.Fatalf("exact artifact cleanup: %v", err)
				}
			}
			if status := fixture.proof(t); !status.Drained || status.ActiveReservations != 0 || status.ActiveJobs != 0 || status.ActiveRequests != 0 {
				t.Fatalf("post-delete cleanup: %+v", status)
			}
		})
	}
}

func TestTranscodeDrainPreparationKeepsAdmissionDeadlineAcrossFence(t *testing.T) {
	fixture := newArtifactExpiryFixture(t)
	payload, err := json.Marshal(fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, downloadPreparePath, bytes.NewReader(payload))
	request.Header.Set("Authorization", "Bearer "+testSecret)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	writer := &blockedArtifactResponse{ResponseRecorder: httptest.NewRecorder(), entered: entered, release: release}
	handler := fixture.server.Handler()
	go func() { defer close(done); handler.ServeHTTP(writer, request) }()
	t.Cleanup(func() {
		unblock()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("preparation handler did not finish during cleanup")
		}
	})
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("actual preparation did not reach publication")
	}
	fixture.advance(12 * time.Hour)
	fixture.fence.Store("durable-fence")
	status := fixture.proof(t)
	if !status.Fenced || status.Drained || status.ActiveRequests != 1 || status.ActiveJobs < 1 || status.ActiveReservations != 0 {
		t.Fatalf("pre-fence publication lost its active reservation: %+v", status)
	}
	unblock()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("actual preparation did not finish publication")
	}
	var result downloadprepare.Result
	if err := json.Unmarshal(writer.Body.Bytes(), &result); err != nil || writer.Code != http.StatusOK || result.FileSize != 8 {
		t.Fatalf("actual preparation result: status=%d result=%+v decode=%v", writer.Code, result, err)
	}
	fixture.advance(playback.MaxTokenTTL - time.Second)
	fixture.open(t, http.MethodHead, http.StatusOK)
	fixture.open(t, http.MethodGet, http.StatusOK)
	fixture.advance(playback.MaxTokenTTL)
	fixture.open(t, http.MethodHead, http.StatusServiceUnavailable)
	fixture.open(t, http.MethodGet, http.StatusServiceUnavailable)
	status = fixture.proof(t)
	if !status.Drained || status.ActiveRequests != 0 || status.ActiveJobs != 0 || status.ActiveReservations != 0 {
		t.Fatalf("preparation retained a completion-time deadline: %+v", status)
	}
}
