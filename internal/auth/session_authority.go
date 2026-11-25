package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/models"
)

// ErrSessionNotAllowed refuses an account change from a login session that
// may not make it: one a device sign-in or another app opened, or one older
// than the record of how sessions are opened (SessionAllows).
var ErrSessionNotAllowed = errors.New("this sign-in cannot make that change")

// SessionAction is an account change that only some login sessions may make.
type SessionAction int

const (
	// ActionManageKeys: enrolling app keys, replacing the person key, and the
	// changes that lead to lasting access in the same way (minting an app
	// code, setting a first password, approving a waiting admin device or a
	// device sign-in in advance).
	ActionManageKeys SessionAction = iota + 1
	// ActionApproveDeviceSignIn: approving a device sign-in that is waiting
	// now.
	ActionApproveDeviceSignIn
)

// SessionOrigin is how a login session was opened.
type SessionOrigin struct {
	// OpenedVia is one of the models.SessionOpened values; empty for a
	// session older than the record.
	OpenedVia string
	// DeviceKeyID is the device key that opened the session, or "".
	DeviceKeyID string
	// GrantKey reports that the device key was enrolled through a grant: it
	// belongs to another app the person allowed.
	GrantKey bool
}

// ReadSessionOrigin reads how the claims' login session was opened.
func ReadSessionOrigin(ctx context.Context, db rowQuerier, claims *Claims) (SessionOrigin, error) {
	if claims == nil || claims.SessionID == "" {
		return SessionOrigin{}, ErrSessionNotAllowed
	}
	var origin SessionOrigin
	var openedVia, keyID, enrolledVia *string
	err := db.QueryRow(ctx, `SELECT s.opened_via, s.device_key_id::text, k.enrolled_via
		FROM auth_sessions s LEFT JOIN auth_device_keys k ON k.id = s.device_key_id
		WHERE s.id = $1 AND s.user_id = $2`, claims.SessionID, claims.UserID).Scan(&openedVia, &keyID, &enrolledVia)
	if errors.Is(err, pgx.ErrNoRows) {
		return SessionOrigin{}, ErrSessionRevoked
	}
	if err != nil {
		return SessionOrigin{}, fmt.Errorf("reading how the session was opened: %w", err)
	}
	if openedVia != nil {
		origin.OpenedVia = *openedVia
	}
	if keyID != nil {
		origin.DeviceKeyID = *keyID
	}
	origin.GrantKey = enrolledVia != nil && *enrolledVia == models.AppKeyEnrolledGrant
	return origin, nil
}

// Allows reports whether a session opened this way may make the change.
//
// Every action refuses a session from a grant-enrolled app key: the app's
// access ends when its key is revoked, so it must not mint access that would
// outlive that. ActionManageKeys also refuses a session opened by device
// sign-in (a TV or shared device holds it, not the person's own app), by an
// administrator acting as the account, and one with no record of how it was
// opened.
func (o SessionOrigin) Allows(action SessionAction) bool {
	if o.GrantKey {
		return false
	}
	if action == ActionApproveDeviceSignIn {
		return true
	}
	switch o.OpenedVia {
	case models.SessionOpenedPassword, models.SessionOpenedProvider, models.SessionOpenedAppKey:
		return true
	}
	return false
}

// SessionAllows refuses the change unless the claims are the account's own
// login session (Claims.IsOwnLoginSession) and the session was opened in a
// way that may make it (SessionOrigin.Allows). It is the one rule the
// operations that hand out lasting access call.
func SessionAllows(ctx context.Context, db rowQuerier, claims *Claims, action SessionAction) error {
	if !claims.IsOwnLoginSession() {
		return ErrSessionNotAllowed
	}
	origin, err := ReadSessionOrigin(ctx, db, claims)
	if err != nil {
		return err
	}
	if !origin.Allows(action) {
		return ErrSessionNotAllowed
	}
	return nil
}
