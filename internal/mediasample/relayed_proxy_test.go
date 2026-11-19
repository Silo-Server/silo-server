package mediasample

import (
	"bufio"
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/nodesource/nodesourcetest"
	"github.com/Silo-Server/silo-server/internal/processmetrics"
)

// FFmpeg's http protocol sends loopback requests through the proxy in
// http_proxy too. A relayed read, through the argv guard and through the
// relayed concat list, must reach the relay directly on a host with a proxy
// set, and the proxy must never see a relay URL.
func TestRelayedInputBypassesHTTPProxyWithRealFFmpeg(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration test")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}
	clip := filepath.Join(t.TempDir(), "clip.mkv")
	if output, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=s=160x96:r=24:d=20",
		"-pix_fmt", "yuv420p", "-g", "48", clip).CombinedOutput(); err != nil {
		t.Skipf("cannot generate the clip: %v: %s", err, output)
	}

	proxy := newRecordingProxy(t)
	for _, name := range []string{"http_proxy", "HTTP_PROXY", "all_proxy", "ALL_PROXY"} {
		t.Setenv(name, "http://"+proxy.addr)
	}
	t.Setenv("no_proxy", "")
	t.Setenv("NO_PROXY", "")
	// The capability check of the relayed concat form runs under the proxy too.
	InvalidateCapabilities()
	t.Cleanup(InvalidateCapabilities)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	caps, err := LoadCapabilities(ctx, ffmpeg)
	if err != nil {
		t.Skipf("ffmpeg capabilities unavailable: %v", err)
	}

	core := nodesourcetest.NewCore(t, clip)
	relayed := core.Open(t).Input()
	runner := Runner{FFmpegPath: ffmpeg, Workload: processmetrics.Trickplay}
	frame, err := runner.Run(ctx, Request{Input: relayed, At: &At{Seconds: 5}, Images: &ImageOutput{Width: 80}, Threads: 1})
	if err != nil || len(frame.Images) != 1 {
		t.Fatalf("relayed frame: %v (%d images)", err, len(frame.Images))
	}
	sheets := Request{
		Input:   relayed,
		Samples: &Samples{Seconds: []float64{2, 6, 10, 14}},
		Sheets:  &SheetsOutput{TileWidth: 64, TileHeight: 38, Columns: 2, Rows: 2, Quality: 90},
		Threads: 1,
	}
	if err := caps.Require(sheets); err != nil {
		t.Fatalf("the relayed concat capability check fails under a proxy: %v", err)
	}
	result, err := runner.Run(ctx, sheets)
	if err != nil || len(result.Sheets) != 1 {
		t.Fatalf("relayed sheets: %v (%d sheets)", err, len(result.Sheets))
	}
	// The input option alone suffices: an ffprobe launch that inherits the
	// proxy variables still connects directly.
	probe := fspath.CommandContext(ctx, ffprobe, "-v", "error", "-show_entries", "format=duration", relayed)
	probe.Env = os.Environ()
	if output, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("relayed ffprobe with the proxy inherited: %v: %s", err, output)
	}

	if core.Requests.Load() == 0 {
		t.Fatal("nothing was read through the core")
	}
	if seen := proxy.requests(); len(seen) > 0 {
		t.Fatalf("the proxy saw relayed reads: %q", seen)
	}
}

// recordingProxy accepts connections and records each request line.
type recordingProxy struct {
	addr string
	mu   sync.Mutex
	seen []string
}

func newRecordingProxy(t *testing.T) *recordingProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &recordingProxy{addr: listener.Addr().String()}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				line, _ := bufio.NewReader(conn).ReadString('\n')
				p.mu.Lock()
				p.seen = append(p.seen, line)
				p.mu.Unlock()
			}()
		}
	}()
	return p
}

func (p *recordingProxy) requests() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.seen...)
}
