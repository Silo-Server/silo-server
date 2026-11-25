package auth

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/models"
)

// Refusals of app key sign-in (docs/architecture/app-keys.md).
var (
	// ErrKeySignatureInvalid refuses a request signed by a key this server
	// doesn't hold, or holds revoked. Signature failures themselves are
	// *appkeys.Error.
	ErrKeySignatureInvalid = errors.New("app key unknown or revoked")
	// ErrKeyOriginMismatch refuses a signed origin the server knows to be
	// wrong for the address the request arrived on.
	ErrKeyOriginMismatch = errors.New("signed origin is not the address the request arrived on")
	// ErrAppKeyDenied refuses a device key an administrator denied, or one
	// that waited for approval longer than appkeys.PendingKeyLifetime.
	ErrAppKeyDenied = errors.New("device key denied")
	// ErrProviderSignInRequired refuses a provider-bound key whose identity
	// row is gone, or whose provider can't re-check the identity and last
	// vouched for it longer ago than a session may live. The person signs in
	// through the provider once, and the key works again.
	ErrProviderSignInRequired = errors.New("sign in through the provider again")
	// ErrPersonKeyConflict refuses enrolling a person key on an account that
	// holds a different one.
	ErrPersonKeyConflict = errors.New("account has a different person key")
)

// KeyRetryError refuses a waiting device key that repeats sign-in sooner
// than appkeys.PendingRetryInterval.
type KeyRetryError struct {
	RetryAfter time.Duration
}

func (e *KeyRetryError) Error() string {
	return fmt.Sprintf("device key is waiting for approval; retry in %s", e.RetryAfter)
}

// KeyRequest is one signed request as a route received it.
type KeyRequest struct {
	Envelope appkeys.Envelope
	// ServerID is this server's identity, which the payload must name.
	ServerID string
	// CheckOrigin refuses, with ErrKeyOriginMismatch, a signed origin the
	// server knows to be wrong. Nil accepts every origin.
	CheckOrigin func(origin string) error
	IP          string
}

// KeyEnrollRequest is a signed enroll from a login session: the person key
// travels beside the envelope.
type KeyEnrollRequest struct {
	KeyRequest
	PersonKey appkeys.PublicKey
	Claims    *Claims
}

// KeySignInResult is the answer a route seals to the request's reply key.
type KeySignInResult struct {
	// Status is appkeys.ReplySignedIn or appkeys.ReplyApprovalRequired.
	Status   string
	DeviceID string
	User     *models.User
	// Pair is the session's tokens; nil while approval is required.
	Pair *TokenPair
	// PersonKeyID is the person key's ID (kid), set by enroll.
	PersonKeyID string
	// ReplyKey and Challenge seal the answer.
	ReplyKey  appkeys.PublicKey
	Challenge []byte
}

// AppKeySignIn signs devices in with app keys: it verifies signed requests,
// spends their challenges, enrolls keys and opens key sessions.
type AppKeySignIn struct {
	pool       *pgxpool.Pool
	keys       *AppKeyRepository
	challenges *KeyChallenges
	service    *Service
	now        func() time.Time
}

// NewAppKeySignIn returns the app key sign-in service. service opens the
// sessions.
func NewAppKeySignIn(pool *pgxpool.Pool, service *Service) *AppKeySignIn {
	return &AppKeySignIn{
		pool: pool, keys: NewAppKeyRepository(pool), challenges: NewKeyChallenges(pool), service: service, now: time.Now,
	}
}

// AppKeySignInEnabled reads the auth.app_key_sign_in switch.
func (a *AppKeySignIn) AppKeySignInEnabled(ctx context.Context) (bool, error) {
	return a.keys.AppKeySignInEnabled(ctx)
}

// IssueChallenge stores a new challenge and answers it with the server's
// clock in seconds, which the device stamps its iat and exp from.
func (a *AppKeySignIn) IssueChallenge(ctx context.Context) (string, int64, error) {
	challenge, err := a.challenges.Issue(ctx)
	if err != nil {
		return "", 0, err
	}
	return challenge, a.now().Unix(), nil
}

// verify runs every check of req for purpose by key, spends the challenge
// the signer committed to, and applies the origin rule. A challenge is spent
// once the signature verifies, even when a later check refuses, and never
// when the signature fails.
func (a *AppKeySignIn) verify(ctx context.Context, req KeyRequest, purpose appkeys.Purpose, key appkeys.PublicKey) (*appkeys.Payload, error) {
	payload, err := appkeys.Verify(req.Envelope, purpose, key, req.ServerID, a.now())
	if payload == nil {
		return nil, err
	}
	if payload.Challenge != nil {
		if spendErr := a.challenges.Spend(ctx, payload.Challenge); err == nil {
			err = spendErr
		}
	}
	if err != nil {
		return nil, err
	}
	if req.CheckOrigin != nil {
		if err := req.CheckOrigin(payload.Origin); err != nil {
			return nil, err
		}
	}
	return payload, nil
}

// inTx runs fn in one transaction.
func (a *AppKeySignIn) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, a.pool, fn)
}

// lockEnabledUser locks the account row and refuses a disabled account.
func lockEnabledUser(ctx context.Context, tx pgx.Tx, userID int) (*models.User, error) {
	user, err := lockUser(ctx, tx, userID)
	if err != nil {
		return nil, err
	}
	if !user.Enabled {
		return nil, ErrUserDisabled
	}
	return user, nil
}

// signedIn finishes a result whose session opened, after its commit.
func (a *AppKeySignIn) signedIn(ctx context.Context, out *KeySignInResult) *KeySignInResult {
	if out.Pair != nil {
		a.service.trustSignInSource(ctx, out.User)
	}
	return out
}

// Introduce enrolls the device key in an introduce signed by the account's
// person key, and opens its session. An admin account's key is stored
// waiting for approval instead, and no session opens.
func (a *AppKeySignIn) Introduce(ctx context.Context, req KeyRequest) (*KeySignInResult, error) {
	if err := req.Envelope.CheckShape(appkeys.PurposeIntroduce); err != nil {
		return nil, err
	}
	person, err := a.keys.PersonKeyByKeyID(ctx, req.Envelope.KeyID)
	if errors.Is(err, ErrAppKeyNotFound) || (err == nil && person.RevokedAt != nil) {
		return nil, ErrKeySignatureInvalid
	}
	if err != nil {
		return nil, err
	}
	personPublic, err := appkeys.PublicKeyFromBytes(person.PublicKey)
	if err != nil {
		return nil, ErrKeySignatureInvalid
	}
	payload, err := a.verify(ctx, req, appkeys.PurposeIntroduce, personPublic)
	if err != nil {
		return nil, err
	}
	expiry, err := a.service.keyProviderExpiry(ctx, person.UserID, KeyBinding{IdentityID: person.IdentityID, ProviderBound: person.ProviderBound})
	if err != nil {
		return nil, err
	}
	out := &KeySignInResult{ReplyKey: *payload.ReplyKey, Challenge: payload.Challenge}
	err = a.inTx(ctx, func(tx pgx.Tx) error {
		user, err := lockEnabledUser(ctx, tx, person.UserID)
		if err != nil {
			return err
		}
		locked, err := lockPersonKey(ctx, tx, user.ID, person.ID)
		if err != nil || locked.RevokedAt != nil {
			return ErrKeySignatureInvalid
		}
		status := models.DeviceKeyStatusActive
		if user.Role == models.RoleAdmin {
			status = models.DeviceKeyStatusPending
		}
		key, err := insertDeviceKeyTx(ctx, tx, NewDeviceKey{
			UserID: user.ID, PublicKey: *payload.DeviceKey, Kind: models.DeviceKeyKindDevice,
			Name: payload.DeviceName, Platform: payload.DevicePlatform, Status: status,
			EnrolledVia: models.AppKeyEnrolledIntroduce, PersonKeyID: &locked.ID,
			IdentityID: locked.IdentityID, ProviderBound: locked.ProviderBound,
		})
		if err != nil {
			return err
		}
		out.User, out.DeviceID = user, key.ID
		if status == models.DeviceKeyStatusPending {
			return a.holdForApproval(ctx, tx, user, key, out)
		}
		out.Status = appkeys.ReplySignedIn
		out.Pair, err = a.service.openKeySessionTx(ctx, tx, user, key, KeySession{IP: req.IP, Expiry: expiry})
		return err
	})
	if err != nil {
		return nil, err
	}
	return a.signedIn(ctx, out), nil
}

// holdForApproval answers approval_required for a waiting key that could
// sign in once approved, and records the attempt for the retry interval. A
// key the provider or local password rules would refuse is refused now.
func (a *AppKeySignIn) holdForApproval(ctx context.Context, tx pgx.Tx, user *models.User, key *models.AppDeviceKey, out *KeySignInResult) error {
	if _, _, err := keySignInPolicy(ctx, tx, user, bindingOfDeviceKey(key)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE auth_device_keys SET last_used_at = NOW() WHERE id = $1`, key.ID); err != nil {
		return fmt.Errorf("recording device key attempt: %w", err)
	}
	out.Status = appkeys.ReplyApprovalRequired
	return nil
}

// SignIn opens a session with an enrolled device key. A waiting key answers
// approval_required, no more often than appkeys.PendingRetryInterval and for
// at most appkeys.PendingKeyLifetime; a denied one ErrAppKeyDenied.
func (a *AppKeySignIn) SignIn(ctx context.Context, req KeyRequest) (*KeySignInResult, error) {
	if err := req.Envelope.CheckShape(appkeys.PurposeSignIn); err != nil {
		return nil, err
	}
	device, err := a.keys.DeviceKeyByKeyID(ctx, req.Envelope.KeyID)
	if errors.Is(err, ErrAppKeyNotFound) || (err == nil && device.RevokedAt != nil) {
		return nil, ErrKeySignatureInvalid
	}
	if err != nil {
		return nil, err
	}
	public, err := appkeys.PublicKeyFromBytes(device.PublicKey)
	if err != nil {
		return nil, ErrKeySignatureInvalid
	}
	payload, err := a.verify(ctx, req, appkeys.PurposeSignIn, public)
	if err != nil {
		return nil, err
	}
	expiry, err := a.service.keyProviderExpiry(ctx, device.UserID, bindingOfDeviceKey(device))
	if err != nil {
		return nil, err
	}
	out := &KeySignInResult{ReplyKey: *payload.ReplyKey, Challenge: payload.Challenge, DeviceID: device.ID}
	err = a.inTx(ctx, func(tx pgx.Tx) error {
		user, err := lockEnabledUser(ctx, tx, device.UserID)
		if err != nil {
			return err
		}
		key, err := lockDeviceKey(ctx, tx, user.ID, device.ID)
		if err != nil || key.RevokedAt != nil {
			return ErrKeySignatureInvalid
		}
		out.User = user
		switch key.Status {
		case models.DeviceKeyStatusDenied:
			return ErrAppKeyDenied
		case models.DeviceKeyStatusPending:
			if err := pendingRetryAllowed(ctx, tx, key); err != nil {
				return err
			}
			return a.holdForApproval(ctx, tx, user, key, out)
		}
		out.Status = appkeys.ReplySignedIn
		out.Pair, err = a.service.openKeySessionTx(ctx, tx, user, key, KeySession{IP: req.IP, Expiry: expiry})
		return err
	})
	if err != nil {
		return nil, err
	}
	return a.signedIn(ctx, out), nil
}

// pendingRetryAllowed refuses a waiting key past its lifetime, and one that
// tried again sooner than the retry interval. Both are measured on the
// database's clock, the one that recorded the key's attempts.
func pendingRetryAllowed(ctx context.Context, tx pgx.Tx, key *models.AppDeviceKey) error {
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT NOW()`).Scan(&now); err != nil {
		return fmt.Errorf("reading the clock: %w", err)
	}
	if now.Sub(key.CreatedAt) >= appkeys.PendingKeyLifetime {
		return ErrAppKeyDenied
	}
	if key.LastUsedAt != nil {
		if wait := appkeys.PendingRetryInterval - now.Sub(*key.LastUsedAt); wait > 0 {
			return &KeyRetryError{RetryAfter: time.Duration(math.Ceil(wait.Seconds())) * time.Second}
		}
	}
	return nil
}

// Enroll registers the person key beside an enroll envelope (when the
// account has none) and the device key in it, from the account's own login
// session, and opens a session bound to the device key. Both keys take the
// enrolling session's provider identity, read under the account lock.
func (a *AppKeySignIn) Enroll(ctx context.Context, req KeyEnrollRequest) (*KeySignInResult, error) {
	if err := SessionAllows(ctx, a.pool, req.Claims, ActionManageKeys); err != nil {
		return nil, err
	}
	if err := req.Envelope.CheckShape(appkeys.PurposeEnroll); err != nil {
		return nil, err
	}
	payload, err := a.verify(ctx, req.KeyRequest, appkeys.PurposeEnroll, req.PersonKey)
	if err != nil {
		return nil, err
	}
	enrolling, err := sessionProviderChainOf(ctx, a.pool, req.Claims.SessionID, req.Claims.UserID)
	if err != nil {
		return nil, err
	}
	expiry, err := a.service.keyProviderExpiry(ctx, req.Claims.UserID, enrolling.binding())
	if err != nil {
		return nil, err
	}
	out := &KeySignInResult{ReplyKey: *payload.ReplyKey, Challenge: payload.Challenge, PersonKeyID: req.PersonKey.KeyID()}
	err = a.inTx(ctx, func(tx pgx.Tx) error {
		user, err := lockEnabledUser(ctx, tx, req.Claims.UserID)
		if err != nil {
			return err
		}
		chain, err := sessionProviderChainOf(ctx, tx, req.Claims.SessionID, user.ID)
		if err != nil {
			return err
		}
		if !chain.binding().same(enrolling.binding()) {
			// The session moved under another identity since the check.
			return ErrSessionRevoked
		}
		bound := chain.binding().ProviderBound
		person, err := enrollPersonKeyTx(ctx, tx, user.ID, req.PersonKey, chain.identityID, bound)
		if err != nil {
			return err
		}
		key, err := insertDeviceKeyTx(ctx, tx, NewDeviceKey{
			UserID: user.ID, PublicKey: *payload.DeviceKey, Kind: models.DeviceKeyKindDevice,
			Name: payload.DeviceName, Platform: payload.DevicePlatform,
			EnrolledVia: models.AppKeyEnrolledSession, PersonKeyID: &person.ID,
			IdentityID: chain.identityID, ProviderBound: bound,
		})
		if err != nil {
			return err
		}
		out.User, out.DeviceID, out.Status = user, key.ID, appkeys.ReplySignedIn
		out.Pair, err = a.service.openKeySessionTx(ctx, tx, user, key, KeySession{IP: req.IP, Expiry: expiry})
		return err
	})
	if err != nil {
		return nil, err
	}
	return a.signedIn(ctx, out), nil
}

// binding is the provider binding keys enrolled from this session take: its
// identity, and whether it came from a provider at all (a session whose
// identity row is gone keeps provider_since).
func (c sessionProviderChain) binding() KeyBinding {
	return KeyBinding{IdentityID: c.identityID, ProviderBound: c.identityID != nil || c.providerSince != nil}
}

// same reports whether two bindings name the same identity.
func (b KeyBinding) same(other KeyBinding) bool {
	if b.ProviderBound != other.ProviderBound || (b.IdentityID == nil) != (other.IdentityID == nil) {
		return false
	}
	return b.IdentityID == nil || *b.IdentityID == *other.IdentityID
}

// livePersonKeyTx locks the account's live person key, or answers nil. tx
// holds the account row lock.
func livePersonKeyTx(ctx context.Context, tx pgx.Tx, userID int) (*models.AppPersonKey, error) {
	key, err := scanPersonKey(tx.QueryRow(ctx, `SELECT `+personKeyColumns+` FROM auth_person_keys
		WHERE user_id = $1 AND revoked_at IS NULL FOR UPDATE`, userID))
	if errors.Is(err, ErrAppKeyNotFound) {
		return nil, nil
	}
	return key, err
}

// enrollPersonKeyTx returns the account's person key when it is key, or
// enrolls key when the account has none. A different live person key is
// ErrPersonKeyConflict.
func enrollPersonKeyTx(ctx context.Context, tx pgx.Tx, userID int, key appkeys.PublicKey, identityID *int64, bound bool) (*models.AppPersonKey, error) {
	current, err := livePersonKeyTx(ctx, tx, userID)
	if err != nil {
		return nil, err
	}
	if current != nil {
		if current.KeyID != key.KeyID() {
			return nil, ErrPersonKeyConflict
		}
		return current, nil
	}
	return insertPersonKeyTx(ctx, tx, NewPersonKey{
		UserID: userID, PublicKey: key, IdentityID: identityID, ProviderBound: bound, EnrolledVia: models.AppKeyEnrolledSession,
	})
}
