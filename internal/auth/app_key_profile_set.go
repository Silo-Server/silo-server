package auth

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/models"
)

// ErrProfileSetAdminAccount refuses a signed profile set for an
// administrator's account: a person key alone never changes an admin's
// household. The account's own verified main profile still can.
var ErrProfileSetAdminAccount = errors.New("an administrator's profiles change only from a session verified as the main profile")

// ProfileSetAuthority is what a verified signed profile set authorizes: the
// account it changes and the set it carries, verbatim.
type ProfileSetAuthority struct {
	UserID      int
	PersonKeyID string
	Set         json.RawMessage
	Pairs       json.RawMessage
	EditsSeen   string
}

// VerifyProfileSet verifies a signed profile-set message with the person key
// its kid names and spends its challenge. The payload has no origin, so no
// origin rule applies. A payload addressed to another account is refused as
// a bad signature, like one for another server; so is an unknown or revoked
// key. A disabled account and an administrator's account are refused once
// the signature verifies. No session opens: the caller applies the set.
func (a *AppKeySignIn) VerifyProfileSet(ctx context.Context, req KeyRequest) (*ProfileSetAuthority, error) {
	if err := req.Envelope.CheckShape(appkeys.PurposeProfileSet); err != nil {
		return nil, err
	}
	person, err := a.keys.PersonKeyByKeyID(ctx, req.Envelope.KeyID)
	if errors.Is(err, ErrAppKeyNotFound) || (err == nil && person.RevokedAt != nil) {
		return nil, unknownKey(req.Envelope, appkeys.PurposeProfileSet)
	}
	if err != nil {
		return nil, err
	}
	personPublic, err := appkeys.PublicKeyFromBytes(person.PublicKey)
	if err != nil {
		return nil, ErrKeySignatureInvalid
	}
	req.CheckOrigin = nil
	payload, err := a.verify(ctx, req, appkeys.PurposeProfileSet, personPublic)
	if err != nil {
		return nil, err
	}
	if payload.AccountID != strconv.Itoa(person.UserID) {
		return nil, ErrKeySignatureInvalid
	}
	var role string
	var enabled bool
	if err := a.pool.QueryRow(ctx, `SELECT role, enabled FROM users WHERE id = $1`, person.UserID).Scan(&role, &enabled); err != nil {
		return nil, err
	}
	if !enabled {
		return nil, ErrUserDisabled
	}
	if role == models.RoleAdmin {
		return nil, ErrProfileSetAdminAccount
	}
	return &ProfileSetAuthority{
		UserID: person.UserID, PersonKeyID: req.Envelope.KeyID,
		Set: payload.Set, Pairs: payload.Pairs, EditsSeen: payload.EditsSeen,
	}, nil
}

// ProfileSetSessionAllowed refuses a login session that may not read or
// send the account's profile set: the rule for sessions that manage keys
// (SessionAllows with ActionManageKeys), which holds back a grant-enrolled
// app, a device sign-in session, an administrator acting as the account, an
// API key, a remote-playback handoff, and a session older than the record
// of how sessions are opened.
func (a *AppKeySignIn) ProfileSetSessionAllowed(ctx context.Context, claims *Claims) error {
	return SessionAllows(ctx, a.pool, claims, ActionManageKeys)
}
