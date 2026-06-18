package jellycompat

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

func TestUserDataDTOPlayedReportsZeroPosition(t *testing.T) {
	// Watched rows store position 0, so played items naturally report 0 ticks.
	data := &catalog.SeasonUserData{
		PositionSeconds: 0,
		DurationSeconds: 1290.0,
		Played:          true,
	}
	dto := userDataDTO("item-1", data, false, nil)
	if dto.PlaybackPositionTicks != 0 {
		t.Fatalf("PlaybackPositionTicks = %d, want 0 when Played=true", dto.PlaybackPositionTicks)
	}
	if !dto.Played {
		t.Fatalf("Played = false, want true")
	}
	if dto.PlayedPercentage != 100 {
		t.Fatalf("PlayedPercentage = %v, want 100 for played item at rest", dto.PlayedPercentage)
	}
}

func TestUserDataDTOPlayedRewatchReportsResumePosition(t *testing.T) {
	// A rewatch in flight keeps Played=true with a live resume point; clients
	// must see both the checkmark and the position (matches Jellyfin).
	data := &catalog.SeasonUserData{
		PositionSeconds: 600.0,
		DurationSeconds: 1290.0,
		Played:          true,
	}
	dto := userDataDTO("item-1", data, false, nil)
	want := secondsToTicks(600.0)
	if dto.PlaybackPositionTicks != want {
		t.Fatalf("PlaybackPositionTicks = %d, want %d for rewatch in flight", dto.PlaybackPositionTicks, want)
	}
	if !dto.Played {
		t.Fatalf("Played = false, want true")
	}
}

func TestUserDataDTOClampsPositionPastDuration(t *testing.T) {
	data := &catalog.SeasonUserData{
		PositionSeconds: 1290.33,
		DurationSeconds: 1290.0,
		Played:          false,
	}
	dto := userDataDTO("item-2", data, false, nil)
	want := secondsToTicks(1290.0)
	if dto.PlaybackPositionTicks != want {
		t.Fatalf("PlaybackPositionTicks = %d, want %d (clamped to duration)", dto.PlaybackPositionTicks, want)
	}
	if dto.PlayedPercentage > 100 {
		t.Fatalf("PlayedPercentage = %v, want <= 100 (derived from the clamped position)", dto.PlayedPercentage)
	}
}

func TestUserDataDTOPreservesValidPosition(t *testing.T) {
	data := &catalog.SeasonUserData{
		PositionSeconds: 600.0,
		DurationSeconds: 1290.0,
		Played:          false,
	}
	dto := userDataDTO("item-3", data, false, nil)
	want := secondsToTicks(600.0)
	if dto.PlaybackPositionTicks != want {
		t.Fatalf("PlaybackPositionTicks = %d, want %d", dto.PlaybackPositionTicks, want)
	}
}

func TestUserDataDTOProgressCompletedZeros(t *testing.T) {
	// Completed rows store position 0, so watched items report 0 ticks.
	progress := &upstreamProgress{
		MediaItemID:     "x",
		PositionSeconds: 0,
		DurationSeconds: 1290.0,
		Completed:       true,
	}
	dto := userDataDTO("item-4", nil, false, progress)
	if dto.PlaybackPositionTicks != 0 {
		t.Fatalf("PlaybackPositionTicks = %d, want 0 when Completed=true", dto.PlaybackPositionTicks)
	}
	if !dto.Played {
		t.Fatalf("Played = false, want true")
	}
	if dto.PlayedPercentage != 100 {
		t.Fatalf("PlayedPercentage = %v, want 100 for completed item at rest", dto.PlayedPercentage)
	}
}

func TestUserDataDTOProgressDoesNotClearPlayedData(t *testing.T) {
	data := &catalog.SeasonUserData{Played: true}
	progress := &upstreamProgress{
		MediaItemID:     "x",
		PositionSeconds: 600.0,
		DurationSeconds: 1290.0,
		Completed:       false,
	}

	dto := userDataDTO("item-4", data, false, progress)
	if !dto.Played {
		t.Fatalf("Played = false, want aggregate played state preserved")
	}
	if dto.PlayCount != 1 {
		t.Fatalf("PlayCount = %d, want watched count preserved", dto.PlayCount)
	}
}

func TestUserDataDTOProgressRewatchKeepsPlayedAndPosition(t *testing.T) {
	progress := &upstreamProgress{
		MediaItemID:     "x",
		PositionSeconds: 600.0,
		DurationSeconds: 1290.0,
		Completed:       true,
	}
	dto := userDataDTO("item-4", nil, false, progress)
	want := secondsToTicks(600.0)
	if dto.PlaybackPositionTicks != want {
		t.Fatalf("PlaybackPositionTicks = %d, want %d for rewatch in flight", dto.PlaybackPositionTicks, want)
	}
	if !dto.Played {
		t.Fatalf("Played = false, want true")
	}
	if dto.PlayCount != 1 {
		t.Fatalf("PlayCount = %d, want 1 (watched state survives rewatch)", dto.PlayCount)
	}
	wantPct := (600.0 / 1290.0) * 100
	if dto.PlayedPercentage != wantPct {
		t.Fatalf("PlayedPercentage = %v, want %v (live rewatch fraction)", dto.PlayedPercentage, wantPct)
	}
}

func TestUserDataDTOProgressClampsPosition(t *testing.T) {
	progress := &upstreamProgress{
		MediaItemID:     "x",
		PositionSeconds: 2000.0,
		DurationSeconds: 1290.0,
		Completed:       false,
	}
	dto := userDataDTO("item-5", nil, false, progress)
	want := secondsToTicks(1290.0)
	if dto.PlaybackPositionTicks != want {
		t.Fatalf("PlaybackPositionTicks = %d, want %d (clamped)", dto.PlaybackPositionTicks, want)
	}
}

func TestClampSeekSecondsCapsToLongestSource(t *testing.T) {
	sources := []PlaybackMediaSource{
		{Version: catalog.FileVersion{Duration: 1290}},
		{Version: catalog.FileVersion{Duration: 1500}},
	}
	got := clampSeekSeconds(2000, sources)
	if got != 1500 {
		t.Fatalf("clampSeekSeconds = %v, want 1500", got)
	}
}

func TestClampSeekSecondsPassesValidSeek(t *testing.T) {
	sources := []PlaybackMediaSource{
		{Version: catalog.FileVersion{Duration: 1290}},
	}
	got := clampSeekSeconds(600, sources)
	if got != 600 {
		t.Fatalf("clampSeekSeconds = %v, want 600", got)
	}
}

func TestClampSeekSecondsHandlesNegative(t *testing.T) {
	got := clampSeekSeconds(-5, []PlaybackMediaSource{{Version: catalog.FileVersion{Duration: 100}}})
	if got != 0 {
		t.Fatalf("clampSeekSeconds = %v, want 0", got)
	}
}

func TestClampSeekSecondsNoDurationLeavesValue(t *testing.T) {
	got := clampSeekSeconds(42, []PlaybackMediaSource{{Version: catalog.FileVersion{Duration: 0}}})
	if got != 42 {
		t.Fatalf("clampSeekSeconds = %v, want 42", got)
	}
}
