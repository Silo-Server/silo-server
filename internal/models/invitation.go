package models

import "time"

// Invitation statuses, derived from lifecycle timestamps at read time.
const (
	InvitationStatusPending  = "pending"
	InvitationStatusAccepted = "accepted"
	InvitationStatusExpired  = "expired"
	InvitationStatusRevoked  = "revoked"
)

// Invitation is a single-use, email-bound claim token carrying the access
// the admin chose at send time. No users row exists until it is accepted.
type Invitation struct {
	ID            int64
	Email         string
	TokenHash     string
	Role          string
	AccessGroupID *int64
	LibraryIDs    []int // nil = all libraries
	CreateProfile bool
	ShowTour      bool
	Note          string
	InvitedBy     int64
	// InvitedByName is the inviter's display name, joined at read time for
	// list views and the claim screen. Not a column.
	InvitedByName  string
	ExpiresAt      time.Time
	AcceptedAt     *time.Time
	AcceptedUserID *int64
	RevokedAt      *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Status derives the lifecycle state from the timestamps. Revoked wins over
// accepted (a revoked row was never accepted — Accept refuses revoked rows),
// and accepted wins over expired: acceptance froze the row's fate before the
// clock ran out.
func (i *Invitation) Status(now time.Time) string {
	switch {
	case i.RevokedAt != nil:
		return InvitationStatusRevoked
	case i.AcceptedAt != nil:
		return InvitationStatusAccepted
	case now.After(i.ExpiresAt):
		return InvitationStatusExpired
	default:
		return InvitationStatusPending
	}
}

// CreateInvitationInput carries the admin's choices for a new invitation.
type CreateInvitationInput struct {
	Email         string
	Role          string
	AccessGroupID *int64
	LibraryIDs    []int
	CreateProfile bool
	ShowTour      bool
	Note          string
	InvitedBy     int64
	ExpiresAt     time.Time
}
