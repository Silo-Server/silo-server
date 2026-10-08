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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/workerdrain"
)

func TestTranscodeDrainRetainsPreparedArtifactDelivery(t *testing.T) {
	server := newTestServer(t)
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg.sh")
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\nfor last; do :; done\nprintf artifact > \"$last\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpeg
	server.watcher.Config().Playback.HWAccel = "none"
	var fence atomic.Value
	fence.Store("")
	source := func(context.Context) (int, string, string, error) {
		return 7, fence.Load().(string), "synthetic-realm", nil
	}
	server.drain = workerdrain.New(source, server.drainActiveJobs, nil)
	listener := httptest.NewServer(server.Handler())
	defer listener.Close()
	preparer := downloadprepare.HTTPPreparer{}
	prepare := downloadprepare.Request{ArtifactID: "prepared", InputPath: "/media/movie.mkv", TargetCodecVideo: "copy", TargetCodecAudio: "copy", AudioTrackIndex: -1}
	result, err := preparer.Prepare(t.Context(), listener.URL, testSecret, prepare)
	if err != nil || result.FileSize != int64(len("artifact")) {
		t.Fatalf("production preparation: %+v %v", result, err)
	}
	fence.Store("durable-fence")
	for _, method := range []string{http.MethodHead, http.MethodGet} {
		response, err := preparer.Open(t.Context(), listener.URL, testSecret, prepare.ArtifactID, method, nil)
		if err != nil {
			t.Fatalf("existing %s artifact refused during drain: %v", method, err)
		}
		body, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("delivery %s: %d %v %v", method, response.StatusCode, readErr, closeErr)
		}
		if method == http.MethodGet && string(body) != "artifact" {
			t.Fatalf("artifact delivery: %q", body)
		}
	}
	proof, err := server.drain.Observe(t.Context())
	if err != nil || proof.Drained || proof.ActiveRequests != 0 || proof.ActiveReservations != 1 {
		t.Fatalf("quiet artifact lost: %+v %v", proof, err)
	}
	if _, err := preparer.Prepare(t.Context(), listener.URL, testSecret, prepare); err == nil {
		t.Fatal("known artifact preparation admitted after fence")
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		response, err := preparer.Open(t.Context(), listener.URL, testSecret, "late", method, nil)
		if err != nil {
			t.Fatal(err)
		}
		closeErr := response.Body.Close()
		if response.StatusCode != http.StatusServiceUnavailable || closeErr != nil {
			t.Fatalf("unknown artifact admitted after fence: %s status=%d close=%v", method, response.StatusCode, closeErr)
		}
	}
	// Raw transport IDs cannot inherit a permit from the artifact namespace.
	for _, route := range []struct{ method, path, body string }{
		{http.MethodGet, "/transcode/artifact:prepared/master.m3u8", ""},
		{http.MethodGet, "/remux/artifact:prepared", ""},
		{http.MethodPost, "/transcode/start", `{"session_id":"artifact:prepared"}`},
	} {
		request, err := http.NewRequestWithContext(t.Context(), route.method, listener.URL+route.path, strings.NewReader(route.body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+testSecret)
		response, err := listener.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("transport inherited artifact permit: %s %s %d", route.method, route.path, response.StatusCode)
		}
	}
	original := server.drain
	server.drain = workerdrain.New(source, server.drainActiveJobs, nil)
	response, restartErr := preparer.Open(t.Context(), listener.URL, testSecret, prepare.ArtifactID, http.MethodGet, nil)
	server.drain = original
	if restartErr != nil {
		t.Fatal(restartErr)
	}
	if closeErr := response.Body.Close(); closeErr != nil || response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("restart resurrected an artifact permit from disk: status=%d close=%v", response.StatusCode, closeErr)
	}
	if err := preparer.Delete(t.Context(), listener.URL, testSecret, prepare.ArtifactID); err != nil {
		t.Fatal(err)
	}
	proof, err = server.drain.Observe(t.Context())
	if err != nil || !proof.Drained || proof.ActiveReservations != 0 {
		t.Fatalf("artifact deletion did not retire permit: %+v %v", proof, err)
	}
	if _, err := os.Stat(filepath.Join(server.artifactRoot, prepare.ArtifactID+".mp4")); !os.IsNotExist(err) {
		t.Fatalf("artifact cleanup: %v", err)
	}
}

func TestTranscodeDrainArtifactFailureAndExactCleanup(t *testing.T) {
	for _, scenario := range []string{"unauthorized admission", "quiet missing artifact", "failed preparation", "failed deletion", "exact deletion"} {
		t.Run(scenario, func(t *testing.T) {
			server := newTestServer(t)
			ffmpeg := filepath.Join(t.TempDir(), "ffmpeg.sh")
			program := "#!/bin/sh\nfor last; do :; done\nprintf artifact > \"$last\"\n"
			if scenario == "failed preparation" {
				program = "#!/bin/sh\nexit 1\n"
			}
			if err := os.WriteFile(ffmpeg, []byte(program), 0700); err != nil {
				t.Fatal(err)
			}
			server.watcher.Config().Playback.FFmpegPath = ffmpeg
			server.watcher.Config().Playback.HWAccel = "none"
			var fence atomic.Value
			fence.Store("")
			server.drain = workerdrain.New(func(context.Context) (int, string, string, error) {
				return 7, fence.Load().(string), "synthetic-realm", nil
			}, server.drainActiveJobs, nil)
			listener := httptest.NewServer(server.Handler())
			defer listener.Close()
			preparer := downloadprepare.HTTPPreparer{}
			request := downloadprepare.Request{ArtifactID: "prepared", InputPath: "/media/movie.mkv", TargetCodecVideo: "copy", TargetCodecAudio: "copy", AudioTrackIndex: -1}
			switch scenario {
			case "unauthorized admission":
				payload, err := json.Marshal(request)
				if err != nil {
					t.Fatal(err)
				}
				for _, token := range []string{"", "wrong-node-bearer"} {
					for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodHead} {
						path := "/downloads/artifacts/prepared"
						if method == http.MethodPost {
							path = "/downloads/prepare"
						}
						request, err := http.NewRequestWithContext(t.Context(), method, listener.URL+path, bytes.NewReader(payload))
						if err != nil {
							t.Fatal(err)
						}
						if token != "" {
							request.Header.Set("Authorization", "Bearer "+token)
						}
						response, err := listener.Client().Do(request)
						if err != nil {
							t.Fatal(err)
						}
						_ = response.Body.Close()
						if response.StatusCode != http.StatusUnauthorized {
							t.Fatalf("unauthenticated artifact admission: %s %d", method, response.StatusCode)
						}
					}
				}
			case "quiet missing artifact":
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					response, err := preparer.Open(t.Context(), listener.URL, testSecret, "missing", method, nil)
					if err != nil {
						t.Fatal(err)
					}
					if closeErr := response.Body.Close(); closeErr != nil || response.StatusCode != http.StatusNotFound {
						t.Fatalf("missing artifact: %s status=%d close=%v", method, response.StatusCode, closeErr)
					}
				}
			case "failed preparation":
				if _, err := preparer.Prepare(t.Context(), listener.URL, testSecret, request); err == nil {
					t.Fatal("faulting command succeeded")
				}
			case "failed deletion", "exact deletion":
				if _, err := preparer.Prepare(t.Context(), listener.URL, testSecret, request); err != nil {
					t.Fatal(err)
				}
				if scenario == "exact deletion" {
					request.ArtifactID = "survivor"
					if _, err := preparer.Prepare(t.Context(), listener.URL, testSecret, request); err != nil {
						t.Fatal(err)
					}
				}
				fence.Store("durable-fence")
				if scenario == "failed deletion" {
					part := filepath.Join(server.artifactRoot, "prepared.mp4.part")
					if err := os.Mkdir(part, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(part, "owned-fault"), []byte("fault"), 0600); err != nil {
						t.Fatal(err)
					}
					if err := preparer.Delete(t.Context(), listener.URL, testSecret, "prepared"); err == nil {
						t.Fatal("faulting deletion succeeded")
					}
				} else if err := preparer.Delete(t.Context(), listener.URL, testSecret, "prepared"); err != nil {
					t.Fatal(err)
				}
			}
			fence.Store("durable-fence")
			proof, err := server.drain.Observe(t.Context())
			wantReservations := 0
			if scenario == "failed deletion" || scenario == "exact deletion" {
				wantReservations = 1
			}
			if err != nil || proof.ActiveJobs != 0 || proof.ActiveRequests != 0 || proof.ActiveReservations != wantReservations || proof.Drained != (wantReservations == 0) {
				t.Fatalf("fault/cleanup proof: %+v %v", proof, err)
			}
			if scenario == "exact deletion" {
				if err := preparer.Delete(t.Context(), listener.URL, testSecret, "survivor"); err != nil {
					t.Fatal(err)
				}
				proof, err = server.drain.Observe(t.Context())
				if err != nil || !proof.Drained {
					t.Fatalf("survivor cleanup: %+v %v", proof, err)
				}
			}
			if scenario == "failed preparation" {
				for _, suffix := range []string{".mp4", ".mp4.part"} {
					if _, err := os.Stat(filepath.Join(server.artifactRoot, "prepared"+suffix)); !os.IsNotExist(err) {
						t.Fatalf("failed preparation cleanup: %v", err)
					}
				}
			}
		})
	}
}

type blockedArtifactResponse struct {
	*httptest.ResponseRecorder
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (w *blockedArtifactResponse) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.entered); <-w.release })
	return w.ResponseRecorder.Write(data)
}

func TestTranscodeDrainCachedArtifactDeleteCannotRacePermitTransfer(t *testing.T) {
	server := newTestServer(t)
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg.sh")
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\nfor last; do :; done\nprintf artifact > \"$last\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpeg
	server.watcher.Config().Playback.HWAccel = "none"
	var fence atomic.Value
	fence.Store("")
	server.drain = workerdrain.New(func(context.Context) (int, string, string, error) {
		return 7, fence.Load().(string), "synthetic-realm", nil
	}, server.drainActiveJobs, nil)
	listener := httptest.NewServer(server.Handler())
	defer listener.Close()
	preparer := downloadprepare.HTTPPreparer{}
	input := downloadprepare.Request{ArtifactID: "prepared", InputPath: "/media/movie.mkv", TargetCodecVideo: "copy", TargetCodecAudio: "copy", AudioTrackIndex: -1}
	if _, err := preparer.Prepare(t.Context(), listener.URL, testSecret, input); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	request := httptest.NewRequest(http.MethodPost, "/downloads/prepare", bytes.NewReader(payload))
	request.Header.Set("Authorization", "Bearer "+testSecret)
	writer := &blockedArtifactResponse{ResponseRecorder: httptest.NewRecorder(), entered: entered, release: release}
	finished := make(chan struct{})
	go func() { server.Handler().ServeHTTP(writer, request); close(finished) }()
	defer func() { unblock(); <-finished }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("routed cached publication did not write result")
	}
	fence.Store("durable-fence")
	deleted := make(chan error, 1)
	go func() { deleted <- preparer.Delete(t.Context(), listener.URL, testSecret, input.ArtifactID) }()
	defer func() { unblock(); <-deleted }()
	// Give deletion the opportunity to complete while the result writer is
	// stalled. A broken read-lock scope makes that ordering deterministic.
	select {
	case err := <-deleted:
		deleted <- err
	case <-time.After(250 * time.Millisecond):
	}
	unblock()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("cached publication did not finish")
	}
	select {
	case err := <-deleted:
		deleted <- err
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("exact deletion did not finish")
	}
	if writer.Code != http.StatusOK {
		t.Fatalf("cached reuse: %d %s", writer.Code, writer.Body)
	}
	proof, err := server.drain.Observe(t.Context())
	if err != nil || !proof.Drained || proof.ActiveReservations != 0 || proof.ActiveRequests != 0 {
		t.Fatalf("successful deletion retained a phantom permit: %+v %v", proof, err)
	}
	if _, err := os.Stat(filepath.Join(server.artifactRoot, input.ArtifactID+".mp4")); !os.IsNotExist(err) {
		t.Fatalf("exact artifact cleanup: %v", err)
	}
}
