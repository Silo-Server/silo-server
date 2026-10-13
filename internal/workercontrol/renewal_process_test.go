//go:build unix

package workercontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/workerinput"
)

func TestSourceRenewalFailureStopsActualProducer(t *testing.T) {
	r := raceRuntime(t)
	program := filepath.Join(t.TempDir(), "ffmpeg")
	// This subprocess fixture implements only the encoder's stdin lifetime.
	// Source-byte and real FFmpeg correctness have separate integration tests.
	script := "#!/bin/sh\nprintf '%s\\n' \"$$\" > producer.pid\nwhile IFS= read -r line; do\n  [ \"$line\" = q ] && exit 0\ndone\n"
	if err := os.WriteFile(program, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	r.config.mu.Lock()
	cfg := *r.config.cfg
	cfg.Playback.FFmpegPath = program
	r.config.cfg = &cfg
	r.config.mu.Unlock()
	if err := r.authority.SetNonce("test-nonce"); err != nil {
		t.Fatal(err)
	}
	if err := r.authority.Apply(raceSnapshot(r, 1, 4, 2000), time.Now()); err != nil {
		t.Fatal(err)
	}
	handles, err := r.openSources(raceAssignment(4))
	if err != nil {
		t.Fatal(err)
	}
	input := handles["source:media"].URL()
	body, _ := json.Marshal(map[string]any{"session_id": "work", inputPathField: input, "source_video_codec": "h264", "target_codec_video": "h264", "target_codec_audio": "aac", "segment_duration": 2, "total_duration": 600})
	ctx := workerinput.WithContext(context.WithValue(t.Context(), commandContextKey{}, r), r.inputs)
	request := httptest.NewRequest(http.MethodPost, "http://worker.example.test/transcode/start", bytes.NewReader(body)).WithContext(ctx)
	w := httptest.NewRecorder()
	r.server.Handler().ServeHTTP(w, request)
	if w.Code < 200 || w.Code >= 300 {
		t.Fatalf("producer start=%d %s", w.Code, w.Body.String())
	}
	pidPath := filepath.Join(r.options.Scratch, "work", "producer.pid")
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	pid := 0
	for pid == 0 {
		if raw, err := os.ReadFile(pidPath); err == nil {
			pid, err = strconv.Atoi(strings.TrimSpace(string(raw)))
			if err != nil {
				t.Fatal(err)
			}
		}
		if pid != 0 {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("encoder process did not start")
		}
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatal("encoder was not alive before renewal failure")
	}
	r.renewals.fail("work", 4)
	if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Fatalf("encoder process survived failed renewal: %v", err)
	}
	if r.workAllowed("work", 4) || r.inputs.Allowed(input) {
		t.Fatal("failed producer retained source authority")
	}
	if sessions := r.tracker.snapshot(); len(sessions) != 0 {
		t.Fatalf("stopped producer retained occupancy: %+v", sessions)
	}
}
