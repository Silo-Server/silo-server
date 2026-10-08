package tonemap

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func awaitNoPreflightsInFlight(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if PreflightsInFlight() == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("PreflightsInFlight() = %d after completing the shared work", PreflightsInFlight())
		}
		runtime.Gosched()
	}
}

func TestPreflightLifetimeSurvivesCanceledCaller(t *testing.T) {
	for _, command := range []string{"source", "version"} {
		for _, failed := range []bool{false, true} {
			name := command + "/success"
			if failed {
				name = command + "/command failure"
			}
			t.Run(name, func(t *testing.T) {
				awaitNoPreflightsInFlight(t)
				resetSourcePreflightCache(t)
				t.Setenv("TMPDIR", t.TempDir())
				request := sourcePreflightTestRequest(t)
				started, release := make(chan struct{}), make(chan struct{})
				unblock := sync.OnceFunc(func() { close(release) })
				var announce sync.Once
				var blockedCalls, conversions atomic.Int32
				var injectFailure atomic.Bool
				injectFailure.Store(failed)
				outputPath := make(chan string, 1)
				runner := func(ctx context.Context, name string, args ...string) ([]byte, error) {
					isVersion := len(args) == 1 && args[0] == "-version"
					isConversion := !isVersion && !strings.Contains(name, "ffprobe")
					if command == "version" && isVersion || command == "source" && isConversion {
						blockedCalls.Add(1)
						announce.Do(func() {
							if isConversion {
								path := args[len(args)-1]
								if err := os.WriteFile(path, []byte("temporary conversion"), 0o600); err != nil {
									outputPath <- ""
								} else {
									outputPath <- path
								}
							}
							close(started)
						})
						select {
						case <-release:
						case <-ctx.Done():
							return nil, ctx.Err()
						}
						if injectFailure.Load() {
							return nil, errors.New("injected FFmpeg command failure")
						}
					}
					if isVersion {
						return []byte("ffmpeg version lifetime test"), nil
					}
					if strings.Contains(name, "ffprobe") {
						if !strings.Contains(strings.Join(args, " "), "stream=codec_name") {
							return []byte(`{"frames":[{"color_range":"tv","color_space":"bt2020nc","color_transfer":"smpte2084","color_primaries":"bt2020"}]}`), nil
						}
						return []byte(`{"streams":[{"codec_name":"h264","pix_fmt":"yuv420p","color_range":"tv","color_space":"bt709","color_transfer":"bt709","color_primaries":"bt709","side_data_list":[]}],"frames":[{"color_range":"tv","color_space":"bt709","color_transfer":"bt709","color_primaries":"bt709","side_data_list":[]}]}`), nil
					}
					conversions.Add(1)
					return nil, nil
				}
				t.Cleanup(func() {
					unblock()
					// Join the real cache task even when the counter itself is the
					// failing RED oracle; no background fixture may outlive cleanup.
					cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
					defer cleanupCancel()
					_ = ValidateSourceWithRunner(cleanupCtx, request, runner)
					awaitNoPreflightsInFlight(t)
				})
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan error, 1)
				go func() { done <- ValidateSourceWithRunner(ctx, request, runner) }()
				select {
				case <-started:
				case <-time.After(10 * time.Second):
					t.Fatal("source validation did not start its FFmpeg command")
				}
				cancel()
				select {
				case err := <-done:
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("canceled caller returned %v", err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("canceled source validation kept its request handler blocked")
				}
				if got := PreflightsInFlight(); got < 1 {
					t.Fatalf("PreflightsInFlight() = %d while a detached %s command is still running", got, command)
				}
				unblock()
				awaitNoPreflightsInFlight(t)
				if command == "source" {
					path := <-outputPath
					if path == "" {
						t.Fatal("command fixture did not write its temporary output")
					}
					if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("source preflight did not remove temporary output: %v", err)
					}
				}
				// An operational fault must release the claim and permit retry.
				// A successful shared task must be reused rather than run again.
				before := blockedCalls.Load()
				injectFailure.Store(false)
				if err := ValidateSourceWithRunner(t.Context(), request, runner); err != nil {
					t.Fatalf("source validation after completing detached command: %v", err)
				}
				awaitNoPreflightsInFlight(t)
				if failed && blockedCalls.Load() <= before {
					t.Fatal("operational failure was incorrectly cached")
				}
				if !failed && blockedCalls.Load() != before {
					t.Fatal("successful shared result was not reused")
				}
			})
		}
	}
}
