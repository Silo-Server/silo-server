package playback_test

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

type retryableIdempotencyError interface {
	error
	Retryable() bool
}

func TestIdempotencyStoreBindsTerminationFor24Hours(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	store := playback.NewIdempotencyStore(2, 24*time.Hour)
	store.SetClock(func() time.Time { return now })
	binding := playback.TerminationBinding{ServerID: "server-1", SessionID: "session-1", Generation: "58d591a3-c011-481d-b837-b2c9c8032b9a", Reason: "global_stream_limit"}

	var calls atomic.Int32
	result, replay, err := store.Do("key-1", binding, func() (playback.TerminationResult, error) {
		calls.Add(1)
		return playback.TerminationResult{Status: playback.TerminationStatusTerminated, CommandID: "command-1"}, nil
	})
	if err != nil || replay || result.Status != playback.TerminationStatusTerminated {
		t.Fatalf("first Do = (%+v, replay=%v, err=%v)", result, replay, err)
	}
	result, replay, err = store.Do("key-1", binding, func() (playback.TerminationResult, error) {
		calls.Add(1)
		return playback.TerminationResult{}, errors.New("must not run")
	})
	if err != nil || !replay || result.CommandID != "command-1" || calls.Load() != 1 {
		t.Fatalf("replay Do = (%+v, replay=%v, err=%v), calls=%d", result, replay, err, calls.Load())
	}

	conflict := binding
	conflict.Generation = "7d556533-6ed8-4593-a31e-52c34f0a5cf4"
	if _, _, err := store.Do("key-1", conflict, nil); !errors.Is(err, playback.ErrIdempotencyConflict) {
		t.Fatalf("conflicting Do error = %v, want ErrIdempotencyConflict", err)
	}

	now = now.Add(24 * time.Hour)
	if _, replay, err := store.Do("key-1", conflict, func() (playback.TerminationResult, error) {
		calls.Add(1)
		return playback.TerminationResult{Status: playback.TerminationStatusAlreadyTerminated}, nil
	}); err != nil || replay {
		t.Fatalf("expired key Do replay=%v err=%v", replay, err)
	}
}

func TestIdempotencyStoreConcurrentIdenticalRequestsExecuteOnce(t *testing.T) {
	store := playback.NewIdempotencyStore(8, 24*time.Hour)
	binding := playback.TerminationBinding{ServerID: "server-1", SessionID: "session-1", Generation: "58d591a3-c011-481d-b837-b2c9c8032b9a", Reason: "global_stream_limit"}

	start := make(chan struct{})
	var calls atomic.Int32
	const workers = 24
	var wg sync.WaitGroup
	wg.Add(workers)
	results := make(chan playback.TerminationResult, workers)
	errs := make(chan error, workers)
	for range workers {
		go func() {
			defer wg.Done()
			result, _, err := store.Do("shared-key", binding, func() (playback.TerminationResult, error) {
				calls.Add(1)
				<-start
				return playback.TerminationResult{Status: playback.TerminationStatusTerminated, CommandID: "one-command"}, nil
			})
			results <- result
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
	}
	for result := range results {
		if result.CommandID != "one-command" {
			t.Fatalf("result = %+v", result)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("executions = %d, want 1", got)
	}
}

func TestIdempotencyStorePanicUnblocksWaiterAndAllowsRetry(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	store := playback.NewIdempotencyStore(1, 24*time.Hour)
	waiterEntered := make(chan struct{})
	var clockCalls atomic.Int32
	store.SetClock(func() time.Time {
		if clockCalls.Add(1) == 2 {
			close(waiterEntered)
		}
		return now
	})
	binding := playback.TerminationBinding{ServerID: "server-1", SessionID: "session-1", Generation: "58d591a3-c011-481d-b837-b2c9c8032b9a"}
	ownerStarted := make(chan struct{})
	panicNow := make(chan struct{})
	panicValue := &struct{ message string }{message: "owner panic"}
	recovered := make(chan any, 1)
	go func() {
		defer func() { recovered <- recover() }()
		_, _, _ = store.Do("key-1", binding, func() (playback.TerminationResult, error) {
			close(ownerStarted)
			<-panicNow
			panic(panicValue)
		})
	}()
	<-ownerStarted

	type outcome struct {
		result playback.TerminationResult
		replay bool
		err    error
	}
	waiterDone := make(chan outcome, 1)
	var waiterCalls atomic.Int32
	go func() {
		result, replay, err := store.Do("key-1", binding, func() (playback.TerminationResult, error) {
			waiterCalls.Add(1)
			return playback.TerminationResult{}, errors.New("waiting request must not execute")
		})
		waiterDone <- outcome{result: result, replay: replay, err: err}
	}()
	<-waiterEntered
	close(panicNow)

	if got := <-recovered; got != panicValue {
		t.Fatalf("recovered panic = %#v, want original %#v", got, panicValue)
	}
	select {
	case waiter := <-waiterDone:
		var retryable retryableIdempotencyError
		if !errors.As(waiter.err, &retryable) || !retryable.Retryable() {
			t.Fatalf("waiter error = %T %v, want typed retryable error", waiter.err, waiter.err)
		}
		if waiter.replay || waiter.result != (playback.TerminationResult{}) {
			t.Fatalf("waiter result = %+v, replay=%v", waiter.result, waiter.replay)
		}
	case <-time.After(time.Second):
		t.Fatal("same-key waiter deadlocked after owner panic")
	}
	if got := waiterCalls.Load(); got != 0 {
		t.Fatalf("waiter operation calls = %d, want 0", got)
	}

	want := playback.TerminationResult{Status: playback.TerminationStatusTerminated, CommandID: "retry-command"}
	result, replay, err := store.Do("key-1", binding, func() (playback.TerminationResult, error) { return want, nil })
	if err != nil || replay || result != want {
		t.Fatalf("retry Do = (%+v, replay=%v, err=%v), want fresh success", result, replay, err)
	}
}

func TestIdempotencyStoreRepeatedPanicsReleaseCapacity(t *testing.T) {
	store := playback.NewIdempotencyStore(1, 24*time.Hour)
	binding := playback.TerminationBinding{ServerID: "server-1", SessionID: "session-1", Generation: "58d591a3-c011-481d-b837-b2c9c8032b9a"}
	for attempt := range 5 {
		func() {
			defer func() {
				if got := recover(); got != attempt {
					t.Fatalf("attempt %d recovered panic = %#v", attempt, got)
				}
			}()
			_, _, _ = store.Do("key-1", binding, func() (playback.TerminationResult, error) {
				panic(attempt)
			})
		}()
		if got := store.Len(); got != 0 {
			t.Fatalf("store size after panic %d = %d, want 0", attempt, got)
		}
	}
	if _, replay, err := store.Do("key-1", binding, func() (playback.TerminationResult, error) {
		return playback.TerminationResult{Status: playback.TerminationStatusTerminated}, nil
	}); err != nil || replay {
		t.Fatalf("post-panic success replay=%v err=%v", replay, err)
	}
}

func TestIdempotencyStoreFailedAttemptIsRetryableAndBounded(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	store := playback.NewIdempotencyStore(1, 24*time.Hour)
	store.SetClock(func() time.Time { return now })
	binding := playback.TerminationBinding{ServerID: "server-1", SessionID: "session-1", Generation: "58d591a3-c011-481d-b837-b2c9c8032b9a", Reason: "global_stream_limit"}
	wantErr := errors.New("dispatch failed")
	if _, _, err := store.Do("key-1", binding, func() (playback.TerminationResult, error) { return playback.TerminationResult{}, wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("failed Do error = %v", err)
	}
	if _, replay, err := store.Do("key-1", binding, func() (playback.TerminationResult, error) {
		return playback.TerminationResult{Status: playback.TerminationStatusTerminated}, nil
	}); err != nil || replay {
		t.Fatalf("retry Do replay=%v err=%v", replay, err)
	}
	other := binding
	other.SessionID = "session-2"
	if _, _, err := store.Do("key-2", other, func() (playback.TerminationResult, error) {
		return playback.TerminationResult{Status: playback.TerminationStatusTerminated}, nil
	}); !errors.Is(err, playback.ErrIdempotencyCapacity) {
		t.Fatalf("second unexpired key error = %v, want ErrIdempotencyCapacity", err)
	}
	conflict := binding
	conflict.Reason = "changed"
	if _, _, err := store.Do("key-1", conflict, nil); !errors.Is(err, playback.ErrIdempotencyConflict) {
		t.Fatalf("oldest unexpired binding was lost: %v", err)
	}
	now = now.Add(24 * time.Hour)
	if _, replay, err := store.Do("key-2", other, func() (playback.TerminationResult, error) {
		return playback.TerminationResult{Status: playback.TerminationStatusTerminated}, nil
	}); err != nil || replay {
		t.Fatalf("expired capacity retry replay=%v err=%v", replay, err)
	}
	if got := store.Len(); got != 1 {
		t.Fatalf("store size = %d, want 1", got)
	}
}

func TestIdempotencyStoreRejectsUniqueInflightWorkAtCapacity(t *testing.T) {
	store := playback.NewIdempotencyStore(1, 24*time.Hour)
	binding := playback.TerminationBinding{ServerID: "server-1", SessionID: "session-1", Generation: "58d591a3-c011-481d-b837-b2c9c8032b9a", ReasonCode: "global_stream_limit"}
	started := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, _, err := store.Do("key-1", binding, func() (playback.TerminationResult, error) {
			close(started)
			<-release
			return playback.TerminationResult{Status: playback.TerminationStatusTerminated}, nil
		})
		firstDone <- err
	}()
	<-started
	waiterDone := make(chan struct {
		result playback.TerminationResult
		replay bool
		err    error
	}, 1)
	go func() {
		result, replay, err := store.Do("key-1", binding, func() (playback.TerminationResult, error) {
			return playback.TerminationResult{}, errors.New("same-key waiter executed")
		})
		waiterDone <- struct {
			result playback.TerminationResult
			replay bool
			err    error
		}{result: result, replay: replay, err: err}
	}()

	other := binding
	other.SessionID = "session-2"
	var otherCalls atomic.Int32
	if _, _, err := store.Do("key-2", other, func() (playback.TerminationResult, error) {
		otherCalls.Add(1)
		return playback.TerminationResult{Status: playback.TerminationStatusTerminated}, nil
	}); !errors.Is(err, playback.ErrIdempotencyCapacity) {
		t.Fatalf("second unique key error = %v, want ErrIdempotencyCapacity", err)
	}
	if got := otherCalls.Load(); got != 0 {
		t.Fatalf("second operation calls = %d, want 0", got)
	}
	if got := store.Len(); got != 1 {
		t.Fatalf("store size while first in-flight = %d, want strict cap 1", got)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Do: %v", err)
	}
	waiter := <-waiterDone
	if waiter.err != nil || !waiter.replay || waiter.result.Status != playback.TerminationStatusTerminated {
		t.Fatalf("same-key waiter result=%+v replay=%v err=%v", waiter.result, waiter.replay, waiter.err)
	}
}

func TestIdempotencyStoreBindsAllTerminationBehavior(t *testing.T) {
	store := playback.NewIdempotencyStore(8, 24*time.Hour)
	binding := playback.TerminationBinding{
		ServerID: "server-1", SessionID: "session-1", Generation: "58d591a3-c011-481d-b837-b2c9c8032b9a",
		SnapshotID: "snapshot-1", ReasonCode: "global_stream_limit", Reason: "limit", Title: "Stopped", Message: "Limit reached", DeadlineMS: 3000,
	}
	if _, _, err := store.Do("key", binding, func() (playback.TerminationResult, error) {
		return playback.TerminationResult{Status: playback.TerminationStatusTerminated}, nil
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	mutations := []struct {
		name string
		edit func(*playback.TerminationBinding)
	}{
		{"snapshot", func(b *playback.TerminationBinding) { b.SnapshotID = "snapshot-2" }},
		{"reason code", func(b *playback.TerminationBinding) { b.ReasonCode = "manual_override" }},
		{"reason", func(b *playback.TerminationBinding) { b.Reason = "other" }},
		{"title", func(b *playback.TerminationBinding) { b.Title = "Other" }},
		{"message", func(b *playback.TerminationBinding) { b.Message = "Other" }},
		{"deadline", func(b *playback.TerminationBinding) { b.DeadlineMS = 4000 }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			changed := binding
			mutation.edit(&changed)
			if _, _, err := store.Do("key", changed, nil); !errors.Is(err, playback.ErrIdempotencyConflict) {
				t.Fatalf("changed binding error = %v, want ErrIdempotencyConflict", err)
			}
		})
	}
}
