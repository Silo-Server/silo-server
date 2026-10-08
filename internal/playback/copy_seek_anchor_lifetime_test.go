//go:build unix

package playback

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func awaitCopySeekLifetime(t *testing.T, done func() bool, detail string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("copy seek-anchor lifetime: %s (claims=%d, slots=%d)", detail, CopySeekAnchorsInFlight(), len(copySeekProbeSlots))
		}
		runtime.Gosched()
	}
}

func TestCopySeekAnchorLifetimeSurvivesCanceledCaller(t *testing.T) {
	for _, pendingAdmission := range []bool{false, true} {
		for _, failed := range []bool{false, true} {
			name := "executing/success"
			if pendingAdmission {
				name = "queued/success"
			}
			if failed {
				name = strings.TrimSuffix(name, "success") + "command failure"
			}
			t.Run(name, func(t *testing.T) {
				awaitCopySeekLifetime(t, func() bool { return CopySeekAnchorsInFlight() == 0 && len(copySeekProbeSlots) == 0 }, "previous work did not finish")
				dir := t.TempDir()
				startedPath, releasePath := filepath.Join(dir, "started.fifo"), filepath.Join(dir, "release.fifo")
				for _, path := range []string{startedPath, releasePath} {
					if err := unix.Mkfifo(path, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				startedPipe, err := os.OpenFile(startedPath, os.O_RDWR, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = startedPipe.Close() })
				releasePipe, err := os.OpenFile(releasePath, os.O_RDWR, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = releasePipe.Close() })
				faultPath := filepath.Join(dir, "fail")
				if failed {
					if err := os.WriteFile(faultPath, nil, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
				ffmpeg := filepath.Join(dir, "ffmpeg")
				script := fmt.Sprintf("#!/bin/sh\nif mkdir %s 2>/dev/null; then\n printf 'started\\n' > %s\n read ignored < %s\nfi\nif test -f %s; then\n printf 'injected seek probe fault\\n' >&2\n exit 7\nfi\nprintf '%%s\\n' '#tb 0: 1/1000' '0, 14500, 14750, 41, 178989, 0xd16e41c4'\n", quote(filepath.Join(dir, "first")), quote(startedPath), quote(releasePath), quote(faultPath))
				if err := os.WriteFile(ffmpeg, []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
				unblock := sync.OnceFunc(func() { _, _ = releasePipe.WriteString("release\n") })
				var releaseSlots func()
				if pendingAdmission {
					for range maxConcurrentCopySeekProbes {
						copySeekProbeSlots <- struct{}{}
					}
					releaseSlots = sync.OnceFunc(func() {
						for range maxConcurrentCopySeekProbes {
							<-copySeekProbeSlots
						}
					})
				} else {
					releaseSlots = func() {}
				}
				t.Cleanup(func() {
					releaseSlots()
					unblock()
					// Join the production singleflight independently of the observer,
					// including when the RED mutation makes it always return zero.
					cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
					defer cleanupCancel()
					_, _, _ = ResolveCopySeekAnchor(cleanupCtx, ffmpeg, "synthetic.mkv", 18, 2)
					awaitCopySeekLifetime(t, func() bool { return CopySeekAnchorsInFlight() == 0 && len(copySeekProbeSlots) == 0 }, "released process retained work")
				})
				started := make(chan error, 1)
				go func() { _, err := bufio.NewReader(startedPipe).ReadString('\n'); started <- err }()
				waitStarted := func() {
					select {
					case err := <-started:
						if err != nil {
							t.Fatal(err)
						}
					case <-time.After(10 * time.Second):
						t.Fatal("seek-anchor child command did not start")
					}
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if pendingAdmission {
					cancel()
				}
				returned := make(chan error, 1)
				go func() {
					_, _, err := ResolveCopySeekAnchor(ctx, ffmpeg, "synthetic.mkv", 18, 2)
					returned <- err
				}()
				if !pendingAdmission {
					waitStarted()
					cancel()
				}
				select {
				case err := <-returned:
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("canceled seek-anchor caller returned %v", err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("canceled seek-anchor caller stayed blocked")
				}
				if got := CopySeekAnchorsInFlight(); got < 1 {
					t.Fatalf("CopySeekAnchorsInFlight() = %d while shared command is queued or running", got)
				}
				if pendingAdmission {
					releaseSlots()
					waitStarted()
				}
				survivor := make(chan error, 1)
				go func() {
					anchor, segment, err := ResolveCopySeekAnchor(t.Context(), ffmpeg, "synthetic.mkv", 18, 2)
					if err == nil && (anchor != 14.75 || segment != 7) {
						err = fmt.Errorf("anchor=%v segment=%d", anchor, segment)
					}
					survivor <- err
				}()
				awaitCopySeekLifetime(t, func() bool { return CopySeekAnchorsInFlight() >= 2 }, "surviving caller was not admitted")
				unblock()
				select {
				case err := <-survivor:
					if failed && (err == nil || !strings.Contains(err.Error(), "injected seek probe fault")) {
						t.Fatalf("faulted shared command returned %v", err)
					}
					if !failed && err != nil {
						t.Fatalf("surviving caller: %v", err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("released shared command stayed blocked")
				}
				awaitCopySeekLifetime(t, func() bool { return CopySeekAnchorsInFlight() == 0 && len(copySeekProbeSlots) == 0 }, "completed command retained work")
				if failed {
					if err := os.Remove(faultPath); err != nil {
						t.Fatal(err)
					}
				}
				anchor, segment, err := ResolveCopySeekAnchor(t.Context(), ffmpeg, "synthetic.mkv", 18, 2)
				if err != nil || anchor != 14.75 || segment != 7 {
					t.Fatalf("follow-up probe anchor=%v segment=%d err=%v", anchor, segment, err)
				}
				awaitCopySeekLifetime(t, func() bool { return CopySeekAnchorsInFlight() == 0 && len(copySeekProbeSlots) == 0 }, "follow-up command retained work")
			})
		}
	}
}
