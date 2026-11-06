package watchtogether

import (
	"context"
	"log/slog"
)

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

// saveGuestControlPolicy remembers a policy the host just applied to a room.
// The room change has already succeeded, so a failed save is logged rather
// than returned: the host keeps the room they asked for and only the next
// room falls back to the earlier choice.
func (s *Service) saveGuestControlPolicy(ctx context.Context, userID int, profileID string, policy GuestControlPolicy) {
	if s.guestControlPreferences == nil {
		return
	}
	if err := s.guestControlPreferences.SaveGuestControlPolicy(ctx, userID, profileID, policy); err != nil {
		slog.WarnContext(ctx, "watch together guest control policy not saved for the next room",
			"component", "watchtogether", "user_id", userID, "profile_id", profileID, "error", err)
	}
}
