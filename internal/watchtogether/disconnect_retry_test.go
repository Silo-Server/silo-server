package watchtogether

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRoomRuntimeRetriesDisconnectPG(t *testing.T) {
	for _, failure := range []string{"begin", "commit"} {
		t.Run(failure, func(t *testing.T) {
			f := newRoomClusterFixture(t)
			if failure == "begin" {
				pool, err := pgxpool.NewWithConfig(t.Context(), f.repo.pool.Config())
				if err != nil {
					t.Fatal(err)
				}
				pool.Close()
				f.host.repo = NewRepository(pool)
			} else {
				_, err := f.repo.pool.Exec(t.Context(), `
CREATE FUNCTION reject_disconnect() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'injected disconnect failure'; END $$;
CREATE CONSTRAINT TRIGGER reject_disconnect AFTER UPDATE ON watch_together_rooms
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_disconnect();`)
				if err != nil {
					t.Fatal(err)
				}
			}
			f.host.Disconnect(f.hostReg, false)
			live := f.host.rooms[f.roomID]
			if live.members[f.hostReg.memberKey].connection != nil || len(live.pendingDisconnects) != 1 {
				t.Fatal("failed disconnect retained a dead socket or lost its retry")
			}
			f.host.sweepIdleRooms()
			if f.host.rooms[f.roomID] != live {
				t.Fatal("janitor evicted the pending disconnect")
			}
			f.host.repo = f.repo
			if failure == "commit" {
				if _, err := f.repo.pool.Exec(t.Context(), `DROP TRIGGER reject_disconnect ON watch_together_rooms`); err != nil {
					t.Fatal(err)
				}
			}
			f.now = f.now.Add(20 * time.Second)
			if err := f.host.reconcileRoom(t.Context(), f.roomID); err != nil {
				t.Fatal(err)
			}
			if len(live.pendingDisconnects) != 0 {
				t.Fatal("committed retry was not cleared")
			}
			snapshot, err := f.guest.Snapshot(t.Context(), f.roomID, 8, "guest")
			if err != nil || snapshot.HostConnected || snapshot.MemberCount != 1 {
				t.Fatalf("dead host still counted as connected: %+v %v", snapshot, err)
			}
			f.now = f.now.Add(f.host.hostDisconnectTTL + time.Second)
			if err = f.guest.reconcileRoom(t.Context(), f.roomID); err != nil {
				t.Fatal(err)
			}
			room, err := f.repo.GetRoomByID(t.Context(), f.roomID)
			if err != nil || room.Phase != RoomPhaseEnded {
				t.Fatalf("disconnected host never closed room: %v", err)
			}
		})
	}
}

func TestRoomRuntimeDisconnectRetryFencesReplacementPG(t *testing.T) {
	f := newRoomClusterFixture(t)
	pool, err := pgxpool.NewWithConfig(t.Context(), f.repo.pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	f.host.repo = NewRepository(pool)
	f.host.Disconnect(f.hostReg, false)
	f.host.repo = f.repo
	replacement := new(recordingConn)
	reg, _, err := f.host.Connect(t.Context(), f.roomID, 7, "host", replacement)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.host.reconcileRoom(t.Context(), f.roomID); err != nil {
		t.Fatal(err)
	}
	live := f.host.rooms[f.roomID]
	if live.members[reg.memberKey].connection != replacement || len(live.pendingDisconnects) != 0 {
		t.Fatal("old disconnect removed the replacement socket")
	}
}
