package chapterthumbs

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/workerinput"
	"github.com/Silo-Server/silo-server/internal/workersource"
)

func TestExtractRegisteredRemoteFrameMatchesLocal(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	path := filepath.Join(t.TempDir(), "chapter.mkv")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	generate := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=s=160x90:r=24:d=4",
		"-an", "-c:v", "libx264", "-threads", "1", "-preset", "ultrafast", "-g", "24", "-output_ts_offset", "11.4", path)
	if output, err := generate.CombinedOutput(); err != nil {
		t.Fatalf("generate chapter fixture: %v: %s", err, output)
	}
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()
	var reads atomic.Int64
	authority, err := workersource.New(workersource.Options{
		Store: workersource.NewMemoryStore(), Origin: origin,
		Active: func(context.Context, int, string, uint64) bool { return true },
		PathApprover: func(_ context.Context, input, kind string) error {
			if input != path || kind != "media" {
				return workersource.ErrDenied
			}
			return nil
		},
		VerifyRequest: func(r *http.Request, _ int) error {
			if r.Header.Get("X-Test-Proof") != "node-proof" {
				return workersource.ErrDenied
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		authority.Handler().ServeHTTP(w, r)
	})
	server.Start()
	defer server.Close()
	source, err := authority.Acquire(ctx, 7, "chapter", 1, path, "media")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := workerinput.New(workerinput.Options{
		Origins: []string{origin}, AllowHTTP: true,
		Allowed: func(work string, generation uint64) bool { return work == "chapter" && generation == 1 },
		Sign:    func(r *http.Request) error { r.Header.Set("X-Test-Proof", "node-proof"); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = adapter.Close() }()
	workerCtx := workerinput.WithContext(ctx, adapter)
	handle, err := adapter.Open(workerCtx, workerinput.Source{ID: source.Ref.ID, Revision: source.Ref.Revision, URL: source.URL, Grant: source.Grant,
		ETag: source.ETag, Size: source.Size, WorkID: source.WorkID, Generation: source.Generation})
	if err != nil {
		t.Fatal(err)
	}
	opts := FrameExtractOptions{InputPath: path, FFmpegPath: ffmpeg, HWAccel: "none", SeekSeconds: 2.5}
	local, reason, err := ExtractFrame(ctx, opts)
	if err != nil || reason != "" {
		t.Fatalf("local frame: reason=%q err=%v", reason, err)
	}
	opts.InputPath = handle.URL()
	remote, reason, err := ExtractFrame(workerCtx, opts)
	if err != nil || reason != "" || !bytes.Equal(remote, local) || reads.Load() == 0 {
		t.Fatalf("registered remote frame: reason=%q err=%v identical=%t reads=%d", reason, err, bytes.Equal(remote, local), reads.Load())
	}
	_ = handle.Close()
	var called bool
	opts.RunFunc = func(context.Context, string, []string) ([]byte, error) { called = true; return []byte("JPEG"), nil }
	if _, _, err := ExtractFrame(workerCtx, opts); err == nil || called {
		t.Fatalf("released chapter input reached executor: err=%v called=%t", err, called)
	}
}
