package watchtogether

import (
	"testing"
	"time"
)

func TestRoomRuntimeSweepsUncachedHostPG(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		t.Run(map[bool]string{false: "node_loss", true: "disconnect"}[disconnect], func(t *testing.T) {
			f := newRoomClusterFixture(t)
			deadline := f.now.Add(f.host.hostDisconnectTTL)
			if disconnect {
				f.host.Disconnect(f.hostReg, false)
			} else {
				deadline = deadline.Add(connectionLease)
			}
			// Simulate loss of every socket-owning process. The survivor has
			// neither a cached room nor a disconnect notification.
			f.host.Close()
			clear(f.guest.rooms)
			f.now = deadline.Add(-time.Second)
			f.guest.sweepExpiredHosts(t.Context())
			room, err := f.repo.GetRoomByID(t.Context(), f.roomID)
			if err != nil || room.Phase == RoomPhaseEnded {
				t.Fatalf("room closed before grace period: %+v %v", room, err)
			}
			f.now = deadline.Add(time.Second)
			f.guest.sweepExpiredHosts(t.Context())
			room, err = f.repo.GetRoomByID(t.Context(), f.roomID)
			if err != nil || room.Phase != RoomPhaseEnded {
				t.Fatalf("uncached room survived host expiry: %+v %v", room, err)
			}
		})
	}
}

func TestRoomRuntimeHostReconnectRollbackPreservesExpiryPG(t *testing.T) {
	f := newRoomClusterFixture(t)
	f.host.Disconnect(f.hostReg, false)
	_, err := f.repo.pool.Exec(t.Context(), `
CREATE FUNCTION reject_host_reconnect() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'injected reconnect failure'; END $$;
CREATE CONSTRAINT TRIGGER reject_host_reconnect AFTER UPDATE ON watch_together_rooms
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_host_reconnect();`)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.host.Connect(t.Context(), f.roomID, 7, "host", new(recordingConn)); err == nil {
		t.Fatal("reconnect unexpectedly committed")
	}
	if _, err = f.repo.pool.Exec(t.Context(), `DROP TRIGGER reject_host_reconnect ON watch_together_rooms`); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(f.host.hostDisconnectTTL + time.Second)
	f.host.sweepExpiredHosts(t.Context())
	room, err := f.repo.GetRoomByID(t.Context(), f.roomID)
	if err != nil || room.Phase != RoomPhaseEnded {
		t.Fatalf("failed reconnect lost host deadline: %+v %v", room, err)
	}
}

func TestRoomRuntimeHostSweepRechecksReplacementPG(t *testing.T) {
	f := newRoomClusterFixture(t)
	f.host.Disconnect(f.hostReg, false)
	f.now = f.now.Add(f.host.hostDisconnectTTL + time.Second)
	ids, err := f.repo.listExpiredHostRoomIDs(t.Context(), f.now.Add(-f.host.hostDisconnectTTL), 100)
	if err != nil || len(ids) != 1 || ids[0] != f.roomID {
		t.Fatalf("expired host was not selected: %v %v", ids, err)
	}
	if _, _, err = f.guest.Connect(t.Context(), f.roomID, 7, "host", new(recordingConn)); err != nil {
		t.Fatal(err)
	}
	// The sweep's candidate list predates a successful remote reconnect.
	f.host.reconcileRooms(t.Context(), ids)
	room, err := f.repo.GetRoomByID(t.Context(), f.roomID)
	if err != nil || room.Phase == RoomPhaseEnded {
		t.Fatalf("stale sweep closed replacement connection: %+v %v", room, err)
	}
	ids, err = f.repo.listExpiredHostRoomIDs(t.Context(), f.now.Add(-f.host.hostDisconnectTTL), 100)
	if err != nil || len(ids) != 0 {
		t.Fatalf("connected host remains an expiry candidate: %v %v", ids, err)
	}
}

func TestRoomRuntimeHostSweepRetriesCloseRollbackPG(t *testing.T) {
	f := newRoomClusterFixture(t)
	f.host.Disconnect(f.hostReg, false)
	f.now = f.now.Add(f.host.hostDisconnectTTL + time.Second)
	_, err := f.repo.pool.Exec(t.Context(), `
CREATE FUNCTION reject_expiry_close() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.phase = 'ended' THEN RAISE EXCEPTION 'injected expiry failure'; END IF;
 RETURN NEW;
END $$;
CREATE CONSTRAINT TRIGGER reject_expiry_close AFTER UPDATE ON watch_together_rooms
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_expiry_close();`)
	if err != nil {
		t.Fatal(err)
	}
	f.host.sweepExpiredHosts(t.Context())
	room, err := f.repo.GetRoomByID(t.Context(), f.roomID)
	if err != nil || room.Phase == RoomPhaseEnded {
		t.Fatalf("failed close was not rolled back: %+v %v", room, err)
	}
	if _, err = f.repo.pool.Exec(t.Context(), `DROP TRIGGER reject_expiry_close ON watch_together_rooms`); err != nil {
		t.Fatal(err)
	}
	f.host.sweepExpiredHosts(t.Context())
	room, err = f.repo.GetRoomByID(t.Context(), f.roomID)
	if err != nil || room.Phase != RoomPhaseEnded {
		t.Fatalf("host deadline lost after close rollback: %+v %v", room, err)
	}
}
