package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	netmail "net/mail"
	"strings"
	"unicode/utf8"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/secret"
)

// AccountResolver turns a provider's answer into a Silo account. It is the
// host half of external sign-in shared by every auth plugin (OIDC, LDAP);
// the order is fixed (docs/architecture/external-sign-in.md):
//
//  1. an identity already linked to (installation, subject) signs in its
//     account;
//  2. a linking flow started from a signed-in session links the identity to
//     that account;
//  3. with email auto-match on and the email explicitly verified, the
//     unlinked ordinary (role user) account holding that email is linked;
//  4. with account creation on, a new account is created;
//  5. otherwise the sign-in is refused (ErrAccountRequired).
//
// Each resolution runs in one transaction under a per-identity advisory
// lock, so concurrent first sign-ins on any node create one account.
type AccountResolver struct {
	pool     *pgxpool.Pool
	accounts *AccountProvisioner
	// onSessionsRevoked runs after commit for an account whose login
	// sessions a role change or a provider re-check revoked, so every
	// replica drops cached compatibility sessions.
	onSessionsRevoked func(context.Context, int)
	// cipher encrypts the plugins' refresh_state at rest; nil stores none.
	cipher *secret.Cipher
}

// NewAccountResolver builds the resolver. accounts provisions new accounts
// with their default profile; onSessionsRevoked may be nil.
func NewAccountResolver(pool *pgxpool.Pool, accounts *AccountProvisioner, onSessionsRevoked func(context.Context, int)) *AccountResolver {
	return &AccountResolver{pool: pool, accounts: accounts, onSessionsRevoked: onSessionsRevoked}
}

// WithSecretCipher makes the resolver store the plugins' refresh_state
// (encrypted with c) for the provider re-check.
func (r *AccountResolver) WithSecretCipher(c *secret.Cipher) *AccountResolver {
	r.cipher = c
	return r
}

// ResolveInput is one sign-in to resolve.
type ResolveInput struct {
	InstallationID int
	// AutoProvision is the binding's account-creation switch.
	AutoProvision bool
	// Network: the installation is a network provider, whose identity
	// defers to the account's primary provider (primaryAuthority).
	Network  bool
	Identity ExternalIdentity
	// LinkingUserID is the signed-in account a linking flow links to; 0 for
	// an ordinary sign-in.
	LinkingUserID int
}

// resolution collects what one transaction did, reported after commit.
type resolution struct {
	user *models.User
	// identityID is the identity the sign-in resolved through.
	identityID     int64
	sessionsRevoke bool
	audit          []auditEvent
}

type auditEvent struct {
	event string
	attrs []any
}

// Resolve finds, links or creates the account for in and applies the
// provider's managed role. It also answers the identity the sign-in went
// through, which the login session it opens carries. The plugin's
// refresh_state is stored on that identity in the same transaction.
func (r *AccountResolver) Resolve(ctx context.Context, in ResolveInput) (*models.User, int64, error) {
	if r == nil || r.pool == nil {
		return nil, 0, ErrProviderUnavailable
	}
	var result resolution
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		result = resolution{}
		if err := r.resolve(ctx, tx, in, &result); err != nil {
			return err
		}
		if result.identityID == 0 {
			return nil
		}
		return r.storeRefreshState(ctx, tx, result.identityID, in.Identity.RefreshState)
	})
	if err != nil {
		return nil, 0, err
	}
	for _, event := range result.audit {
		auditAuthEvent(ctx, event.event, event.attrs...)
	}
	if result.sessionsRevoke && r.onSessionsRevoked != nil {
		r.onSessionsRevoked(ctx, result.user.ID)
	}
	return result.user, result.identityID, nil
}

func (r *AccountResolver) resolve(ctx context.Context, tx pgx.Tx, in ResolveInput, out *resolution) error {
	identity := in.Identity
	if err := lockExternalSubject(ctx, tx, in.InstallationID, identity.Subject); err != nil {
		return err
	}
	linked, err := identityBySubject(ctx, tx, in.InstallationID, identity.Subject)
	if err != nil && !errors.Is(err, ErrIdentityNotFound) {
		return err
	}

	// 1. Identity already linked.
	if linked != nil {
		if in.LinkingUserID != 0 {
			if linked.UserID != in.LinkingUserID {
				return ErrIdentityLinkedElsewhere
			}
			// A linking flow for the identity the account already holds.
			return ErrAccountAlreadyLinked
		}
		user, err := lockUser(ctx, tx, linked.UserID)
		if err != nil {
			return err
		}
		if !user.Enabled {
			return ErrUserDisabled
		}
		if err := recordIdentitySignIn(ctx, tx, linked.ID, identity); err != nil {
			return err
		}
		out.identityID = linked.ID
		return r.finish(ctx, tx, in, user, out)
	}

	// 2. Linking flow in progress.
	if in.LinkingUserID != 0 {
		user, err := lockUser(ctx, tx, in.LinkingUserID)
		if err != nil {
			if IsNotFound(err) {
				return ErrNotPermitted
			}
			return err
		}
		if !user.Enabled {
			return ErrUserDisabled
		}
		linked, err := linkIdentityTx(ctx, tx, user, in.InstallationID, identity, true, true)
		if err != nil {
			return err
		}
		out.identityID = linked.ID
		out.audit = append(out.audit, auditEvent{"identity_linked", []any{
			auditInstallationID, in.InstallationID, auditUserID, user.ID, "method", "linking"}})
		return r.finish(ctx, tx, in, user, out)
	}

	// 3. Email auto-match (admin opt-in, verified email only). Only ordinary
	// accounts match: an email the provider asserts must never hand it the
	// Owner, an admin or a break-glass account, which an administrator
	// links explicitly. Any other holder of the email falls through to
	// creation, which refuses with email_in_use.
	//
	// Silo never verified the account's email (registration and profile
	// edits store what was typed), so whoever set it may hold the account's
	// credentials. Nobody re-authenticated as that account here, so every
	// credential the provider did not vouch for ends with the link: login
	// and Audiobookshelf sessions, device approvals and API keys.
	if identity.Email != "" && identity.emailVerified() {
		autoMatch, err := emailAutoMatchEnabled(ctx, tx)
		if err != nil {
			return err
		}
		if autoMatch {
			user, err := scanUser(tx.QueryRow(ctx, `SELECT `+allColumns+` FROM users
				WHERE email = $1 AND role = 'user' AND NOT is_owner AND NOT break_glass FOR UPDATE`, NormalizeEmail(identity.Email)))
			if err != nil && !IsNotFound(err) {
				return err
			}
			if user != nil {
				alreadyLinked, err := accountHasIdentityAt(ctx, tx, user.ID, in.InstallationID)
				if err != nil {
					return err
				}
				if !alreadyLinked {
					if !user.Enabled {
						return ErrUserDisabled
					}
					linked, err := linkIdentityTx(ctx, tx, user, in.InstallationID, identity, true, false)
					if err != nil {
						return err
					}
					if err := RevokeSignInsInTransaction(ctx, tx, user.ID); err != nil {
						return err
					}
					keys, err := deleteAPIKeys(ctx, tx, user.ID)
					if err != nil {
						return err
					}
					out.sessionsRevoke = true
					out.identityID = linked.ID
					out.audit = append(out.audit, auditEvent{"identity_linked", []any{
						auditInstallationID, in.InstallationID, auditUserID, user.ID, "method", "email_match",
						"credentials_revoked", true, "api_keys", keys}})
					return r.finish(ctx, tx, in, user, out)
				}
			}
		}
	}

	// 4. Account creation.
	if !in.AutoProvision {
		return ErrAccountRequired
	}
	user, identityID, err := r.createAccount(ctx, tx, in)
	if err != nil {
		return err
	}
	out.identityID = identityID
	out.audit = append(out.audit, auditEvent{"account_created", []any{
		auditInstallationID, in.InstallationID, auditUserID, user.ID, auditUsername, user.Username, auditRole, user.Role}})
	out.user = user
	return nil
}

// finish applies the managed role and records the account the sign-in
// resolved to. A network identity (in.Network) of an account that also has a
// primary provider identity (primaryAuthority) leaves the role to that
// provider, and cannot sign in while that provider refuses the account.
func (r *AccountResolver) finish(ctx context.Context, tx pgx.Tx, in ResolveInput, user *models.User, out *resolution) error {
	managed := in.Identity.ManagedRole
	if in.Network {
		authority, err := primaryAuthorityOf(ctx, tx, user.ID, in.InstallationID)
		if err != nil {
			return err
		}
		if authority.refused {
			return ErrNotPermitted
		}
		if authority.defers {
			managed = pluginv1.AuthManagedRole_AUTH_MANAGED_ROLE_UNSPECIFIED
		}
	}
	synced, event, err := syncManagedRole(ctx, tx, user, managed)
	if err != nil {
		return err
	}
	if event != nil {
		event.attrs = append(event.attrs, auditInstallationID, in.InstallationID)
		out.audit = append(out.audit, *event)
	}
	if synced != nil {
		out.sessionsRevoke = true
		user = synced
	}
	out.user = user
	return nil
}

func lockUser(ctx context.Context, tx rowQuerier, id int) (*models.User, error) {
	return scanUser(tx.QueryRow(ctx, `SELECT `+allColumns+` FROM users WHERE id = $1 FOR UPDATE`, id))
}

// linkIdentityTx links identity to user. An account holds at most one
// identity per installation, and an identity belongs to one account. Linking
// turns the account's local password off unless it is a break-glass account.
// attachSessions moves the account's open local sessions under the identity,
// for a link the account's holder made (self-service or an administrator);
// email auto-match revokes them instead.
func linkIdentityTx(ctx context.Context, tx pgx.Tx, user *models.User, installationID int, identity ExternalIdentity, signedIn, attachSessions bool) (*LinkedIdentity, error) {
	existing, err := identityBySubject(ctx, tx, installationID, identity.Subject)
	switch {
	case err == nil && existing.UserID != user.ID:
		return nil, ErrIdentityLinkedElsewhere
	case err == nil:
		return nil, ErrAccountAlreadyLinked
	case !errors.Is(err, ErrIdentityNotFound):
		return nil, err
	}
	alreadyLinked, err := accountHasIdentityAt(ctx, tx, user.ID, installationID)
	if err != nil {
		return nil, err
	}
	if alreadyLinked {
		return nil, ErrAccountAlreadyLinked
	}
	linked, err := insertIdentity(ctx, tx, user.ID, installationID, identity, signedIn)
	if err != nil {
		if isDuplicateKeyError(err) {
			return nil, ErrIdentityLinkedElsewhere
		}
		return nil, err
	}
	if user.LocalPasswordLoginEnabled && !user.BreakGlass {
		if _, err := tx.Exec(ctx, `UPDATE users SET local_password_login_enabled = false, updated_at = NOW() WHERE id = $1`, user.ID); err != nil {
			return nil, fmt.Errorf("turning off local password sign-in: %w", err)
		}
		user.LocalPasswordLoginEnabled = false
	}
	if attachSessions && !user.BreakGlass {
		// The provider now answers for the account, so its open sessions are
		// re-checked with the provider like sessions opened through it. A
		// break-glass account keeps its local sessions independent.
		if _, err := tx.Exec(ctx, `UPDATE auth_sessions SET identity_id = $1, provider_since = COALESCE(provider_since, created_at)
			WHERE user_id = $2 AND identity_id IS NULL AND impersonator_user_id IS NULL
				AND revoked_at IS NULL AND expires_at > NOW()`, linked.ID, user.ID); err != nil {
			return nil, fmt.Errorf("attaching sessions to the identity: %w", err)
		}
	}
	return linked, nil
}

// Audit record keys and demotion refusal reasons.
const (
	auditInstallationID = "installation_id"
	auditUserID         = "user_id"
	auditCheckStatus    = "status"
	auditRole           = "role"
	auditUsername       = "username"

	demotionReasonOwner      = "owner"
	demotionReasonBreakGlass = "break_glass"
	demotionReasonLastAdmin  = "last_admin"

	// fallbackUsername names a new account when the provider gave nothing
	// usable.
	fallbackUsername = "user"
)

// maxUsernameAttempts bounds the suffixes tried for a new username.
const maxUsernameAttempts = 50

func (r *AccountResolver) createAccount(ctx context.Context, tx pgx.Tx, in ResolveInput) (*models.User, int64, error) {
	identity := in.Identity
	email := normalizedProviderEmail(identity.Email)
	if email != "" {
		taken, err := loginIdentifierTaken(ctx, tx, email)
		if err != nil {
			return nil, 0, err
		}
		if taken {
			return nil, 0, ErrEmailInUse
		}
	}
	role := models.RoleUser
	if identity.ManagedRole == pluginv1.AuthManagedRole_AUTH_MANAGED_ROLE_ADMIN {
		role = models.RoleAdmin
	}
	password, err := randomPluginOnlyPassword()
	if err != nil {
		return nil, 0, fmt.Errorf("generate plugin-only password: %w", err)
	}
	localPasswordLogin := false
	base := usernameBase(identity)
	profileName := defaultProfileName(identity)
	withProfile := r.accounts != nil && r.accounts.SupportsTransactionalProfiles()
	if !withProfile {
		slog.WarnContext(ctx, "profile store cannot join account creation; creating the account without a profile",
			"component", "auth", auditInstallationID, in.InstallationID)
	}

	for attempt := 0; attempt < maxUsernameAttempts; attempt++ {
		username := base
		if attempt > 0 {
			username = fmt.Sprintf("%s_%d", base, attempt+1)
		}
		taken, err := loginIdentifierTaken(ctx, tx, username)
		if err != nil {
			return nil, 0, err
		}
		if taken {
			continue
		}
		accountEmail := email
		if accountEmail == "" {
			// Silo accounts need a unique email. A provider that sends none
			// gets a reserved, undeliverable placeholder.
			accountEmail = fmt.Sprintf("%s@plugin-%d.invalid", username, in.InstallationID)
		}
		input := CreateAccountInput{
			User: models.CreateUserInput{
				Email:                     accountEmail,
				Username:                  username,
				Password:                  password,
				LocalPasswordLoginEnabled: &localPasswordLogin,
				Role:                      role,
			},
			DefaultProfile: DefaultProfileOptions{Enabled: withProfile, Name: profileName},
		}
		// A savepoint keeps a lost race on the username from aborting the
		// whole transaction.
		savepoint, err := tx.Begin(ctx)
		if err != nil {
			return nil, 0, err
		}
		var user *models.User
		if r.accounts != nil {
			user, err = r.accounts.CreateAccountInTransaction(ctx, savepoint, input)
		} else {
			user, err = createUser(ctx, savepoint, input.User)
		}
		if err != nil {
			_ = savepoint.Rollback(ctx)
			if IsDuplicate(err) {
				if email != "" {
					if taken, takenErr := loginIdentifierTaken(ctx, tx, email); takenErr == nil && taken {
						return nil, 0, ErrEmailInUse
					}
				}
				continue
			}
			return nil, 0, fmt.Errorf("creating account: %w", err)
		}
		if err := savepoint.Commit(ctx); err != nil {
			return nil, 0, err
		}
		linked, err := insertIdentity(ctx, tx, user.ID, in.InstallationID, identity, true)
		if err != nil {
			return nil, 0, err
		}
		return user, linked.ID, nil
	}
	return nil, 0, fmt.Errorf("creating account: no free username for %q", base)
}

// loginIdentifierTaken reports whether any account uses value as its
// username or email. Sign-in treats both columns as one identity space.
func loginIdentifierTaken(ctx context.Context, tx pgx.Tx, value string) (bool, error) {
	var taken bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE username = $1 OR email = $1)`, value).Scan(&taken)
	if err != nil {
		return false, fmt.Errorf("checking login identifier: %w", err)
	}
	return taken, nil
}

func normalizedProviderEmail(email string) string {
	email = strings.TrimSpace(email)
	if email == "" {
		return ""
	}
	parsed, err := netmail.ParseAddress(email)
	if err != nil || parsed.Address != email {
		return ""
	}
	return NormalizeEmail(email)
}

// maxGeneratedUsername keeps generated names readable; suffixes add a few
// characters.
const maxGeneratedUsername = 64

// usernameBase picks the new account's username: the provider's username,
// else the email's local part, else the subject, sanitized. A provider
// username with an @ (a Kanidm SPN, an Entra or AD user principal name)
// contributes the part before it, since the sanitizer would otherwise glue
// the domain on. Never matched against existing accounts: it only names a
// new one.
func usernameBase(identity ExternalIdentity) string {
	username, _, _ := strings.Cut(identity.Username, "@")
	candidates := []string{username}
	if local, _, ok := strings.Cut(identity.Email, "@"); ok {
		candidates = append(candidates, local)
	}
	subject := identity.Subject
	if i := strings.LastIndex(subject, "|"); i >= 0 {
		subject = subject[i+1:]
	}
	candidates = append(candidates, subject)
	for _, candidate := range candidates {
		if base := sanitizeUsername(candidate); base != "" {
			return truncateRunes(base, maxGeneratedUsername)
		}
	}
	return fallbackUsername
}

func defaultProfileName(identity ExternalIdentity) string {
	name := firstNonEmpty(identity.DisplayName, identity.Username)
	if name == "" {
		name = usernameBase(identity)
	}
	return truncateRunes(name, 100)
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:limit]))
}

// adminRoleLock serializes demotions so two concurrent sign-ins cannot each
// demote one of the last two enabled admins.
const adminRoleLock = "silo:auth:admin-role"

// syncManagedRole applies the provider's managed role to user in tx. It
// returns the updated account when the role changed (its login sessions are
// revoked in tx, as an administrator role change does) and the audit event
// for any change or skipped demotion. ADMIN promotes, USER demotes, anything
// else leaves the role alone. The Owner, break-glass accounts and the last
// enabled admin are never demoted.
func syncManagedRole(ctx context.Context, tx pgx.Tx, user *models.User, managed pluginv1.AuthManagedRole) (*models.User, *auditEvent, error) {
	var target string
	switch managed {
	case pluginv1.AuthManagedRole_AUTH_MANAGED_ROLE_ADMIN:
		target = models.RoleAdmin
	case pluginv1.AuthManagedRole_AUTH_MANAGED_ROLE_USER:
		target = models.RoleUser
	default:
		return nil, nil, nil
	}
	if user.Role == target {
		return nil, nil, nil
	}
	if target == models.RoleUser {
		if reason, err := demotionBlocked(ctx, tx, user); err != nil || reason != "" {
			if reason != "" {
				slog.WarnContext(ctx, "provider asked to demote an admin; kept the admin role", "component", "auth",
					auditUserID, user.ID, "reason", reason)
				return nil, &auditEvent{"role_change_skipped", []any{auditUserID, user.ID, "old_role", user.Role, "new_role", target, "reason", reason}}, nil
			}
			return nil, nil, err
		}
	}
	if err := updateUser(ctx, tx, user.ID, models.UpdateUserInput{Role: &target}); err != nil {
		return nil, nil, fmt.Errorf("applying provider role: %w", err)
	}
	if err := RevokeSignInsInTransaction(ctx, tx, user.ID); err != nil {
		return nil, nil, err
	}
	updated, err := userByID(ctx, tx, user.ID)
	if err != nil {
		return nil, nil, err
	}
	return updated, &auditEvent{"role_changed", []any{auditUserID, user.ID, "old_role", user.Role, "new_role", target}}, nil
}

// demotionBlocked names why user must keep the admin role, or "" when a
// demotion may proceed.
func demotionBlocked(ctx context.Context, tx pgx.Tx, user *models.User) (string, error) {
	switch {
	case user.IsOwner:
		return demotionReasonOwner, nil
	case user.BreakGlass && user.LocalPasswordLoginEnabled:
		// Break-glass exists for local recovery; an account that cannot use
		// its password gets no immunity from it.
		return demotionReasonBreakGlass, nil
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, adminRoleLock); err != nil {
		return "", fmt.Errorf("acquiring admin role lock: %w", err)
	}
	var others int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE role = 'admin' AND enabled AND id <> $1`, user.ID).Scan(&others); err != nil {
		return "", fmt.Errorf("counting admins: %w", err)
	}
	if others == 0 {
		return demotionReasonLastAdmin, nil
	}
	return "", nil
}

// auditAuthEvent writes one external sign-in audit record to the
// operational log (component auth, audit_event set), which administrators
// read and filter in the admin log views.
func auditAuthEvent(ctx context.Context, event string, attrs ...any) {
	args := append([]any{"component", "auth", "audit_event", event}, attrs...)
	slog.InfoContext(ctx, "auth audit", args...)
}
