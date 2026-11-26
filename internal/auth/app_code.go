package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/models"
)

// AppCodeLifetime is how long an app code stays good. A join code never
// outlives its invitation.
const AppCodeLifetime = 10 * time.Minute

// App code kinds (auth_app_codes.kind).
const (
	appCodeKindJoin       = "join"
	appCodeKindSession    = "session"
	appCodeKindSignInLink = "sign_in_link"
)

// What redeeming an app code did with the request's person key: enrolled
// as the account's person key (or already it), kept out because the
// account holds a different one (a session code), or put in place of a
// different one, which is revoked (a sign-in link's code).
const (
	PersonKeyEnrolled = "enrolled"
	PersonKeyKept     = "kept"
	PersonKeyReplaced = "replaced"
)

var (
	// ErrAppCodeInvalid refuses an app code that is unknown, already
	// redeemed or expired, and a join code whose invitation can no longer be
	// claimed or a session code whose session has ended.
	ErrAppCodeInvalid = errors.New("app code unknown, used or expired")
	// ErrAppCodeNameTaken refuses a join whose account name or email another
	// account holds, or another unspent join code reserves.
	ErrAppCodeNameTaken = errors.New("that account name is taken")
	// ErrJoinNotAvailable refuses a join that would create an account
	// whose only credential is its keys: the server has local password
	// sign-in off, so the person joins through the sign-in provider.
	ErrJoinNotAvailable = errors.New("this server adds accounts through its sign-in provider")
)

// The fields and keys a refused app code names (NameTakenError.Field,
// KeyEnrolledError.Member): the request members that carry them.
const (
	NameFieldUsername = "username"
	NameFieldEmail    = "email"
	KeyMemberPerson   = "person_key"
	KeyMemberDevice   = "device_key"
)

// NameTakenError is ErrAppCodeNameTaken with what clashed: Field is
// "username" or "email" when the issue could tell, "" at redeem, where the
// account insert refuses without saying which.
type NameTakenError struct{ Field string }

func (e *NameTakenError) Error() string { return ErrAppCodeNameTaken.Error() }
func (e *NameTakenError) Unwrap() error { return ErrAppCodeNameTaken }

// KeyEnrolledError is ErrAppKeyAlreadyEnrolled at a redeem, saying which key
// is enrolled already: Member is "person_key" or "device_key". The app makes
// that key again; a name taken (NameTakenError) needs the page instead.
type KeyEnrolledError struct{ Member string }

func (e *KeyEnrolledError) Error() string { return ErrAppKeyAlreadyEnrolled.Error() }
func (e *KeyEnrolledError) Unwrap() error { return ErrAppKeyAlreadyEnrolled }

// keyEnrolledAs names the key a redeem's enrollment found enrolled already.
func keyEnrolledAs(member string, err error) error {
	if errors.Is(err, ErrAppKeyAlreadyEnrolled) {
		return &KeyEnrolledError{Member: member}
	}
	return err
}

// IssuedAppCode is an app code as its creator hands it to the page.
type IssuedAppCode struct {
	Code      string
	ExpiresAt time.Time
	// LinkAllowed reports that a page may put the code in a link: redeeming
	// it creates a new account with no administrative power. A link can be
	// received by an app other than Silo, which would redeem the code first,
	// so every other code (a session code, a join code for an administrator's
	// invitation) moves only by the person's own hands.
	LinkAllowed bool
}

// ExpiresIn is the code's remaining life in whole seconds, at least one.
func (c IssuedAppCode) ExpiresIn(now time.Time) int {
	return max(int(c.ExpiresAt.Sub(now)/time.Second), 1)
}

// newAppCode makes a code and the hash stored for it.
func newAppCode() (string, []byte, error) {
	raw := make([]byte, appkeys.AppCodeSize)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("generating app code: %w", err)
	}
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(raw), sum[:], nil
}

// appCodeHash is the stored hash of a code as the redeem payload carries it
// (base64url of the 32 bytes); ok is false for anything else.
func appCodeHash(code string) ([]byte, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(code)
	if err != nil || len(raw) != appkeys.AppCodeSize {
		return nil, false
	}
	sum := sha256.Sum256(raw)
	return sum[:], true
}

// JoinAppCode is a join code about to be issued for an invitation.
type JoinAppCode struct {
	InvitationID int64
	// Username and Email are the account the redeem creates; Email may be
	// empty.
	Username string
	Email    string
	// InvitationExpiresAt bounds the code's life.
	InvitationExpiresAt time.Time
}

// IssueJoinAppCodeTx issues a join code for an invitation. tx holds the
// invitation's row lock, taken by the caller after checking it can be
// claimed, so two codes for one invitation are issued one after the other;
// the new code replaces any unspent one, which stops working. The name and
// email must be free: no account holds either, and no other unspent join
// code reserves either as its name or its email. The reservation is advisory: redeeming creates the
// account, and a name taken in between refuses the redeem.
func IssueJoinAppCodeTx(ctx context.Context, tx pgx.Tx, in JoinAppCode) (IssuedAppCode, error) {
	username := NormalizeUsername(in.Username)
	email := NormalizeEmail(in.Email)
	if _, err := tx.Exec(ctx, `DELETE FROM auth_app_codes WHERE invitation_id = $1 AND redeemed_at IS NULL`, in.InvitationID); err != nil {
		return IssuedAppCode{}, fmt.Errorf("replacing the invitation's app code: %w", err)
	}
	if err := clearDeadReservationsTx(ctx, tx, username, email); err != nil {
		return IssuedAppCode{}, err
	}
	for _, check := range []struct{ field, name string }{{NameFieldUsername, username}, {NameFieldEmail, email}} {
		if check.name == "" {
			continue
		}
		taken, err := loginIdentifierTaken(ctx, tx, check.name)
		if err != nil {
			return IssuedAppCode{}, err
		}
		if !taken {
			taken, err = reservedByAnotherCode(ctx, tx, check.name)
			if err != nil {
				return IssuedAppCode{}, err
			}
		}
		if taken {
			return IssuedAppCode{}, &NameTakenError{Field: check.field}
		}
	}
	code, hash, err := newAppCode()
	if err != nil {
		return IssuedAppCode{}, err
	}
	var out IssuedAppCode
	err = tx.QueryRow(ctx, `INSERT INTO auth_app_codes (code_hash, kind, invitation_id, reserved_username, reserved_email, expires_at)
		VALUES ($1, $2, $3, $4, $5, LEAST(clock_timestamp() + make_interval(secs => $6), $7))
		RETURNING expires_at`, hash, appCodeKindJoin, in.InvitationID, username, nullIfBlank(email),
		AppCodeLifetime.Seconds(), in.InvitationExpiresAt).Scan(&out.ExpiresAt)
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "23505" {
		// Only the reserved name is unique among unspent codes.
		return IssuedAppCode{}, &NameTakenError{Field: NameFieldUsername}
	}
	if err != nil {
		return IssuedAppCode{}, fmt.Errorf("storing app code: %w", err)
	}
	out.Code = code
	return out, nil
}

// clearDeadReservationsTx deletes the unspent join codes that reserve name
// or email (as either) but can never be redeemed: expired, or for an
// invitation that can no longer be claimed (accepted, revoked, replaced by a
// resend or superseded, or expired; a deleted invitation's codes are gone
// with it). A dead code reserves nothing, so it is removed before the
// reservation check and the insert, which keeps one rule for both: the
// unique index on reserved_username then holds only live codes. Retention
// would delete an expired code later anyway; one whose invitation was
// revoked would otherwise block the name for up to its ten minutes. The
// caller holds the settings lock, which every issue and claim takes first.
func clearDeadReservationsTx(ctx context.Context, tx pgx.Tx, username, email string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM auth_app_codes c
		WHERE c.kind = $3 AND c.redeemed_at IS NULL
			AND (c.reserved_username IN ($1::citext, NULLIF($2, '')::citext) OR c.reserved_email IN ($1::citext, NULLIF($2, '')::citext))
			AND (c.expires_at <= clock_timestamp() OR NOT EXISTS (
				SELECT 1 FROM invitations i WHERE i.id = c.invitation_id
					AND i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.expires_at > clock_timestamp()))`,
		username, email, appCodeKindJoin); err != nil {
		return fmt.Errorf("clearing dead reservations: %w", err)
	}
	return nil
}

// reservedByAnotherCode reports whether another unspent, unexpired join code
// reserves name as its username or its email, compared case-insensitively as
// sign-in compares them. The caller deleted its invitation's own code first,
// so a renewal doesn't clash with itself, and holds the settings lock, which
// every issue takes first, so two issues can't both pass.
func reservedByAnotherCode(ctx context.Context, tx pgx.Tx, name string) (bool, error) {
	var reserved bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM auth_app_codes
		WHERE kind = $2 AND redeemed_at IS NULL AND expires_at > clock_timestamp()
			AND (reserved_username = $1::citext OR reserved_email = $1::citext))`, name, appCodeKindJoin).Scan(&reserved)
	if err != nil {
		return false, fmt.Errorf("checking app code reservations: %w", err)
	}
	return reserved, nil
}

// IssueSessionAppCode issues an app code for the claims' own account, so the
// Silo app on another device can enroll its keys ("Open in Silo"). Only a
// session that may manage keys issues one (ActionManageKeys), acting as the
// household's main profile; acting is checked again under the account lock.
// The code records the session and its provider binding: the keys it
// enrolls take that binding, and a code whose session has ended by the time
// it is redeemed no longer works.
func (a *AppKeySignIn) IssueSessionAppCode(ctx context.Context, claims *Claims, acting ActingProfile) (IssuedAppCode, error) {
	if err := SessionAllows(ctx, a.pool, claims, ActionManageKeys); err != nil {
		return IssuedAppCode{}, err
	}
	unlocked, err := sessionChainUnlocked(ctx, a.pool, claims.SessionID, claims.UserID)
	if err != nil {
		return IssuedAppCode{}, err
	}
	binding := unlocked.binding()
	var out IssuedAppCode
	var audit *auditmutation.Entry
	err = a.inTx(ctx, func(tx pgx.Tx) error {
		user, err := lockEnabledUser(ctx, tx, claims.UserID)
		if err != nil {
			return err
		}
		if err := recheckActingProfile(ctx, tx, user.ID, acting); err != nil {
			return err
		}
		if binding.ProviderBound {
			// A provider session whose identity is gone would mint keys
			// bound to nothing, as enroll refuses.
			if _, _, err := lockKeyIdentity(ctx, tx, user, binding.IdentityID); err != nil {
				return sessionIdentityGone(err)
			}
		}
		code, hash, err := newAppCode()
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO auth_app_codes (code_hash, kind, user_id, session_id, identity_id, provider_bound, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, clock_timestamp() + make_interval(secs => $7)) RETURNING expires_at`,
			hash, appCodeKindSession, user.ID, claims.SessionID, binding.IdentityID, binding.ProviderBound,
			AppCodeLifetime.Seconds()).Scan(&out.ExpiresAt); err != nil {
			return fmt.Errorf("storing app code: %w", err)
		}
		out.Code = code
		// The session row last; one that ended or moved under another
		// identity meanwhile rolls the code back.
		chain, err := sessionProviderChainOf(ctx, tx, claims.SessionID, user.ID)
		if err != nil {
			return err
		}
		if !chain.binding().same(binding) {
			return ErrSessionRevoked
		}
		// The code's kind and expiry only: never the code or its hash.
		audit, err = recordKeyAdded(ctx, tx, auditmutation.ActionAppCodeIssued, user.ID, http.StatusOK, keyAdded{
			auditFieldAppCodeKind: appCodeKindSession, auditFieldExpiresAt: auditInstant(out.ExpiresAt),
		})
		return err
	})
	if err != nil {
		return IssuedAppCode{}, err
	}
	auditmutation.CommitMutation(ctx, audit)
	return out, nil
}

// JoinAccount is the account a join code creates, as its invitation and
// code describe it.
type JoinAccount struct {
	InvitationID int64
	Username     string
	Email        string
}

// JoinClaimer claims an invitation for a join inside the redeem's
// transaction: it locks the invitation, checks it can still be claimed and
// that local password sign-in is on (ErrLocalLoginDisabled otherwise, under
// the settings lock), creates the account with no password and the
// invitation's role, libraries, group and first profile, and marks the
// invitation accepted. A name or email taken since the code was issued is
// ErrDuplicate. The invitation service implements it.
type JoinClaimer interface {
	ClaimJoinTx(ctx context.Context, tx pgx.Tx, join JoinAccount) (*models.User, error)
}

// SetJoinClaimer wires the invitation side of a join. Without it a join code
// can't be redeemed.
func (a *AppKeySignIn) SetJoinClaimer(claimer JoinClaimer) { a.joins = claimer }

// KeyRedeemRequest is a signed redeem: the person key travels beside the
// envelope, and the app code is in its payload.
type KeyRedeemRequest struct {
	KeyRequest
	PersonKey appkeys.PublicKey
}

// appCodeRow is an app code as redeem reads it before locking anything.
type appCodeRow struct {
	kind          string
	userID        *int
	invitationID  *int64
	username      string
	email         string
	sessionID     string
	identityID    *int64
	providerBound bool
	// signInLinkID and signInLinkCreatedBy: a sign-in link's code's link and
	// the account that made it.
	signInLinkID        *int64
	signInLinkCreatedBy *int
}

func readAppCode(ctx context.Context, db rowQuerier, hash []byte) (appCodeRow, error) {
	var row appCodeRow
	var username, email, sessionID *string
	err := db.QueryRow(ctx, `SELECT kind, user_id, invitation_id, reserved_username, reserved_email, session_id, identity_id, provider_bound,
			sign_in_link_id, sign_in_link_created_by
		FROM auth_app_codes WHERE code_hash = $1 AND redeemed_at IS NULL AND expires_at > clock_timestamp()`, hash).Scan(
		&row.kind, &row.userID, &row.invitationID, &username, &email, &sessionID, &row.identityID, &row.providerBound,
		&row.signInLinkID, &row.signInLinkCreatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return appCodeRow{}, ErrAppCodeInvalid
	}
	if err != nil {
		return appCodeRow{}, fmt.Errorf("reading app code: %w", err)
	}
	row.username, row.email, row.sessionID = derefString(username), derefString(email), derefString(sessionID)
	return row, nil
}

// spendAppCode marks the code redeemed. Of two redemptions of one code, on
// any API processes, the second finds no unspent row.
func spendAppCode(ctx context.Context, tx pgx.Tx, hash []byte) error {
	tag, err := tx.Exec(ctx, `UPDATE auth_app_codes SET redeemed_at = clock_timestamp()
		WHERE code_hash = $1 AND redeemed_at IS NULL AND expires_at > clock_timestamp()`, hash)
	if err != nil {
		return fmt.Errorf("spending app code: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrAppCodeInvalid
	}
	return nil
}

// RedeemAppCode enrolls the person key beside a redeem envelope and the
// device key in it with the app code in its payload, and opens the device
// key's session. A join code creates the account, with no password, and
// claims its invitation in the same transaction. A session code enrolls
// the device key on the account that issued it: the person key is enrolled
// when the account has none, and an account holding a different one keeps
// it, with the device key enrolled under the account's key (a person who
// lost every other device replaces it afterwards with ReplacePersonKey).
// A sign-in link's code enrolls both keys on its account, unbound to any
// provider, and its person key replaces a different one the account holds
// (redeemSignInLinkCode); it is refused for an administrator's account. Keys
// from a code start active, also on an administrator's account: the code
// came from the account's own session or its invitation.
func (a *AppKeySignIn) RedeemAppCode(ctx context.Context, req KeyRedeemRequest) (*KeySignInResult, error) {
	if err := req.Envelope.CheckShape(appkeys.PurposeRedeem); err != nil {
		return nil, err
	}
	payload, err := a.verify(ctx, req.KeyRequest, appkeys.PurposeRedeem, req.PersonKey)
	if err != nil {
		return nil, err
	}
	hash, ok := appCodeHash(payload.Code)
	if !ok {
		return nil, ErrAppCodeInvalid
	}
	code, err := readAppCode(ctx, a.pool, hash)
	if err != nil {
		return nil, err
	}
	out := &KeySignInResult{ReplyKey: *payload.ReplyKey, Challenge: payload.Challenge}
	var audit *auditmutation.Entry
	switch {
	case code.kind == appCodeKindJoin && code.invitationID != nil:
		audit, err = a.redeemJoin(ctx, req, payload, hash, code, out)
	case code.kind == appCodeKindSession && code.userID != nil:
		audit, err = a.redeemSession(ctx, req, payload, hash, code, out)
	case code.kind == appCodeKindSignInLink && code.userID != nil:
		audit, err = a.redeemSignInLinkCode(ctx, req, payload, hash, code, out)
	default:
		err = ErrAppCodeInvalid
	}
	if err != nil {
		return nil, err
	}
	auditmutation.CommitMutation(ctx, audit)
	return a.signedIn(ctx, out), nil
}

// redeemJoin creates the account a join code describes and enrolls both
// keys on it. Lock order: the settings lock, the invitation, the code, then
// other invitations the claim supersedes and the new account's rows. Every
// invitation claim and every app code issue takes the settings lock before
// any invitation or code row, so they run one after the other.
func (a *AppKeySignIn) redeemJoin(ctx context.Context, req KeyRedeemRequest, payload *appkeys.Payload, hash []byte, code appCodeRow, out *KeySignInResult) (*auditmutation.Entry, error) {
	if a.joins == nil {
		return nil, ErrAppCodeInvalid
	}
	expiry := time.Now().Add(a.service.jwt.RefreshExpiry())
	var audit *auditmutation.Entry
	err := a.inTx(ctx, func(tx pgx.Tx) error {
		if err := lockServerSettings(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM invitations WHERE id = $1 FOR UPDATE`, *code.invitationID); err != nil {
			return fmt.Errorf("locking invitation: %w", err)
		}
		if err := spendAppCode(ctx, tx, hash); err != nil {
			return err
		}
		user, err := a.joins.ClaimJoinTx(ctx, tx, JoinAccount{InvitationID: *code.invitationID, Username: code.username, Email: code.email})
		switch {
		case errors.Is(err, ErrLocalLoginDisabled):
			return ErrJoinNotAvailable
		case IsDuplicate(err):
			return &NameTakenError{}
		case err != nil:
			return err
		}
		chain, err := lockChain(ctx, tx, user, KeyBinding{})
		if err != nil {
			return err
		}
		person, err := insertPersonKeyTx(ctx, tx, NewPersonKey{
			UserID: user.ID, PublicKey: req.PersonKey, EnrolledVia: models.AppKeyEnrolledAppCode, IP: req.IP,
		})
		if err != nil {
			return keyEnrolledAs(KeyMemberPerson, err)
		}
		key, err := a.enrollRedeemedDevice(ctx, tx, user, person, KeyBinding{}, payload, chain.session(req.IP, expiry), PersonKeyEnrolled, out)
		if err != nil {
			return err
		}
		audit, err = recordKeyAdded(ctx, tx, auditmutation.ActionAppCodeRedeemed, user.ID, http.StatusOK,
			keyAdded{auditFieldAppCodeKind: appCodeKindJoin}.withDevice(key).withPerson(person, true))
		return err
	})
	return audit, err
}

// redeemSession enrolls the keys of a session code on its account, bound
// as the session that issued it was. Lock order: the account, the code,
// then the provider locks, the keys and the issuing session's row.
func (a *AppKeySignIn) redeemSession(ctx context.Context, req KeyRedeemRequest, payload *appkeys.Payload, hash []byte, code appCodeRow, out *KeySignInResult) (*auditmutation.Entry, error) {
	binding := KeyBinding{IdentityID: code.identityID, ProviderBound: code.providerBound}
	expiry, err := a.service.keyProviderExpiry(ctx, *code.userID, binding)
	if err != nil {
		return nil, sessionIdentityGone(err)
	}
	var audit *auditmutation.Entry
	err = a.inTx(ctx, func(tx pgx.Tx) error {
		user, err := lockEnabledUser(ctx, tx, *code.userID)
		if err != nil {
			return err
		}
		if err := spendAppCode(ctx, tx, hash); err != nil {
			return err
		}
		chain, err := lockChain(ctx, tx, user, binding)
		if err != nil {
			return sessionIdentityGone(err)
		}
		person, status, personCreated, err := redeemPersonKeyTx(ctx, tx, user.ID, req.PersonKey, binding, req.IP)
		if err != nil {
			return keyEnrolledAs(KeyMemberPerson, err)
		}
		key, err := a.enrollRedeemedDevice(ctx, tx, user, person, binding, payload, chain.session(req.IP, expiry), status, out)
		if err != nil {
			return err
		}
		// The issuing session's row last: a code whose session ended, or
		// moved under another identity, since it was issued no longer works.
		issuing, err := sessionProviderChainOf(ctx, tx, code.sessionID, user.ID)
		if errors.Is(err, ErrSessionRevoked) || (err == nil && !issuing.binding().same(binding)) {
			return ErrAppCodeInvalid
		}
		if err != nil {
			return err
		}
		audit, err = recordKeyAdded(ctx, tx, auditmutation.ActionAppCodeRedeemed, user.ID, http.StatusOK,
			keyAdded{auditFieldAppCodeKind: appCodeKindSession}.withDevice(key).withPerson(person, personCreated))
		return err
	})
	return audit, err
}

// redeemPersonKeyTx returns the person key a redeemed device key enrolls
// under: key, enrolled, when the account has no live person key or already
// holds key; otherwise the account's own person key, kept. A redeem never
// replaces or revokes the account's person key, so its other devices and
// the devices waiting for approval are untouched; key was only the proof
// the device signed with, and is not stored.
func redeemPersonKeyTx(ctx context.Context, tx pgx.Tx, userID int, key appkeys.PublicKey, binding KeyBinding, ip string) (person *models.AppPersonKey, status string, created bool, err error) {
	current, err := livePersonKeyTx(ctx, tx, userID)
	if err != nil {
		return nil, "", false, err
	}
	if current != nil {
		if current.KeyID == key.KeyID() {
			return current, PersonKeyEnrolled, false, nil
		}
		return current, PersonKeyKept, false, nil
	}
	person, err = insertPersonKeyTx(ctx, tx, NewPersonKey{
		UserID: userID, PublicKey: key, IdentityID: binding.IdentityID,
		ProviderBound: binding.ProviderBound, EnrolledVia: models.AppKeyEnrolledAppCode, IP: ip,
	})
	if err != nil {
		return nil, "", false, err
	}
	return person, PersonKeyEnrolled, true, nil
}

// enrollRedeemedDevice enrolls the payload's device key under person with
// the code's provider binding, opens its session, fills the answer and
// returns the key.
func (a *AppKeySignIn) enrollRedeemedDevice(ctx context.Context, tx pgx.Tx, user *models.User, person *models.AppPersonKey,
	binding KeyBinding, payload *appkeys.Payload, session KeySession, personStatus string, out *KeySignInResult,
) (*models.AppDeviceKey, error) {
	key, err := insertDeviceKeyTx(ctx, tx, NewDeviceKey{
		UserID: user.ID, PublicKey: *payload.DeviceKey, Kind: models.DeviceKeyKindDevice,
		Name: payload.DeviceName, Platform: payload.DevicePlatform,
		EnrolledVia: models.AppKeyEnrolledAppCode, PersonKeyID: &person.ID,
		IdentityID: binding.IdentityID, ProviderBound: binding.ProviderBound, IP: session.IP,
	})
	if err != nil {
		return nil, keyEnrolledAs(KeyMemberDevice, err)
	}
	out.User, out.DeviceID, out.Status = user, key.ID, appkeys.ReplySignedIn
	out.PersonKeyStatus = personStatus
	if out.Pair, err = a.service.openKeySessionTx(ctx, tx, user, key, session); err != nil {
		return key, err
	}
	return key, out.reportPersonKey(ctx, tx, user.ID)
}

// SessionKeyAccess answers what the claims' session may do with the
// account's app keys (ReadSessionKeyAccess).
func (a *AppKeySignIn) SessionKeyAccess(ctx context.Context, claims *Claims) (SessionKeyAccess, error) {
	return ReadSessionKeyAccess(ctx, a.pool, claims)
}
