package auth

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/auditmutation"
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
	// ErrProviderCheckIncomplete refuses a key sign-in whose provider re-check
	// got an answer this server could not apply; asking again repeats it
	// later, as at refresh.
	ErrProviderCheckIncomplete = errors.New("the provider's answer could not be applied")
	// ErrPersonKeyConflict refuses enrolling a person key on an account that
	// holds a different one.
	ErrPersonKeyConflict = errors.New("account has a different person key")
)

// KeyRetryError refuses a waiting device key that repeats sign-in sooner
// than appkeys.PendingRetryInterval.
type KeyRetryError struct {
	RetryAfter time.Duration
	// Changed marks a key whose provider binding changed while the request
	// ran; the device asks again at once, not because it is waiting.
	Changed bool
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
	// Acting is the main profile the route checked, checked again under
	// the account lock.
	Acting ActingProfile
}

// KeySignInResult is the answer a route seals to the request's reply key.
type KeySignInResult struct {
	// Status is appkeys.ReplySignedIn or appkeys.ReplyApprovalRequired.
	Status   string
	DeviceID string
	User     *models.User
	// Pair is the session's tokens; nil while approval is required.
	Pair *TokenPair
	// PersonKeyID is the account's person key's ID (kid), set by enroll and
	// by an app code's redeem.
	PersonKeyID string
	// PersonKeyStatus is PersonKeyEnrolled, PersonKeyKept or
	// PersonKeyReplaced, set by an app code's redeem.
	PersonKeyStatus string
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
	joins      JoinClaimer
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
	var (
		payload *appkeys.Payload
		err     error
	)
	if purpose == appkeys.PurposeProfileSet {
		// What the set may hold is judged once, for both forms of the
		// request, after the envelope is verified (VerifyProfileSet).
		payload, err = appkeys.VerifyProfileSet(req.Envelope, key, req.ServerID, a.now())
	} else {
		payload, err = appkeys.Verify(req.Envelope, purpose, key, req.ServerID, a.now())
	}
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

// lockedChain is what keySignInPolicy decided under its locks.
type lockedChain struct {
	identityID    *int64
	providerSince *time.Time
}

// lockChain takes the provider and settings locks for binding (keySignInPolicy)
// after the account row lock and before any key or session row.
func lockChain(ctx context.Context, tx pgx.Tx, user *models.User, binding KeyBinding) (lockedChain, error) {
	identityID, providerSince, err := keySignInPolicy(ctx, tx, user, binding)
	return lockedChain{identityID: identityID, providerSince: providerSince}, err
}

func (c lockedChain) session(ip string, expiry time.Time) KeySession {
	return KeySession{IP: ip, Expiry: expiry, IdentityID: c.identityID, ProviderSince: c.providerSince}
}

// errKeyBindingChanged is a key brought under another identity (linking)
// between reading its binding and locking its row. The key is still good, so
// the device is asked to try again on a fresh challenge, when it sees the
// new binding; answering it as a bad key would make the device discard it.
var errKeyBindingChanged = &KeyRetryError{RetryAfter: time.Second, Changed: true}

// unknownKey answers a request signed by a key this server doesn't hold, or
// holds revoked, after a signature check against a throwaway key, so it takes
// about as long as a known key's refusal.
func unknownKey(env appkeys.Envelope, purpose appkeys.Purpose) error {
	appkeys.DecoyVerify(env, purpose)
	return ErrKeySignatureInvalid
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
		return nil, unknownKey(req.Envelope, appkeys.PurposeIntroduce)
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
	binding := KeyBinding{IdentityID: person.IdentityID, ProviderBound: person.ProviderBound}
	expiry, err := a.service.keyProviderExpiry(ctx, person.UserID, binding)
	if err != nil && !errors.Is(err, errKeyIdentityGone) {
		return nil, err
	}
	gone := false
	var audit *auditmutation.Entry
	out := &KeySignInResult{ReplyKey: *payload.ReplyKey, Challenge: payload.Challenge}
	err = a.inTx(ctx, func(tx pgx.Tx) error {
		user, err := lockEnabledUser(ctx, tx, person.UserID)
		if err != nil {
			return err
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
			return ErrKeySignatureInvalid
		}
		if err != nil {
			return err
		}
		if !(KeyBinding{IdentityID: locked.IdentityID, ProviderBound: locked.ProviderBound}).same(binding) {
			return errKeyBindingChanged
		}
		status := models.DeviceKeyStatusActive
		if user.Role == models.RoleAdmin {
			status = models.DeviceKeyStatusPending
			if err := checkPendingKeyRoom(ctx, tx, user.ID); err != nil {
				return err
			}
		}
		key, err := insertDeviceKeyTx(ctx, tx, NewDeviceKey{
			UserID: user.ID, PublicKey: *payload.DeviceKey, Kind: models.DeviceKeyKindDevice,
			Name: payload.DeviceName, Platform: payload.DevicePlatform, Status: status,
			EnrolledVia: models.AppKeyEnrolledIntroduce, PersonKeyID: &locked.ID,
			IdentityID: locked.IdentityID, ProviderBound: locked.ProviderBound, IP: req.IP,
		})
		if err != nil {
			return err
		}
		audit, err = recordKeyAdded(ctx, tx, auditmutation.ActionDeviceKeyIntroduced, user.ID, http.StatusOK,
			keyAdded{auditFieldDeviceKeyStatus: status}.withDevice(key))
		if err != nil {
			return err
		}
		out.User, out.DeviceID = user, key.ID
		if status == models.DeviceKeyStatusPending {
			return holdForApproval(ctx, tx, key, out)
		}
		out.Status = appkeys.ReplySignedIn
		out.Pair, err = a.service.openKeySessionTx(ctx, tx, user, key, chain.session(req.IP, expiry))
		return err
	})
	if err != nil {
		return nil, err
	}
	if gone {
		return nil, ErrKeySignatureInvalid
	}
	auditmutation.CommitMutation(ctx, audit)
	return a.signedIn(ctx, out), nil
}

// holdForApproval answers approval_required for a waiting key and records
// the attempt for the retry interval. The caller has already run the
// provider and local password rules for it, so a key they would refuse is
// refused now. A waiting key never opened a session, so last_used_at stays
// empty.
func holdForApproval(ctx context.Context, tx pgx.Tx, key *models.AppDeviceKey, out *KeySignInResult) error {
	if _, err := tx.Exec(ctx, `UPDATE auth_device_keys SET last_attempt_at = clock_timestamp() WHERE id = $1`, key.ID); err != nil {
		return fmt.Errorf("recording device key attempt: %w", err)
	}
	out.Status = appkeys.ReplyApprovalRequired
	return nil
}

// SignIn opens a session with an enrolled device key. A waiting key answers
// approval_required, no more often than appkeys.PendingRetryInterval and for
// at most appkeys.PendingKeyLifetime; a denied one ErrAppKeyDenied. Those
// checks run before the provider check, so a waiting or denied key never
// reaches the provider.
func (a *AppKeySignIn) SignIn(ctx context.Context, req KeyRequest) (*KeySignInResult, error) {
	if err := req.Envelope.CheckShape(appkeys.PurposeSignIn); err != nil {
		return nil, err
	}
	device, err := a.keys.DeviceKeyByKeyID(ctx, req.Envelope.KeyID)
	if errors.Is(err, ErrAppKeyNotFound) || (err == nil && device.RevokedAt != nil && !sweptPendingKey(device)) {
		return nil, unknownKey(req.Envelope, appkeys.PurposeSignIn)
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
	if err := keyStatusAllows(ctx, a.pool, device); err != nil {
		return nil, err
	}
	var expiry time.Time
	if device.Status == models.DeviceKeyStatusActive {
		if expiry, err = a.service.keyProviderExpiry(ctx, device.UserID, bindingOfDeviceKey(device)); err != nil && !errors.Is(err, errKeyIdentityGone) {
			return nil, err
		}
	}
	gone := false
	out := &KeySignInResult{ReplyKey: *payload.ReplyKey, Challenge: payload.Challenge, DeviceID: device.ID}
	err = a.inTx(ctx, func(tx pgx.Tx) error {
		user, err := lockEnabledUser(ctx, tx, device.UserID)
		if err != nil {
			return err
		}
		chain, err := lockChain(ctx, tx, user, bindingOfDeviceKey(device))
		if errors.Is(err, errKeyIdentityGone) {
			gone = true
			return revokeDeviceKeyTx(ctx, tx, user.ID, device.ID)
		}
		if err != nil {
			return err
		}
		key, err := lockDeviceKey(ctx, tx, user.ID, device.ID)
		// A waiting key the cleanup revoked since the read above still
		// answers key_denied below, as one it revoked earlier does.
		if errors.Is(err, ErrAppKeyNotFound) || (err == nil && key.RevokedAt != nil && !sweptPendingKey(key)) {
			return ErrKeySignatureInvalid
		}
		if err != nil {
			return err
		}
		if !bindingOfDeviceKey(key).same(bindingOfDeviceKey(device)) {
			return errKeyBindingChanged
		}
		out.User = user
		if err := keyStatusAllows(ctx, tx, key); err != nil {
			return err
		}
		if key.Status == models.DeviceKeyStatusPending {
			return holdForApproval(ctx, tx, key, out)
		}
		if device.Status != models.DeviceKeyStatusActive {
			// Approved since it was read: its provider check hasn't run.
			// The device asks again on a fresh challenge.
			return &KeyRetryError{RetryAfter: time.Second}
		}
		out.Status = appkeys.ReplySignedIn
		out.Pair, err = a.service.openKeySessionTx(ctx, tx, user, key, chain.session(req.IP, expiry))
		return err
	})
	if err != nil {
		return nil, err
	}
	if gone {
		return nil, ErrKeySignatureInvalid
	}
	return a.signedIn(ctx, out), nil
}

// sweptPendingKey reports a waiting key the retention step revoked once it
// outlived appkeys.PendingKeyLifetime. Its signature is still checked, and it
// answers ErrAppKeyDenied (keyStatusAllows) as it did before the sweep, so
// the device learns it was not approved rather than that its key is unknown.
func sweptPendingKey(key *models.AppDeviceKey) bool {
	return key.Status == models.DeviceKeyStatusPending && key.RevokedAt != nil &&
		key.RevokedAt.Sub(key.CreatedAt) >= appkeys.PendingKeyLifetime
}

// keyStatusAllows refuses a denied key, and a waiting key past its lifetime
// or trying again sooner than the retry interval. Both times are measured on
// the database's clock, the one that recorded the key's attempts.
func keyStatusAllows(ctx context.Context, db rowQuerier, key *models.AppDeviceKey) error {
	switch key.Status {
	case models.DeviceKeyStatusDenied:
		return ErrAppKeyDenied
	case models.DeviceKeyStatusPending:
	default:
		return nil
	}
	var now time.Time
	// The wall clock, not the transaction's start: a request that waited on
	// the account lock must not judge, or stamp, a time already past.
	if err := db.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return fmt.Errorf("reading the clock: %w", err)
	}
	if now.Sub(key.CreatedAt) >= appkeys.PendingKeyLifetime {
		return ErrAppKeyDenied
	}
	if key.LastAttemptAt != nil {
		if wait := appkeys.PendingRetryInterval - now.Sub(*key.LastAttemptAt); wait > 0 {
			return &KeyRetryError{RetryAfter: time.Duration(math.Ceil(wait.Seconds())) * time.Second}
		}
	}
	return nil
}

// Enroll registers the person key beside an enroll envelope (when the
// account has none) and the device key in it, from the account's own login
// session, and opens a session bound to the device key. Both keys take the
// enrolling session's provider identity, decided and locked before the
// session row is.
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
	enrolling, err := sessionChainUnlocked(ctx, a.pool, req.Claims.SessionID, req.Claims.UserID)
	if err != nil {
		return nil, err
	}
	expiry, err := a.service.keyProviderExpiry(ctx, req.Claims.UserID, enrolling.binding())
	if err != nil {
		return nil, sessionIdentityGone(err)
	}
	var audit *auditmutation.Entry
	out := &KeySignInResult{ReplyKey: *payload.ReplyKey, Challenge: payload.Challenge, PersonKeyID: req.PersonKey.KeyID()}
	err = a.inTx(ctx, func(tx pgx.Tx) error {
		user, err := lockEnabledUser(ctx, tx, req.Claims.UserID)
		if err != nil {
			return err
		}
		if err := recheckActingProfile(ctx, tx, user.ID, req.Acting); err != nil {
			return err
		}
		locked, err := lockChain(ctx, tx, user, enrolling.binding())
		if err != nil {
			return sessionIdentityGone(err)
		}
		binding := enrolling.binding()
		person, personCreated, err := enrollPersonKeyTx(ctx, tx, user.ID, req.PersonKey, binding, req.IP)
		if err != nil {
			return err
		}
		key, err := insertDeviceKeyTx(ctx, tx, NewDeviceKey{
			UserID: user.ID, PublicKey: *payload.DeviceKey, Kind: models.DeviceKeyKindDevice,
			Name: payload.DeviceName, Platform: payload.DevicePlatform,
			EnrolledVia: models.AppKeyEnrolledSession, PersonKeyID: &person.ID,
			IdentityID: binding.IdentityID, ProviderBound: binding.ProviderBound, IP: req.IP,
		})
		if err != nil {
			return err
		}
		// The enrolling session's row last, after the key rows: one that
		// ended or moved under another identity since it was read rolls the
		// enrollment back.
		chain, err := sessionProviderChainOf(ctx, tx, req.Claims.SessionID, user.ID)
		if err != nil {
			return err
		}
		if !chain.binding().same(binding) {
			return ErrSessionRevoked
		}
		out.User, out.DeviceID, out.Status = user, key.ID, appkeys.ReplySignedIn
		out.Pair, err = a.service.openKeySessionTx(ctx, tx, user, key, locked.session(req.IP, expiry))
		if err != nil {
			return err
		}
		audit, err = recordKeyAdded(ctx, tx, auditmutation.ActionAppKeysEnrolled, user.ID, http.StatusOK,
			keyAdded{auditFieldSessionOpenedVia: chain.openedVia}.withDevice(key).withPerson(person, personCreated))
		return err
	})
	if err != nil {
		return nil, err
	}
	auditmutation.CommitMutation(ctx, audit)
	return a.signedIn(ctx, out), nil
}

// sessionChainUnlocked reads a live own session's provider chain without
// locking its row, so the identity it names can be locked first.
func sessionChainUnlocked(ctx context.Context, db rowQuerier, sessionID string, userID int) (sessionProviderChain, error) {
	var chain sessionProviderChain
	err := db.QueryRow(ctx, `SELECT identity_id, provider_since FROM auth_sessions
		WHERE id = $1 AND user_id = $2 AND impersonator_user_id IS NULL AND purpose = $3
			AND revoked_at IS NULL AND expires_at > NOW()`, sessionID, userID, models.SessionPurposeAccount).Scan(&chain.identityID, &chain.providerSince)
	if errors.Is(err, pgx.ErrNoRows) {
		return sessionProviderChain{}, ErrSessionRevoked
	}
	if err != nil {
		return sessionProviderChain{}, fmt.Errorf("reading session identity: %w", err)
	}
	return chain, nil
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
// holds the account row lock. A provider-bound key whose identity is gone
// from the account is dead and the key list leaves it out, so it is revoked
// here and the account counts as having no person key: enrolling or
// replacing must not answer a conflict with a key nobody can see or use.
func livePersonKeyTx(ctx context.Context, tx pgx.Tx, userID int) (*models.AppPersonKey, error) {
	key, err := scanPersonKey(tx.QueryRow(ctx, `SELECT `+personKeyColumns+` FROM auth_person_keys
		WHERE user_id = $1 AND revoked_at IS NULL FOR UPDATE`, userID))
	if errors.Is(err, ErrAppKeyNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !key.ProviderBound || key.IdentityID == nil {
		return key, nil
	}
	live, err := accountHasIdentity(ctx, tx, userID, *key.IdentityID)
	if err != nil {
		return nil, fmt.Errorf("reading person key identity: %w", err)
	}
	if live {
		return key, nil
	}
	if err := revokePersonKeyTx(ctx, tx, userID, key.ID); err != nil {
		return nil, err
	}
	return nil, nil
}

// enrollPersonKeyTx returns the account's person key when it is key, or
// enrolls key when the account has none, recording ip as the enrolling
// request's address. A different live person key is ErrPersonKeyConflict.
// created reports that this call enrolled it.
func enrollPersonKeyTx(ctx context.Context, tx pgx.Tx, userID int, key appkeys.PublicKey, binding KeyBinding, ip string) (person *models.AppPersonKey, created bool, err error) {
	current, err := livePersonKeyTx(ctx, tx, userID)
	if err != nil {
		return nil, false, err
	}
	if current != nil {
		if current.KeyID != key.KeyID() {
			return nil, false, ErrPersonKeyConflict
		}
		return current, false, nil
	}
	person, err = insertPersonKeyTx(ctx, tx, NewPersonKey{
		UserID: userID, PublicKey: key, IdentityID: binding.IdentityID, ProviderBound: binding.ProviderBound,
		EnrolledVia: models.AppKeyEnrolledSession, IP: ip,
	})
	return person, err == nil, err
}
