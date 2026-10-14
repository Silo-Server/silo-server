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
	"syscall"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodehttp"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestDistinctNativeTransportJobsShareOnlyTrustedPlaybackIdentity(t *testing.T) {
	f := newStopFixture(t)
	start := func(work, job string, forged bool) int {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"session_id": work, "job_id": "parent-play", inputPathField: f.source, "source_video_codec": "h264", "target_codec_video": "h264", "target_codec_audio": "aac", "segment_duration": 2})
		ctx := t.Context()
		if job != "" {
			ctx = nodehttp.WithPlaybackJob(ctx, job)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, f.worker.URL+"/transcode/start", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if forged {
			request.Header.Set("X-Silo-Playback-Job", "parent-play")
		}
		if err := nodeauth.Sign(request, f.worker.URL, "legacy-test-secret"); err != nil {
			t.Fatal(err)
		}
		response, err := nodehttp.Client(&http.Client{}).Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		return response.StatusCode
	}
	if status := start("transport-initial", "parent-play", false); status != http.StatusAccepted {
		t.Fatalf("initial transport status=%d", status)
	}
	if status := start("transport-seek", "parent-play", false); status != http.StatusAccepted {
		t.Fatalf("distinct seek transport refused at one-job ceiling: status=%d", status)
	}
	initialPID := waitJobProducerPID(t, f, "transport-initial")
	seekPID := waitJobProducerPID(t, f, "transport-seek")
	program := f.runtime.options.FFmpeg
	if err := os.Rename(program, program+".saved"); err != nil {
		t.Fatal(err)
	}
	failedStatus := start("transport-failed-candidate", "parent-play", false)
	if err := os.Rename(program+".saved", program); err != nil {
		t.Fatal(err)
	}
	if failedStatus != http.StatusInternalServerError || f.jobs(t) != 2 {
		t.Fatalf("failed candidate disturbed healthy transports: status=%d jobs=%d", failedStatus, f.jobs(t))
	}
	for _, pid := range []int{initialPID, seekPID} {
		if err := syscall.Kill(pid, 0); err != nil {
			t.Fatalf("failed candidate stopped healthy producer PID%d: %v", pid, err)
		}
	}
	for _, request := range []struct{ work, job string }{{"unrelated", "another-play"}, {"parent-play", ""}, {"forged", ""}} {
		if status := start(request.work, request.job, true); status != http.StatusServiceUnavailable {
			t.Fatalf("unrelated/raw header/body identity shared slot: %s status=%d", request.work, status)
		}
	}
	manager := playback.NewTranscodeManager()
	manager.JWTSecretFn = func() string { return "legacy-test-secret" }
	if err := manager.CancelRemoteTranscode(context.Background(), "transport-initial", f.worker.URL); err != nil {
		t.Fatal(err)
	}
	if status := start("still-unrelated", "another-play", false); status != http.StatusServiceUnavailable {
		t.Fatal("stopping old transport released live seek's job")
	}
	if err := manager.CancelRemoteTranscode(context.Background(), "transport-seek", f.worker.URL); err != nil {
		t.Fatal(err)
	}
	if status := start("next-play", "another-play", false); status != http.StatusAccepted {
		t.Fatalf("last transport retained job capacity: status=%d", status)
	}
}

func waitJobProducerPID(t *testing.T, f *stopFixture, work string) int {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		paths, err := filepath.Glob(filepath.Join(f.runtime.options.Scratch, work+"*", "producer.pid"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			if raw, err := os.ReadFile(path); err == nil {
				pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
				if err == nil && pid > 0 && syscall.Kill(pid, 0) == nil {
					return pid
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

func TestExecutionJobIdentityNamespacesAreDisjoint(t *testing.T) {
	for _, id := range []string{"same", "playback:same", "work:same", "a:b.c-d_e"} {
		if executionJobIdentity(id, "") == executionJobIdentity("other", id) {
			t.Fatalf("fallback work and explicit playback share accounting key for %q", id)
		}
	}
}
