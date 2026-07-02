package downloads

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestValidSubMode(t *testing.T) {
	for _, m := range []string{SubModeAll, SubModeFuture, SubModeLatestSeason, SubModeSpecificSeasons} {
		if !ValidSubMode(m) {
			t.Errorf("ValidSubMode(%q) = false, want true", m)
		}
	}
	if ValidSubMode("bogus") {
		t.Errorf("ValidSubMode(bogus) = true, want false")
	}
}

func TestSubscriptionCoversSeason(t *testing.T) {
	three := 3
	cases := []struct {
		name   string
		sub    Subscription
		season int
		want   bool
	}{
		{"all covers any", Subscription{Mode: SubModeAll}, 1, true},
		{"future covers any season", Subscription{Mode: SubModeFuture}, 7, true},
		{"latest covers target", Subscription{Mode: SubModeLatestSeason, TargetSeason: &three}, 3, true},
		{"latest covers newer season", Subscription{Mode: SubModeLatestSeason, TargetSeason: &three}, 4, true},
		{"latest excludes older season", Subscription{Mode: SubModeLatestSeason, TargetSeason: &three}, 2, false},
		{"latest without target excludes", Subscription{Mode: SubModeLatestSeason}, 3, false},
		{"specific includes listed", Subscription{Mode: SubModeSpecificSeasons, SeasonNumbers: []int{1, 3}}, 3, true},
		{"specific excludes unlisted", Subscription{Mode: SubModeSpecificSeasons, SeasonNumbers: []int{1, 3}}, 2, false},
		{"unknown mode excludes", Subscription{Mode: "bogus"}, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.sub.CoversSeason(tc.season); got != tc.want {
				t.Fatalf("CoversSeason(%d) = %v, want %v", tc.season, got, tc.want)
			}
		})
	}
}

func TestSubscriptionCoversEpisodeFutureCutoff(t *testing.T) {
	subscribedAt := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	before := time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)
	after := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)

	future := &Subscription{Mode: SubModeFuture, CreatedAt: subscribedAt}
	if future.coversEpisode(&models.Episode{SeasonNumber: 1, AirDate: &before}) {
		t.Errorf("future-only must skip an episode that aired before the subscription")
	}
	if !future.coversEpisode(&models.Episode{SeasonNumber: 1, AirDate: &after}) {
		t.Errorf("future-only must cover an episode that aired after the subscription")
	}
	// Same-day boundary: air_date is date-only (midnight), so an episode
	// airing the day the user subscribed must still count as future.
	sameDay := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	if !future.coversEpisode(&models.Episode{SeasonNumber: 1, AirDate: &sameDay}) {
		t.Errorf("future-only must cover an episode airing the same day as the subscription")
	}

	// No air date: fall back to the episode's ingest time.
	if future.coversEpisode(&models.Episode{SeasonNumber: 1, AirDate: nil, CreatedAt: subscribedAt.Add(-time.Hour)}) {
		t.Errorf("future-only must skip a no-air-date episode ingested before the subscription")
	}
	if !future.coversEpisode(&models.Episode{SeasonNumber: 1, AirDate: nil, CreatedAt: subscribedAt.Add(time.Hour)}) {
		t.Errorf("future-only must cover a no-air-date episode ingested after the subscription")
	}

	// 'all' has no air-date cutoff: it covers in-season episodes regardless.
	all := &Subscription{Mode: SubModeAll, CreatedAt: subscribedAt}
	if !all.coversEpisode(&models.Episode{SeasonNumber: 1, AirDate: &before}) {
		t.Errorf("all-mode must cover episodes regardless of air date")
	}
}

func TestSubscriptionAdmits(t *testing.T) {
	unlimited := &Subscription{MaxStorageBytes: 0}
	if !unlimited.Admits(1<<40, 1<<30) {
		t.Errorf("MaxStorageBytes<=0 must admit anything (unlimited)")
	}
	capped := &Subscription{MaxStorageBytes: 100}
	if !capped.Admits(60, 40) { // 100 <= 100
		t.Errorf("must admit when used+add equals the cap")
	}
	if capped.Admits(61, 40) { // 101 > 100
		t.Errorf("must reject when used+add exceeds the cap")
	}
}
