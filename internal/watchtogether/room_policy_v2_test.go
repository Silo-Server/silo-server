package watchtogether

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type competingPolicyRepo struct {
	*stubRepo
	calls int
}

func (r *competingPolicyRepo) UpdatePolicy(context.Context, string, GuestControlPolicy, int64, int64) (*Room, error) {
	r.calls++
	r.room.Generation += 2
	r.room.GuestControlPolicy = GuestControlPolicyHostOnly
	return nil, ErrRoomStateConflict
}
func TestPolicyHostAuthorityAndCompetingWinner(t *testing.T) {
	now := time.Now()
	repo := &stubRepo{room: baseRoom(now)}
	svc := newServiceForTest(now, repo, nil, nil, nil)
	for _, who := range []struct {
		user    int
		profile string
	}{{7, "guest"}, {8, "host"}} {
		if _, err := svc.UpdatePolicy(t.Context(), "room-1", who.user, who.profile, GuestControlPolicyGuestPlayPause); !errors.Is(err, ErrRoomForbidden) {
			t.Fatalf("authority: %v", err)
		}
	}
	conn := new(recordingConn)
	svc.rooms["room-1"].members["guest"] = &memberState{userID: 8, profileID: "guest", connection: conn}
	got, err := svc.UpdatePolicy(t.Context(), "room-1", 7, "host", GuestControlPolicyGuestPlayPause)
	if err != nil || got.GuestControlPolicy != GuestControlPolicyGuestPlayPause || len(conn.payloads) != 1 {
		t.Fatalf("policy/dispatch: %v", err)
	}
	competing := &competingPolicyRepo{stubRepo: repo}
	svc.repo = competing
	before := len(conn.payloads)
	got, err = svc.UpdatePolicy(t.Context(), "room-1", 7, "host", GuestControlPolicyGuestPlayPause)
	if err != nil || got.GuestControlPolicy != GuestControlPolicyHostOnly || competing.calls != 1 || len(conn.payloads) != before {
		t.Fatal("conflict did not retain authoritative winner without replay/broadcast")
	}
}

type savedGuestControl struct {
	policy  GuestControlPolicy
	readErr error
	saved   []GuestControlPolicy
}

func (p *savedGuestControl) GuestControlPolicy(context.Context, int, string) (GuestControlPolicy, error) {
	return p.policy, p.readErr
}

func (p *savedGuestControl) SaveGuestControlPolicy(_ context.Context, _ int, _ string, policy GuestControlPolicy) error {
	p.saved = append(p.saved, policy)
	return nil
}

func TestRoomStartsWithAndSavesHostGuestControlPolicy(t *testing.T) {
	for _, tc := range []struct {
		name  string
		saved savedGuestControl
		want  GuestControlPolicy
	}{
		{"saved_guest_play_pause", savedGuestControl{policy: GuestControlPolicyGuestPlayPause}, GuestControlPolicyGuestPlayPause},
		{"saved_host_only", savedGuestControl{policy: GuestControlPolicyHostOnly}, GuestControlPolicyHostOnly},
		{"unknown_value", savedGuestControl{policy: "everyone"}, GuestControlPolicyHostOnly},
		{"read_failure", savedGuestControl{policy: GuestControlPolicyGuestPlayPause, readErr: errors.New("store down")}, GuestControlPolicyHostOnly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			repo := &roomCreationStore{result: baseRoom(now)}
			s := newServiceForTest(now, &stubRepo{room: baseRoom(now)}, nil, nil, nil)
			s.repo = repo
			s.SetGuestControlPreferences(&tc.saved)
			_, err := s.CreateRoomWithIdentity(t.Context(), "00000000-0000-4000-8000-000000000003",
				CreateRoomInput{HostUserID: 7, HostProfileID: "host", SelectionMode: RoomSelectionModeHostPick})
			if err != nil || len(repo.inputs) != 1 || repo.inputs[0].GuestControlPolicy != tc.want {
				t.Fatalf("created policy = %+v, err %v; want %s", repo.inputs, err, tc.want)
			}
		})
	}

	now := time.Now()
	repo := &stubRepo{room: baseRoom(now)}
	svc := newServiceForTest(now, repo, nil, nil, nil)
	saved := &savedGuestControl{}
	svc.SetGuestControlPreferences(saved)
	if _, err := svc.UpdatePolicyAndRemember(t.Context(), "room-1", 8, "guest", GuestControlPolicyGuestPlayPause); !errors.Is(err, ErrRoomForbidden) {
		t.Fatalf("guest policy change: %v", err)
	}
	// The frozen v1 route changes the room without saving the choice.
	if _, err := svc.UpdatePolicy(t.Context(), "room-1", 7, "host", GuestControlPolicyHostOnly); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdatePolicyAndRemember(t.Context(), "room-1", 7, "host", GuestControlPolicyGuestPlayPause); err != nil {
		t.Fatal(err)
	}
	svc.repo = &competingPolicyRepo{stubRepo: repo}
	if _, err := svc.UpdatePolicyAndRemember(t.Context(), "room-1", 7, "host", GuestControlPolicyHostOnly); err != nil {
		t.Fatal(err)
	}
	if len(saved.saved) != 1 || saved.saved[0] != GuestControlPolicyGuestPlayPause {
		t.Fatalf("saved = %v, want only the host's stored change", saved.saved)
	}
}

func TestGuestControlPolicySaveKeepsRoomsLatestPolicyPG(t *testing.T) {
	pool := selectionPG(t)
	repo := NewRepository(pool)
	room := baseRoom(time.Now().UTC())
	room.GuestControlPolicy = GuestControlPolicyGuestPlayPause
	if _, err := repo.CreateRoom(t.Context(), room); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var saved []GuestControlPolicy
	record := func(policy GuestControlPolicy) {
		mu.Lock()
		saved = append(saved, policy)
		mu.Unlock()
	}

	// Server A has read guest_play_pause and is slow to save it.
	aRead, releaseA, aDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseA) }) }
	// Runs before the pool closes, so a failed wait cannot leave A's
	// transaction holding the pool open.
	t.Cleanup(release)
	go func() {
		aDone <- repo.saveCommittedGuestControlPolicy(t.Context(), room.ID, 7, "host", func(policy GuestControlPolicy) error {
			close(aRead)
			<-releaseA
			record(policy)
			return nil
		})
	}()
	select {
	case <-aRead:
	case err := <-aDone:
		t.Fatalf("first save returned before reading the room: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("first save did not read the room")
	}
	// Server B commits host_only and saves while A is still pending.
	if _, err := pool.Exec(t.Context(), `UPDATE watch_together_rooms SET guest_control_policy='host_only' WHERE id=$1`, room.ID); err != nil {
		t.Fatal(err)
	}
	bDone := make(chan error, 1)
	go func() {
		bDone <- repo.saveCommittedGuestControlPolicy(t.Context(), room.ID, 7, "host", func(policy GuestControlPolicy) error {
			record(policy)
			return nil
		})
	}()
	// B waits on the profile's lock rather than reading ahead of A's save.
	deadline := time.Now().Add(5 * time.Second)
	for waiting := 0; waiting == 0; {
		if time.Now().After(deadline) {
			t.Fatal("second save did not wait for the first")
		}
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity
 WHERE application_name = current_setting('application_name') AND wait_event_type = 'Lock' AND wait_event = 'advisory'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
	}
	release()
	if err := errors.Join(<-aDone, <-bDone); err != nil {
		t.Fatal(err)
	}
	if len(saved) != 2 || saved[1] != GuestControlPolicyHostOnly {
		t.Fatalf("saves = %v, want the room's latest policy (host_only) saved last", saved)
	}
}
