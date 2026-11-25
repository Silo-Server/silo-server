package handlers

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
)

// TestCachedInsightsReadCancelsAFillNobodyWaitsFor pins that a shared fill
// stops once every request waiting on it has left, and keeps running while
// one still waits. An abandoned fill would otherwise hold a read-gate slot,
// or its place in the queue for one, for the full read timeout.
func TestCachedInsightsReadCancelsAFillNobodyWaitsFor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &AdminInsightsProvider{ttl: time.Minute}
		c := cache.NewTTLCache[*int]()
		defer c.Close()

		fillErr := make(chan error, 1)
		load := func(ctx context.Context) (*int, error) {
			<-ctx.Done()
			fillErr <- ctx.Err()
			return nil, ctx.Err()
		}

		// Two requests share one fill; the first leaves while the second waits.
		leaving, leave := context.WithCancel(context.Background())
		staying, stay := context.WithCancel(context.Background())
		defer stay()
		first := make(chan error, 1)
		go func() { _, err := cachedInsightsRead(leaving, p, c, "k", load); first <- err }()
		synctest.Wait()
		second := make(chan error, 1)
		go func() {
			_, err := cachedInsightsRead(staying, p, c, "k", func(context.Context) (*int, error) {
				t.Error("second request started its own fill instead of joining the first")
				return nil, nil
			})
			second <- err
		}()
		synctest.Wait()

		leave()
		if err := <-first; !errors.Is(err, context.Canceled) {
			t.Fatalf("first request: err = %v, want context.Canceled", err)
		}
		synctest.Wait()
		select {
		case err := <-fillErr:
			t.Fatalf("fill ended (%v) while a request still waited on it", err)
		default:
		}

		// The last waiter leaving cancels the fill.
		stay()
		if err := <-second; !errors.Is(err, context.Canceled) {
			t.Fatalf("second request: err = %v, want context.Canceled", err)
		}
		synctest.Wait()
		select {
		case err := <-fillErr:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("fill ended with %v, want its context canceled", err)
			}
		default:
			t.Fatal("fill kept running after every waiter left")
		}
		if _, ok := c.Get("k"); ok {
			t.Fatal("canceled fill published a cache entry")
		}
	})
}
