package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// ErrVisitorHostUnavailable refuses a visitor session for a host account that
// is gone or disabled.
var ErrVisitorHostUnavailable = errors.New("watch party host account is unavailable")

var errIncompleteVisitorSession = errors.New("incomplete watch party visitor session")

// WatchPartyVisitorSessionInput describes the login session a Watch Party
// visitor receives when it accepts an invite.
type WatchPartyVisitorSessionInput struct {
	SessionID     string
	HostUserID    int
	HostProfileID string
	RoomID        string
	VisitorID     string
	// EndsAt is the latest the session may last; refresh never extends it.
	EndsAt     time.Time
	DeviceName string
	IPAddress  string
}

// CheckWatchPartyHost refuses a visitor session for a host account that is
// gone or disabled. Call it before the acceptance transaction: it reads
// through the pool.
func (s *Service) CheckWatchPartyHost(ctx context.Context, hostUserID int) error {
	user, err := s.users.GetByID(ctx, hostUserID)
	if err != nil {
		if IsNotFound(err) {
			return ErrVisitorHostUnavailable
		}
		return fmt.Errorf("loading watch party host: %w", err)
	}
	if user == nil || !user.Enabled {
		return ErrVisitorHostUnavailable
	}
	return nil
}

// CreateWatchPartyVisitorSession inserts a visitor's login session through
// db, the caller's transaction, which must also insert the visitor row that
// names it (watch_together_visitors.auth_session_id): the session is only
// active while that row exists, its room is open and it was not removed.
// It uses no other connection. The session belongs to the host's account,
// acts only as the host's profile with the user role, and ends at EndsAt.
// Check the host with CheckWatchPartyHost first, and issue the session's
// tokens with WatchPartyVisitorTokens once the transaction commits.
func (s *Service) CreateWatchPartyVisitorSession(ctx context.Context, db sessionExecQuerier, in WatchPartyVisitorSessionInput) error {
	if in.SessionID == "" || in.HostUserID <= 0 || strings.TrimSpace(in.HostProfileID) == "" ||
		in.RoomID == "" || in.VisitorID == "" || !in.EndsAt.After(time.Now()) {
		return errIncompleteVisitorSession
	}
	endsAt := in.EndsAt
	return s.sessions.createWithQuerier(ctx, db, models.AuthSession{
		ID:                in.SessionID,
		UserID:            in.HostUserID,
		DeviceName:        in.DeviceName,
		IPAddress:         in.IPAddress,
		ExpiresAt:         endsAt,
		Purpose:           models.SessionPurposeWatchPartyVisitor,
		BoundProfileID:    in.HostProfileID,
		BoundRoomID:       in.RoomID,
		BoundVisitorID:    in.VisitorID,
		AbsoluteExpiresAt: &endsAt,
	})
}

// WatchPartyVisitorTokens issues the token pair of a visitor session that
// CreateWatchPartyVisitorSession created and its transaction committed.
func (s *Service) WatchPartyVisitorTokens(in WatchPartyVisitorSessionInput) (*TokenPair, error) {
	endsAt := in.EndsAt
	session := &models.AuthSession{
		ID:                in.SessionID,
		UserID:            in.HostUserID,
		Purpose:           models.SessionPurposeWatchPartyVisitor,
		BoundProfileID:    in.HostProfileID,
		BoundRoomID:       in.RoomID,
		BoundVisitorID:    in.VisitorID,
		AbsoluteExpiresAt: &endsAt,
	}
	return s.generateTokenPair(boundSessionClaims(Claims{UserID: in.HostUserID, Role: models.RoleUser, SessionID: in.SessionID}, session))
}
