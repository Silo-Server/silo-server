package jellycompat

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemorySessionExpiredGetPreservesConcurrentReplacement(t *testing.T) {
	for _, replacement := range []string{"live", "expired", "deleted"} {
		t.Run(replacement, func(t *testing.T) {
			now := time.Now()
			entered, proceed := make(chan struct{}), make(chan struct{})
			var armed atomic.Bool
			release := sync.OnceFunc(func() { close(proceed) })
			defer release()
			state := NewPlaybackSessionStore(time.Hour, func() time.Time {
				// Get checks time after copying the row and releasing its read
				// lock. Pause that exact observation while a real writer wins.
				if armed.CompareAndSwap(true, false) {
					close(entered)
					<-proceed
				}
				return now
			})
			state.Put(PlaybackSession{ID: "same", ClientName: "expired", ExpiresAt: now.Add(-time.Second)})
			armed.Store(true)
			result := make(chan bool, 1)
			go func() {
				_, ok := state.Get("same")
				result <- ok
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("Get did not capture old row")
			}
			if replacement == "deleted" {
				state.Delete("same")
			} else {
				expires := now.Add(time.Hour)
				if replacement == "expired" {
					expires = now.Add(-time.Millisecond)
				}
				state.Put(PlaybackSession{ID: "same", ClientName: "replacement", ExpiresAt: expires})
			}
			release()
			select {
			case ok := <-result:
				if ok {
					t.Fatal("Get that observed expiry must still return a miss")
				}
			case <-time.After(time.Second):
				t.Fatal("Get did not finish")
			}
			state.mu.RLock()
			row, exists := state.sessions["same"]
			state.mu.RUnlock()
			if replacement == "live" {
				if !exists || row.ClientName != "replacement" {
					t.Fatal("stale expired Get deleted live replacement")
				}
			} else if exists {
				t.Fatal("expired or deleted row survived eviction")
			}
			assertMemoryGatesReleased(t, state)
		})
	}
}
