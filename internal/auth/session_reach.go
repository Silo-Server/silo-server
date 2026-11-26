package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/models"
)

// ErrSessionOutOfReach refuses a session limited to itself (LimitedToItself)
// that tries to end another session of its account.
var ErrSessionOutOfReach = errors.New("a session opened by device sign-in or by another app can end only itself")

// tvDevicePlatforms are the X-Silo-Device-Platform and device sign-in
// device_platform values the TV apps send, lower-cased: Apple TV, Android TV
// and Roku.
var tvDevicePlatforms = []string{"tvos", "android-tv", "roku"}

// SessionDeviceKey is what a stored session records about the device key
// that opened it.
type SessionDeviceKey struct {
	// ID is auth_sessions.device_key_id, or "" when no device key opened
	// the session.
	ID string
	// EnrolledVia is the key's auth_device_keys.enrolled_via, or "" when
	// no key row with that ID is stored.
	EnrolledVia string
}

// LimitedToItself reports whether a login session may see and end only
// itself among its account's sessions. Two kinds of session are: one opened
// by device sign-in or Quick Connect, on a device the account holder may not
// control, such as a shared TV; and one opened by another app the person
// allowed, whose device key was enrolled through a grant. A session one of
// the person's own device keys opened is not.
//
// key decides first. A session whose key was enrolled through a grant is
// limited. Revoking such a key ends its sessions, and the database deletes
// a session with its key row, so a session naming a key that is not stored
// should not exist; if one does, it is limited, since nothing stored says
// the key was the person's own.
//
// Otherwise openedVia, the stored auth_sessions.opened_via, the record of
// how the session was opened, decides whenever it is recorded: limited when
// it is models.SessionOpenedDeviceLogin. Two kinds of session have none.
// One a server of the previous release wrote carries only
// viaDeviceSignIn, the stored auth_sessions.via_device_sign_in: true for a
// device sign-in, false for any other. That flag answers this question and
// no other: false does not say which other way the session was opened, so it
// unlocks nothing that needs a recorded openedVia (SessionOrigin.Allows).
// A session older than both columns has neither, and nothing recorded tells
// those apart, so it is limited when its recorded platform is a TV's, the
// devices that sign in by code; an old TV session opened with a password is
// limited too, which costs nothing, since no TV app reads more of the list
// than id and current or ends another session. An administrator acting as an
// account opens an ordinary session; an API-key caller has no session and is
// never limited.
func LimitedToItself(openedVia string, viaDeviceSignIn *bool, devicePlatform string, key SessionDeviceKey) bool {
	if key.ID != "" && (key.EnrolledVia == "" || key.EnrolledVia == models.AppKeyEnrolledGrant) {
		return true
	}
	if openedVia != "" {
		return openedVia == models.SessionOpenedDeviceLogin
	}
	if viaDeviceSignIn != nil {
		return *viaDeviceSignIn
	}
	platform := strings.ToLower(strings.TrimSpace(devicePlatform))
	return slices.Contains(tvDevicePlatforms, platform)
}

// limitedToItself applies LimitedToItself to the stored session, reading
// the session and its device key in one query. A session that is no longer
// stored is limited: it fails closed.
func (r *SessionRepository) limitedToItself(ctx context.Context, sessionID string) (bool, error) {
	var via *bool
	var openedVia, platform string
	var key SessionDeviceKey
	err := r.pool.QueryRow(ctx, `SELECT COALESCE(s.opened_via, ''), s.via_device_sign_in, COALESCE(s.device_platform, ''),
		COALESCE(s.device_key_id::text, ''), COALESCE(k.enrolled_via, '')
		FROM auth_sessions s LEFT JOIN auth_device_keys k ON k.id = s.device_key_id
		WHERE s.id = $1`, sessionID).Scan(&openedVia, &via, &platform, &key.ID, &key.EnrolledVia)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading session reach: %w", err)
	}
	return LimitedToItself(openedVia, via, platform, key), nil
}

// ListByUserPageByID is ListByUserPage in ascending id order, strictly after
// afterID when it is not empty. A session limited to itself pages with it:
// the order and the cursor then say nothing about when the other sessions
// were opened.
func (r *SessionRepository) ListByUserPageByID(ctx context.Context, userID int, afterID string, limit int) ([]*models.AuthSession, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+sessionColumns+` FROM auth_sessions
		WHERE user_id = $1 AND expires_at > NOW() AND revoked_at IS NULL
		AND ($2 = '' OR id > $2)
		ORDER BY id LIMIT $3`, userID, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing session page by id for user %d: %w", userID, err)
	}
	defer rows.Close()
	return scanSessions(rows)
}

// SessionLimitedToItself reports whether the login session sessionID is
// limited to itself (LimitedToItself). An empty sessionID, an API-key caller,
// is not.
func (s *Service) SessionLimitedToItself(ctx context.Context, sessionID string) (bool, error) {
	if sessionID == "" {
		return false, nil
	}
	return s.sessions.limitedToItself(ctx, sessionID)
}

// GetSessionsPageByID returns one page of the user's live sessions in id
// order; see SessionRepository.ListByUserPageByID.
func (s *Service) GetSessionsPageByID(ctx context.Context, userID int, afterID string, limit int) ([]*models.AuthSession, error) {
	return s.sessions.ListByUserPageByID(ctx, userID, afterID, limit)
}

// RevokeSessionFrom is RevokeSession called from the login session
// callerSessionID. A caller limited to itself may end only its own session:
// another session of the account is ErrSessionOutOfReach. A session of
// another account, or none, is ErrSessionNotFound for every caller, judged
// first, so the refusal says nothing about sessions outside the account.
func (s *Service) RevokeSessionFrom(ctx context.Context, callerSessionID, sessionID string, userID int) error {
	return s.RevokeSessionChecked(ctx, callerSessionID, sessionID, userID, nil)
}
