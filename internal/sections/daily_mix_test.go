package sections

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func mixPool(n int) []*models.MediaItem {
	pool := make([]*models.MediaItem, n)
	for i := range pool {
		pool[i] = &models.MediaItem{ContentID: fmt.Sprintf("movie:%03d", i)}
	}
	return pool
}

func mixIDs(items []*models.MediaItem) []string {
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.ContentID
	}
	return ids
}

func TestDailyBestOfKeepsASmallPoolWhole(t *testing.T) {
	pool := mixPool(15)
	got := dailyBestOf(pool, 20, "critically_acclaimed", time.Now())
	if !reflect.DeepEqual(mixIDs(got), mixIDs(pool)) {
		t.Fatalf("pool smaller than the limit changed: %v", mixIDs(got))
	}
}

func TestDailyBestOfIsStableForADayAndKeepsRatingOrder(t *testing.T) {
	pool := mixPool(100)
	morning := time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC)
	evening := time.Date(2026, 10, 8, 23, 0, 0, 0, time.UTC)

	first := dailyBestOf(pool, 20, "critically_acclaimed", morning)
	if len(first) != 20 {
		t.Fatalf("got %d items, want 20", len(first))
	}
	if !reflect.DeepEqual(mixIDs(first), mixIDs(dailyBestOf(pool, 20, "critically_acclaimed", evening))) {
		t.Fatal("the mix changed within one UTC day")
	}
	// The pool arrives best first; the mix must keep that order.
	for i := 1; i < len(first); i++ {
		if first[i-1].ContentID >= first[i].ContentID {
			t.Fatalf("mix is out of pool order at %d: %v", i, mixIDs(first))
		}
	}
}

func TestDailyBestOfChangesByDayAndKey(t *testing.T) {
	pool := mixPool(100)
	today := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	ids := mixIDs(dailyBestOf(pool, 20, "critically_acclaimed", today))

	if reflect.DeepEqual(ids, mixIDs(dailyBestOf(pool, 20, "critically_acclaimed", today.AddDate(0, 0, 1)))) {
		t.Error("the next day shows the same mix")
	}
	if reflect.DeepEqual(ids, mixIDs(dailyBestOf(pool, 20, "mood_collection|feel_good", today))) {
		t.Error("two different rows show the same mix")
	}
}

func TestDiscoveryLimitsDefaultsAndPool(t *testing.T) {
	cases := []struct{ itemLimit, limit, pool int }{
		{0, 20, 100},
		{12, 12, 60},
		{100, 100, 250}, // capped
		{300, 300, 300}, // never below the limit
	}
	for _, tc := range cases {
		if limit, pool := discoveryLimits(ResolvedSection{ItemLimit: tc.itemLimit}); limit != tc.limit || pool != tc.pool {
			t.Errorf("item limit %d = (%d, %d), want (%d, %d)", tc.itemLimit, limit, pool, tc.limit, tc.pool)
		}
	}
}

// A shared daily-mix row must not serve yesterday's pick from cache after UTC
// midnight, or two nodes would disagree for the rest of the cache window.
func TestResolvedListKeyCarriesTheDayForDailyMixRows(t *testing.T) {
	day1 := &Fetcher{Clock: fixedClock(time.Date(2026, 10, 8, 23, 59, 0, 0, time.UTC))}
	day2 := &Fetcher{Clock: fixedClock(time.Date(2026, 10, 9, 0, 1, 0, 0, time.UTC))}
	key := func(f *Fetcher, sectionType SectionType) string {
		t.Helper()
		k, err := f.resolvedListKey(context.Background(), ResolvedSection{ID: "row", SectionType: sectionType}, nil, nil, catalog.AccessFilter{})
		if err != nil {
			t.Fatalf("resolvedListKey: %v", err)
		}
		return k
	}
	if key(day1, SectionCriticallyAcclaimed) == key(day2, SectionCriticallyAcclaimed) {
		t.Error("critically acclaimed shares a cache key across UTC days")
	}
	if key(day1, SectionRecentlyAdded) != key(day2, SectionRecentlyAdded) {
		t.Error("recently added gained a day in its cache key")
	}
}
