package workerinput

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

func TestFFmpegSourceTransferFaultParity(t *testing.T) {
	ffmpeg, e := exec.LookPath("ffmpeg")
	if e != nil {
		t.Skip("actual FFmpeg is required for this recovery control")
	}
	fixture := filepath.Join(t.TempDir(), "source.ts")
	if out, e := exec.CommandContext(t.Context(), ffmpeg, "-v", "error", "-threads", "1", "-filter_threads", "1", "-f", "lavfi", "-i", "testsrc2=size=96x64:rate=30", "-t", "4", "-c:v", "mpeg2video", "-g", "15", "-f", "mpegts", fixture).CombinedOutput(); e != nil {
		t.Fatalf("fixture: %v %s", e, out)
	}
	data, e := os.ReadFile(fixture)
	if e != nil {
		t.Fatal(e)
	}
	args := func(input string) []string {
		return []string{"-v", "error", "-threads", "1", "-i", input, "-map", "0:v:0", "-f", "framemd5", "-"}
	}
	baseline, e := exec.CommandContext(t.Context(), ffmpeg, args(fixture)...).Output()
	if e != nil {
		t.Fatal(e)
	}

	if sourceTransferFrameCount(baseline) != 120 {
		t.Fatalf("local frame count%d", sourceTransferFrameCount(baseline))
	}
	for _, fault := range []string{"none", "503", "short-body"} {
		t.Run(fault, func(t *testing.T) {
			pub, key, _ := ed25519.GenerateKey(rand.Reader)
			origin := httptest.NewUnstartedServer(nil)
			url := "http://" + origin.Listener.Addr().String()
			verifier := workeridentity.Verifier{Origin: url, Domain: workeridentity.DomainSourceRequest, AllowHTTP: true, ResolveKey: func(int) (ed25519.PublicKey, error) { return pub, nil }}
			var calls atomic.Int64
			origin.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if e := verifier.Verify(r, 17); e != nil {
					t.Error(e)
					w.WriteHeader(401)
					return
				}
				first := calls.Add(1) == 1
				if first && fault == "503" {
					http.Error(w, "temporary", http.StatusServiceUnavailable)
					return
				}
				start, end := int64(0), int64(len(data)-1)
				status := 200
				if value := r.Header.Get("Range"); value != "" {
					var e error
					start, end, e = requestedRange(value, int64(len(data)))
					if e != nil {
						t.Error(e)
						w.WriteHeader(400)
						return
					}
					status = 206
					w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
				}
				w.Header().Set("ETag", `"r"`)
				w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
				w.WriteHeader(status)
				if first && fault == "short-body" {
					_, _ = w.Write(data[start : start+min(int64(16384), (end-start+1)/2)])
					return
				}
				_, _ = w.Write(data[start : end+1])
			})
			origin.Start()
			defer origin.Close()
			authority, e := workerwire.NewLocalAuthority(17, "boot", "nonce", nil)
			if e != nil {
				t.Fatal(e)
			}
			defer authority.Close()
			if e = authority.Apply(workerwire.Snapshot{Version: 1, NodeID: 17, BootID: "boot", Nonce: "nonce", Sequence: 1, Active: map[string]uint64{"work": 1}, ValidForMillis: 2000}, time.Now()); e != nil {
				t.Fatal(e)
			}
			a, e := New(Options{LocalRoot: t.TempDir(), Origins: []string{url}, AllowHTTP: true, Allowed: authority.Allowed, Sign: func(r *http.Request) error {
				return workeridentity.Sign(r, url, workeridentity.DomainSourceRequest, key, 17, true)
			}})
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = a.Close() }()
			h, e := a.Open(t.Context(), Source{ID: "s", Revision: "r", URL: url + "/source", Grant: "g", ETag: `"r"`, Size: int64(len(data)), WorkID: "work", Generation: 1})
			if e != nil {
				t.Fatal(e)
			}
			command := CommandContext(WithContext(t.Context(), a), ffmpeg, args(h.URL())...)
			var stderr bytes.Buffer
			command.Stderr = &stderr
			actual, e := command.Output()
			t.Logf("actual FFmpeg fault=%s frames=%d requests=%d bytes=%d/%d", fault, sourceTransferFrameCount(actual), calls.Load(), len(actual), len(baseline))
			if e != nil || !bytes.Equal(baseline, actual) {
				t.Fatalf("fault=%s output mismatch: %v stderr=%s frames=%d/%d", fault, e, stderr.String(), sourceTransferFrameCount(actual), sourceTransferFrameCount(baseline))
			}
			if !authority.Allowed("work", 1) {
				t.Fatal("fault parity exceeded original2second work authority")
			}
			if fault != "none" && calls.Load() < 2 {
				t.Fatal("fault did not require another upstream request")
			}
		})
	}
}

func sourceTransferFrameCount(out []byte) int {
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			n++
		}
	}
	return n
}
