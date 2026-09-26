package watchtogether

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

func fallbackTestResolver() *CatalogSelectionResolver {
	return NewCatalogSelectionResolver(roomWatchDetail{versions: []catalog.FileVersion{
		{FileID: 1, Resolution: "2160p", HDR: true, FileSize: 300},
		{FileID: 2, Resolution: "2160p", FileSize: 200},
		{FileID: 3, Resolution: "1080p", FileSize: 100},
		{FileID: 4, Resolution: "1080p", FileSize: 400, EditionKey: "different-cut"},
		{FileID: 5, Resolution: "1080p", FileSize: 400, PresentationPartIndex: 2},
	}})
}

func TestSourceFallbackSelectsOneSharedVersionForTheRefusal(t *testing.T) {
	resolver := fallbackTestResolver()
	for _, test := range []struct {
		reason string
		want   int
	}{
		{"no_alternate_version", 3},
		{"hdr_transcode_unsupported", 2},
		{"subtitle_conversion_unsupported", 2},
		{"transcoding_disabled", 2},
	} {
		t.Run(test.reason, func(t *testing.T) {
			resolved, err := resolver.ResolveSourceFallback(t.Context(), 7, "host", SelectItemInput{ContentID: "movie-1", FileID: new(1)}, test.reason)
			if err != nil || *resolved.FileID != test.want {
				t.Fatalf("fallback = %+v, %v; want file %d", resolved, err, test.want)
			}
		})
	}
	if _, err := resolver.ResolveSourceFallback(t.Context(), 7, "host", SelectItemInput{ContentID: "movie-1", FileID: new(3)}, "no_alternate_version"); !errors.Is(err, ErrSourceFallbackUnavailable) {
		t.Fatalf("exhausted fallback returned %v; must not revisit a higher-ranked file or another edition/part", err)
	}
}

func TestSourceFallbackGuestPreservesAnchorAndFencesStaleReports(t *testing.T) {
	now := time.Now().UTC()
	room := baseRoom(now)
	room.SelectedFileID = new(1)
	room.SelectionMode = RoomSelectionModeVote
	svc := newServiceForTest(now, &stubRepo{room: room}, nil, nil, fallbackTestResolver())
	t.Cleanup(svc.Close)
	live := svc.rooms[room.ID]
	for _, profile := range []string{"host", "guest"} {
		user := 7
		if profile == "guest" {
			user = 8
		}
		live.members[buildMemberKey(user, profile)] = &memberState{userID: user, profileID: profile, connection: &recordingConn{}, sessionID: profile, isReady: true, waitingCommand: &TransportCommand{CommandID: "old"}, correctionCommand: &TransportCommand{CommandID: "old-correction"}}
	}
	input := SourceFallbackInput{SelectionRevision: room.SelectionRevision, FailedFileID: 1, Reason: "no_alternate_version"}
	if _, err := svc.FallbackSource(t.Context(), room.ID, 9, "outsider", input); !errors.Is(err, ErrRoomForbidden) {
		t.Fatalf("outsider fallback = %v", err)
	}
	got, err := svc.FallbackSource(t.Context(), room.ID, 8, "guest", input)
	if err != nil || *got.SelectedFileID != 3 || got.SelectionRevision != 2 || got.AnchorPositionSeconds != 20 || got.PlaybackState != RoomPlaybackStateWaiting || !live.room.ResumeOnReady {
		t.Fatalf("guest fallback = %+v, %v", got, err)
	}
	for _, member := range live.members {
		if member.sessionID != "" || member.isReady || member.waitingCommand != nil || member.correctionCommand != nil {
			t.Fatalf("old attachment survived: %+v", member)
		}
	}
	stale, err := svc.FallbackSource(t.Context(), room.ID, 7, "host", input)
	if err != nil || stale.Generation != got.Generation || *stale.SelectedFileID != 3 {
		t.Fatalf("stale report changed selection: %+v, %v", stale, err)
	}
}

func TestSourceFallbackConcurrentViewersAcrossNodesPG(t *testing.T) {
	f := newRoomClusterFixture(t)
	f.host.selectionResolver, f.guest.selectionResolver = fallbackTestResolver(), fallbackTestResolver()
	if _, err := f.repo.pool.Exec(t.Context(), `UPDATE watch_together_rooms SET selected_file_id=1, anchor_position_seconds=900, anchor_updated_at=$2, playback_state='playing', is_paused=false WHERE id=$1`, f.roomID, f.now); err != nil {
		t.Fatal(err)
	}
	input := SourceFallbackInput{SelectionRevision: 1, FailedFileID: 1, Reason: "no_alternate_version"}
	results := make(chan Snapshot, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, caller := range []struct {
		svc     *Service
		user    int
		profile string
	}{{f.host, 7, "host"}, {f.guest, 8, "guest"}} {
		wg.Go(func() {
			snapshot, err := caller.svc.FallbackSource(t.Context(), f.roomID, caller.user, caller.profile, input)
			results <- snapshot
			errs <- err
		})
	}
	wg.Wait()
	for range 2 {
		got, err := <-results, <-errs
		if err != nil || got.SelectionRevision != 2 || *got.SelectedFileID != 3 || got.AnchorPositionSeconds != 900 {
			t.Fatalf("concurrent fallback = %+v, %v", got, err)
		}
	}
	for _, svc := range []*Service{f.host, f.guest} {
		got, err := svc.Snapshot(t.Context(), f.roomID, 7, "host")
		if err != nil || got.SelectionRevision != 2 || got.PlaybackState != RoomPlaybackStateWaiting || len(got.Members) != 2 {
			t.Fatalf("shared fallback state = %+v, %v", got, err)
		}
		for _, member := range got.Members {
			if member.IsReady {
				t.Fatal("stale readiness survived source fallback")
			}
		}
	}
}

func TestSourceFallbackPreservesPausedRoom(t *testing.T) {
	now := time.Now().UTC()
	room := baseRoom(now)
	room.SelectedFileID = new(1)
	room.IsPaused, room.PlaybackState = true, RoomPlaybackStatePaused
	svc := newServiceForTest(now, &stubRepo{room: room}, nil, nil, fallbackTestResolver())
	t.Cleanup(svc.Close)
	live := svc.rooms[room.ID]
	live.members[buildMemberKey(7, "host")] = &memberState{userID: 7, profileID: "host", connection: stubConn{}}
	got, err := svc.FallbackSource(t.Context(), room.ID, 7, "host", SourceFallbackInput{SelectionRevision: 1, FailedFileID: 1, Reason: "no_alternate_version"})
	if err != nil || got.AnchorPositionSeconds != room.AnchorPositionSeconds || !got.IsPaused || live.room.ResumeOnReady {
		t.Fatalf("paused room resumed or moved: %+v, %v", got, err)
	}
}
