package jellycompat

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestRuntimeTicks(t *testing.T) {
	tests := []struct {
		name            string
		durationSeconds int
		runtimeMinutes  int
		want            int64
	}{
		{name: "probe only", durationSeconds: 3600, want: secondsToTicks(3600)},
		{name: "runtime only", runtimeMinutes: 60, want: minutesToTicks(60)},
		{name: "neither", want: 0},
		{name: "probe wins", durationSeconds: 120, runtimeMinutes: 90, want: secondsToTicks(120)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runtimeTicks(tt.durationSeconds, tt.runtimeMinutes); got != tt.want {
				t.Fatalf("runtimeTicks(%d, %d) = %d, want %d", tt.durationSeconds, tt.runtimeMinutes, got, tt.want)
			}
		})
	}

	if secondsToTicks(3600) != minutesToTicks(60) {
		t.Fatalf("unit mismatch: 3600 seconds = %d ticks, 60 minutes = %d ticks", secondsToTicks(3600), minutesToTicks(60))
	}
}

func TestItemFromListRuntimeResolution(t *testing.T) {
	m := newMapper(NewResourceIDCodec(), &config.Config{})
	dto := m.itemFromList(upstreamListItem{
		ContentID:       "movie-1",
		Type:            "movie",
		Title:           "Movie",
		DurationSeconds: 5400,
	}, false, nil, nil)
	if dto.RunTimeTicks != secondsToTicks(5400) {
		t.Fatalf("RunTimeTicks = %d, want %d", dto.RunTimeTicks, secondsToTicks(5400))
	}

	unknown := m.itemFromList(upstreamListItem{ContentID: "movie-2", Type: "movie", Title: "Unknown"}, false, nil, nil)
	raw, err := json.Marshal(unknown)
	if err != nil {
		t.Fatalf("marshal dto: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal dto: %v", err)
	}
	if _, ok := fields["RunTimeTicks"]; ok {
		t.Fatalf("RunTimeTicks should be absent when neither duration is known: %s", raw)
	}
}

func TestEpisodeFromUpstreamRuntimeResolution(t *testing.T) {
	m := newMapper(NewResourceIDCodec(), &config.Config{})
	tests := []struct {
		name            string
		durationSeconds int
		runtimeMinutes  int
		want            int64
	}{
		{name: "probe only", durationSeconds: 1800, want: secondsToTicks(1800)},
		{name: "runtime only", runtimeMinutes: 30, want: minutesToTicks(30)},
		{name: "neither", want: 0},
		{name: "probe wins", durationSeconds: 1800, runtimeMinutes: 45, want: secondsToTicks(1800)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dto := m.episodeFromUpstream(upstreamEpisode{
				ContentID:       "episode-1",
				Title:           "Episode",
				Runtime:         tt.runtimeMinutes,
				DurationSeconds: tt.durationSeconds,
			}, false, nil)
			if dto.RunTimeTicks != tt.want {
				t.Fatalf("RunTimeTicks = %d, want %d", dto.RunTimeTicks, tt.want)
			}
		})
	}
}

type countingProbedDurationSource struct {
	contentCalls int
	episodeCalls int
	contentIDs   []string
	episodeIDs   []string
	content      map[string]int
	episodes     map[string]int
}

func (s *countingProbedDurationSource) ProbedDurationsByContentIDs(_ context.Context, ids []string) map[string]int {
	s.contentCalls++
	s.contentIDs = append([]string(nil), ids...)
	return s.content
}

func (s *countingProbedDurationSource) ProbedDurationsByEpisodeIDs(_ context.Context, ids []string) map[string]int {
	s.episodeCalls++
	s.episodeIDs = append([]string(nil), ids...)
	return s.episodes
}

func TestFillListItemDurationsBucketsAndDeduplicates(t *testing.T) {
	src := &countingProbedDurationSource{
		content:  map[string]int{"movie-1": 600},
		episodes: map[string]int{"episode-1": 1200},
	}
	items := []upstreamListItem{
		{ContentID: "movie-1", Type: "movie"},
		{ContentID: "movie-1", Type: "MOVIE"},
		{ContentID: "episode-1", Type: "episode"},
		{ContentID: "episode-1", Type: "Episode"},
		{ContentID: "series-1", Type: "series"},
		{ContentID: "season-1", Type: "season"},
	}

	fillListItemDurations(context.Background(), src, items)

	if src.contentCalls != 1 || !reflect.DeepEqual(src.contentIDs, []string{"movie-1"}) {
		t.Fatalf("content calls/ids = %d/%v, want 1/[movie-1]", src.contentCalls, src.contentIDs)
	}
	if src.episodeCalls != 1 || !reflect.DeepEqual(src.episodeIDs, []string{"episode-1"}) {
		t.Fatalf("episode calls/ids = %d/%v, want 1/[episode-1]", src.episodeCalls, src.episodeIDs)
	}
	if items[0].DurationSeconds != 600 || items[1].DurationSeconds != 600 {
		t.Fatalf("movie durations = %d/%d, want 600/600", items[0].DurationSeconds, items[1].DurationSeconds)
	}
	if items[2].DurationSeconds != 1200 || items[3].DurationSeconds != 1200 {
		t.Fatalf("episode durations = %d/%d, want 1200/1200", items[2].DurationSeconds, items[3].DurationSeconds)
	}
	if items[4].DurationSeconds != 0 || items[5].DurationSeconds != 0 {
		t.Fatalf("non-playable durations changed: %d/%d", items[4].DurationSeconds, items[5].DurationSeconds)
	}
}

func TestFillListItemDurationsSkipsEmptyBucketsAndNilSource(t *testing.T) {
	src := &countingProbedDurationSource{content: nil}
	items := []upstreamListItem{{ContentID: "movie-1", Type: "movie", Runtime: 90}}
	fillListItemDurations(context.Background(), src, items)
	if src.contentCalls != 1 || src.episodeCalls != 0 {
		t.Fatalf("content/episode calls = %d/%d, want 1/0", src.contentCalls, src.episodeCalls)
	}
	if items[0].DurationSeconds != 0 {
		t.Fatalf("DurationSeconds = %d, want 0 after nil result", items[0].DurationSeconds)
	}
	dto := newMapper(NewResourceIDCodec(), &config.Config{}).itemFromList(items[0], false, nil, nil)
	if dto.RunTimeTicks != minutesToTicks(90) {
		t.Fatalf("nil probe result did not fall back to catalog runtime: %d", dto.RunTimeTicks)
	}

	before := append([]upstreamListItem(nil), items...)
	fillListItemDurations(context.Background(), nil, items)
	if !reflect.DeepEqual(items, before) {
		t.Fatalf("nil source mutated items: got %+v, want %+v", items, before)
	}
}

// A failed lookup returns a nil map. That must not wipe a duration an earlier
// producer already resolved, otherwise a transient DB error would downgrade an
// item that was already correct.
func TestFillListItemDurationsKeepsResolvedDurationOnEmptyResult(t *testing.T) {
	src := &countingProbedDurationSource{content: nil, episodes: nil}
	items := []upstreamListItem{
		{ContentID: "movie-1", Type: "movie", DurationSeconds: 5400},
		{ContentID: "episode-1", Type: "episode", DurationSeconds: 1500},
	}

	fillListItemDurations(context.Background(), src, items)

	if items[0].DurationSeconds != 5400 || items[1].DurationSeconds != 1500 {
		t.Fatalf("durations = %d/%d, want 5400/1500 preserved", items[0].DurationSeconds, items[1].DurationSeconds)
	}
}

func TestWriteEpisodeModelsPageUsesProbedTargetDuration(t *testing.T) {
	codec := NewResourceIDCodec()
	seriesID := "series-1"
	seasonID := "season-1"
	episodeID := "episode-1"
	episodeRepo := &fakeSeasonEpisodeRepo{bySeason: map[string][]*models.Episode{
		episodeBySeasonKey(seriesID, 1): {
			{
				ContentID:     episodeID,
				SeriesID:      seriesID,
				SeasonID:      seasonID,
				SeasonNumber:  1,
				EpisodeNumber: 1,
				Title:         "Episode",
				Runtime:       0,
			},
		},
	}}
	itemRepo := &countingItemRepo{itemsByID: map[string]*models.MediaItem{
		seriesID: {ContentID: seriesID, Type: "series", Title: "Series"},
	}}
	durationSrc := &countingProbedDurationSource{episodes: map[string]int{episodeID: 1500}}
	h := &ItemsHandler{
		content:     &countingContentService{seasons: []upstreamSeason{{ContentID: seasonID, SeasonNumber: 1, Title: "Season 1", EpisodeCount: 1}}},
		userData:    &mockUserDataService{},
		codec:       codec,
		mapper:      newMapper(codec, &config.Config{}),
		images:      NewImageCache(time.Hour, time.Now),
		itemRepo:    itemRepo,
		episodeRepo: episodeRepo,
		durationSrc: durationSrc,
	}

	result := performEpisodesRequest(t, h, "/Shows/"+codec.EncodeStringID(EncodedIDItem, seriesID)+"/Episodes", codec.EncodeStringID(EncodedIDItem, seriesID))
	if len(result.Items) != 1 {
		t.Fatalf("len(Items) = %d, want 1", len(result.Items))
	}
	if result.Items[0].RunTimeTicks != secondsToTicks(1500) {
		t.Fatalf("RunTimeTicks = %d, want %d", result.Items[0].RunTimeTicks, secondsToTicks(1500))
	}
}
