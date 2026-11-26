package auth

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/models"
)

// Sign-in links (docs/architecture/app-keys.md, "Sign-in links"): an
// administrator makes a single-use link for one account, and its holder
// trades it for an app code of kind sign_in_link and, while local password
// sign-in is on, a set-password token. The link and the token are base64url
// of 32 random bytes; only their SHA-256 is stored.
const (
	// SignInLinkLifetime is how long an unredeemed link stays good.
	SignInLinkLifetime = 24 * time.Hour
	// SignInLinkSetPasswordLifetime is how long the set-password token a
	// redeem hands out stays good, never past the link's own expiry.
	SignInLinkSetPasswordLifetime = time.Hour
)

var (
	// ErrSignInLinkInvalid refuses a link or set-password token that is
	// unknown, used or expired, and one whose account is disabled: one
	// answer for each, so a probe learns nothing.
	ErrSignInLinkInvalid = errors.New("sign-in link unknown, used or expired")
	// ErrSignInLinkNeedsProvider refuses a sign-in link while local password
	// sign-in is off on the server: the link could only give the account a
	// way in that skips the sign-in provider.
	ErrSignInLinkNeedsProvider = errors.New("this server signs people in through its sign-in provider")
	// ErrSignInLinkAccountChanged refuses a link to be emailed when the
	// account's address changed since the caller read it.
	ErrSignInLinkAccountChanged = errors.New("the account's email address changed while the link was made")
	// ErrScopedKeyAdminCredentials refuses a scoped API key handing out a
	// way into an administrator's account, as it may not set one's password.
	ErrScopedKeyAdminCredentials = errors.New("a scoped API key may not change an admin account's credentials")
)

// EndSignInLinksTx ends every sign-in link of the account in db, the
// caller's transaction, which holds the account row: the links, the app codes
// redeemed links left, and the set-password tokens they handed out, together,
// so a place that ends a link can't forget one of the three. Whatever ends a
// link calls it: a new link, a password set by any route (the link's own, a
// reset, an administrator's, the person's change), signing the account out
// everywhere, disabling the account and promoting it.
func EndSignInLinksTx(ctx context.Context, db interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, userID int) error {
	if _, err := db.Exec(ctx, `DELETE FROM auth_sign_in_links WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("ending sign-in links: %w", err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM auth_app_codes WHERE user_id = $1 AND kind = $2`, userID, appCodeKindSignInLink); err != nil {
		return fmt.Errorf("ending sign-in link codes: %w", err)
	}
	return nil
}

// SignInLinkIssue is an administrator's request for a sign-in link.
type SignInLinkIssue struct {
	UserID int
	// IssuedBy is the acting account; 0 is no account.
	IssuedBy int
	// ScopedKey marks a request made with a scoped API key.
	ScopedKey bool
	// Delivery is recorded in the audit row: "email" or "link".
	Delivery string
	// ExpectedEmail, when set, is the address the link is about to be
	// emailed to: the account must still have it when the link is stored,
	// compared as sign-in compares addresses (ErrSignInLinkAccountChanged).
	ExpectedEmail *string
}

// IssuedSignInLink is a stored link. Token is the raw token, shown once.
type IssuedSignInLink struct {
	Token     string
	ExpiresAt time.Time
}

// Why a redeemed link handed out no app code (SignInLinkRedemption.NoCode).
const (
	// NoCodeAppKeysOff: app key sign-in is off (or the server is in demo
	// mode).
	NoCodeAppKeysOff = "app_keys_off"
	// NoCodeAdministrator: the account is an administrator's when the link
	// is redeemed. A link gives an administrator the password route only:
	// no key appears on such an account without its holder signing in.
	NoCodeAdministrator = "administrator"
)

// SignInLinkRedemption is what a redeemed link hands its holder.
type SignInLinkRedemption struct {
	// AppCode is the app code for the Silo app; nil while app key sign-in
	// is off or for an administrator's account (NoCode says which). Its
	// LinkAllowed is false: the code is for an existing account and never
	// travels in a link.
	AppCode *IssuedAppCode
	// NoCode is NoCodeAppKeysOff or NoCodeAdministrator when AppCode is
	// nil.
	NoCode string
	// SetPasswordToken sets the account's password once
	// (SetPasswordWithSignInLink); empty when the account may not set one.
	SetPasswordToken string
}

// signInLinkPasswordInput is the account update the Owner rules judge a
// sign-in link as: a link lets its holder in as surely as a new password.
func signInLinkPasswordInput() models.UpdateUserInput {
	return models.UpdateUserInput{Password: new(""), LocalPasswordLoginEnabled: new(true)}
}

// CheckSignInLinkIssuer applies the rules for who may set the account's
// password as an administrator (CheckOwnerUpdate on a password write, and a
// scoped API key never on an administrator's account) to a sign-in link.
func CheckSignInLinkIssuer(actor OwnerActor, target *models.User, scopedKey bool) error {
	if err := CheckOwnerUpdate(actor, target, signInLinkPasswordInput()); err != nil {
		return err
	}
	if scopedKey && target != nil && target.Role == models.RoleAdmin {
		return ErrScopedKeyAdminCredentials
	}
	return nil
}

// IssueSignInLink stores a new sign-in link for the account, replacing the
// account's earlier links, and audits it without the token. The issuer and
// the account are locked in id order before the settings lock, so an
// ownership move or a promotion that commits first is judged here; the
// Owner rules and the scoped-key rule are CheckSignInLinkIssuer's. A
// disabled account is ErrUserDisabled and, while local password sign-in is
// off, every account is ErrSignInLinkNeedsProvider.
func (a *AppKeySignIn) IssueSignInLink(ctx context.Context, in SignInLinkIssue) (IssuedSignInLink, error) {
	token, hash, err := newAppCode()
	if err != nil {
		return IssuedSignInLink{}, err
	}
	var out IssuedSignInLink
	var audit *auditmutation.Entry
	err = a.inTx(ctx, func(tx pgx.Tx) error {
		target, actor, err := lockSignInLinkAccounts(ctx, tx, in.UserID, in.IssuedBy)
		if err != nil {
			return err
		}
		if err := CheckSignInLinkIssuer(actor, target, in.ScopedKey); err != nil {
			return err
		}
		if !target.Enabled {
			return ErrUserDisabled
		}
		if in.ExpectedEmail != nil && !strings.EqualFold(NormalizeEmail(target.Email), NormalizeEmail(*in.ExpectedEmail)) {
			return ErrSignInLinkAccountChanged
		}
		if err := lockServerSettings(ctx, tx); err != nil {
			return err
		}
		allowed, err := localPasswordLoginAllowed(ctx, tx)
		if err != nil {
			return err
		}
		if !allowed {
			return ErrSignInLinkNeedsProvider
		}
		// A new link replaces the account's earlier links, their codes and
		// their set-password tokens.
		if err := EndSignInLinksTx(ctx, tx, target.ID); err != nil {
			return err
		}
		var createdBy *int
		if in.IssuedBy > 0 {
			createdBy = new(in.IssuedBy)
		}
		if err := tx.QueryRow(ctx, `INSERT INTO auth_sign_in_links (token_hash, user_id, created_by, expires_at)
			VALUES ($1, $2, $3, clock_timestamp() + make_interval(secs => $4)) RETURNING expires_at`,
			hash, target.ID, createdBy, SignInLinkLifetime.Seconds()).Scan(&out.ExpiresAt); err != nil {
			return fmt.Errorf("storing sign-in link: %w", err)
		}
		audit, err = auditmutation.RecordMutation(ctx, tx, auditmutation.ActionSignInLinkCreated, auditmutation.TargetUser,
			strconv.Itoa(target.ID), http.StatusCreated, auditmutation.Changes(nil, map[string]any{
				"delivery": in.Delivery, auditFieldExpiresAt: auditInstant(out.ExpiresAt),
			}))
		return err
	})
	if err != nil {
		return IssuedSignInLink{}, err
	}
	auditmutation.CommitMutation(ctx, audit)
	out.Token = token
	return out, nil
}

// lockSignInLinkAccounts locks the account a link is for and the issuer and
// the request's audit actors in id order, as a password reset link's issue
// does: the account FOR UPDATE, the others FOR KEY SHARE, which waits for an
// ownership move (it locks both accounts FOR UPDATE) but not for other
// writers of the issuer's row.
func lockSignInLinkAccounts(ctx context.Context, tx pgx.Tx, userID, issuerID int) (*models.User, OwnerActor, error) {
	ids := append([]int{userID}, auditmutation.ActorIDs(ctx)...)
	if issuerID > 0 {
		ids = append(ids, issuerID)
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	actor := OwnerActor{ID: issuerID}
	var target *models.User
	for _, id := range ids {
		if id == userID {
			user, err := lockUser(ctx, tx, id)
			if err != nil {
				return nil, actor, err
			}
			target = user
			if id == issuerID {
				actor.IsOwner = user.IsOwner
			}
			continue
		}
		var isOwner bool
		err := tx.QueryRow(ctx, `SELECT is_owner FROM users WHERE id = $1 FOR KEY SHARE`, id).Scan(&isOwner)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, actor, fmt.Errorf("locking sign-in link issuer: %w", err)
		}
		if id == issuerID {
			actor.IsOwner = isOwner
		}
	}
	return target, actor, nil
}

// RedeemSignInLink spends a sign-in link: in one transaction the link is
// marked redeemed and, while app key sign-in is on (keysOn), an app code of
// kind sign_in_link is stored for its account, so of two concurrent
// redemptions exactly one succeeds. A set-password token comes with it; it
// lives an hour, never past the link. An account that is an administrator's
// when the link is redeemed, judged under its row lock in the transaction
// that spends the link, gets the set-password token only: no app code and
// no key. An unknown, used or expired link, and
// one whose account is disabled, is ErrSignInLinkInvalid; while local
// password sign-in is off it is ErrSignInLinkNeedsProvider and stays
// unspent.
func (a *AppKeySignIn) RedeemSignInLink(ctx context.Context, token string, keysOn bool) (SignInLinkRedemption, error) {
	hash, ok := appCodeHash(token)
	if !ok {
		return SignInLinkRedemption{}, ErrSignInLinkInvalid
	}
	var userID int
	err := a.pool.QueryRow(ctx, `SELECT user_id FROM auth_sign_in_links
		WHERE token_hash = $1 AND redeemed_at IS NULL AND expires_at > clock_timestamp()`, hash).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return SignInLinkRedemption{}, ErrSignInLinkInvalid
	}
	if err != nil {
		return SignInLinkRedemption{}, fmt.Errorf("reading sign-in link: %w", err)
	}
	passwordToken, passwordHash, err := newAppCode()
	if err != nil {
		return SignInLinkRedemption{}, err
	}
	var out SignInLinkRedemption
	var audit *auditmutation.Entry
	err = a.inTx(ctx, func(tx pgx.Tx) error {
		user, err := lockSignInLinkAccount(ctx, tx, userID)
		if err != nil {
			return err
		}
		var link spentSignInLink
		err = tx.QueryRow(ctx, `UPDATE auth_sign_in_links SET redeemed_at = clock_timestamp(),
				set_password_hash = $3,
				set_password_expires_at = LEAST(clock_timestamp() + make_interval(secs => $4), expires_at)
			WHERE token_hash = $1 AND user_id = $2 AND redeemed_at IS NULL AND expires_at > clock_timestamp()
			RETURNING id, created_by`,
			hash, userID, passwordHash, SignInLinkSetPasswordLifetime.Seconds()).Scan(&link.id, &link.createdBy)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSignInLinkInvalid
		}
		if err != nil {
			return fmt.Errorf("spending sign-in link: %w", err)
		}
		out.SetPasswordToken = passwordToken
		values := keyAdded{"set_password_offered": true, auditFieldSignInLink: link.id}
		switch {
		case user.Role == models.RoleAdmin:
			out.NoCode = NoCodeAdministrator
		case !keysOn:
			out.NoCode = NoCodeAppKeysOff
		default:
			code, err := insertSignInLinkAppCodeTx(ctx, tx, userID, link)
			if err != nil {
				return err
			}
			out.AppCode = &code
			values[auditFieldAppCodeKind] = appCodeKindSignInLink
			values[auditFieldExpiresAt] = auditInstant(code.ExpiresAt)
		}
		if out.NoCode != "" {
			values["no_code_reason"] = out.NoCode
		}
		audit, err = recordKeyAdded(ctx, tx, auditmutation.ActionSignInLinkRedeemed, userID, http.StatusOK, values)
		return err
	})
	if err != nil {
		return SignInLinkRedemption{}, err
	}
	auditmutation.CommitMutation(ctx, audit)
	return out, nil
}

// Audit fields naming the link a redeem spent and the account that made
// it; never its token.
const (
	auditFieldSignInLink          = "sign_in_link_id"
	auditFieldSignInLinkCreatedBy = "sign_in_link_created_by"
)

// spentSignInLink is the link a redeem spent, as its app code records it.
type spentSignInLink struct {
	id        int64
	createdBy *int
}

// lockSignInLinkAccount locks the account a link or its set-password token
// belongs to, then the settings, and answers the account as locked: a
// disabled account's link is ErrSignInLinkInvalid, and while local password
// sign-in is off the link is ErrSignInLinkNeedsProvider. Holding the
// settings lock serializes the use with a change of
// auth.local_password_login.
func lockSignInLinkAccount(ctx context.Context, tx pgx.Tx, userID int) (*models.User, error) {
	user, err := lockEnabledUser(ctx, tx, userID)
	if errors.Is(err, ErrUserDisabled) || IsNotFound(err) {
		return nil, ErrSignInLinkInvalid
	}
	if err != nil {
		return nil, err
	}
	if err := lockServerSettings(ctx, tx); err != nil {
		return nil, err
	}
	allowed, err := localPasswordLoginAllowed(ctx, tx)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrSignInLinkNeedsProvider
	}
	return user, nil
}

// insertSignInLinkAppCodeTx stores an app code of kind sign_in_link for the
// account, recording the link and who made it. Such a code is bound to no
// session and no provider identity: the administrator who made the link
// vouched for the person.
func insertSignInLinkAppCodeTx(ctx context.Context, tx pgx.Tx, userID int, link spentSignInLink) (IssuedAppCode, error) {
	code, hash, err := newAppCode()
	if err != nil {
		return IssuedAppCode{}, err
	}
	out := IssuedAppCode{Code: code}
	if err := tx.QueryRow(ctx, `INSERT INTO auth_app_codes (code_hash, kind, user_id, sign_in_link_id, sign_in_link_created_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, clock_timestamp() + make_interval(secs => $6)) RETURNING expires_at`,
		hash, appCodeKindSignInLink, userID, link.id, link.createdBy, AppCodeLifetime.Seconds()).Scan(&out.ExpiresAt); err != nil {
		return IssuedAppCode{}, fmt.Errorf("storing app code: %w", err)
	}
	return out, nil
}

// SetPasswordWithSignInLink sets the account's password with the token a
// redeemed link handed out, once. The password passes the rules a password
// reset's completion checks, before the token is looked at, so a refused
// password doesn't spend it. As a reset does, it settles a temporary
// password and signs the account out everywhere (ResetPasswordInTransaction);
// it also turns the account's local password sign-in on. App keys stay.
// It answers the account's ID, for the caller's post-commit hook. An
// unknown, used or expired token is ErrSignInLinkInvalid.
func (a *AppKeySignIn) SetPasswordWithSignInLink(ctx context.Context, token, newPassword string) (int, error) {
	if err := CheckNewPassword(ctx, a.service.settings, newPassword); err != nil {
		return 0, err
	}
	hash, ok := appCodeHash(token)
	if !ok {
		return 0, ErrSignInLinkInvalid
	}
	var userID int
	err := a.pool.QueryRow(ctx, `SELECT user_id FROM auth_sign_in_links
		WHERE set_password_hash = $1 AND set_password_used_at IS NULL AND set_password_expires_at > clock_timestamp()`, hash).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrSignInLinkInvalid
	}
	if err != nil {
		return 0, fmt.Errorf("reading set-password token: %w", err)
	}
	var audit *auditmutation.Entry
	err = a.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := lockSignInLinkAccount(ctx, tx, userID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE auth_sign_in_links SET set_password_used_at = clock_timestamp()
			WHERE set_password_hash = $1 AND user_id = $2 AND set_password_used_at IS NULL
				AND set_password_expires_at > clock_timestamp()`, hash, userID)
		if err != nil {
			return fmt.Errorf("spending set-password token: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrSignInLinkInvalid
		}
		if err := ResetPasswordInTransaction(ctx, tx, userID, newPassword); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET local_password_login_enabled = true WHERE id = $1`, userID); err != nil {
			return fmt.Errorf("turning local password sign-in on: %w", err)
		}
		audit, err = auditmutation.RecordMutation(ctx, tx, auditmutation.ActionSignInLinkPasswordSet, auditmutation.TargetUser,
			strconv.Itoa(userID), http.StatusNoContent, auditmutation.SecretChanged(auditmutation.FieldPassword))
		return err
	})
	if err != nil {
		return 0, err
	}
	auditmutation.CommitMutation(ctx, audit)
	return userID, nil
}

// LocalPasswordLoginAllowed reads the server-wide local password switch,
// which a sign-in link needs.
func (a *AppKeySignIn) LocalPasswordLoginAllowed(ctx context.Context) (bool, error) {
	return localPasswordLoginAllowed(ctx, a.pool)
}

// redeemSignInLinkCode enrolls the keys of a sign-in link's app code on its
// account. The person lost every device, so the request's person key
// becomes the account's: enrolled when the account has none or holds it
// already, and in place of a different one otherwise, which is revoked
// with the devices it introduced that still wait for approval, as
// replacing the person key does. Devices already enrolled keep their keys.
// A new or replacing person key and its device are bound to no provider;
// the account's own person key sent again keeps its provider binding, and
// the device enrolled under it takes that binding, as a device it introduces
// does. An administrator's account is refused. Lock order: the account, the
// code, the settings or the provider locks, the person key, the device keys
// and sessions.
func (a *AppKeySignIn) redeemSignInLinkCode(ctx context.Context, req KeyRedeemRequest, payload *appkeys.Payload, hash []byte, code appCodeRow, out *KeySignInResult) (*auditmutation.Entry, error) {
	binding, err := ownPersonKeyBinding(ctx, a.pool, *code.userID, req.PersonKey)
	if err != nil {
		return nil, err
	}
	expiry, err := a.service.keyProviderExpiry(ctx, *code.userID, binding)
	if err != nil {
		return nil, sessionIdentityGone(err)
	}
	var audit, replaced *auditmutation.Entry
	err = a.inTx(ctx, func(tx pgx.Tx) error {
		user, err := lockEnabledUser(ctx, tx, *code.userID)
		if err != nil {
			return err
		}
		// A link gives an administrator the password route only. Promotion
		// deletes the account's link codes; this holds the line without it.
		if user.Role == models.RoleAdmin {
			return ErrAppCodeInvalid
		}
		if err := spendAppCode(ctx, tx, hash); err != nil {
			return err
		}
		chain, err := lockChain(ctx, tx, user, binding)
		if err != nil {
			return sessionIdentityGone(err)
		}
		took, err := takeOverPersonKeyTx(ctx, tx, user.ID, req.PersonKey, req.IP)
		if err != nil {
			return keyEnrolledAs(KeyMemberPerson, err)
		}
		// The binding read before the locks must be the locked key's: a new
		// key is unbound, the account's own key keeps its binding.
		lockedBinding := KeyBinding{}
		if !took.created {
			lockedBinding = personKeyBinding(took.person)
		}
		if !lockedBinding.same(binding) {
			return errKeyBindingChanged
		}
		key, err := a.enrollRedeemedDevice(ctx, tx, user, took.person, binding, payload, chain.session(req.IP, expiry), took.status, out)
		if err != nil {
			return err
		}
		link := map[string]any{auditFieldSignInLink: code.signInLinkID}
		if code.signInLinkCreatedBy != nil {
			link[auditFieldSignInLinkCreatedBy] = *code.signInLinkCreatedBy
		}
		values := keyAdded{auditFieldAppCodeKind: appCodeKindSignInLink}.withDevice(key).withPerson(took.person, took.created)
		maps.Copy(values, link)
		before := map[string]any{}
		if took.replaced != "" {
			before[auditFieldPersonKey] = took.replaced
		}
		changes := auditmutation.Changes(before, values)
		if took.denied > 0 {
			changes = append(changes, auditmutation.Created(auditFieldDeniedDeviceKeys, took.denied)...)
		}
		audit, err = auditmutation.RecordMutation(ctx, tx, auditmutation.ActionAppCodeRedeemed, auditmutation.TargetUser,
			strconv.Itoa(user.ID), http.StatusOK, changes)
		if err != nil || took.replaced == "" {
			return err
		}
		// A replacement is also recorded as one, as replacePersonKey records
		// it, so a watch on person key replacements sees it: the key before
		// and after, the link and the account that made the link.
		after := map[string]any{auditFieldPersonKey: took.person.KeyID}
		maps.Copy(after, link)
		changes = auditmutation.Changes(map[string]any{auditFieldPersonKey: took.replaced}, after)
		if took.denied > 0 {
			changes = append(changes, auditmutation.Created(auditFieldDeniedDeviceKeys, took.denied)...)
		}
		replaced, err = auditmutation.RecordMutation(ctx, tx, auditmutation.ActionPersonKeyReplaced, auditmutation.TargetUser,
			strconv.Itoa(user.ID), http.StatusOK, changes)
		return err
	})
	if err != nil {
		return nil, err
	}
	auditmutation.CommitMutation(ctx, replaced)
	return audit, nil
}

// ownPersonKeyBinding is the provider binding a sign-in link's redeem
// enrolls under: the account's live person key's when the request sends that
// key (read before any lock; the redeem checks it again under the locks),
// none otherwise.
func ownPersonKeyBinding(ctx context.Context, db rowQuerier, userID int, key appkeys.PublicKey) (KeyBinding, error) {
	var identityID *int64
	var providerBound bool
	err := db.QueryRow(ctx, `SELECT identity_id, provider_bound FROM auth_person_keys
		WHERE user_id = $1 AND key_id = $2 AND revoked_at IS NULL`, userID, key.KeyID()).Scan(&identityID, &providerBound)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !providerBound) {
		return KeyBinding{}, nil
	}
	if err != nil {
		return KeyBinding{}, fmt.Errorf("reading person key binding: %w", err)
	}
	return KeyBinding{IdentityID: identityID, ProviderBound: true}, nil
}

// personKeyBinding is a person key's provider binding.
func personKeyBinding(key *models.AppPersonKey) KeyBinding {
	if !key.ProviderBound {
		return KeyBinding{}
	}
	return KeyBinding{IdentityID: key.IdentityID, ProviderBound: true}
}

// personKeyTakeOver is what a sign-in link's redeem did with the
// account's person key.
type personKeyTakeOver struct {
	person *models.AppPersonKey
	// status is PersonKeyEnrolled or PersonKeyReplaced.
	status string
	// created reports that the redeem stored the key.
	created bool
	// replaced is the kid of the person key it retired, or "".
	replaced string
	// denied counts the waiting devices retired with it.
	denied int64
}

// takeOverPersonKeyTx makes key the account's person key for a sign-in
// link's redeem: PersonKeyEnrolled when the account had none or holds key
// already, PersonKeyReplaced when it held another, which is retired with
// the devices it introduced that still wait for approval.
func takeOverPersonKeyTx(ctx context.Context, tx pgx.Tx, userID int, key appkeys.PublicKey, ip string) (personKeyTakeOver, error) {
	current, err := livePersonKeyTx(ctx, tx, userID)
	if err != nil {
		return personKeyTakeOver{}, err
	}
	out := personKeyTakeOver{status: PersonKeyEnrolled, created: true}
	if current != nil {
		if current.KeyID == key.KeyID() {
			return personKeyTakeOver{person: current, status: PersonKeyEnrolled}, nil
		}
		if out.denied, err = retirePersonKeyTx(ctx, tx, userID, current.ID); err != nil {
			return personKeyTakeOver{}, err
		}
		out.status, out.replaced = PersonKeyReplaced, current.KeyID
	}
	out.person, err = insertPersonKeyTx(ctx, tx, NewPersonKey{
		UserID: userID, PublicKey: key, EnrolledVia: models.AppKeyEnrolledAppCode, IP: ip,
	})
	if err != nil {
		return personKeyTakeOver{}, err
	}
	return out, nil
}
