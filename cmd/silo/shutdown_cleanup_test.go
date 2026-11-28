package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

// A request that never returns makes the HTTP drain use its whole deadline;
// the cleanup steps after it must still get a live context of their own.
func TestShutdownCleanupStepsRunAfterHTTPDrainDeadline(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(started)
		<-release // never returns on its own
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { close(release); _ = srv.Close() })

	go func() {
		resp, err := http.Get("http://" + ln.Addr().String())
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-started

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("HTTP shutdown error = %v, want deadline exceeded", err)
	}

	var ran []string
	step := func(name string) shutdownCleanupStep {
		return shutdownCleanupStep{logErr: func(error) {}, run: func(ctx context.Context) error {
			if err := ctx.Err(); err != nil {
				t.Errorf("%s got a finished context: %v", name, err)
			}
			if _, ok := ctx.Deadline(); !ok {
				t.Errorf("%s got no deadline", name)
			}
			ran = append(ran, name)
			return nil
		}}
	}
	slow := shutdownCleanupStep{logErr: func(error) {}, run: func(ctx context.Context) error {
		<-ctx.Done()
		ran = append(ran, "slow")
		return ctx.Err()
	}}
	runShutdownCleanupSteps(100*time.Millisecond, []shutdownCleanupStep{
		step("stream telemetry shutdown"), slow, step("stale session cleanup"), step("heartbeat cleanup"),
	})

	want := []string{"stream telemetry shutdown", "slow", "stale session cleanup", "heartbeat cleanup"}
	if len(ran) != len(want) {
		t.Fatalf("ran %v, want %v", ran, want)
	}
	for i := range want {
		if ran[i] != want[i] {
			t.Fatalf("ran %v, want %v", ran, want)
		}
	}
}
