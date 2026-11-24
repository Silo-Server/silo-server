package config

import (
	"context"
	"errors"
	"testing"
	"time"
)

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
	}}, "k", UnratedContentCacheTTL, parse)

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
	}}, "k", UnratedContentCacheTTL, parse)

	if got := c.Get(context.Background()); got != "new" {
		t.Fatalf("older read answered %q, want the newer cached value", got)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.value != "new" {
		t.Fatalf("cache holds %q, want new", c.value)
	}
}

// Until a read succeeds, Lookup says its answer is only the parse of "", so a
// caller can tell a default from an administrator's choice; a stored value,
// including an unset one, is the setting.
func TestCachedSettingLookupReportsWhetherASettingWasRead(t *testing.T) {
	parse := func(value string) string { return "parsed:" + value }
	fail := true
	c := NewCachedSetting[string](hookSettingReader{get: func() (string, error) {
		if fail {
			return "", errors.New("boom")
		}
		return "", nil
	}}, "k", UnratedContentCacheTTL, parse)

	if got, ok := c.Lookup(context.Background()); ok || got != "parsed:" {
		t.Fatalf("Lookup after a failed read = (%q, %v), want (parsed:, false)", got, ok)
	}
	fail = false
	if got, ok := c.Lookup(context.Background()); !ok || got != "parsed:" {
		t.Fatalf("Lookup of an unset setting = (%q, %v), want (parsed:, true)", got, ok)
	}
	fail = true
	c.mu.Lock()
	c.expires = time.Time{}
	c.mu.Unlock()
	if _, ok := c.Lookup(context.Background()); !ok {
		t.Fatal("Lookup after a later failed read lost the setting it had read")
	}
}
