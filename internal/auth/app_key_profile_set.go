package auth

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"

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
	// Set, Pairs and EditsSeenRaw are the payload's members as sent, not yet
	// held to any rule: the caller validates them as it validates a set sent
	// in a session's body. Pairs and EditsSeenRaw are nil when absent;
	// EditsSeen is the string when edits_seen is one.
	Set          json.RawMessage
	Pairs        json.RawMessage
	EditsSeen    string
	EditsSeenRaw json.RawMessage
}

// VerifyProfileSet verifies a signed profile-set message with the person key
// its kid names and spends its challenge. The payload has no origin, so no
// origin rule applies. A payload addressed to another account is refused as
// a bad signature, like one for another server; so is an unknown or revoked
// key. Once the signature verifies, the key must still pass the rules a key
// sign-in runs (its provider identity, local passwords), and a disabled
// account and an administrator's account are refused. No session opens: the
// caller applies the set, and checks the account again under its own lock.
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
	// The key must still be good for signing in: the same provider and
	// local password rules a key sign-in runs, under the same locks. A
	// provider-bound key whose identity is gone is revoked here, as there.
	binding := KeyBinding{IdentityID: person.IdentityID, ProviderBound: person.ProviderBound}
	if _, err := a.service.keyProviderExpiry(ctx, person.UserID, binding); err != nil && !errors.Is(err, errKeyIdentityGone) {
		return nil, err
	}
	gone := false
	err = a.inTx(ctx, func(tx pgx.Tx) error {
		user, err := lockEnabledUser(ctx, tx, person.UserID)
		if err != nil {
			return err
		}
		if _, err := lockChain(ctx, tx, user, binding); errors.Is(err, errKeyIdentityGone) {
			gone = true
			return revokePersonKeyTx(ctx, tx, user.ID, person.ID)
		} else if err != nil {
			return err
		}
		locked, err := lockPersonKey(ctx, tx, user.ID, person.ID)
		if errors.Is(err, ErrAppKeyNotFound) || (err == nil && locked.RevokedAt != nil) {
			return ErrKeySignatureInvalid
		}
		if err != nil {
			return err
		}
		if !(KeyBinding{IdentityID: locked.IdentityID, ProviderBound: locked.ProviderBound}).same(binding) {
			return errKeyBindingChanged
		}
		if user.Role == models.RoleAdmin {
			return ErrProfileSetAdminAccount
		}
		return nil
	})
	if gone {
		return nil, ErrKeySignatureInvalid
	}
	if err != nil {
		return nil, err
	}
	return &ProfileSetAuthority{
		UserID: person.UserID, PersonKeyID: req.Envelope.KeyID,
		Set: payload.Set, Pairs: payload.Pairs, EditsSeen: payload.EditsSeen, EditsSeenRaw: payload.EditsSeenRaw,
	}, nil
}

// SessionMayManageKeys refuses a login session that may not manage the
// account's keys or read or send its profile set (SessionAllows with
// ActionManageKeys): a grant-enrolled app, a device sign-in session, an
// administrator acting as the account, an API key, a remote-playback
// handoff, and a session older than the record of how sessions are opened.
// The routes ask it before they look at the profile, so such a session is
// told it may not do this, not to switch profile.
func (a *AppKeySignIn) SessionMayManageKeys(ctx context.Context, claims *Claims) error {
	return SessionAllows(ctx, a.pool, claims, ActionManageKeys)
}
