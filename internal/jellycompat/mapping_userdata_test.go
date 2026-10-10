package jellycompat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestUserDataDTOPlayedReportsZeroPosition(t *testing.T) {
	// Watched rows store position 0, so played items naturally report 0 ticks.
	data := &catalog.SeasonUserData{
		PositionSeconds: 0,
		DurationSeconds: 1290.0,
		Played:          true,
	}
	dto := userDataDTO("item-1", false, data, false, nil)
	if dto.PlaybackPositionTicks != 0 {
		t.Fatalf("PlaybackPositionTicks = %d, want 0 when Played=true", dto.PlaybackPositionTicks)
	}
	if !dto.Played {
		t.Fatalf("Played = false, want true")
	}
	// Jellyfin omits PlayedPercentage once a video is watched; clients that
	// draw a bar for any positive value would show a full bar otherwise.
	if dto.PlayedPercentage != 0 {
		t.Fatalf("PlayedPercentage = %v, want 0 (omitted) for played item at rest", dto.PlayedPercentage)
	}
	raw, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "PlayedPercentage") {
		t.Fatalf("UserData JSON = %s, want no PlayedPercentage for played item at rest", raw)
	}
}

func TestUserDataDTOFullyPlayedFolderReportsHundred(t *testing.T) {
	// A fully watched series or season reports 100, as a Jellyfin folder does.
	// A partly watched one still omits the field.
	data := catalog.SeasonUserDataFromCounts(userstore.SeriesWatchCounts{TotalEpisodes: 10, WatchedCount: 10})
	dto := userDataDTO("series-1", true, data, false, nil)
	if !dto.Played {
		t.Fatalf("Played = false, want true")
	}
	if dto.PlayedPercentage != 100 {
		t.Fatalf("PlayedPercentage = %v, want 100 for fully played series", dto.PlayedPercentage)
	}

	partial := catalog.SeasonUserDataFromCounts(userstore.SeriesWatchCounts{TotalEpisodes: 10, WatchedCount: 4})
	if dto := userDataDTO("series-1", true, partial, false, nil); dto.Played || dto.PlayedPercentage != 0 {
		t.Fatalf("partly watched series: Played = %v, PlayedPercentage = %v, want false and 0", dto.Played, dto.PlayedPercentage)
	}
}

func TestUserDataDTOPlayedFolderWithOwnProgressRowReportsHundred(t *testing.T) {
	// A series can still carry a completed progress row of its own; detail and
	// browse then build its user data from that row instead of an episode
	// rollup. It is still a played folder.
	data := &catalog.SeasonUserData{Played: true}
	progress := &upstreamProgress{MediaItemID: "series-1", Completed: true}
	dto := userDataDTO("series-1", true, data, false, progress)
	if !dto.Played || dto.PlayedPercentage != 100 {
		t.Fatalf("Played = %v, PlayedPercentage = %v, want true and 100", dto.Played, dto.PlayedPercentage)
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
	dto := userDataDTO("item-1", false, data, false, nil)
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
	dto := userDataDTO("item-2", false, data, false, nil)
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
	dto := userDataDTO("item-3", false, data, false, nil)
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
	dto := userDataDTO("item-4", false, nil, false, progress)
	if dto.PlaybackPositionTicks != 0 {
		t.Fatalf("PlaybackPositionTicks = %d, want 0 when Completed=true", dto.PlaybackPositionTicks)
	}
	if !dto.Played {
		t.Fatalf("Played = false, want true")
	}
	if dto.PlayedPercentage != 0 {
		t.Fatalf("PlayedPercentage = %v, want 0 (omitted) for completed item at rest", dto.PlayedPercentage)
	}
	raw, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "PlayedPercentage") {
		t.Fatalf("UserData JSON = %s, want no PlayedPercentage for completed item at rest", raw)
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

	dto := userDataDTO("item-4", false, data, false, progress)
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
	dto := userDataDTO("item-4", false, nil, false, progress)
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
	dto := userDataDTO("item-5", false, nil, false, progress)
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
