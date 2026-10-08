//go:build unix

package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Silo-Server/silo-server/internal/workerdrain"
)

func TestProxyDrainCountsDetachedSubtitleFill(t *testing.T) {
	for _, tc := range []struct {
		name       string
		exitCode   int
		changeFile bool
		committed  int
	}{{"publish", 0, false, 1}, {"command failure", 1, false, 0}, {"source mutation", 0, true, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			startedPath, releasePath := filepath.Join(dir, "started.fifo"), filepath.Join(dir, "release.fifo")
			for _, path := range []string{startedPath, releasePath} {
				if err := unix.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			}
			startedPipe, err := os.OpenFile(startedPath, os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = startedPipe.Close() }()
			releasePipe, err := os.OpenFile(releasePath, os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = releasePipe.Close() }()
			quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
			ffmpeg := filepath.Join(dir, "ffmpeg")
			script := fmt.Sprintf("#!/bin/sh\ncase \" $* \" in *' -ss '*) printf window; exit 0;; esac\nprintf 'started\\n' > %s\nprintf partial\nread ignored < %s\nexit %d\n", quote(startedPath), quote(releasePath), tc.exitCode)
			if err := os.WriteFile(ffmpeg, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { _, _ = releasePipe.WriteString("release\n") }) }
			defer unblock()
			t.Cleanup(func() {
				deadline := time.Now().Add(5 * time.Second)
				for {
					parts, err := filepath.Glob(filepath.Join(dir, "subtitle-cache", "*.part-*"))
					if err != nil || len(parts) == 0 {
						return
					}
					if time.Now().After(deadline) {
						t.Error("subtitle child cleanup retained temporary work")
						return
					}
					runtime.Gosched()
				}
			})
			started := make(chan error, 1)
			go func() { _, err := bufio.NewReader(startedPipe).ReadString('\n'); started <- err }()
			const secret = "subtitle-control-secret"
			srv := newSocketProxyServer(t, secret, nil)
			srv.watcher.Config().Playback.FFmpegPath = ffmpeg
			srv.watcher.Config().Playback.TranscodeDir = dir
			var fence, deny atomic.Bool
			srv.drain = workerdrain.New(func(context.Context) (int, string, string, error) {
				if fence.Load() {
					return 7, "durable-fence", "synthetic-realm", nil
				}
				return 7, "", "synthetic-realm", nil
			}, srv.probesInFlight, func(context.Context, string) bool { return deny.Load() })
			source := writeSocketProxyMedia(t)
			token := socketProxyMediaToken(t, secret, source)
			listener := httptest.NewServer(srv.Handler())
			defer listener.Close()
			response, err := listener.Client().Get(listener.URL + "/stream/subtitles/" + token + "/0.sup?windowed=1&position=1&duration=1")
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil || response.StatusCode != http.StatusOK || string(body) != "window" {
				t.Fatalf("routed foreground subtitle: %d %q %v", response.StatusCode, body, err)
			}
			select {
			case err := <-started:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("actual background subtitle child did not start")
			}
			status := func() workerdrain.Status {
				t.Helper()
				request, err := http.NewRequest(http.MethodGet, listener.URL+"/admin/drain", nil)
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Authorization", "Bearer "+secret)
				response, err := listener.Client().Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = response.Body.Close() }()
				var proof workerdrain.Status
				if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&proof) != nil {
					t.Fatalf("subtitle drain status: %d", response.StatusCode)
				}
				return proof
			}
			fence.Store(true)
			deny.Store(true) // Positive revocation closes the request's media permit.
			busy := status()
			if busy.Drained || busy.ActiveJobs < 1 || busy.ActiveRequests != 0 || busy.ActiveReservations != 0 {
				t.Fatalf("detached subtitle falsely retired: %+v", busy)
			}
			if tc.changeFile {
				if err := os.WriteFile(source, []byte("replaced source with different size"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			unblock()
			deadline := time.Now().Add(5 * time.Second)
			for {
				proof := status()
				if proof.Drained {
					if proof.ActiveJobs != 0 {
						t.Fatal("drained receipt still has subtitle work")
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("subtitle cache cleanup did not reach authoritative zero")
				}
				runtime.Gosched()
			}
			parts, err := filepath.Glob(filepath.Join(dir, "subtitle-cache", "*.part-*"))
			if err != nil || len(parts) != 0 {
				t.Fatalf("subtitle temporary cleanup: %v %v", parts, err)
			}
			entries, err := filepath.Glob(filepath.Join(dir, "subtitle-cache", "*.sup"))
			if err != nil || len(entries) != tc.committed {
				t.Fatalf("subtitle publication after lifecycle: %v %v", entries, err)
			}
		})
	}
}
