package watchtogether

import "testing"

func TestConnectedMembersLoadsSharedRuntimePG(t *testing.T) {
	f := newRoomClusterFixture(t)
	reader := newServiceForTest(f.now, &stubRepo{room: baseRoom(f.now)}, nil, nil, nil)
	reader.repo = NewRepository(f.repo.pool)
	t.Cleanup(reader.Close)
	// This server has never held either socket or received a room event.
	clear(reader.rooms)
	members, err := reader.ConnectedMembers(t.Context(), f.roomID, 8, "guest")
	if err != nil || len(members) != 2 {
		t.Fatalf("shared members = %+v, %v", members, err)
	}
	if !members[0].IsHost || members[0].UserID != 7 || !members[1].IsSelf || members[1].UserID != 8 {
		t.Fatalf("member identities = %+v", members)
	}
	// A cached read must also observe disconnects committed by another node.
	f.guest.Disconnect(f.guestReg, true)
	members, err = reader.ConnectedMembers(t.Context(), f.roomID, 7, "host")
	if err != nil || len(members) != 1 || members[0].UserID != 7 {
		t.Fatalf("members after disconnect = %+v, %v", members, err)
	}
}

func TestLobbyReadySurvivesStaleNodeAdoptionPG(t *testing.T) {
	f := newRoomClusterFixture(t)
	if _, err := f.host.StopPlaybackOnce(t.Context(), f.roomID, 7, "host"); err != nil {
		t.Fatal(err)
	}
	// Cache the old selection on a third server, then let the host stage a
	// new item and the guest mark it ready before that server reconciles.
	reader := newServiceForTest(f.now, &stubRepo{room: baseRoom(f.now)}, nil, nil, nil)
	reader.repo = NewRepository(f.repo.pool)
	t.Cleanup(reader.Close)
	if err := reader.reconcileRoom(t.Context(), f.roomID); err != nil {
		t.Fatal(err)
	}
	f.host.selectionResolver = &stubSelectionResolver{resolved: &ResolvedSelection{ContentID: "movie-2"}}
	if _, err := f.host.StageItem(t.Context(), f.roomID, 7, "host", SelectItemInput{ContentID: "movie-2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.guest.HandleLobbyReadyForConnection(t.Context(), f.guestReg, 8, "guest", true); err != nil {
		t.Fatal(err)
	}
	if err := reader.reconcileRoom(t.Context(), f.roomID); err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.host.Snapshot(t.Context(), f.roomID, 7, "host")
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range snapshot.Members {
		if member.UserID == 8 {
			if !member.LobbyReady {
				t.Fatal("stale node erased the guest's ready flag for the new selection")
			}
			return
		}
	}
	t.Fatal("guest missing from shared snapshot")
}
