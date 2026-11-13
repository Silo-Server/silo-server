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

// UpdatePolicyAndRemember is UpdatePolicy that also saves the policy as the
// host profile's choice for its next room. The frozen v1 route keeps
// UpdatePolicy.
//
// The save runs after the room operation has committed and released the room,
// so a slow settings store delays only this request. It saves only when the
// room ended at the requested policy: a write that lost to a competing one
// with a different policy returns the winner's and saves nothing. Concurrent
// changes from different rooms or servers are last-writer-wins, the same as
// any other setting.
func (s *Service) UpdatePolicyAndRemember(ctx context.Context, roomID string, user int, profile string, policy GuestControlPolicy) (Snapshot, error) {
	snapshot, err := s.UpdatePolicy(ctx, roomID, user, profile, policy)
	if err == nil && snapshot.GuestControlPolicy == policy {
		s.rememberGuestControlPolicy(ctx, roomID, user, profile, policy)
	}
	return snapshot, err
}

// rememberGuestControlPolicy saves the host's policy for its next room. The
// room change has already succeeded, so a failed save is logged rather than
// returned: the host keeps the room they asked for and only the next room
// falls back to the earlier choice. It is detached from the request's
// cancellation, so a client that disconnects after the commit still has its
// choice saved.
func (s *Service) rememberGuestControlPolicy(ctx context.Context, roomID string, userID int, profileID string, policy GuestControlPolicy) {
	if s.guestControlPreferences == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), guestControlSaveTimeout)
	defer cancel()
	if err := s.guestControlPreferences.SaveGuestControlPolicy(ctx, userID, profileID, policy); err != nil {
		slog.WarnContext(ctx, "watch together guest control policy not saved for the next room",
			"component", "watchtogether", "room_id", roomID, "user_id", userID, "profile_id", profileID, "error", err)
	}
}
