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

func TestStageKeepsRoomInLobby(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 20, 0, time.UTC)
	service, repo, host, guest := stagedServiceForTest(t, now, lobbyRoom(now), "movie-2")
	service.rooms[repo.room.ID].members[buildMemberKey(8, "guest")].lobbyReady = false

	snapshot, err := service.StageItem(context.Background(), repo.room.ID, 7, "host", SelectItemInput{ContentID: "movie-2"})
	if err != nil {
		t.Fatalf("StageItem() error = %v", err)
	}
	if snapshot.Phase != RoomPhaseLobby || snapshot.PlaybackState != RoomPlaybackStateIdle {
		t.Fatalf("staging changed phase: %s/%s", snapshot.Phase, snapshot.PlaybackState)
	}
	if snapshot.SelectionRevision != 0 {
		t.Fatalf("staging bumped the selection revision to %d", snapshot.SelectionRevision)
	}
	if snapshot.Generation != 2 || repo.room.Generation != 2 {
		t.Fatalf("generation = %d (persisted %d), want 2", snapshot.Generation, repo.room.Generation)
	}
	if snapshot.SelectedContentID == nil || *snapshot.SelectedContentID != "movie-2" || snapshot.SelectedFileID == nil || *snapshot.SelectedFileID != 7 {
		t.Fatalf("staged selection not applied: %+v", snapshot)
	}
	if repo.room.SelectedContentID == nil || *repo.room.SelectedContentID != "movie-2" || !repo.room.AnchorUpdatedAt.Equal(now) {
		t.Fatalf("persisted row = %+v", repo.room)
	}
	if len(host.payloads) != 1 || len(guest.payloads) != 1 || host.payloads[0]["type"] != "snapshot" {
		t.Fatalf("expected one snapshot per member, got host=%d guest=%d", len(host.payloads), len(guest.payloads))
	}
}

func TestStageClearsLobbyReadyOnlyWhenContentChanges(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 20, 0, time.UTC)
	room := lobbyRoom(now)
	room.SelectedContentID = stringPtr("movie-2")
	service, repo, _, _ := stagedServiceForTest(t, now, room, "movie-2")
	guest := service.rooms[repo.room.ID].members[buildMemberKey(8, "guest")]

	if _, err := service.StageItem(context.Background(), repo.room.ID, 7, "host", SelectItemInput{ContentID: "movie-2"}); err != nil {
		t.Fatalf("restage same: %v", err)
	}
	if !guest.lobbyReady {
		t.Fatal("re-staging the same content cleared lobby ready")
	}

	service.selectionResolver = &stubSelectionResolver{resolved: &ResolvedSelection{ContentID: "movie-3"}}
	snapshot, err := service.StageItem(context.Background(), repo.room.ID, 7, "host", SelectItemInput{ContentID: "movie-3"})
	if err != nil {
		t.Fatalf("stage different: %v", err)
	}
	if guest.lobbyReady {
		t.Fatal("staging different content kept lobby ready")
	}
	for _, member := range snapshot.Members {
		if member.LobbyReady {
			t.Fatalf("snapshot still reports %s ready", member.DisplayName)
		}
	}
}

func TestStageIdenticalResolvedSelectionIsNoOp(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 20, 0, time.UTC)
	room := lobbyRoom(now.Add(-time.Minute))
	room.SelectedContentID = new("movie-2")
	room.SelectedFileID = new(7)
	room.SelectedLibraryID = new(8)
	service, repo, host, guest := stagedServiceForTest(t, now, room, "movie-2")

	// Different request inputs may resolve to the same canonical selection.
	snapshot, err := service.StageItem(t.Context(), room.ID, 7, "host", SelectItemInput{ContentID: "alias"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Generation != room.Generation || snapshot.SelectionRevision != room.SelectionRevision || snapshot.AnchorUpdatedAt != room.AnchorUpdatedAt.Format(time.RFC3339) {
		t.Fatalf("identical stage changed the room: %+v", snapshot)
	}
	if repo.room.Generation != room.Generation || !repo.room.AnchorUpdatedAt.Equal(room.AnchorUpdatedAt) {
		t.Fatalf("identical stage persisted a change: %+v", repo.room)
	}
	if !service.rooms[room.ID].members[buildMemberKey(8, "guest")].lobbyReady {
		t.Fatal("identical stage cleared lobby readiness")
	}
	if len(host.payloads) != 0 || len(guest.payloads) != 0 {
		t.Fatal("identical stage broadcast another snapshot")
	}
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

func TestStartStagedEntersWaitingAndBumpsRevision(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 20, 0, time.UTC)
	room := lobbyRoom(now)
	room.SelectedContentID = stringPtr("movie-2")
	service, repo, host, guest := stagedServiceForTest(t, now, room, "movie-2")
	live := service.rooms[repo.room.ID]
	live.waitingTimer = time.AfterFunc(time.Hour, func() {})
	hostMember := live.members[buildMemberKey(7, "host")]
	hostMember.sessionID = "stale"
	hostMember.isReady = true
	hostMember.lobbyReady = true
	hostMember.correctionCommand = &TransportCommand{CommandID: "stale-correction"}

	snapshot, err := service.StartStagedOnce(context.Background(), repo.room.ID, 7, "host")
	if err != nil {
		t.Fatalf("StartStagedOnce() error = %v", err)
	}
	if snapshot.Phase != RoomPhasePlaying || snapshot.PlaybackState != RoomPlaybackStateWaiting || !snapshot.IsPaused {
		t.Fatalf("start did not enter waiting: %+v", snapshot)
	}
	if snapshot.SelectionRevision != 1 || snapshot.Generation != 2 {
		t.Fatalf("revision/generation = %d/%d, want 1/2", snapshot.SelectionRevision, snapshot.Generation)
	}
	if snapshot.SelectedContentID == nil || *snapshot.SelectedContentID != "movie-2" {
		t.Fatalf("start changed the staged item: %+v", snapshot.SelectedContentID)
	}
	if hostMember.sessionID != "" || hostMember.isReady || hostMember.lobbyReady || hostMember.correctionCommand != nil {
		t.Fatalf("start did not reset member epoch state: %+v", hostMember)
	}
	if live.members[buildMemberKey(8, "guest")].lobbyReady {
		t.Fatal("guest lobby ready survived start")
	}
	if live.waitingTimer != nil {
		t.Fatal("waiting deadline from the previous epoch was not disarmed")
	}
	if len(host.payloads) != 1 || len(guest.payloads) != 1 {
		t.Fatalf("expected one snapshot per member, got host=%d guest=%d", len(host.payloads), len(guest.payloads))
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

func TestLobbyReadyTogglesAndBroadcasts(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 20, 0, time.UTC)
	room := lobbyRoom(now)
	room.SelectedContentID = stringPtr("movie-1")
	service, repo, host, guest := stagedServiceForTest(t, now, room, "movie-1")
	live := service.rooms[repo.room.ID]
	live.members[buildMemberKey(8, "guest")].lobbyReady = false
	reg := registrationFor(repo.room.ID, 8, "guest", guest)

	snapshot, err := service.HandleLobbyReadyForConnection(context.Background(), reg, 8, "guest", true)
	if err != nil {
		t.Fatalf("ready: %v", err)
	}
	var self *MemberSummary
	for i := range snapshot.Members {
		if snapshot.Members[i].IsSelf {
			self = &snapshot.Members[i]
		}
	}
	if self == nil || !self.LobbyReady {
		t.Fatalf("snapshot does not show self ready: %+v", snapshot.Members)
	}
	if len(host.payloads) != 1 || len(guest.payloads) != 1 {
		t.Fatalf("ready did not broadcast once per member: host=%d guest=%d", len(host.payloads), len(guest.payloads))
	}
	if repo.room.Generation != 1 {
		t.Fatal("lobby ready must not persist or bump the generation")
	}

	// Same value again is a no-op with no broadcast.
	if _, err := service.HandleLobbyReadyForConnection(context.Background(), reg, 8, "guest", true); err != nil {
		t.Fatal(err)
	}
	if len(host.payloads) != 1 {
		t.Fatal("repeated ready broadcast again")
	}
	if _, err := service.HandleLobbyReadyForConnection(context.Background(), reg, 8, "guest", false); err != nil || live.members[buildMemberKey(8, "guest")].lobbyReady {
		t.Fatalf("unready: %v ready=%v", err, live.members[buildMemberKey(8, "guest")].lobbyReady)
	}

	// A registration whose connection is not the member's current one is refused.
	stale := registrationFor(repo.room.ID, 8, "guest", stubConn{})
	if _, err := service.HandleLobbyReadyForConnection(context.Background(), stale, 8, "guest", true); !errors.Is(err, ErrRoomForbidden) {
		t.Fatalf("stale connection: %v", err)
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

func TestSelectionModeSwitchClearsStagedAndLobbyReady(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 20, 0, time.UTC)
	room := lobbyRoom(now)
	room.SelectedContentID = stringPtr("movie-1")
	room.SelectedFileID = intPtr(7)
	service, repo, host, guest := stagedServiceForTest(t, now, room, "movie-1")
	live := service.rooms[repo.room.ID]

	snapshot, err := service.UpdateSelectionMode(context.Background(), repo.room.ID, 7, "host", RoomSelectionModeVote)
	if err != nil {
		t.Fatalf("UpdateSelectionMode() error = %v", err)
	}
	if snapshot.SelectionMode != RoomSelectionModeVote || snapshot.Phase != RoomPhaseLobby || snapshot.SelectionRevision != 0 {
		t.Fatalf("switch: %+v", snapshot)
	}
	if snapshot.SelectedContentID != nil || snapshot.SelectedFileID != nil || repo.room.SelectedContentID != nil {
		t.Fatalf("staged item survived the switch: %+v / %+v", snapshot, repo.room)
	}
	if snapshot.Generation != 2 || repo.room.SelectionMode != RoomSelectionModeVote {
		t.Fatalf("persist: gen=%d mode=%s", snapshot.Generation, repo.room.SelectionMode)
	}
	if live.members[buildMemberKey(8, "guest")].lobbyReady {
		t.Fatal("lobby ready survived the switch")
	}
	if len(host.payloads) != 1 || len(guest.payloads) != 1 {
		t.Fatalf("broadcast: host=%d guest=%d", len(host.payloads), len(guest.payloads))
	}

	// Same mode again is a no-op: no persist, no broadcast.
	again, err := service.UpdateSelectionMode(context.Background(), repo.room.ID, 7, "host", RoomSelectionModeVote)
	if err != nil || again.Generation != 2 || len(host.payloads) != 1 {
		t.Fatalf("repeat: %+v %v payloads=%d", again, err, len(host.payloads))
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

func TestSwitchToHostPickAllowsDirectSelection(t *testing.T) {
	service, repo := newVoteRoomService(t, RoomSelectionModeVote, voteRoomSuggestions())
	if _, err := service.SelectItem(context.Background(), repo.room.ID, 7, "host", SelectItemInput{ContentID: "movie-winner"}); !errors.Is(err, ErrVoteRoomSelection) {
		t.Fatalf("vote room direct selection: %v", err)
	}
	if _, err := service.UpdateSelectionMode(context.Background(), repo.room.ID, 7, "host", RoomSelectionModeHostPick); err != nil {
		t.Fatalf("switch: %v", err)
	}
	snapshot, err := service.SelectItem(context.Background(), repo.room.ID, 7, "host", SelectItemInput{ContentID: "movie-winner"})
	if err != nil || snapshot.Phase != RoomPhasePlaying {
		t.Fatalf("host_pick direct selection after switch: %+v %v", snapshot, err)
	}
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
