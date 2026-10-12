package main

import (
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

func TestRouteAwayFailsReadinessFirst(t *testing.T) {
	var shuttingDown atomic.Bool
	routeAwayBeforeShutdown(&shuttingDown, 0, make(chan os.Signal))
	if !shuttingDown.Load() {
		t.Fatal("readiness still passes after shutdown began")
	}
}

func TestRouteAwayWaitsUntilASecondSignal(t *testing.T) {
	var shuttingDown atomic.Bool
	sigCh := make(chan os.Signal, 1)
	done := make(chan struct{})
	go func() {
		routeAwayBeforeShutdown(&shuttingDown, time.Hour, sigCh)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !shuttingDown.Load() {
		if time.Now().After(deadline) {
			t.Fatal("readiness did not fail")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-done:
		t.Fatal("returned before the delay or a second signal")
	default:
	}
	sigCh <- syscall.SIGTERM
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a second signal did not end the wait")
	}
}

func TestRouteAwayEndsAfterTheDelay(t *testing.T) {
	var shuttingDown atomic.Bool
	done := make(chan struct{})
	go func() {
		routeAwayBeforeShutdown(&shuttingDown, 10*time.Millisecond, make(chan os.Signal))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the wait did not end after its delay")
	}
	if !shuttingDown.Load() {
		t.Fatal("readiness still passes after the wait")
	}
}
