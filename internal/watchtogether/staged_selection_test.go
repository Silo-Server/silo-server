package watchtogether

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func lobbyRoom(now time.Time) Room {
	room := baseRoom(now)
	room.Phase = RoomPhaseLobby
	room.PlaybackState = RoomPlaybackStateIdle
	room.SelectionRevision = 0
	room.SelectedContentID = nil
	room.IsPaused = true
	return room
}

func stagedServiceForTest(t *testing.T, now time.Time, room Room, contentID string) (*Service, *stubRepo, *recordingConn, *recordingConn) {
	t.Helper()
	repo := &stubRepo{room: room}
	service := newServiceForTest(now, repo, nil, nil, &stubSelectionResolver{resolved: &ResolvedSelection{ContentID: contentID, FileID: intPtr(7), LibraryID: intPtr(8)}})
	host := new(recordingConn)
	guest := new(recordingConn)
	service.rooms[room.ID].members[buildMemberKey(7, "host")] = &memberState{userID: 7, profileID: "host", displayName: "Host", connection: host}
	service.rooms[room.ID].members[buildMemberKey(8, "guest")] = &memberState{userID: 8, profileID: "guest", displayName: "Guest", connection: guest, lobbyReady: true}
	return service, repo, host, guest
}

func TestStageUpdatesChangedResolvedFileOrLibrary(t *testing.T) {
	for _, field := range []string{"file", "library"} {
		t.Run(field, func(t *testing.T) {
			now := time.Date(2026, 4, 10, 12, 0, 20, 0, time.UTC)
			room := lobbyRoom(now.Add(-time.Minute))
			room.SelectedContentID = new("movie-2")
			room.SelectedFileID = new(7)
			room.SelectedLibraryID = new(8)
			if field == "file" {
				room.SelectedFileID = new(9)
			} else {
				room.SelectedLibraryID = nil
			}
			service, repo, host, guest := stagedServiceForTest(t, now, room, "movie-2")
			snapshot, err := service.StageItem(t.Context(), room.ID, 7, "host", SelectItemInput{ContentID: "movie-2"})
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Generation != room.Generation+1 || snapshot.SelectionRevision != room.SelectionRevision || snapshot.AnchorUpdatedAt != now.Format(time.RFC3339) {
				t.Fatalf("changed variant was not staged: %+v", snapshot)
			}
			if repo.room.SelectedFileID == nil || *repo.room.SelectedFileID != 7 || repo.room.SelectedLibraryID == nil || *repo.room.SelectedLibraryID != 8 {
				t.Fatalf("resolved variant was not persisted: %+v", repo.room)
			}
			if len(host.payloads) != 1 || len(guest.payloads) != 1 {
				t.Fatal("changed variant was not broadcast")
			}
		})
	}
}

func TestStageRefusals(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 20, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		mutate  func(*Room)
		user    int
		profile string
		want    error
	}{
		{name: "vote room", mutate: func(r *Room) { r.SelectionMode = RoomSelectionModeVote }, user: 7, profile: "host", want: ErrVoteRoomSelection},
		{name: "already playing", mutate: func(r *Room) { r.Phase = RoomPhasePlaying }, user: 7, profile: "host", want: ErrRoomNotInLobby},
		{name: "ended", mutate: func(r *Room) { r.Phase = RoomPhaseEnded }, user: 7, profile: "host", want: ErrRoomClosed},
		{name: "guest", mutate: func(*Room) {}, user: 8, profile: "guest", want: ErrRoomForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			room := lobbyRoom(now)
			tc.mutate(&room)
			service, repo, host, _ := stagedServiceForTest(t, now, room, "movie-2")
			_, err := service.StageItem(context.Background(), repo.room.ID, tc.user, tc.profile, SelectItemInput{ContentID: "movie-2"})
			if !errors.Is(err, tc.want) {
				t.Fatalf("StageItem() error = %v, want %v", err, tc.want)
			}
			if len(host.payloads) != 0 {
				t.Fatal("refused stage still broadcast")
			}
		})
	}
	room := lobbyRoom(now)
	service, repo, _, _ := stagedServiceForTest(t, now, room, "movie-2")
	if _, err := service.StageItem(context.Background(), repo.room.ID, 7, "host", SelectItemInput{ContentID: "  "}); !errors.Is(err, ErrInvalidSelection) {
		t.Fatalf("blank content: %v", err)
	}
}

func TestStartRefusals(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 20, 0, time.UTC)

	t.Run("nothing staged", func(t *testing.T) {
		service, repo, _, _ := stagedServiceForTest(t, now, lobbyRoom(now), "movie-2")
		if _, err := service.StartStagedOnce(context.Background(), repo.room.ID, 7, "host"); !errors.Is(err, ErrNoStagedSelection) {
			t.Fatalf("error = %v, want ErrNoStagedSelection", err)
		}
	})
	t.Run("guest", func(t *testing.T) {
		room := lobbyRoom(now)
		room.SelectedContentID = stringPtr("movie-2")
		service, repo, _, _ := stagedServiceForTest(t, now, room, "movie-2")
		if _, err := service.StartStagedOnce(context.Background(), repo.room.ID, 8, "guest"); !errors.Is(err, ErrRoomForbidden) {
			t.Fatalf("error = %v, want ErrRoomForbidden", err)
		}
	})
	t.Run("already playing is a no-op", func(t *testing.T) {
		service, repo, host, _ := stagedServiceForTest(t, now, baseRoom(now), "movie-1")
		snapshot, err := service.StartStagedOnce(context.Background(), repo.room.ID, 7, "host")
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if snapshot.SelectionRevision != 1 || snapshot.Generation != 1 || len(host.payloads) != 0 {
			t.Fatalf("double press restarted playback: %+v (%d payloads)", snapshot, len(host.payloads))
		}
	})
}

func TestAttachSessionRefusedWhileStaged(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 20, 0, time.UTC)
	room := lobbyRoom(now)
	room.SelectedContentID = stringPtr("movie-1")
	repo := &stubRepo{room: room}
	service := newServiceForTest(
		now,
		repo,
		&stubSessions{session: &playback.Session{ID: "host-session", UserID: 7, ProfileID: "host", MediaFileID: 42}},
		&stubFiles{file: &models.MediaFile{ID: 42, ContentID: "movie-1"}},
		nil,
	)
	service.rooms[room.ID].members[buildMemberKey(7, "host")] = &memberState{userID: 7, profileID: "host", connection: stubConn{}}
	reg := registrationFor(room.ID, 7, "host", stubConn{})
	if _, err := service.AttachSessionForConnection(context.Background(), reg, 7, "host", "host-session"); !errors.Is(err, ErrSessionMismatch) {
		t.Fatalf("AttachSessionForConnection() error = %v, want ErrSessionMismatch", err)
	}
}

func TestClusterAdoptionPreservesCommittedLobbyReady(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 20, 0, time.UTC)
	room := lobbyRoom(now)
	room.SelectedContentID = stringPtr("movie-1")
	repo := &stubRepo{room: room}
	service := newServiceForTest(now, repo, nil, nil, nil)
	live := service.rooms[room.ID]
	key := buildMemberKey(8, "guest")
	live.members[key] = &memberState{userID: 8, profileID: "guest", connection: new(recordingConn), lobbyReady: true}

	adopt := func(next Room) {
		service.mu.Lock()
		defer service.mu.Unlock()
		state := service.runtimeLocked(live)
		service.adoptRuntimeLocked(context.Background(), live, next, state)
	}

	// Same content at a newer generation (say, a policy change elsewhere):
	// the question "ready for this?" has not changed.
	next := room
	next.Generation = 2
	adopt(next)
	if !live.members[key].lobbyReady {
		t.Fatal("unchanged staged item cleared lobby ready")
	}

	next.Generation = 3
	next.SelectedContentID = stringPtr("movie-2")
	adopt(next)
	if !live.members[key].lobbyReady {
		t.Fatal("adoption erased readiness committed for the current staged item")
	}
}

func TestLobbyReadyRefusedOutsideLobby(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 20, 0, time.UTC)
	service, repo, _, guest := stagedServiceForTest(t, now, baseRoom(now), "movie-1")
	reg := registrationFor(repo.room.ID, 8, "guest", guest)
	if _, err := service.HandleLobbyReadyForConnection(context.Background(), reg, 8, "guest", true); !errors.Is(err, ErrRoomNotInLobby) {
		t.Fatalf("playing room: %v", err)
	}
	if len(guest.payloads) != 0 {
		t.Fatal("refused ready broadcast")
	}
}

func TestSelectionModeSwitchRefusals(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 20, 0, time.UTC)
	t.Run("while playing", func(t *testing.T) {
		service, repo, _, _ := stagedServiceForTest(t, now, baseRoom(now), "movie-1")
		if _, err := service.UpdateSelectionMode(context.Background(), repo.room.ID, 7, "host", RoomSelectionModeVote); !errors.Is(err, ErrRoomNotInLobby) {
			t.Fatalf("error = %v, want ErrRoomNotInLobby", err)
		}
	})
	t.Run("guest", func(t *testing.T) {
		service, repo, _, _ := stagedServiceForTest(t, now, lobbyRoom(now), "movie-1")
		if _, err := service.UpdateSelectionMode(context.Background(), repo.room.ID, 8, "guest", RoomSelectionModeVote); !errors.Is(err, ErrRoomForbidden) {
			t.Fatalf("error = %v, want ErrRoomForbidden", err)
		}
	})
	t.Run("bad mode", func(t *testing.T) {
		service, repo, _, _ := stagedServiceForTest(t, now, lobbyRoom(now), "movie-1")
		if _, err := service.UpdateSelectionMode(context.Background(), repo.room.ID, 7, "host", RoomSelectionMode("dice")); !errors.Is(err, ErrInvalidSelection) {
			t.Fatalf("error = %v, want ErrInvalidSelection", err)
		}
	})
}

func TestStopPlaybackReturnsToLobbyAndKeepsSelection(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 20, 0, time.UTC)
	room := baseRoom(now) // playing movie-1, revision 1, generation 1
	service, repo, host, guest := stagedServiceForTest(t, now, room, "movie-1")
	live := service.rooms[repo.room.ID]
	live.waitingTimer = time.AfterFunc(time.Hour, func() {})
	hostMember := live.members[buildMemberKey(7, "host")]
	hostMember.sessionID = "session-1"
	hostMember.isReady = true
	hostMember.correctionCommand = &TransportCommand{CommandID: "stale-correction"}

	snapshot, err := service.StopPlaybackOnce(context.Background(), repo.room.ID, 7, "host")
	if err != nil {
		t.Fatalf("StopPlaybackOnce() error = %v", err)
	}
	if snapshot.Phase != RoomPhaseLobby || snapshot.PlaybackState != RoomPlaybackStateIdle || !snapshot.IsPaused {
		t.Fatalf("stop did not return to the lobby: %+v", snapshot)
	}
	if snapshot.SelectionRevision != 2 || snapshot.Generation != 2 {
		t.Fatalf("revision/generation = %d/%d, want 2/2", snapshot.SelectionRevision, snapshot.Generation)
	}
	if snapshot.SelectedContentID == nil || *snapshot.SelectedContentID != "movie-1" {
		t.Fatalf("stop dropped the staged item: %+v", snapshot.SelectedContentID)
	}
	if hostMember.sessionID != "" || hostMember.isReady || hostMember.correctionCommand != nil {
		t.Fatalf("stop left the previous epoch's session attached: %+v", hostMember)
	}
	if live.members[buildMemberKey(8, "guest")].lobbyReady {
		t.Fatal("guest lobby ready survived the epoch change")
	}
	if live.waitingTimer != nil {
		t.Fatal("waiting deadline was not disarmed")
	}
	if len(host.payloads) != 1 || len(guest.payloads) != 1 {
		t.Fatalf("expected one snapshot per member, got host=%d guest=%d", len(host.payloads), len(guest.payloads))
	}
	// The party is still open: the host can start the same item again.
	started, err := service.StartStagedOnce(context.Background(), repo.room.ID, 7, "host")
	if err != nil || started.Phase != RoomPhasePlaying || started.SelectionRevision != 3 {
		t.Fatalf("restart after stop: %+v %v", started, err)
	}
}

func TestStopPlaybackRefusals(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 20, 0, time.UTC)
	t.Run("guest", func(t *testing.T) {
		service, repo, _, _ := stagedServiceForTest(t, now, baseRoom(now), "movie-1")
		if _, err := service.StopPlaybackOnce(context.Background(), repo.room.ID, 8, "guest"); !errors.Is(err, ErrRoomForbidden) {
			t.Fatalf("error = %v, want ErrRoomForbidden", err)
		}
	})
	t.Run("lobby is a no-op", func(t *testing.T) {
		room := lobbyRoom(now)
		room.SelectedContentID = stringPtr("movie-2")
		service, repo, host, _ := stagedServiceForTest(t, now, room, "movie-2")
		snapshot, err := service.StopPlaybackOnce(context.Background(), repo.room.ID, 7, "host")
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if snapshot.Phase != RoomPhaseLobby || snapshot.Generation != room.Generation || len(host.payloads) != 0 {
			t.Fatalf("stop disturbed a lobby: %+v (%d payloads)", snapshot, len(host.payloads))
		}
	})
	t.Run("ended", func(t *testing.T) {
		room := baseRoom(now)
		room.Phase = RoomPhaseEnded
		service, repo, _, _ := stagedServiceForTest(t, now, room, "movie-1")
		if _, err := service.StopPlaybackOnce(context.Background(), repo.room.ID, 7, "host"); !errors.Is(err, ErrRoomClosed) {
			t.Fatalf("error = %v, want ErrRoomClosed", err)
		}
	})
}
