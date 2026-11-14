package handlers

import (
	"context"
	"testing"
	"time"
)

func TestAdminDownloadsStatsProviderInvalidateClearsEveryVariant(t *testing.T) {
	t.Parallel()

	provider, err := NewAdminDownloadsStatsProvider(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	t.Cleanup(provider.Close)

	keys := []string{
		adminDownloadsStatsCachePrefix + "10",
		adminDownloadsStatsCachePrefix + "25",
	}
	for _, key := range keys {
		provider.cache.Set(key, &AdminDownloadsStats{}, time.Minute)
	}

	provider.Invalidate()

	for _, key := range keys {
		if _, ok := provider.cache.Get(key); ok {
			t.Fatalf("%s survived Invalidate", key)
		}
	}
}
