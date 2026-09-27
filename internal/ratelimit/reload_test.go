package ratelimit

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type sequencedReloadStore struct {
	mapSettingsStore
	reads                               atomic.Int32
	firstRead, secondRead, releaseFirst chan struct{}
}

func (s *sequencedReloadStore) GetAll(context.Context) (map[string]string, error) {
	if s.reads.Add(1) == 1 {
		close(s.firstRead)
		<-s.releaseFirst
		return map[string]string{"ratelimit.global.requests_per_second": "100"}, nil
	}
	close(s.secondRead)
	return map[string]string{"ratelimit.global.requests_per_second": "200"}, nil
}
func TestReloadSerializesSnapshotReadAndApply(t *testing.T) {
	store := &sequencedReloadStore{firstRead: make(chan struct{}), secondRead: make(chan struct{}), releaseFirst: make(chan struct{})}
	mw := NewMiddleware(nil, nil, store, false)
	done := make(chan error, 2)
	go func() { done <- mw.Reload(t.Context()) }()
	<-store.firstRead
	go func() { done <- mw.Reload(t.Context()) }()
	// A second store read must not overtake the held first snapshot. The timeout
	// bounds this negative observation; completion below waits on actual reloads.
	select {
	case <-store.secondRead:
		close(store.releaseFirst)
		<-done
		<-done
		t.Fatal("new snapshot read overtook the previous reload")
	case <-time.After(100 * time.Millisecond):
	}
	close(store.releaseFirst)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if mw.cfg.GlobalReqPerSecond != 200 {
		t.Fatal("older snapshot won", mw.cfg.GlobalReqPerSecond)
	}
}
