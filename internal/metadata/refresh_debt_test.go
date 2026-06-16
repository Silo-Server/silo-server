package metadata

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestRefreshDebtReasonsForItem(t *testing.T) {
	item := &models.MediaItem{
		Status:                    "matched",
		TmdbID:                    "123",
		EpisodeMetadataIncomplete: true,
		RefreshFailures:           2,
	}

	mask := refreshDebtReasonsForItem(item)
	if hasRefreshDebtReason(mask, RefreshDebtReasonEpisodeIncomplete) {
		t.Fatalf("expected episode incomplete reason to stay on episode targets, got mask %d", mask)
	}
	if !hasRefreshDebtReason(mask, RefreshDebtReasonRefreshFailure) {
		t.Fatalf("expected refresh failure reason in mask %d", mask)
	}
	if !hasRefreshDebtReason(mask, RefreshDebtReasonCoreMetadataIncomplete) {
		t.Fatalf("expected core metadata reason in mask %d", mask)
	}
}

func TestRefreshDebtReasonsForItemSkipsUnmatchedFailureOnly(t *testing.T) {
	item := &models.MediaItem{
		Status:          "pending",
		RefreshFailures: 1,
	}

	mask := refreshDebtReasonsForItem(item)
	if mask != 0 {
		t.Fatalf("expected no scheduled refresh debt for unmatched item, got %d", mask)
	}
}

func TestRefreshDebtReasonsForItemFlagsMissingTMDBWithOtherProviderIDs(t *testing.T) {
	item := &models.MediaItem{
		Type:   "series",
		Status: "matched",
		TvdbID: "420105",
		ImdbID: "tt18076310",
		TmdbID: "",
	}

	mask := refreshDebtReasonsForItem(item)
	if !hasRefreshDebtReason(mask, RefreshDebtReasonProviderIDIncomplete) {
		t.Fatalf("reason mask = %d, want provider id incomplete", mask)
	}
}

func TestRefreshDebtReasonsForItemDoesNotFlagProviderIDIncompleteWithoutAlternateIDs(t *testing.T) {
	item := &models.MediaItem{
		Type:   "series",
		Status: "matched",
		TmdbID: "",
	}

	mask := refreshDebtReasonsForItem(item)
	if hasRefreshDebtReason(mask, RefreshDebtReasonProviderIDIncomplete) {
		t.Fatalf("reason mask = %d, did not want provider id incomplete", mask)
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

func TestNextRefreshDelayEpisodeSchedule(t *testing.T) {
	reasonMask := RefreshDebtReasonEpisodeIncomplete
	cases := []struct {
		attempts int
		want     time.Duration
	}{
		// Stepped backoff up to the terminal cap...
		{attempts: 0, want: 24 * time.Hour},
		{attempts: 1, want: 24 * time.Hour},
		{attempts: 2, want: 3 * 24 * time.Hour},
		// ...then we give up: episode-incomplete debt goes terminal at attempt 3+.
		{attempts: 3, want: refreshDebtTerminalDelay},
		{attempts: 4, want: refreshDebtTerminalDelay},
		{attempts: 5, want: refreshDebtTerminalDelay},
	}

	for _, tc := range cases {
		if got := nextRefreshDelay(reasonMask, tc.attempts); got != tc.want {
			t.Fatalf("attempts=%d delay=%s want %s", tc.attempts, got, tc.want)
		}
	}
}

func TestNextRefreshDelayNonEpisodeScheduleUnaffected(t *testing.T) {
	// Non-episode reasons keep the full stepped backoff and never go terminal.
	reasonMask := RefreshDebtReasonCoreMetadataIncomplete
	cases := []struct {
		attempts int
		want     time.Duration
	}{
		{attempts: 2, want: 3 * 24 * time.Hour},
		{attempts: 3, want: 7 * 24 * time.Hour},
		{attempts: 4, want: 14 * 24 * time.Hour},
		{attempts: 5, want: 30 * 24 * time.Hour},
	}
	for _, tc := range cases {
		if got := nextRefreshDelay(reasonMask, tc.attempts); got != tc.want {
			t.Fatalf("attempts=%d delay=%s want %s", tc.attempts, got, tc.want)
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

func TestIsTerminalEpisodeDebt(t *testing.T) {
	if isTerminalEpisodeDebt(RefreshDebtReasonEpisodeIncomplete, refreshDebtEpisodeTerminalAttempts-1) {
		t.Fatalf("debt below the attempt cap must not be terminal")
	}
	if !isTerminalEpisodeDebt(RefreshDebtReasonEpisodeIncomplete, refreshDebtEpisodeTerminalAttempts) {
		t.Fatalf("episode-incomplete debt at the attempt cap must be terminal")
	}
	if isTerminalEpisodeDebt(RefreshDebtReasonCoreMetadataIncomplete, refreshDebtEpisodeTerminalAttempts+5) {
		t.Fatalf("non-episode debt must never be terminal regardless of attempts")
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
