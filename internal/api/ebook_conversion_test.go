package api

import (
	"context"
	"sync/atomic"
	"testing"
)

// stubSettings counts Get calls so we can assert the flag predicate caches.
type stubSettings struct {
	value string
	gets  int64
}

func (s *stubSettings) Get(_ context.Context, _ string) (string, error) {
	atomic.AddInt64(&s.gets, 1)
	return s.value, nil
}
func (s *stubSettings) Set(context.Context, string, string) error { return nil }
func (s *stubSettings) GetAll(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}

// The flag predicate must not hit the settings store on every call: the read
// path (every ebook read + capability poll) would otherwise issue a DB query
// per request.
func TestEbookFlagPredicate_CachesWithinTTL(t *testing.T) {
	s := &stubSettings{value: "true"}
	pred := ebookFlagPredicate(s, "ebook.kindle_conversion_enabled")

	for i := 0; i < 50; i++ {
		if !pred(context.Background()) {
			t.Fatalf("call %d: predicate returned false for a truthy flag", i)
		}
	}
	if got := atomic.LoadInt64(&s.gets); got > 2 {
		t.Fatalf("settings.Get called %d times across 50 predicate calls; expected it to be cached", got)
	}
}
