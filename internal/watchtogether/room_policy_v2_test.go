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
	// A write that loses to a competing one with a different policy saves nothing.
	svc.repo = &competingPolicyRepo{stubRepo: repo}
	if _, err := svc.UpdatePolicyAndRemember(t.Context(), "room-1", 7, "host", GuestControlPolicyGuestPlayPause); err != nil {
		t.Fatal(err)
	}
	if len(saved.saved) != 1 || saved.saved[0] != GuestControlPolicyGuestPlayPause {
		t.Fatalf("saved = %v, want only the host's stored change", saved.saved)
	}
}

type blockingGuestControl struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *blockingGuestControl) unblock() { p.once.Do(func() { close(p.release) }) }

func (p *blockingGuestControl) GuestControlPolicy(context.Context, int, string) (GuestControlPolicy, error) {
	return GuestControlPolicyHostOnly, nil
}

func (p *blockingGuestControl) SaveGuestControlPolicy(ctx context.Context, _ int, _ string, _ GuestControlPolicy) error {
	select {
	case p.entered <- struct{}{}:
	default:
	}
	select {
	case <-p.release:
	case <-ctx.Done():
	}
	return ctx.Err()
}

func TestSlowGuestControlSaveDoesNotHoldTheRoomPG(t *testing.T) {
	pool := selectionPG(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	room := baseRoom(now)
	if _, err := NewRepository(pool).CreateRoom(t.Context(), room); err != nil {
		t.Fatal(err)
	}
	s := newServiceForTest(now, &stubRepo{room: room}, nil, nil, nil)
	s.repo = NewRepository(pool)
	s.now = func() time.Time { return now }
	t.Cleanup(s.Close)
	prefs := &blockingGuestControl{entered: make(chan struct{}, 1), release: make(chan struct{})}
	t.Cleanup(prefs.unblock)
	s.SetGuestControlPreferences(prefs)

	done := make(chan error, 1)
	go func() {
		_, err := s.UpdatePolicyAndRemember(t.Context(), room.ID, 7, "host", GuestControlPolicyGuestPlayPause)
		done <- err
	}()
	select {
	case <-prefs.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("save never started")
	}
	// The settings store is stuck. The room's next operation must not wait on
	// it; only the request that is saving does.
	other := make(chan error, 1)
	go func() {
		_, err := s.UpdatePolicy(t.Context(), room.ID, 7, "host", GuestControlPolicyHostOnly)
		other <- err
	}()
	select {
	case err := <-other:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("next room operation waited on the settings save")
	}
	prefs.unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("policy request did not return after the save")
	}
}
