package auth

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/plugins"
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

// usableIdentitySQL is an identity of the account at one of providers (a
// SQL int[] expression, the installations signInProviders answers): a
// provider the person can sign in with from an ordinary device.
func usableIdentitySQL(providers string) string {
	return `EXISTS (SELECT 1 FROM plugin_auth_identities i
	WHERE i.user_id = u.id AND i.plugin_installation_id = ANY(` + providers + `))`
}

// primarySignInBindingSQL is the SQL test that the plugin_auth_bindings row
// named by alias is an enabled OIDC or LDAP binding: enabled, and not a
// network identity binding, which signs in only people on its network.
func primarySignInBindingSQL(alias string) string {
	return alias + `.enabled AND NOT ` + plugins.AuthBindingIsNetworkSQL(alias+".plugin_installation_id", alias+".capability_id")
}

// signInProviders answers the installations whose identities can sign people
// in: an enabled installation with an enabled OIDC or LDAP binding
// (primarySignInBindingSQL), and for an OAuth binding a public URL
// (OAuthSignInServed). A network identity signs in only from a device on
// its network, and an OAuth provider signs nobody in without a public URL,
// so neither counts; a credentials (directory) provider needs no public URL.
// It is the one rule for "a provider people can sign in with" here, written
// so a single definition can replace it and any other copy.
func signInProviders(ctx context.Context, db dbQuerier, publicURL string) ([]int, error) {
	rows, err := db.Query(ctx, `SELECT p.id, COALESCE((SELECT c.metadata->'auth_modes' FROM plugin_capabilities c
			WHERE c.plugin_installation_id = b.plugin_installation_id AND c.capability_type = 'auth_provider.v1'
				AND c.capability_id = b.capability_id), '[]'::jsonb)
		FROM plugin_installations p
		JOIN plugin_auth_bindings b ON b.plugin_installation_id = p.id AND `+primarySignInBindingSQL("b")+`
		WHERE p.enabled`)
	if err != nil {
		return nil, fmt.Errorf("checking for an enabled sign-in provider: %w", err)
	}
	type binding struct {
		InstallationID int
		Modes          []string
	}
	bindings, err := pgx.CollectRows(rows, pgx.RowToStructByPos[binding])
	if err != nil {
		return nil, fmt.Errorf("checking for an enabled sign-in provider: %w", err)
	}
	providers := []int{}
	for _, b := range bindings {
		if ProviderModeForAuthModes(b.Modes) != ProviderModeOAuth || OAuthSignInServed(publicURL) {
			providers = append(providers, b.InstallationID)
		}
	}
	return providers, nil
}

// SignInProviderInstallations is signInProviders with the public URL db
// stores (server.public_url), for a database that is not this server's
// own yet, such as a restored one.
func SignInProviderInstallations(ctx context.Context, db dbQuerier) ([]int, error) {
	publicURL, err := readServerSetting(ctx, db, config.ServerPublicURLSettingKey)
	if err != nil {
		return nil, err
	}
	return signInProviders(ctx, db, publicURL)
}

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
// switch and auth.local_password_login, and providers an int[] expression
// for SignInProviderInstallations. No provider identity it can sign in
// with, no password that signs in, and no key that does. Whether the
// account is enabled is the caller's condition.
func AccountHasNoWayInSQL(appKeysOn, localOn, providers string) string {
	return `(NOT ` + usableIdentitySQL(providers) + ` AND NOT ` + passwordWorksSQL(localOn) +
		` AND NOT (` + appKeysOn + ` AND ` + keyWorksSQL(localOn) + `))`
}

// AccountHasNoWayInWithoutKeysSQL is AccountHasNoWayInSQL for a database
// with no app key tables (a backup from before them), where no key is a way
// in.
func AccountHasNoWayInWithoutKeysSQL(localOn, providers string) string {
	return `(NOT ` + usableIdentitySQL(providers) + ` AND NOT ` + passwordWorksSQL(localOn) + `)`
}

// SignInChange names a server-wide sign-in switch going off.
type SignInChange string

// The two changes AccountsAtRisk judges.
const (
	// ChangeAppKeysOff is the app key switch going off.
	ChangeAppKeysOff SignInChange = "app_keys_off"
	// ChangeLocalPasswordsOff is auth.local_password_login going off.
	ChangeLocalPasswordsOff SignInChange = "local_passwords_off"
	// changePublicURL is server.public_url changing, which only a settings
	// write judges (CheckPublicURLChange).
	changePublicURL SignInChange = "public_url"
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

// accountsAtRiskSQL lists the accounts at risk for each change, judged on
// the settings the change leaves: $1 is the installations whose identities
// can sign people in under them (signInProviders), and the app keys query's
// $2 is auth.local_password_login.
//
//   - App keys off: an account whose keys sign in now (keyWorksSQL under $2)
//     and that has no password that signs in and no provider identity. With
//     the switch off its keys are no way in, so it has none. The list does
//     not depend on the switch: while it is already off it names the
//     accounts that still have no way in.
//   - Local passwords off: an account that afterwards has no password that
//     signs in and no provider identity, and either holds working keys and
//     no usable password (afterwards a key that is not provider-bound works
//     only for a break-glass account, so it has keys as its only way in, or
//     none), or keeps a key that still works then (a provider-bound key, or
//     a break-glass account's), which would be its only way in. An account
//     with a password and only local keys is left with no way in, as an
//     account with only a password always has been, and is not listed.
var accountsAtRiskSQL = map[SignInChange]string{
	ChangeAppKeysOff: `NOT ` + passwordWorksSQL("$2") + ` AND NOT ` + usableIdentitySQL("$1") + ` AND ` + keyWorksSQL("$2"),
	ChangeLocalPasswordsOff: `NOT ` + passwordWorksSQL("false") + ` AND NOT ` + usableIdentitySQL("$1") +
		` AND ((NOT ` + usablePasswordSQL + ` AND ` + keyWorksSQL("true") + `) OR ` + keyWorksSQL("false") + `)`,
}

// AccountsAtRisk lists the enabled accounts change would leave with no way
// in, or with keys as their only way in, in username order. db is the pool
// or the settings write's transaction.
func AccountsAtRisk(ctx context.Context, db dbQuerier, change SignInChange) ([]AccountAtRisk, error) {
	settings, err := storedSignInSettings(ctx, db)
	if err != nil {
		return nil, err
	}
	accounts, _, err := accountsAtRisk(ctx, db, change, settings)
	return accounts, err
}

// SignInSettings are the two settings the "no way in" checks read:
// auth.local_password_login, and server.public_url, which an OAuth provider
// needs. A settings write passes the values it leaves; a read takes the
// stored ones.
type SignInSettings struct {
	localOn   bool
	publicURL string
}

// SignInSettingsOf is SignInSettings for effective setting values (a
// settings write's before or after).
func SignInSettingsOf(local, publicURL string) SignInSettings {
	return SignInSettings{localOn: settingBool(local, true), publicURL: publicURL}
}

// storedSignInSettings reads both settings in one statement, so a read never
// mixes a value from before a settings write with one from after it.
func storedSignInSettings(ctx context.Context, db dbQuerier) (SignInSettings, error) {
	var local, publicURL string
	if err := db.QueryRow(ctx, `SELECT
			COALESCE((SELECT value FROM server_settings WHERE key = $1), ''),
			COALESCE((SELECT value FROM server_settings WHERE key = $2), '')`,
		config.AuthLocalPasswordLoginSettingKey, config.ServerPublicURLSettingKey).Scan(&local, &publicURL); err != nil {
		return SignInSettings{}, fmt.Errorf("reading the sign-in settings: %w", err)
	}
	return SignInSettings{localOn: settingBool(local, true), publicURL: strings.TrimSpace(publicURL)}, nil
}

// accountsAtRisk is AccountsAtRisk under settings, and reports whether any
// provider can sign people in under them (for the remedy).
func accountsAtRisk(ctx context.Context, db dbQuerier, change SignInChange, settings SignInSettings) ([]AccountAtRisk, bool, error) {
	where, ok := accountsAtRiskSQL[change]
	if !ok {
		return nil, false, fmt.Errorf("unknown sign-in change %q", change)
	}
	providers, err := signInProviders(ctx, db, settings.publicURL)
	if err != nil {
		return nil, false, err
	}
	args := []any{providers}
	if change == ChangeAppKeysOff {
		args = append(args, settings.localOn)
	}
	accounts, err := listAccountsAtRisk(ctx, db, where, args...)
	return accounts, len(providers) > 0, err
}

// listAccountsAtRisk lists the enabled accounts where selects, in username
// order.
func listAccountsAtRisk(ctx context.Context, db dbQuerier, where string, args ...any) ([]AccountAtRisk, error) {
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
	// LocalOn is auth.local_password_login as the change leaves it, and
	// ProviderAvailable whether any provider can sign people in then: they
	// decide the remedy the detail offers.
	LocalOn           bool
	ProviderAvailable bool
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
	breakGlass := slices.ContainsFunc(e.Accounts, func(a AccountAtRisk) bool { return a.BreakGlass })
	switch e.Change {
	case ChangeLocalPasswordsOff:
		return countAccounts(len(e.Accounts), "would", "would") + " have only Silo app keys, or no way at all, to sign in: " + list +
			". " + WayInRemedy(false, breakGlass, e.ProviderAvailable) + ", then turn local passwords off."
	case changePublicURL:
		return countAccounts(len(e.Accounts), "would", "would") + " be left with no sign-in provider they can use, and no way in but app keys, or none: " + list +
			". " + WayInRemedy(e.LocalOn, breakGlass, e.ProviderAvailable) + ", then change the address."
	}
	return countAccounts(len(e.Accounts), "signs", "sign") + " in only with Silo app keys: " + list +
		". " + WayInRemedy(e.LocalOn, breakGlass, e.ProviderAvailable) + ", then turn app keys off."
}

// countAccounts is "1 account <one>" or "N accounts <many>".
func countAccounts(n int, one, many string) string {
	if n == 1 {
		return "1 account " + one
	}
	return fmt.Sprintf("%d accounts %s", n, many)
}

// WayInRemedy is the sentence that tells an administrator how to give the
// named accounts a way in, true for the settings: a password signs in while
// local passwords are on, and otherwise only a break-glass administrator
// (offered when breakGlass says one is named); linking a provider account
// helps only while a provider can sign people in (providerAvailable), and
// otherwise one has to be set up first (an OAuth provider needs the server's
// public address).
func WayInRemedy(localOn, breakGlass, providerAvailable bool) string {
	provider := "link a sign-in provider account for each"
	if !providerAvailable {
		provider = "set up a sign-in provider people can use (an OAuth provider needs the server's public address) and link an account for each"
	}
	switch {
	case localOn:
		return "Give each a password, or " + provider
	case breakGlass:
		return strings.ToUpper(provider[:1]) + provider[1:] + ", or give a break-glass administrator a password"
	}
	return strings.ToUpper(provider[:1]) + provider[1:]
}

// refuseAccountsAtRisk lists the accounts change leaves at risk and refuses
// with *AccountsAtRiskError while any of them blocks it. For app keys off an
// account with a live sign-in link does not block (SignInLinkExpiresAt);
// this server makes none yet, so every listed account does.
func refuseAccountsAtRisk(ctx context.Context, db dbQuerier, change SignInChange, settings SignInSettings) error {
	accounts, providers, err := accountsAtRisk(ctx, db, change, settings)
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
		return &AccountsAtRiskError{Change: change, Accounts: blocking, LocalOn: settings.localOn, ProviderAvailable: providers}
	}
	return nil
}

// CheckAppKeysOff refuses turning the app key switch off while an account
// would be left with no way in (AccountsAtRisk, ChangeAppKeysOff), judged
// with auth.local_password_login as the same write leaves it (localAfter,
// the effective value). The settings write calls it in its transaction,
// under the settings lock, before it ends the key sessions.
func CheckAppKeysOff(ctx context.Context, tx pgx.Tx, after SignInSettings) error {
	return refuseAccountsAtRisk(ctx, tx, ChangeAppKeysOff, after)
}

// CheckPublicURLChange refuses changing server.public_url while an account
// would lose the only provider it can sign in with and have no password that
// signs in: an OAuth provider signs nobody in without a public URL. Whether
// its keys still sign it in (app keys on) or not (off), it would be left
// with keys as its only way in or with none. Only accounts the change takes
// a provider from are judged; before and after are the settings around the
// write.
func CheckPublicURLChange(ctx context.Context, tx pgx.Tx, before, after SignInSettings) error {
	if normalizeHostBaseURL(before.publicURL) == normalizeHostBaseURL(after.publicURL) {
		return nil
	}
	had, err := signInProviders(ctx, tx, before.publicURL)
	if err != nil {
		return err
	}
	has, err := signInProviders(ctx, tx, after.publicURL)
	if err != nil {
		return err
	}
	accounts, err := listAccountsAtRisk(ctx, tx, usableIdentitySQL("$1")+` AND NOT `+usableIdentitySQL("$2")+
		` AND NOT `+passwordWorksSQL("$3"), had, has, after.localOn)
	if err != nil || len(accounts) == 0 {
		return err
	}
	return &AccountsAtRiskError{Change: changePublicURL, Accounts: accounts, LocalOn: after.localOn, ProviderAvailable: len(has) > 0}
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
	providers, err := SignInProviderInstallations(ctx, tx)
	if err != nil {
		return err
	}
	var keysOnly bool
	if err := tx.QueryRow(ctx, `SELECT NOT `+usableIdentitySQL("$2")+` AND `+keyWorksSQL("true")+`
		FROM users u WHERE u.id = $1`, current.ID, providers).Scan(&keysOnly); err != nil {
		return fmt.Errorf("checking the account's other ways in: %w", err)
	}
	if keysOnly {
		return ErrKeysOnlyWayIn
	}
	return nil
}
