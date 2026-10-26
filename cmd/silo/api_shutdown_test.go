package main

import (
	"errors"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestAPIShutdownDelay(t *testing.T) {
	for _, test := range []struct {
		value string
		want  time.Duration
	}{
		{"", defaultAPIShutdownDelay},
		{"0", 0},
		{"15", 15 * time.Second},
		{"2s", 2 * time.Second},
		{"-1s", defaultAPIShutdownDelay},
		{"99999999999", defaultAPIShutdownDelay}, // more seconds than a Duration holds
		{"later", defaultAPIShutdownDelay},
	} {
		if got := apiShutdownDelay(test.value); got != test.want {
			t.Errorf("apiShutdownDelay(%q) = %v, want %v", test.value, got, test.want)
		}
	}
}

// apiStopWait runs waitForAPIStop in the background with its inputs exposed.
type apiStopWait struct {
	signals    chan os.Signal
	restarts   chan struct{}
	serverErrs chan error
	routeAway  atomic.Bool
	done       chan struct{}
}

func startAPIStopWait(delay time.Duration) *apiStopWait {
	w := &apiStopWait{
		signals:    make(chan os.Signal, 1),
		restarts:   make(chan struct{}, 1),
		serverErrs: make(chan error, 1),
		done:       make(chan struct{}),
	}
	go func() {
		waitForAPIStop(w.signals, w.restarts, w.serverErrs, delay, func() { w.routeAway.Store(true) })
		close(w.done)
	}()
	return w
}

// signalAndAwaitRouteAway sends the first signal and waits until readiness fails.
func (w *apiStopWait) signalAndAwaitRouteAway(t *testing.T) {
	t.Helper()
	w.signals <- syscall.SIGTERM
	deadline := time.Now().Add(5 * time.Second)
	for !w.routeAway.Load() {
		if time.Now().After(deadline) {
			t.Fatal("readiness did not fail after a signal")
		}
		time.Sleep(time.Millisecond)
	}
}

func (w *apiStopWait) requireWaiting(t *testing.T) {
	t.Helper()
	select {
	case <-w.done:
		t.Fatal("stopped before the delay, a second signal, a restart or a server error")
	default:
	}
}

func (w *apiStopWait) requireStopped(t *testing.T, why string) {
	t.Helper()
	select {
	case <-w.done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not stop the server", why)
	}
}

func TestAPIStopWithoutDelayStopsAtOnce(t *testing.T) {
	w := startAPIStopWait(0)
	w.signalAndAwaitRouteAway(t)
	w.requireStopped(t, "a signal with no delay")
}

func TestAPIStopEndsAfterTheDelay(t *testing.T) {
	w := startAPIStopWait(10 * time.Millisecond)
	w.signalAndAwaitRouteAway(t)
	w.requireStopped(t, "the end of the delay")
}

func TestAPIStopWaitEndsEarly(t *testing.T) {
	for _, test := range []struct {
		name string
		send func(*apiStopWait)
	}{
		{"second signal", func(w *apiStopWait) { w.signals <- syscall.SIGTERM }},
		{"restart request", func(w *apiStopWait) { w.restarts <- struct{}{} }},
		{"server error", func(w *apiStopWait) { w.serverErrs <- errors.New("listener failed") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := startAPIStopWait(time.Hour)
			w.signalAndAwaitRouteAway(t)
			w.requireWaiting(t)
			test.send(w)
			w.requireStopped(t, "a "+test.name+" during the wait")
		})
	}
}

func TestAPIStopWithoutSignalSkipsRouteAway(t *testing.T) {
	for _, test := range []struct {
		name string
		send func(*apiStopWait)
	}{
		{"restart request", func(w *apiStopWait) { w.restarts <- struct{}{} }},
		{"server error", func(w *apiStopWait) { w.serverErrs <- errors.New("listener failed") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := startAPIStopWait(time.Hour)
			test.send(w)
			w.requireStopped(t, "a "+test.name)
			if w.routeAway.Load() {
				t.Fatal("readiness failed without a signal")
			}
		})
	}
}
