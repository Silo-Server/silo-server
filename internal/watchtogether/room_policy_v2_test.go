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
