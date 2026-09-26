package jobrunner

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

type terminalStore struct {
	Store
	failure error
}

func (s terminalStore) Heartbeat(context.Context, int64) error { return s.failure }

func TestHeartbeatTerminalSignal(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failure  error
		terminal bool
	}{
		{"active", nil, false},
		{"transient", errors.New("database unavailable"), false},
		{"terminal", ErrJobTerminal, true},
		{"wrapped terminal", fmt.Errorf("heartbeat: %w", ErrJobTerminal), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			r := New(t.Context(), nil, terminalStore{failure: tc.failure}, "test", nil)
			if keep := r.refreshHeartbeat(ctx, 42, cancel); keep == tc.terminal {
				t.Fatalf("continue=%v, terminal=%v", keep, tc.terminal)
			}
			if stopped := ctx.Err() != nil; stopped != tc.terminal {
				t.Fatalf("stopped=%v, terminal=%v", stopped, tc.terminal)
			}
		})
	}
}

func TestTerminalHeartbeatStopsQueuedAndRunningWork(t *testing.T) {
	for _, queued := range []bool{false, true} {
		name := "running"
		if queued {
			name = "queued"
		}
		t.Run(name, func(t *testing.T) {
			sem := NewSemaphore(1)
			if queued {
				sem <- struct{}{}
			}
			r := New(t.Context(), sem, terminalStore{failure: ErrJobTerminal}, "test", nil)
			started := make(chan struct{})
			done := make(chan bool, 1)
			r.Dispatch(42, func(ctx context.Context) { close(started); <-ctx.Done(); done <- true }, func(context.Context) { done <- false })
			if !queued {
				<-started
			}
			r.mu.Lock()
			cancel := r.cancels[42]
			r.mu.Unlock()
			if r.refreshHeartbeat(t.Context(), 42, cancel) {
				t.Fatal("terminal heartbeat continued")
			}
			if ran := <-done; ran == queued {
				t.Fatalf("ran=%v, queued=%v", ran, queued)
			}
			r.wg.Wait()
		})
	}
}
