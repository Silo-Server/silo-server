package handlers

import (
	"context"
	"testing"
	"time"
)

type stubDownloadsStatsSource struct {
	stats       *AdminDownloadsStats
	err         error
	gotLimit    int
	invalidated int
	callCount   int
}

func (s *stubDownloadsStatsSource) Get(_ context.Context, limit int) (*AdminDownloadsStats, error) {
	s.callCount++
	s.gotLimit = limit
	return s.stats, s.err
}

func (s *stubDownloadsStatsSource) Invalidate() { s.invalidated++ }

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
