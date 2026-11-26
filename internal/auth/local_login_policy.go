package auth

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/auth/passwordhash"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

var (
	// ErrLocalLoginDisabled refuses a correct local password because the
	// server turned local password sign-in off and the account is not a
	// break-glass admin.
	ErrLocalLoginDisabled = errors.New("local password sign-in is turned off on this server")
	// ErrBreakGlassRequired refuses a change that would leave the server with
	// local password sign-in off and no enabled break-glass admin that can
	// still use its password.
	ErrBreakGlassRequired = errors.New("at least one enabled break-glass admin with password sign-in is required while local password sign-in is off")
	// ErrTemporaryPasswordRequired refuses turning an account's local
	// password sign-in back on without a temporary password while it is off:
	// the stored password may then be one nobody knows.
	ErrTemporaryPasswordRequired = errors.New("a temporary password is required to turn this account's password sign-in on: its stored password may be one nobody knows")
	// ErrSignInProviderRequired refuses a change that would leave local
	// password sign-in off with no OIDC or LDAP provider people can use:
	// turning the switch off without one, or taking the last one away while
	// it is off. Nobody but the break-glass admins could sign in.
	// The plugins package owns the value so its writes can recognize it.
	ErrSignInProviderRequired = plugins.ErrSignInProviderRequired
)

// rowQuerier is what the policy reads need: a pool or a transaction.
type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// The policy settings are plain (never encrypted) server_settings rows, read
// straight from the database on every decision so that a change made on any
// node, or with the CLI, applies at once everywhere.
func readServerSetting(ctx context.Context, db rowQuerier, key string) (string, error) {
	var value string
	err := db.QueryRow(ctx, `SELECT value FROM server_settings WHERE key = $1`, key).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading setting %s: %w", key, err)
	}
	return strings.TrimSpace(value), nil
}

func readBoolSetting(ctx context.Context, db rowQuerier, key string, fallback bool) (bool, error) {
	value, err := readServerSetting(ctx, db, key)
	if err != nil {
		return fallback, err
	}
	// Normalization stores only true or false; anything else is the default.
	return settingBool(value, fallback), nil
}

func localPasswordLoginAllowed(ctx context.Context, db rowQuerier) (bool, error) {
	return readBoolSetting(ctx, db, config.AuthLocalPasswordLoginSettingKey, true)
}

// EnsureLocalPasswordLoginAllowedInTransaction serializes local account
// creation with settings changes, so a refused signup cannot spend an invite.
func EnsureLocalPasswordLoginAllowedInTransaction(ctx context.Context, tx pgx.Tx) error {
	if err := lockServerSettings(ctx, tx); err != nil {
		return err
	}
	allowed, err := localPasswordLoginAllowed(ctx, tx)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrLocalLoginDisabled
	}
	return nil
}

func emailAutoMatchEnabled(ctx context.Context, db rowQuerier) (bool, error) {
	return readBoolSetting(ctx, db, config.AuthEmailAutoMatchSettingKey, false)
}

// LocalPasswordLoginAllowed reports the server-wide local password switch
// (auth.local_password_login, default on).
func (r *UserRepository) LocalPasswordLoginAllowed(ctx context.Context) (bool, error) {
	return localPasswordLoginAllowed(ctx, r.pool)
}

// EnableLocalPasswordLogin turns the server-wide switch back on, for the
// recovery command.
func (r *UserRepository) EnableLocalPasswordLogin(ctx context.Context) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := lockServerSettings(ctx, tx); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO server_settings (key, value) VALUES ($1, 'true')
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, config.AuthLocalPasswordLoginSettingKey)
		if err != nil {
			return fmt.Errorf("writing %s: %w", config.AuthLocalPasswordLoginSettingKey, err)
		}
		return nil
	})
}

// EnableAccountLocalLogin turns one account's local password sign-in back
// on, for the recovery command: an account linked to an external provider
// has it off, so an outage of that provider, or removing its plugin, would
// otherwise leave the account (possibly the Owner or the only admin) with no
// way in. With a non-empty temporaryPassword the account also gets that
// password, which it must replace at its next sign-in, and every sign-in it
// holds is revoked. login is a username or email.
//
// While the account's password sign-in is off, temporaryPassword is
// required (ErrTemporaryPasswordRequired): the stored hash may be a random
// password nobody knows, such as the one an account a provider created
// holds, and turning sign-in on with it would count the account as a
// break-glass admin nobody can use. The switch is read under the account's
// row lock in the transaction that changes it. With password sign-in already
// on, the call is unchanged.
func (r *UserRepository) EnableAccountLocalLogin(ctx context.Context, login, temporaryPassword string) (*models.User, error) {
	user, err := LookupLogin(ctx, r, login)
	if err != nil {
		return nil, err
	}
	var hash string
	if temporaryPassword != "" {
		raw, err := passwordhash.Generate(ctx, []byte(temporaryPassword), passwordHashCost)
		if err != nil {
			return nil, fmt.Errorf("hashing password: %w", err)
		}
		hash = string(raw)
	}
	err = pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var on bool
		if err := tx.QueryRow(ctx, `SELECT local_password_login_enabled FROM users WHERE id = $1 FOR UPDATE`, user.ID).Scan(&on); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("reading local password sign-in: %w", err)
		}
		if !on && hash == "" {
			return ErrTemporaryPasswordRequired
		}
		tag, err := tx.Exec(ctx, `UPDATE users SET local_password_login_enabled = true,
				password_hash = CASE WHEN $2 = '' THEN password_hash ELSE $2 END,
				password_change_required = CASE WHEN $2 = '' THEN password_change_required ELSE true END,
				updated_at = NOW()
			WHERE id = $1`, user.ID, hash)
		if err != nil {
			return fmt.Errorf("enabling local password sign-in: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if hash == "" {
			return nil
		}
		return RevokeSignInsInTransaction(ctx, tx, user.ID)
	})
	if err != nil {
		return nil, err
	}
	return r.GetByID(ctx, user.ID)
}

// lockServerSettings takes the advisory lock every server_settings mutation
// holds, so a break-glass change and a local-login setting change cannot
// validate against each other's stale state.
func lockServerSettings(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, config.ServerSettingsMutationLock); err != nil {
		return fmt.Errorf("acquiring settings lock: %w", err)
	}
	return nil
}

// usableBreakGlassCount counts enabled break-glass admins that can still
// use a local password, other than excludeID: the same test as
// usableBreakGlass, so a password sign-in that is on with no password stored
// does not count.
func usableBreakGlassCount(ctx context.Context, db rowQuerier, excludeID int) (int, error) {
	var count int
	err := db.QueryRow(ctx, `SELECT COUNT(*) FROM users
		WHERE break_glass AND role = 'admin' AND enabled AND `+passwordSignsInSQL("users")+` AND id <> $1`,
		excludeID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting break-glass accounts: %w", err)
	}
	return count, nil
}

// LocalLoginSettings are the settings a local password sign-in check reads,
// as effective values: auth.local_password_login and server.public_url.
type LocalLoginSettings struct {
	LocalPasswordLogin string
	PublicURL          string
}

// CheckLocalLoginSettingChange refuses a settings save that would leave
// local password sign-in off with no break-glass admin who could still sign
// in (ErrBreakGlassRequired, checked when the switch goes off), or with no
// sign-in provider that can sign everyone else in
// (ErrSignInProviderRequired, checked when the switch goes off and when the
// public URL changes while it is off: an OpenID Connect provider needs one).
// The caller runs it inside the settings mutation, which already holds the
// settings lock.
func CheckLocalLoginSettingChange(ctx context.Context, db dbQuerier, before, after LocalLoginSettings) error {
	if settingBool(after.LocalPasswordLogin, true) {
		return nil
	}
	turningOff := settingBool(before.LocalPasswordLogin, true)
	if !turningOff {
		// Already off: refuse only a public URL change that takes away the
		// last provider people can use. A server already without one may
		// change it freely, and setting a URL is a way back.
		if before.PublicURL == after.PublicURL {
			return nil
		}
		usable, err := signInProviderEnabled(ctx, db, before.PublicURL)
		if err != nil || !usable {
			return err
		}
	}
	if turningOff {
		count, err := usableBreakGlassCount(ctx, db, 0)
		if err != nil {
			return err
		}
		if count == 0 {
			return ErrBreakGlassRequired
		}
	}
	return requireSignInProvider(ctx, db, after.PublicURL)
}

// requireSignInProvider answers ErrSignInProviderRequired unless a provider
// can sign people in.
func requireSignInProvider(ctx context.Context, db plugins.SignInQuerier, publicURL string) error {
	enabled, err := signInProviderEnabled(ctx, db, publicURL)
	if err != nil {
		return err
	}
	if !enabled {
		return ErrSignInProviderRequired
	}
	return nil
}

func init() {
	plugins.RegisterSignInCheck(signInWorksForEveryone)
}

// signInWorksForEveryone is the check plugin writes and the provisioning
// status use: local password sign-in is on, or an OIDC or LDAP provider can
// sign people in. A plugin write holds the
// server-settings lock while it asks (see plugins.signInGuard).
func signInWorksForEveryone(ctx context.Context, db plugins.SignInQuerier) (bool, error) {
	allowed, err := localPasswordLoginAllowed(ctx, db)
	if err != nil || allowed {
		return allowed, err
	}
	publicURL, err := readServerSetting(ctx, db, config.ServerPublicURLSettingKey)
	if err != nil {
		return false, err
	}
	return signInProviderEnabled(ctx, db, publicURL)
}

// signInProviderEnabled reports whether an OIDC or LDAP sign-in provider can
// sign people in: an enabled installation with an enabled sign-in binding
// that is not a network identity binding, whose mode is served. A network
// provider signs in only people on its network, and an OAuth provider signs
// nobody in without a public URL (OAuthSignInServed), so neither counts. A
// credentials (directory) provider needs no public URL.
func signInProviderEnabled(ctx context.Context, db plugins.SignInQuerier, publicURL string) (bool, error) {
	providers, err := signInProviders(ctx, db, publicURL)
	return len(providers) > 0, err
}

// signInProviders answers the installations that can sign people in, by the
// rule signInProviderEnabled states: an enabled installation with an enabled
// OIDC or LDAP binding, and for an OpenID Connect binding a public URL. The
// guard asks whether there is one; LocalPasswordOffAccounts asks whether an
// account's link is to one.
func signInProviders(ctx context.Context, db plugins.SignInQuerier, publicURL string) ([]int, error) {
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

// primarySignInBindingSQL is the SQL test that the plugin_auth_bindings row
// named by alias is an enabled OIDC or LDAP binding: enabled, and not a
// network identity binding, which signs in only people on its network.
func primarySignInBindingSQL(alias string) string {
	return alias + `.enabled AND NOT ` + plugins.AuthBindingIsNetworkSQL(alias+".plugin_installation_id", alias+".capability_id")
}

// passwordSignsInSQL is the SQL form of models.User.HasLocalPassword, which
// the sign-in path asks: password sign-in on for the account and a password
// set. Change both together.
func passwordSignsInSQL(alias string) string {
	return alias + `.local_password_login_enabled AND COALESCE(` + alias + `.password_hash, '') <> ''`
}

// Who loses password sign-in when the server turns it off, grouped by what
// connects them to the provider at their first sign-in through it.
const (
	// LocalPasswordOffEmailMatch: email auto-match can link the account.
	LocalPasswordOffEmailMatch = "email_match"
	// LocalPasswordOffAdminLink: nothing links the account until an
	// administrator does.
	LocalPasswordOffAdminLink = "admin_link"
)

// What decides an affected account's outcome (LocalPasswordOffAccount.Reason).
const (
	// LocalPasswordOffReasonEmailMatch: an ordinary account with an email,
	// and email auto-match is on.
	LocalPasswordOffReasonEmailMatch = "email_match"
	// LocalPasswordOffReasonEmailMatchOff: an ordinary account with an
	// email, but email auto-match is off.
	LocalPasswordOffReasonEmailMatchOff = "email_match_off"
	// LocalPasswordOffReasonAdministrator: an admin or the Owner, which
	// email auto-match never links.
	LocalPasswordOffReasonAdministrator = "administrator"
	// LocalPasswordOffReasonNoEmail: an ordinary account without an email.
	LocalPasswordOffReasonNoEmail = "no_email"
)

// LocalPasswordOffQuery asks for one page of one group.
type LocalPasswordOffQuery struct {
	Group string
	// EmailAutoMatch overrides the saved auth.email_auto_match, to answer
	// for an edit that changes both settings; nil uses the saved value.
	EmailAutoMatch *bool
	// AfterUsername and AfterID are the last row of the previous page;
	// empty and 0 for the first.
	AfterUsername string
	AfterID       int
	Limit         int
}

// LocalPasswordOffAccount is one account that loses its way in.
type LocalPasswordOffAccount struct {
	ID       int
	Username string
	Email    string
	Role     string
	IsOwner  bool
	Reason   string
}

// LocalPasswordOffPage answers LocalPasswordOffQuery.
type LocalPasswordOffPage struct {
	Accounts []LocalPasswordOffAccount
	// More is set when the group has rows after this page.
	More bool
	// EmailAutoMatch is the value the grouping used.
	EmailAutoMatch  bool
	EmailMatchTotal int
	AdminLinkTotal  int
}

// localPasswordOffSQL answers LocalPasswordOffAccounts in one query: $1 is
// whether email matching is on, $2 whether the email_match group is asked
// for, $3 and $4 the last row of the previous page, $5 the row limit, $6
// the installations that can sign people in (signInProviders).
func localPasswordOffSQL() string {
	return `WITH affected AS (
			SELECT u.id, COALESCE(u.username::text, '') AS username, COALESCE(u.email, '')::text AS email,
				COALESCE(u.role, '') AS role, COALESCE(u.is_owner, false) AS is_owner,
				CASE
					WHEN ` + emailMatchCandidateSQL("u") + ` AND btrim(COALESCE(u.email, '')) <> '' THEN
						CASE WHEN $1 THEN 'email_match' ELSE 'email_match_off' END
					WHEN ` + emailMatchCandidateSQL("u") + ` THEN 'no_email'
					ELSE 'administrator'
				END AS reason
			FROM users u
			WHERE u.enabled AND ` + passwordSignsInSQL("u") + ` AND NOT u.break_glass
				AND NOT EXISTS (SELECT 1 FROM plugin_auth_identities i WHERE i.user_id = u.id AND i.plugin_installation_id = ANY($6))),
		totals AS (
			SELECT COUNT(*) FILTER (WHERE reason = 'email_match') AS matched,
				COUNT(*) FILTER (WHERE reason <> 'email_match') AS unmatched
			FROM affected)
		SELECT t.matched, t.unmatched, a.id, a.username, a.email, a.role, a.is_owner, a.reason
		FROM totals t
		LEFT JOIN LATERAL (
			SELECT * FROM affected
			WHERE (reason = 'email_match') = $2 AND (username, id) > ($3::text, $4::int)
			ORDER BY username, id
			LIMIT $5) a ON true
		ORDER BY a.username, a.id`
}

// LocalPasswordOffAccounts answers who loses their way in when local
// password sign-in is turned off: enabled accounts that can sign in with a
// password today (passwordSignsInSQL), are not break-glass
// (localPasswordPolicy exempts those), and have no linked identity at an
// provider that can sign people in (signInProviders, the guard's rule: no
// network provider, and an OpenID Connect one only with a public URL).
// Each is grouped by whether email auto-match can link it at its first
// provider sign-in,
// with the rule account resolution applies (emailMatchCandidateSQL); a link
// to a network provider elsewhere does not stop that match. Both group
// totals and the page come from one query, ordered by username (empty for
// an account without one), then ID.
func (s *IdentityService) LocalPasswordOffAccounts(ctx context.Context, q LocalPasswordOffQuery) (LocalPasswordOffPage, error) {
	var page LocalPasswordOffPage
	if q.Group != LocalPasswordOffEmailMatch && q.Group != LocalPasswordOffAdminLink {
		return page, fmt.Errorf("unknown group %q", q.Group)
	}
	if q.Limit <= 0 {
		return page, errors.New("limit must be positive")
	}
	var matching bool
	if q.EmailAutoMatch != nil {
		matching = *q.EmailAutoMatch
	} else {
		saved, err := emailAutoMatchEnabled(ctx, s.pool)
		if err != nil {
			return page, err
		}
		matching = saved
	}
	page.EmailAutoMatch = matching
	publicURL, err := readServerSetting(ctx, s.pool, config.ServerPublicURLSettingKey)
	if err != nil {
		return page, err
	}
	providers, err := signInProviders(ctx, s.pool, publicURL)
	if err != nil {
		return page, err
	}
	rows, err := s.pool.Query(ctx, localPasswordOffSQL(),
		matching, q.Group == LocalPasswordOffEmailMatch, q.AfterUsername, q.AfterID, q.Limit+1, providers)
	if err != nil {
		return page, fmt.Errorf("listing accounts that lose password sign-in: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id                            *int
			username, email, role, reason *string
			owner                         *bool
		)
		if err := rows.Scan(&page.EmailMatchTotal, &page.AdminLinkTotal, &id, &username, &email, &role, &owner, &reason); err != nil {
			return page, fmt.Errorf("listing accounts that lose password sign-in: %w", err)
		}
		if id == nil {
			continue
		}
		page.Accounts = append(page.Accounts, LocalPasswordOffAccount{
			ID: *id, Username: *username, Email: *email, Role: *role, IsOwner: *owner, Reason: *reason,
		})
	}
	if err := rows.Err(); err != nil {
		return page, fmt.Errorf("listing accounts that lose password sign-in: %w", err)
	}
	if len(page.Accounts) > q.Limit {
		page.Accounts = page.Accounts[:q.Limit]
		page.More = true
	}
	return page, nil
}

func settingBool(value string, fallback bool) bool {
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return parsed
}

// usableBreakGlass reports whether user, as stored, counts toward the
// break-glass requirement: an enabled break-glass admin with a password that
// signs in (models.User.HasLocalPassword).
func usableBreakGlass(user *models.User) bool {
	return user != nil && user.BreakGlass && user.Role == models.RoleAdmin && user.Enabled && user.HasLocalPassword()
}

// EnsureBreakGlassAfterAdminChange refuses an administrator change to current
// (input nil deletes it) that would remove the last usable break-glass admin
// while local password sign-in is off. It runs in the account mutation's
// transaction and takes the settings lock, so a concurrent settings write
// cannot turn local sign-in off in between.
func EnsureBreakGlassAfterAdminChange(ctx context.Context, tx pgx.Tx, current *models.User, input *models.UpdateUserInput) error {
	if !usableBreakGlass(current) {
		return nil
	}
	if input != nil {
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
		if usableBreakGlass(&after) {
			return nil
		}
	}
	if err := lockServerSettings(ctx, tx); err != nil {
		return err
	}
	allowed, err := localPasswordLoginAllowed(ctx, tx)
	if err != nil || allowed {
		return err
	}
	count, err := usableBreakGlassCount(ctx, tx, current.ID)
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrBreakGlassRequired
	}
	return nil
}
