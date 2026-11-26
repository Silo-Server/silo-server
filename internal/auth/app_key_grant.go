package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/models"
)

// ErrGrantInvalid refuses a grant that was already redeemed, that names an
// account with no live person key or a person key other than the one that
// signed it, or an account that is disabled or an administrator's. The
// verifier's own grant refusals are *appkeys.Error.
var ErrGrantInvalid = errors.New("grant used, or not valid for this account")

// grantSigner is the person key the verifier looked up for a grant, and a
// store failure the lookup met: the verifier can only answer that the grant
// does not verify, and a failure to ask must not look like that. missed is
// an account with no person key to check the grant against.
type grantSigner struct {
	key    *models.AppPersonKey
	err    error
	missed bool
}

// lookup finds the unrevoked person key of the account a grant names.
// accountID is the account's wire ID, a decimal number in its one spelling.
// A provider-bound key whose identity is gone is found too: the redemption's
// transaction revokes it, as its next use anywhere does.
func (s *grantSigner) lookup(ctx context.Context, keys *AppKeyRepository) appkeys.PersonKeyLookup {
	return func(accountID string) (appkeys.PublicKey, error) {
		s.missed = true
		userID, err := strconv.Atoi(accountID)
		if err != nil || userID <= 0 || strconv.Itoa(userID) != accountID {
			return appkeys.PublicKey{}, ErrGrantInvalid
		}
		key, err := keys.UnrevokedPersonKey(ctx, userID)
		if err != nil {
			if !errors.Is(err, ErrAppKeyNotFound) {
				s.err = err
			}
			return appkeys.PublicKey{}, ErrGrantInvalid
		}
		public, err := appkeys.PublicKeyFromBytes(key.PublicKey)
		if err != nil {
			return appkeys.PublicKey{}, ErrGrantInvalid
		}
		s.key, s.missed = key, false
		return public, nil
	}
}

// RedeemGrant enrolls another app's key with a grant the account's person
// key signed, and opens the app's session (docs/architecture/app-keys.md,
// "Grants"). The grant-redeem envelope is signed by the app's key, the one
// the grant names, and carries the grant. The challenge is spent once that
// outer signature verifies. The key enrolled is an app key that takes the
// provider binding of the person key that signed the grant, so the provider
// rule applies to the app as to the person's own devices. A grant works
// once: its ID is recorded in the transaction that enrolls the key.
func (a *AppKeySignIn) RedeemGrant(ctx context.Context, req KeyRequest) (*KeySignInResult, error) {
	signer := &grantSigner{}
	payload, grant, err := appkeys.VerifyGrantRedeem(req.Envelope, req.ServerID, a.now(), signer.lookup(ctx, a.keys))
	if payload == nil {
		return nil, err
	}
	if payload.Challenge != nil {
		if spendErr := a.challenges.Spend(ctx, payload.Challenge); err == nil {
			err = spendErr
		}
	}
	if signer.err != nil {
		return nil, signer.err
	}
	if signer.missed && payload.Grant != nil {
		// No person key to check the grant against: check it against a key
		// nobody holds, so this refusal takes about as long as a grant that
		// fails its signature and the time doesn't say which accounts have a
		// person key.
		appkeys.DecoyVerify(*payload.Grant, appkeys.PurposeGrant)
	}
	if err != nil {
		return nil, err
	}
	if req.CheckOrigin != nil {
		if err := req.CheckOrigin(payload.Origin); err != nil {
			return nil, err
		}
	}
	person := signer.key
	binding := KeyBinding{IdentityID: person.IdentityID, ProviderBound: person.ProviderBound}
	// The provider rule of the person key that signed the grant: a person
	// key whose identity is gone is revoked in the transaction and the grant
	// refused; the other provider answers are the ones a sign-in gets.
	expiry, err := a.service.keyProviderExpiry(ctx, person.UserID, binding)
	if err != nil && !errors.Is(err, errKeyIdentityGone) {
		return nil, err
	}
	name := payload.DeviceName
	if name == "" {
		name = grant.AppName
	}
	gone := false
	var audit *auditmutation.Entry
	out := &KeySignInResult{ReplyKey: *payload.ReplyKey, Challenge: payload.Challenge}
	err = a.inTx(ctx, func(tx pgx.Tx) error {
		user, err := lockUser(ctx, tx, person.UserID)
		if err != nil {
			return err
		}
		if !user.Enabled || user.Role == models.RoleAdmin {
			return ErrGrantInvalid
		}
		chain, err := lockChain(ctx, tx, user, binding)
		if errors.Is(err, errKeyIdentityGone) {
			gone = true
			return revokePersonKeyTx(ctx, tx, user.ID, person.ID)
		}
		if err != nil {
			return err
		}
		locked, err := lockPersonKey(ctx, tx, user.ID, person.ID)
		if errors.Is(err, ErrAppKeyNotFound) || (err == nil && locked.RevokedAt != nil) {
			return ErrGrantInvalid
		}
		if err != nil {
			return err
		}
		if !(KeyBinding{IdentityID: locked.IdentityID, ProviderBound: locked.ProviderBound}).same(binding) {
			return errKeyBindingChanged
		}
		if err := claimGrantTx(ctx, tx, user.ID, grant); err != nil {
			return err
		}
		key, err := insertDeviceKeyTx(ctx, tx, NewDeviceKey{
			UserID: user.ID, PublicKey: *grant.AppKey, Kind: models.DeviceKeyKindApp,
			Name: name, Platform: payload.DevicePlatform,
			EnrolledVia: models.AppKeyEnrolledGrant, PersonKeyID: &locked.ID,
			IdentityID: locked.IdentityID, ProviderBound: locked.ProviderBound, IP: req.IP,
		})
		if err != nil {
			return err
		}
		out.User, out.DeviceID, out.Status, out.GrantedApp = user, key.ID, appkeys.ReplySignedIn, true
		out.Pair, err = a.service.openKeySessionTx(ctx, tx, user, key, chain.session(req.IP, expiry))
		if err != nil {
			return err
		}
		audit, err = recordKeyAdded(ctx, tx, auditmutation.ActionAppGrantRedeemed, user.ID, http.StatusOK, keyAdded{}.withDevice(key))
		return err
	})
	if err != nil {
		return nil, err
	}
	if gone {
		return nil, ErrGrantInvalid
	}
	auditmutation.CommitMutation(ctx, audit)
	return a.signedIn(ctx, out), nil
}

// claimGrantTx records the account's grant as used. Of two redemptions of
// one grant, on any API processes, the second finds the row and is refused.
// The row is kept past the grant's exp, until the retention step deletes it.
// tx holds the account row lock.
func claimGrantTx(ctx context.Context, tx pgx.Tx, userID int, grant *appkeys.Payload) error {
	tag, err := tx.Exec(ctx, `INSERT INTO auth_grant_uses (user_id, grant_id, expires_at)
		VALUES ($1, $2, to_timestamp($3)) ON CONFLICT (user_id, grant_id) DO NOTHING`, userID, grant.GrantID, grant.ExpiresAt)
	if err != nil {
		return fmt.Errorf("recording grant use: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrGrantInvalid
	}
	return nil
}
