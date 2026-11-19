package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/watchstate"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

type recordingWatchDispatcher struct {
	events []watchsync.LocalWatchEvent
}

func (d *recordingWatchDispatcher) HandleLocalWatchEvent(_ context.Context, event watchsync.LocalWatchEvent) error {
	d.events = append(d.events, event)
	return nil
}

// A retried mark-watched must not record a second play: the seam behind v1
// POST /watched/{id} and v2 markWatched skips the history insert and the
// provider event when the profile already completed the target. An unmark
// clears that state, so the next mark records again.
func TestMarkLeafTargetsWatchedIsIdempotentUntilUnmarked(t *testing.T) {
	ctx := t.Context()
	store := newProfileTestStore(t)
	dispatcher := &recordingWatchDispatcher{}
	handler := &ItemsHandler{
		watchState:           watchstate.NewService(testUserStoreProvider{store: store}).WithStableIdentityResolver(watchstate.NewStableIdentityResolver(manualMarkIdentityFixture{}, nil, manualMarkIdentityFixture{})),
		localWatchDispatcher: dispatcher,
	}
	targets := []watchedLeafTarget{{ContentID: "movie-1", DurationSeconds: 7200}}

	for i := 0; i < 2; i++ {
		if err := handler.markLeafTargetsWatched(ctx, 1, "profile-1", targets, false); err != nil {
			t.Fatalf("mark %d: %v", i+1, err)
		}
	}
	history, err := store.ListHistory(ctx, "profile-1", 10, 0)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history after two marks = %d rows, want 1", len(history))
	}
	if len(dispatcher.events) != 1 || dispatcher.events[0].Kind != watchsync.LocalWatchEventMarkedWatched || len(dispatcher.events[0].Plays) != 1 {
		t.Fatalf("events after two marks = %+v, want one marked-watched event with one play", dispatcher.events)
	}

	if err := handler.markLeafTargetsUnwatched(ctx, 1, "profile-1", targets); err != nil {
		t.Fatalf("unmark: %v", err)
	}
	if err := handler.markLeafTargetsWatched(ctx, 1, "profile-1", targets, false); err != nil {
		t.Fatalf("mark after unmark: %v", err)
	}
	history, err = store.ListHistory(ctx, "profile-1", 10, 0)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	// Unmark keeps the first play in history and sends providers nothing;
	// the mark after it records and sends a second play.
	if len(history) != 2 {
		t.Fatalf("history after unmark+mark = %d rows, want the first play and the new one", len(history))
	}
	if len(dispatcher.events) != 2 ||
		dispatcher.events[1].Kind != watchsync.LocalWatchEventMarkedWatched || len(dispatcher.events[1].Plays) != 1 {
		t.Fatalf("events = %+v, want watched, watched", dispatcher.events)
	}
	if dispatcher.events[1].Plays[0].HistoryID == dispatcher.events[0].Plays[0].HistoryID {
		t.Fatal("the second play reused the first history id")
	}
}

// Provider events require an exportable identity, as production resolves from
// the catalog. A local-only ID correctly records history without exporting it.
type manualMarkIdentityFixture struct{}

func (manualMarkIdentityFixture) GetByID(_ context.Context, id string) (*models.MediaItem, error) {
	return &models.MediaItem{ContentID: id, Type: "movie"}, nil
}
func (manualMarkIdentityFixture) GetByContentID(_ context.Context, id string) ([]*models.MediaItemProviderID, error) {
	return []*models.MediaItemProviderID{{ContentID: id, ItemType: "movie", Provider: "tmdb", ProviderID: "123"}}, nil
}
func (manualMarkIdentityFixture) FindContentIDByProviderIDs(context.Context, map[string]string, string, string) (string, error) {
	return "movie-1", nil
}

// selectionWarmRecorder records the pre-play work a detail read schedules.
type selectionWarmRecorder struct {
	probes []int
	warms  []int
}

func (r *selectionWarmRecorder) ScheduleSelectedFileProbe(_ string, fileID int, _ catalog.AccessFilter) {
	r.probes = append(r.probes, fileID)
}

func (r *selectionWarmRecorder) ScheduleSelectedFileWarm(_ string, fileID int, _ catalog.AccessFilter) {
	r.warms = append(r.warms, fileID)
}

// TestScheduleSelectedVirtualProbeRepairsAStampedEmptyRow pins the selection
// gate's repair shape: a row stamped with no tracks still needs its inventory
// enumerated, because the stamp only says a probe ran — not that its evidence
// was adopted. Production's four stamped-empty rows were never re-probed here,
// so their menus stayed blank through every visit.
func TestScheduleSelectedVirtualProbeRepairsAStampedEmptyRow(t *testing.T) {
	probedAt := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		version   catalog.FileVersion
		wantProbe bool
	}{
		{
			name:      "stamped empty",
			version:   catalog.FileVersion{FileID: 7, FilePath: "virtual://movie/tt0113277?result=a", ProbedAt: &probedAt},
			wantProbe: true,
		},
		{
			name: "stamped with tracks",
			version: catalog.FileVersion{FileID: 8, FilePath: "virtual://movie/tt0113277?result=b", ProbedAt: &probedAt,
				AudioTracks: []models.AudioTrack{{Index: 1, Codec: "ac3"}}},
			wantProbe: false,
		},
		{
			name:      "unstamped",
			version:   catalog.FileVersion{FileID: 9, FilePath: "virtual://movie/tt0113277?result=c"},
			wantProbe: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &selectionWarmRecorder{}
			h := &ItemsHandler{virtualSelectionProber: recorder}
			h.scheduleSelectedVirtualProbe("movie:tt0113277", itemTypeMovie, []catalog.FileVersion{tc.version},
				catalog.AccessFilter{SelectedFileID: tc.version.FileID})
			if got := len(recorder.probes) == 1; got != tc.wantProbe {
				t.Fatalf("probe scheduled = %v, want %v (probes %v)", got, tc.wantProbe, recorder.probes)
			}
			if len(recorder.warms) != 1 {
				t.Fatalf("warm scheduled %v times, want 1", len(recorder.warms))
			}
		})
	}
}
