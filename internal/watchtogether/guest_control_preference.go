package watchtogether

import (
	"context"
	"log/slog"
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
func (s *Service) rememberGuestControlPolicy(ctx context.Context, roomID string, userID int, profileID string, policy GuestControlPolicy) {
	if s.guestControlPreferences == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), guestControlSaveTimeout)
	defer cancel()
	save := func(policy GuestControlPolicy) error {
		return s.guestControlPreferences.SaveGuestControlPolicy(ctx, userID, profileID, policy)
	}
	var err error
	if repo, shared := s.repo.(*Repository); shared {
		err = repo.saveCommittedGuestControlPolicy(ctx, roomID, userID, profileID, save)
	} else {
		err = save(policy)
	}
	if err != nil {
		slog.WarnContext(ctx, "watch together guest control policy not saved for the next room",
			"component", "watchtogether", "room_id", roomID, "user_id", userID, "profile_id", profileID, "error", err)
	}
}
