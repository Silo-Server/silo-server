package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/models"
)

// ErrSessionNotAllowed refuses an account change from a caller that may not
// make it: a login session a device sign-in or another app opened, one older
// than the record of how sessions are opened, an API key, a remote-playback
// session, or an administrator acting as the account (SessionAllows).
var ErrSessionNotAllowed = errors.New("this sign-in cannot make that change")

// ErrActingAsAccount is ErrSessionNotAllowed for an administrator acting as
// the account. errors.Is matches both; a caller that tells the person what
// to do next checks this one first, since signing in again as the account
// is not open to the administrator.
var ErrActingAsAccount = fmt.Errorf("%w: an administrator acting as the account", ErrSessionNotAllowed)

// NotOwnSessionError is the refusal for claims that are not the account's
// own login session (Claims.IsOwnLoginSession): ErrActingAsAccount for an
// administrator acting as the account, ErrSessionNotAllowed otherwise.
func NotOwnSessionError(claims *Claims) error {
	if claims != nil && claims.ImpersonatorUserID != nil {
		return ErrActingAsAccount
	}
	return ErrSessionNotAllowed
}

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
	// ActionReadKeys: reading the account's key list or its waiting devices,
	// and denying a waiting device. The list names every device the person
	// signed in, which a TV or another app has no business seeing, and a TV
	// must not refuse the administrator's new phone.
	ActionReadKeys
	// ActionIssueCredential: handing the account a credential that outlives
	// the session asking for it, by a route older than app keys: a personal
	// API key, or linking a sign-in provider account.
	ActionIssueCredential
)

// SessionOrigin is how a login session was opened.
type SessionOrigin struct {
	// OpenedVia is one of the models.SessionOpened values; empty for a
	// session older than the record.
	OpenedVia string
	// DeviceKeyID is the device key that opened the session, or "".
	DeviceKeyID string
	// GrantKey reports that the device key was enrolled through a grant: it
	// belongs to another app the person allowed. A session naming a key whose
	// row is not stored counts too, failing closed.
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
	// A key row that is not stored counts as another app's, as in
	// LimitedToItself: nothing stored says the key was the person's own.
	origin.GrantKey = keyID != nil && (enrolledVia == nil || *enrolledVia == models.AppKeyEnrolledGrant)
	return origin, nil
}

// Allows reports whether a session opened this way may make the change.
//
// Every action refuses a session from a grant-enrolled app key: the app's
// access ends when its key is revoked, so it must not mint access that would
// outlive that, nor read the person's devices. ActionManageKeys and
// ActionReadKeys also refuse a session opened by device sign-in (a TV or
// shared device holds it, not the person's own app), and one with no record
// of how it was opened: only a session the person opened by password, by a
// sign-in provider or with their own device key passes. SessionAllows
// refuses an administrator acting as the account, an API key and a
// remote-playback session before this runs.
func (o SessionOrigin) Allows(action SessionAction) bool {
	if o.GrantKey {
		return false
	}
	if action == ActionApproveDeviceSignIn || action == ActionIssueCredential {
		// These predate the record of how sessions are opened: every
		// other session keeps what the operation's own rule gives it.
		return true
	}
	switch o.OpenedVia {
	case models.SessionOpenedPassword, models.SessionOpenedProvider, models.SessionOpenedAppKey:
		return true
	}
	return false
}

// SessionMayIssueCredential refuses ActionIssueCredential to a session a
// grant-enrolled app opened: an API key or a linked sign-in would outlive
// revoking the app's key. Every other caller is left to the operation's own
// rule, which already keeps out API keys and administrators acting as the
// account.
func SessionMayIssueCredential(ctx context.Context, db rowQuerier, claims *Claims) error {
	return refuseGrantSession(ctx, db, claims, ActionIssueCredential)
}

// refuseGrantSession answers ErrSessionNotAllowed when the claims' own login
// session may not take action, for the actions only a session another app
// opened is refused. A caller that is not the account's own login session
// passes.
func refuseGrantSession(ctx context.Context, db rowQuerier, claims *Claims, action SessionAction) error {
	if !claims.IsOwnLoginSession() {
		return nil
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

// SessionAllows refuses the change unless the claims are the account's own
// login session (Claims.IsOwnLoginSession) and the session was opened in a
// way that may make it (SessionOrigin.Allows). It is the one rule the
// operations that hand out lasting access call.
func SessionAllows(ctx context.Context, db rowQuerier, claims *Claims, action SessionAction) error {
	_, err := allowedSessionOrigin(ctx, db, claims, action)
	return err
}

// allowedSessionOrigin is SessionAllows that also answers how the session
// was opened, for a caller with one more thing to check about it.
func allowedSessionOrigin(ctx context.Context, db rowQuerier, claims *Claims, action SessionAction) (SessionOrigin, error) {
	if !claims.IsOwnLoginSession() {
		return SessionOrigin{}, NotOwnSessionError(claims)
	}
	origin, err := ReadSessionOrigin(ctx, db, claims)
	if err != nil {
		return SessionOrigin{}, err
	}
	if !origin.Allows(action) {
		return SessionOrigin{}, ErrSessionNotAllowed
	}
	return origin, nil
}

// SessionKeyAccess is what a login session may do with the account's app
// keys, for a page deciding what to offer before it asks.
type SessionKeyAccess struct {
	// List: ActionReadKeys.
	List bool
	// Manage: ActionManageKeys.
	Manage bool
	// SignInAgain: the session is refused for how it was opened (a device
	// sign-in, or no record of how), and a fresh sign-in in the same
	// browser would pass: the account has a local password the server
	// accepts, or a sign-in provider account. A person with neither only
	// ever gets a browser session by device sign-in, so offering it would be
	// a dead end.
	SignInAgain bool
}

// ReadSessionKeyAccess answers SessionKeyAccess for the claims. An API key,
// an administrator acting as the account, a remote-playback session and a
// session a grant-enrolled app opened may do none of it, and signing in
// again would not change that.
func ReadSessionKeyAccess(ctx context.Context, db rowQuerier, claims *Claims) (SessionKeyAccess, error) {
	if !claims.IsOwnLoginSession() {
		return SessionKeyAccess{}, nil
	}
	origin, err := ReadSessionOrigin(ctx, db, claims)
	if err != nil {
		return SessionKeyAccess{}, err
	}
	out := SessionKeyAccess{List: origin.Allows(ActionReadKeys), Manage: origin.Allows(ActionManageKeys)}
	if origin.GrantKey || (origin.OpenedVia != "" && origin.OpenedVia != models.SessionOpenedDeviceLogin) {
		return out, nil
	}
	out.SignInAgain, err = accountHasOtherSignIn(ctx, db, claims.UserID)
	return out, err
}

// accountHasOtherSignIn reports whether the account can sign in in a browser
// without device sign-in: a local password the server accepts for it (the
// server allows local passwords, or the account is break-glass), or any
// sign-in provider account.
func accountHasOtherSignIn(ctx context.Context, db rowQuerier, userID int) (bool, error) {
	allowed, err := localPasswordLoginAllowed(ctx, db)
	if err != nil {
		return false, err
	}
	var yes bool
	err = db.QueryRow(ctx, `SELECT
		(u.local_password_login_enabled AND COALESCE(u.password_hash, '') <> '' AND ($2 OR u.break_glass))
		OR EXISTS (SELECT 1 FROM plugin_auth_identities i WHERE i.user_id = u.id)
		FROM users u WHERE u.id = $1`, userID, allowed).Scan(&yes)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrSessionRevoked
	}
	if err != nil {
		return false, fmt.Errorf("reading the account's sign-in methods: %w", err)
	}
	return yes, nil
}
