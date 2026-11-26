package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/models"
)

// The facts below are written as SQL over users u, so one query can judge
// every account. They mirror the Go checks named beside each; a change to
// one of those changes its fragment too. A full restore lists the accounts
// it leaves with no way in with AccountHasNoWayInSQL, so the restore and
// the settings checks agree.

// usablePasswordSQL is models.User.HasLocalPassword: a stored hash and the
// account's own local password sign-in on. Whether the server accepts local
// passwords is separate (passwordWorksSQL).
const usablePasswordSQL = `(u.local_password_login_enabled AND COALESCE(u.password_hash, '') <> '')`

// usableIdentitySQL is usableIdentityCount's rule, the one the unlink guard
// uses: an identity of the account at an enabled installation with an
// enabled sign-in binding, so the person can sign in through the provider.
const usableIdentitySQL = `EXISTS (SELECT 1 FROM plugin_auth_identities i
	JOIN plugin_installations p ON p.id = i.plugin_installation_id AND p.enabled
	WHERE i.user_id = u.id AND EXISTS (
		SELECT 1 FROM plugin_auth_bindings b WHERE b.plugin_installation_id = p.id AND b.enabled))`

// passwordWorksSQL is a password that signs in now: a usable one, and the
// server allows local passwords (localOn, a boolean SQL expression) or the
// account is break-glass (localPasswordPolicy).
func passwordWorksSQL(localOn string) string {
	return `(` + usablePasswordSQL + ` AND (` + localOn + ` OR u.break_glass))`
}

// keyWorksSQL is a key that signs in while app key sign-in is on, with local
// passwords as localOn says. A key that is not provider-bound follows
// localPasswordPolicy (keySignInPolicy): an active device key opens a
// session, and so does a person key introducing a device, except on an
// administrator's account, where the new key waits for approval. A
// provider-bound key needs its identity at an enabled installation
// (lockKeyIdentity), whatever the local password setting. A waiting or
// denied device key is no way in.
func keyWorksSQL(localOn string) string {
	return `((` + localOn + ` OR u.break_glass) AND (
		EXISTS (SELECT 1 FROM auth_device_keys k WHERE k.user_id = u.id AND k.revoked_at IS NULL
			AND k.status = 'active' AND NOT k.provider_bound)
		OR (u.role <> 'admin' AND EXISTS (SELECT 1 FROM auth_person_keys k WHERE k.user_id = u.id
			AND k.revoked_at IS NULL AND NOT k.provider_bound)))
	OR EXISTS (SELECT 1 FROM plugin_auth_identities i
		JOIN plugin_installations p ON p.id = i.plugin_installation_id AND p.enabled
		WHERE i.user_id = u.id AND (
			EXISTS (SELECT 1 FROM auth_device_keys k WHERE k.user_id = u.id AND k.identity_id = i.id
				AND k.revoked_at IS NULL AND k.status = 'active' AND k.provider_bound)
			OR (u.role <> 'admin' AND EXISTS (SELECT 1 FROM auth_person_keys k WHERE k.user_id = u.id
				AND k.identity_id = i.id AND k.revoked_at IS NULL AND k.provider_bound)))))`
}

// AccountHasNoWayInSQL is the condition on an account u (the users row) that
// it has no way to sign in under the two switches: appKeysOn and localOn
// are boolean SQL expressions (placeholders or literals) for the app key
// switch and auth.local_password_login. No provider identity it can sign in
// with, no password that signs in, and no key that does. Whether the
// account is enabled is the caller's condition.
func AccountHasNoWayInSQL(appKeysOn, localOn string) string {
	return `(NOT ` + usableIdentitySQL + ` AND NOT ` + passwordWorksSQL(localOn) +
		` AND NOT (` + appKeysOn + ` AND ` + keyWorksSQL(localOn) + `))`
}

// AccountHasNoWayInWithoutKeysSQL is AccountHasNoWayInSQL for a database
// with no app key tables (a backup from before them), where no key is a way
// in.
func AccountHasNoWayInWithoutKeysSQL(localOn string) string {
	return `(NOT ` + usableIdentitySQL + ` AND NOT ` + passwordWorksSQL(localOn) + `)`
}

// SignInChange names a server-wide sign-in switch going off.
type SignInChange string

// The two changes AccountsAtRisk judges.
const (
	// ChangeAppKeysOff is the app key switch going off.
	ChangeAppKeysOff SignInChange = "app_keys_off"
	// ChangeLocalPasswordsOff is auth.local_password_login going off.
	ChangeLocalPasswordsOff SignInChange = "local_passwords_off"
)

// AccountAtRisk is an enabled account that a switch change leaves with no
// way in, or with keys as its only way in.
type AccountAtRisk struct {
	ID         int
	Username   string
	Role       string
	IsOwner    bool
	BreakGlass bool
	// HasPassword is a usable local password (models.User.HasLocalPassword),
	// whatever the server's local password setting.
	HasPassword bool
	// SignInLinkExpiresAt is when the account's live, unused sign-in link
	// expires, or nil for none. This server makes no sign-in links yet, so
	// it is always nil and every listed account blocks the change.
	SignInLinkExpiresAt *time.Time
}

// accountsAtRiskSQL lists the accounts at risk for each change. The app
// keys query takes $1, auth.local_password_login as it is now.
//
//   - App keys off: an account whose keys sign in now (keyWorksSQL under $1)
//     and that has no password that signs in and no provider identity. With
//     the switch off its keys are no way in, so it has none. The list does
//     not depend on the switch: while it is already off it names the
//     accounts that still have no way in.
//   - Local passwords off: an account that holds a key that works with local
//     passwords on, and has no usable password and no provider identity.
//     Afterwards a key that is not provider-bound works only for a
//     break-glass account, so the account is left with keys as its only way
//     in, or with none; a password would not help it then, so only linking
//     a provider takes it off the list.
var accountsAtRiskSQL = map[SignInChange]string{
	ChangeAppKeysOff:        `NOT ` + passwordWorksSQL("$1") + ` AND NOT ` + usableIdentitySQL + ` AND ` + keyWorksSQL("$1"),
	ChangeLocalPasswordsOff: `NOT ` + usablePasswordSQL + ` AND NOT ` + usableIdentitySQL + ` AND ` + keyWorksSQL("true"),
}

// AccountsAtRisk lists the enabled accounts change would leave with no way
// in, or with keys as their only way in, in username order. db is the pool
// or the settings write's transaction.
func AccountsAtRisk(ctx context.Context, db dbQuerier, change SignInChange) ([]AccountAtRisk, error) {
	where, ok := accountsAtRiskSQL[change]
	if !ok {
		return nil, fmt.Errorf("unknown sign-in change %q", change)
	}
	var args []any
	if change == ChangeAppKeysOff {
		localOn, err := localPasswordLoginAllowed(ctx, db)
		if err != nil {
			return nil, err
		}
		args = append(args, localOn)
	}
	rows, err := db.Query(ctx, `SELECT u.id, COALESCE(u.username::text, u.email::text, ''), u.role, u.is_owner,
			u.break_glass, `+usablePasswordSQL+`
		FROM users u
		WHERE u.enabled AND `+where+`
		ORDER BY lower(COALESCE(u.username::text, u.email::text, '')), u.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("listing accounts at risk: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (AccountAtRisk, error) {
		var a AccountAtRisk
		err := row.Scan(&a.ID, &a.Username, &a.Role, &a.IsOwner, &a.BreakGlass, &a.HasPassword)
		return a, err
	})
}

// AccountsAtRiskError refuses a switch change while Accounts would be left
// with no way in, or with keys as their only way in. It is
// ErrLastSignInMethod to errors.Is: the change would take away an account's
// last way to sign in.
type AccountsAtRiskError struct {
	Change   SignInChange
	Accounts []AccountAtRisk
}

func (e *AccountsAtRiskError) Error() string { return e.Detail() }

// Unwrap makes the refusal ErrLastSignInMethod.
func (e *AccountsAtRiskError) Unwrap() error { return ErrLastSignInMethod }

// accountsNamed is how many accounts a refusal names before it counts the
// rest.
const accountsNamed = 10

// Detail is the refusal's sentence, which names the accounts (at most
// accountsNamed, then how many more) and the one remedy that works for the
// change.
func (e *AccountsAtRiskError) Detail() string {
	names := make([]string, 0, min(len(e.Accounts), accountsNamed))
	for _, a := range e.Accounts[:min(len(e.Accounts), accountsNamed)] {
		names = append(names, a.Username)
	}
	list := strings.Join(names, ", ")
	if more := len(e.Accounts) - len(names); more > 0 {
		list += fmt.Sprintf(" and %d more", more)
	}
	accounts := fmt.Sprintf("%d accounts", len(e.Accounts))
	if len(e.Accounts) == 1 {
		accounts = "1 account"
	}
	if e.Change == ChangeLocalPasswordsOff {
		return accounts + " would have only Silo app keys, or no way at all, to sign in: " + list +
			". Link a sign-in provider account for each, then turn local passwords off."
	}
	return accounts + " sign in only with Silo app keys: " + list +
		". Give each a password or link a sign-in provider account for it, then turn app keys off."
}

// refuseAccountsAtRisk lists the accounts change leaves at risk and refuses
// with *AccountsAtRiskError while any of them blocks it. For app keys off an
// account with a live sign-in link does not block (SignInLinkExpiresAt);
// this server makes none yet, so every listed account does.
func refuseAccountsAtRisk(ctx context.Context, db dbQuerier, change SignInChange) error {
	accounts, err := AccountsAtRisk(ctx, db, change)
	if err != nil {
		return err
	}
	blocking := accounts[:0]
	for _, a := range accounts {
		if change == ChangeLocalPasswordsOff || a.SignInLinkExpiresAt == nil {
			blocking = append(blocking, a)
		}
	}
	if len(blocking) > 0 {
		return &AccountsAtRiskError{Change: change, Accounts: blocking}
	}
	return nil
}

// CheckAppKeysOff refuses turning the app key switch off while an account
// would be left with no way in (AccountsAtRisk, ChangeAppKeysOff). The
// settings write calls it in its transaction, under the settings lock,
// before it ends the key sessions.
func CheckAppKeysOff(ctx context.Context, tx pgx.Tx) error {
	return refuseAccountsAtRisk(ctx, tx, ChangeAppKeysOff)
}

// ErrKeysOnlyWayIn refuses an administrator change that would make app keys
// an account's only way in while local password sign-in is off: making a
// break-glass admin of an account with keys and no usable password or
// provider identity (its keys start to work, its missing password still
// doesn't matter), or taking the password sign-in of such a break-glass
// admin away.
var ErrKeysOnlyWayIn = fmt.Errorf("%w: local password sign-in is off, and the change would leave the account with app keys as its only way in", ErrLastSignInMethod)

// keysOnlyCandidate is an account whose keys would work while local
// passwords are off and that has no password to sign in with instead.
func keysOnlyCandidate(u *models.User) bool {
	return u.BreakGlass && u.Role == models.RoleAdmin && u.Enabled && !u.HasLocalPassword()
}

// EnsureKeysNotOnlyWayInAfterAdminChange refuses, with ErrKeysOnlyWayIn, an
// administrator change to current that would leave it with app keys as its
// only way in while local password sign-in is off (keysOnlyCandidate, and
// no provider identity it can sign in with). An account already in that
// state is not refused an unrelated edit. It runs in the account mutation's
// transaction, after the account row lock, and takes the settings lock, as
// EnsureBreakGlassAfterAdminChange does.
func EnsureKeysNotOnlyWayInAfterAdminChange(ctx context.Context, tx pgx.Tx, current *models.User, input *models.UpdateUserInput) error {
	if current == nil || input == nil || keysOnlyCandidate(current) {
		return nil
	}
	after := *current
	if input.BreakGlass != nil {
		after.BreakGlass = *input.BreakGlass
	}
	if input.Role != nil {
		after.Role = *input.Role
		if after.Role != models.RoleAdmin {
			after.BreakGlass = false
		}
	}
	if input.Enabled != nil {
		after.Enabled = *input.Enabled
	}
	if input.LocalPasswordLoginEnabled != nil {
		after.LocalPasswordLoginEnabled = *input.LocalPasswordLoginEnabled
	}
	if input.Password != nil && *input.Password != "" {
		after.PasswordHash = "set"
	}
	if !keysOnlyCandidate(&after) || tx == nil {
		// No transaction is a store that can't be read (a test fake); the
		// account mutation always passes its own.
		return nil
	}
	if err := lockServerSettings(ctx, tx); err != nil {
		return err
	}
	allowed, err := localPasswordLoginAllowed(ctx, tx)
	if err != nil || allowed {
		return err
	}
	// After the change the account is break-glass, so its local keys work
	// as they would with local passwords on; the stored row still has the
	// flag as it was.
	var keysOnly bool
	if err := tx.QueryRow(ctx, `SELECT NOT `+usableIdentitySQL+` AND `+keyWorksSQL("true")+`
		FROM users u WHERE u.id = $1`, current.ID).Scan(&keysOnly); err != nil {
		return fmt.Errorf("checking the account's other ways in: %w", err)
	}
	if keysOnly {
		return ErrKeysOnlyWayIn
	}
	return nil
}
