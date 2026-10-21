package metadata

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestRefreshDebtReasonsForItem(t *testing.T) {
	for _, tc := range []struct {
		name          string
		item          models.MediaItem
		want, wantNot []int64
		wantNone      bool
	}{
		{
			name: "matched item with failures and missing core metadata",
			item: models.MediaItem{Status: "matched", TmdbID: "123", EpisodeMetadataIncomplete: true, RefreshFailures: 2},
			// Episode incomplete stays on episode targets.
			want:    []int64{RefreshDebtReasonRefreshFailure, RefreshDebtReasonCoreMetadataIncomplete},
			wantNot: []int64{RefreshDebtReasonEpisodeIncomplete},
		},
		{
			name:     "unmatched item with only a failure owes nothing",
			item:     models.MediaItem{Status: "pending", RefreshFailures: 1},
			wantNone: true,
		},
		{
			name: "missing TMDB beside other provider IDs",
			item: models.MediaItem{Type: "series", Status: "matched", TvdbID: "420105", ImdbID: "tt18076310", TmdbID: ""},
			want: []int64{RefreshDebtReasonProviderIDIncomplete},
		},
		{
			name:    "missing TMDB without alternate IDs",
			item:    models.MediaItem{Type: "series", Status: "matched", TmdbID: ""},
			wantNot: []int64{RefreshDebtReasonProviderIDIncomplete},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mask := refreshDebtReasonsForItem(&tc.item)
			if tc.wantNone && mask != 0 {
				t.Fatalf("reason mask = %d, want none", mask)
			}
			for _, reason := range tc.want {
				if !hasRefreshDebtReason(mask, reason) {
					t.Fatalf("reason mask = %d, want reason %d", mask, reason)
				}
			}
			for _, reason := range tc.wantNot {
				if hasRefreshDebtReason(mask, reason) {
					t.Fatalf("reason mask = %d, did not want reason %d", mask, reason)
				}
			}
		})
	}
}

func TestRefreshDebtReasonMaskValuesAreStable(t *testing.T) {
	tests := map[string]int64{
		"episode_incomplete":       1,
		"stale_provider_id":        2,
		"refresh_failure":          4,
		"core_metadata_incomplete": 8,
		"provider_id_incomplete":   16,
	}
	got := map[string]int64{
		"episode_incomplete":       RefreshDebtReasonEpisodeIncomplete,
		"stale_provider_id":        RefreshDebtReasonStaleProviderID,
		"refresh_failure":          RefreshDebtReasonRefreshFailure,
		"core_metadata_incomplete": RefreshDebtReasonCoreMetadataIncomplete,
		"provider_id_incomplete":   RefreshDebtReasonProviderIDIncomplete,
	}
	for name, want := range tests {
		if got[name] != want {
			t.Fatalf("%s mask = %d, want %d", name, got[name], want)
		}
	}
}

func TestRefreshDebtPriorityProviderIDIncomplete(t *testing.T) {
	if got := refreshDebtPriority(RefreshDebtReasonProviderIDIncomplete); got != 240 {
		t.Fatalf("provider id incomplete priority = %d, want 240", got)
	}
	combined := RefreshDebtReasonProviderIDIncomplete | RefreshDebtReasonStaleProviderID
	if got := refreshDebtPriority(combined); got != 250 {
		t.Fatalf("combined stale/provider priority = %d, want stale priority 250", got)
	}
}

func TestNextRefreshDelaySchedule(t *testing.T) {
	cases := []struct {
		reasonMask int64
		attempts   int
		want       time.Duration
	}{
		// Episode-incomplete debt: stepped backoff up to the terminal cap...
		{RefreshDebtReasonEpisodeIncomplete, 0, 24 * time.Hour},
		{RefreshDebtReasonEpisodeIncomplete, 1, 24 * time.Hour},
		{RefreshDebtReasonEpisodeIncomplete, 2, 3 * 24 * time.Hour},
		// ...then we give up: episode-incomplete debt goes terminal at attempt 3+.
		{RefreshDebtReasonEpisodeIncomplete, 3, refreshDebtTerminalDelay},
		{RefreshDebtReasonEpisodeIncomplete, 4, refreshDebtTerminalDelay},
		{RefreshDebtReasonEpisodeIncomplete, 5, refreshDebtTerminalDelay},
		// Non-episode reasons keep the full stepped backoff and never go terminal.
		{RefreshDebtReasonCoreMetadataIncomplete, 2, 3 * 24 * time.Hour},
		{RefreshDebtReasonCoreMetadataIncomplete, 3, 7 * 24 * time.Hour},
		{RefreshDebtReasonCoreMetadataIncomplete, 4, 14 * 24 * time.Hour},
		{RefreshDebtReasonCoreMetadataIncomplete, 5, 30 * 24 * time.Hour},
	}
	for _, tc := range cases {
		if got := nextRefreshDelay(tc.reasonMask, tc.attempts); got != tc.want {
			t.Fatalf("reasons=%d attempts=%d delay=%s want %s", tc.reasonMask, tc.attempts, got, tc.want)
		}
	}
}

func TestNextRefreshDelayTerminalOnlyWhenPureEpisodeDebt(t *testing.T) {
	// Pure episode-incomplete debt at/over the cap is parked on the rare terminal cadence.
	if got := nextRefreshDelay(RefreshDebtReasonEpisodeIncomplete, refreshDebtEpisodeTerminalAttempts); got != refreshDebtTerminalDelay {
		t.Fatalf("pure terminal delay = %s, want %s", got, refreshDebtTerminalDelay)
	}
	// Episode-incomplete + a still-fixable reason keeps the normal backoff, not 90d, so the
	// fixable reason is not parked for a quarter of a year.
	combined := RefreshDebtReasonEpisodeIncomplete | RefreshDebtReasonRefreshFailure
	if got := nextRefreshDelay(combined, refreshDebtEpisodeTerminalAttempts); got == refreshDebtTerminalDelay {
		t.Fatalf("mixed-reason delay must not use the terminal cadence, got %s", got)
	}
}

func TestEffectiveRefreshDebtPriorityDemotesTerminalEpisodeDebt(t *testing.T) {
	threshold := refreshDebtEpisodeTerminalAttempts

	// Below the cap: pure episode-incomplete debt keeps the top priority band.
	if got := effectiveRefreshDebtPriority(RefreshDebtReasonEpisodeIncomplete, threshold-1); got != 300 {
		t.Fatalf("pre-terminal episode priority = %d, want 300", got)
	}

	// At/over the cap: pure episode-incomplete debt falls to the terminal floor.
	if got := effectiveRefreshDebtPriority(RefreshDebtReasonEpisodeIncomplete, threshold); got != refreshDebtTerminalPriority {
		t.Fatalf("terminal episode priority = %d, want %d", got, refreshDebtTerminalPriority)
	}

	// At/over the cap with a still-fixable reason: falls to that reason's band, not the floor.
	combined := RefreshDebtReasonEpisodeIncomplete | RefreshDebtReasonProviderIDIncomplete
	if got := effectiveRefreshDebtPriority(combined, threshold); got != 240 {
		t.Fatalf("terminal episode+provider priority = %d, want provider band 240", got)
	}

	// Non-episode debt is never demoted.
	if got := effectiveRefreshDebtPriority(RefreshDebtReasonCoreMetadataIncomplete, threshold+5); got != 150 {
		t.Fatalf("core metadata priority = %d, want 150", got)
	}
}
