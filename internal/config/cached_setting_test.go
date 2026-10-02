package config

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The caching and last-good-value behavior is covered through
// UnratedContentPolicy; these pin the answers before any read succeeds.
func TestCachedSettingDefaultsBeforeASuccessfulRead(t *testing.T) {
	ctx := context.Background()
	parse := func(value string) string { return "parsed:" + value }

	failing := NewCachedSetting(&countingSettingReader{err: errors.New("boom")}, "k", unratedContentCacheTTL, parse)
	if got := failing.Get(ctx); got != "parsed:" {
		t.Fatalf("failed first read = %q, want the parse of an empty value", got)
	}
	if got := NewCachedSetting(nil, "k", unratedContentCacheTTL, parse).Get(ctx); got != "parsed:" {
		t.Fatalf("nil reader = %q, want the parse of an empty value", got)
	}
	var unset *CachedSetting[string]
	if got := unset.Get(ctx); got != "" {
		t.Fatalf("nil setting = %q, want the zero value", got)
	}
}

type hookSettingReader struct{ get func() (string, error) }

func (h hookSettingReader) Get(context.Context, string) (string, error) { return h.get() }

// A read that fails after another caller refreshed the cache answers with that
// newer value, not the snapshot taken before either read.
func TestCachedSettingFailedReadAnswersWithTheLatestCachedValue(t *testing.T) {
	parse := func(value string) string { return value }
	var c *CachedSetting[string]
	c = NewCachedSetting[string](hookSettingReader{get: func() (string, error) {
		// A concurrent caller's successful read lands while this one runs.
		c.mu.Lock()
		c.value, c.read = "fresh", true
		c.mu.Unlock()
		return "", errors.New("boom")
	}}, "k", unratedContentCacheTTL, parse)

	if got := c.Get(context.Background()); got != "fresh" {
		t.Fatalf("Get = %q, want the value the concurrent read cached", got)
	}
}

// A read that started earlier but finished later does not overwrite the value
// a newer read already cached.
func TestCachedSettingKeepsTheNewerOfTwoOverlappingReads(t *testing.T) {
	parse := func(value string) string { return value }
	var c *CachedSetting[string]
	calls := 0
	c = NewCachedSetting[string](hookSettingReader{get: func() (string, error) {
		calls++
		if calls == 1 {
			// While the first (older) read is in flight, a newer read starts
			// and caches the updated setting.
			c.mu.Lock()
			c.expires = time.Time{}
			c.mu.Unlock()
			if got := c.Get(context.Background()); got != "new" {
				t.Fatalf("newer read = %q, want new", got)
			}
			return "old", nil
		}
		return "new", nil
	}}, "k", unratedContentCacheTTL, parse)

	if got := c.Get(context.Background()); got != "new" {
		t.Fatalf("older read answered %q, want the newer cached value", got)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.value != "new" {
		t.Fatalf("cache holds %q, want new", c.value)
	}
}
