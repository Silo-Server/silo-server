package watchtogether

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// guestControlSaveTimeout bounds the settings write that remembers a host's
// policy after its room change has committed.
const guestControlSaveTimeout = 5 * time.Second

// GuestControlPreferences stores the guest-control policy a host profile last
// chose, so the next room it hosts starts with it on every client. The
// settings contract owns the value (watch_party.guest_control_policy, profile
// scope).
type GuestControlPreferences interface {
	GuestControlPolicy(ctx context.Context, userID int, profileID string) (GuestControlPolicy, error)
	SaveGuestControlPolicy(ctx context.Context, userID int, profileID string, policy GuestControlPolicy) error
}

// SetGuestControlPreferences wires the host's saved policy. Without it every
// room starts host-only and a policy change is not remembered.
func (s *Service) SetGuestControlPreferences(preferences GuestControlPreferences) {
	s.guestControlPreferences = preferences
}

// initialGuestControlPolicy is the policy a new room hosted by the profile
// starts with. A failed read starts the room host-only, the narrower policy the
// host can widen from the room.
func (s *Service) initialGuestControlPolicy(ctx context.Context, userID int, profileID string) GuestControlPolicy {
	if s.guestControlPreferences == nil {
		return GuestControlPolicyHostOnly
	}
	policy, err := s.guestControlPreferences.GuestControlPolicy(ctx, userID, profileID)
	if err != nil {
		slog.WarnContext(ctx, "watch together saved guest control policy unavailable; starting host-only",
			"component", "watchtogether", "user_id", userID, "profile_id", profileID, "error", err)
		return GuestControlPolicyHostOnly
	}
	if policy != GuestControlPolicyGuestPlayPause {
		return GuestControlPolicyHostOnly
	}
	return policy
}

// rememberGuestControlPolicy saves the policy the host's room holds after a
// committed change. The room change has already succeeded, so a failed save is
// logged rather than returned: the host keeps the room they asked for and only
// the next room falls back to the earlier choice.
//
// It runs detached from the request and the room operation's deadline, so a
// client that disconnects after the commit still has its choice saved. With
// the shared repository it saves the room's committed policy under a
// cluster-wide lock on the profile's choice, so changes to the same room
// committed on different servers leave the room's latest policy saved.
//
// With the shared repository the save also runs on its own goroutine. Room
// operations call this after their commit but before they release the room
// and publish the change to other servers, so a slow settings store must not
// hold the host's request, the room's next command, or other servers' copies
// of the room. Reading the committed policy at save time keeps the outcome
// independent of the order the goroutines run in.
func (s *Service) rememberGuestControlPolicy(ctx context.Context, roomID string, userID int, profileID string, policy GuestControlPolicy) {
	if s.guestControlPreferences == nil {
		return
	}
	detached := context.WithoutCancel(ctx)
	save := func(ctx context.Context, policy GuestControlPolicy) error {
		return s.guestControlPreferences.SaveGuestControlPolicy(ctx, userID, profileID, policy)
	}
	logFailure := func(ctx context.Context, err error) {
		if err != nil {
			slog.WarnContext(ctx, "watch together guest control policy not saved for the next room",
				"component", "watchtogether", "room_id", roomID, "user_id", userID, "profile_id", profileID, "error", err)
		}
	}
	repo, shared := s.repo.(*Repository)
	if !shared {
		ctx, cancel := context.WithTimeout(detached, guestControlSaveTimeout)
		defer cancel()
		logFailure(ctx, save(ctx, policy))
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(detached, guestControlSaveTimeout)
		defer cancel()
		// Queue this server's saves for the profile here, so a burst of
		// changes waits without each holding a pooled connection on the
		// profile's advisory lock.
		unlock, err := s.guestControlSaveLocks.lock(ctx, fmt.Sprintf("%d:%s", userID, profileID))
		if err == nil {
			err = repo.saveCommittedGuestControlPolicy(ctx, roomID, userID, profileID, func(policy GuestControlPolicy) error {
				return save(ctx, policy)
			})
			unlock()
		}
		logFailure(ctx, err)
	}()
}

// profileLocks is a lock per key that a waiter can give up on when its
// context ends, and that frees its entry once nobody holds or waits for it.
type profileLocks struct {
	mu    sync.Mutex
	locks map[string]*profileLock
}

type profileLock struct {
	held  chan struct{}
	users int
}

func (l *profileLocks) lock(ctx context.Context, key string) (unlock func(), err error) {
	l.mu.Lock()
	if l.locks == nil {
		l.locks = make(map[string]*profileLock)
	}
	entry := l.locks[key]
	if entry == nil {
		entry = &profileLock{held: make(chan struct{}, 1)}
		l.locks[key] = entry
	}
	entry.users++
	l.mu.Unlock()
	release := func() {
		l.mu.Lock()
		if entry.users--; entry.users == 0 {
			delete(l.locks, key)
		}
		l.mu.Unlock()
	}
	select {
	case entry.held <- struct{}{}:
		return func() {
			<-entry.held
			release()
		}, nil
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
}
