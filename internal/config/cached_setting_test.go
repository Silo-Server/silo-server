package config

import (
	"context"
	"errors"
	"testing"
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
