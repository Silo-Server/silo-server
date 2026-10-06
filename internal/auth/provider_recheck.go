package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/plugins"
	"github.com/Silo-Server/silo-server/internal/secret"
)

// Provider re-check statuses, stored in plugin_auth_identities.last_check_status
// and shown to administrators. A sign-in through the provider records active.
const (
	CheckStatusActive       = "active"
	CheckStatusNotFound     = "not_found"
	CheckStatusDisabled     = "disabled"
	CheckStatusNotPermitted = "not_permitted"
	CheckStatusUnsupported  = "unsupported"
	CheckStatusUnavailable  = "unavailable"
)

const (
	// providerRecheckTimeout bounds the CheckAccount call a refresh waits
	// for.
	providerRecheckTimeout = 10 * time.Second
	// providerRecheckLockWait bounds how long a refresh waits for another
	// node's check of the same identity before it counts the provider as
	// unavailable.
	providerRecheckLockWait = providerRecheckTimeout + 5*time.Second
	// providerRecheckWriteWait bounds the row locks the writes after the
	// plugin call wait for. It is generous: giving up there loses a refresh
	// token the provider may already have rotated.
	providerRecheckWriteWait = time.Minute
	// providerRecheckRetryWait is how long an identity whose provider could
	// not be reached waits before the next attempt; refreshes in between
	// use the stored answer (the outage policy), so an outage does not make
	// every refresh wait on the provider.
	providerRecheckRetryWait = 2 * time.Minute
	// providerRecheckConcurrency caps the re-checks one node runs at once,
	// each holding a database connection while it waits on the provider. A
	// refresh that finds every slot taken counts the provider as
	// unavailable.
	providerRecheckConcurrency = 8
)

// ErrProviderNotLoaded is an AccountCheckerSource answer: this node has no
// enabled sign-in provider for the installation.
var ErrProviderNotLoaded = errors.New("sign-in provider not loaded on this node")

// errCheckerLoad is a re-check this node could not run because it could not
// load the installation's plugin. Like a node without the provider, nothing
// is stored: the failure is this node's, not the provider's answer. A
// refresh counts the provider as unavailable; the scheduled pass counts the
// check as failed.
var errCheckerLoad = errors.New("provider re-check could not load the auth plugin")

// AccountChecker is an auth plugin's AuthProviderChecks.CheckAccount.
// *pluginhost.AuthProviderClient implements it.
type AccountChecker interface {
	CheckAccount(ctx context.Context, req *pluginv1.CheckAccountRequest) (*pluginv1.CheckAccountResponse, error)
}

// AccountCheckerSource finds the CheckAccount client of an installation. It
// answers ErrProviderNotLoaded when this node has no enabled sign-in
// provider for the installation, nil and no error when the plugin has no
// CheckAccount, and another error when its plugin cannot be reached.
type AccountCheckerSource interface {
	AccountChecker(ctx context.Context, installationID int) (AccountChecker, error)
}

// ProviderRecheck re-checks, at session refresh, the external identity a
// login session came from (docs/architecture/external-sign-in.md, "Provider
// re-check"). A refresh asks the provider again once the identity's last
// answer is older than auth.provider_recheck_interval, so an account the
// provider removed, disabled or no longer admits loses its Silo sessions
// even though refresh keeps sliding them.
//
// At most one CheckAccount runs per (installation, subject) across all
// nodes: one connection holds the identity's advisory lock (the lock sign-in
// takes too) through the call and the transaction that writes its answer,
// so the next call on any node presents the rotated refresh_state.
//
// That write can be lost after the plugin spent the stored refresh token at
// the provider: the call fails in transport, the node or plugin dies, or the
// transaction does not commit. So before each call the host commits
// check_outcome_unknown on the identity and clears it only when a committed
// answer shows the plugin's state is current: an active or refusing answer,
// or a refresh_state the plugin returned. An unavailable or unsupported
// answer without a refresh_state leaves it set, since the plugin may not
// have presented the stored token at all. While it is set, the next refusal
// of a call that presented a stored refresh_state may be the provider
// refusing a token it already rotated, not a revocation, and is applied as
// unsupported (isRefusal).
type ProviderRecheck struct {
	resolver  *AccountResolver
	source    AccountCheckerSource
	timeout   time.Duration
	lockWait  time.Duration
	writeWait time.Duration
	retryWait time.Duration
	// slots bounds the re-checks this node runs at once.
	slots chan struct{}
}

// NewProviderRecheck builds the re-check over the resolver's database,
// secret cipher and session-revocation hook.
func NewProviderRecheck(resolver *AccountResolver, source AccountCheckerSource) *ProviderRecheck {
	return &ProviderRecheck{
		resolver:  resolver,
		source:    source,
		timeout:   providerRecheckTimeout,
		lockWait:  providerRecheckLockWait,
		writeWait: providerRecheckWriteWait,
		retryWait: providerRecheckRetryWait,
		slots:     make(chan struct{}, providerRecheckConcurrency),
	}
}

// recheckVerdict is how a refresh treats its session after the re-check.
type recheckVerdict int

const (
	// verdictSlide slides the session window as usual.
	verdictSlide recheckVerdict = iota
	// verdictAbsoluteAge keeps the session within an absolute age from its
	// creation (the provider cannot re-check the account).
	verdictAbsoluteAge
)

// check decides what a refresh of session may do. ErrSessionRevoked ends
// the session (the provider refused the account, or a role change revoked
// its sessions); ErrProviderUnavailable refuses this refresh only (the
// provider could not answer and the outage policy is fail_closed).
func (r *ProviderRecheck) check(ctx context.Context, session *models.AuthSession) (recheckVerdict, error) {
	if r == nil || session.IdentityID == nil || r.resolver == nil || r.resolver.pool == nil {
		return verdictSlide, nil
	}
	pool := r.resolver.pool
	identity, err := identityByID(ctx, pool, *session.IdentityID)
	if err != nil {
		if errors.Is(err, ErrIdentityNotFound) {
			// Unlinked since the session opened: a local session now.
			return verdictSlide, nil
		}
		return verdictSlide, err
	}
	if identity.UserID != session.UserID {
		return verdictSlide, nil
	}
	interval, failClosed, err := r.policy(ctx)
	if err != nil {
		return verdictSlide, err
	}
	if !r.recheckDue(identity, interval, time.Now()) {
		return verdictFor(identity.LastCheckStatus, failClosed)
	}
	checkStatus, revoked, err := r.recheck(ctx, identity, interval, false)
	if errors.Is(err, errCheckerLoad) {
		return verdictFor(CheckStatusUnavailable, failClosed)
	}
	if err != nil {
		return verdictSlide, err
	}
	if revoked {
		return verdictSlide, ErrSessionRevoked
	}
	return verdictFor(checkStatus, failClosed)
}

func (r *ProviderRecheck) policy(ctx context.Context) (time.Duration, bool, error) {
	pool := r.resolver.pool
	rawInterval, err := readServerSetting(ctx, pool, config.AuthProviderRecheckIntervalSettingKey)
	if err != nil {
		return 0, false, err
	}
	outage, err := readServerSetting(ctx, pool, config.AuthProviderRecheckOutagePolicySettingKey)
	if err != nil {
		return 0, false, err
	}
	return config.AuthProviderRecheckInterval(rawInterval), outage == config.AuthRecheckFailClosed, nil
}

// recheckDue reports whether the identity needs a new provider answer: it
// has none, the last one is older than interval, or the provider could not
// be reached last time and the retry wait has passed.
func (r *ProviderRecheck) recheckDue(identity *LinkedIdentity, interval time.Duration, now time.Time) bool {
	if identity.LastCheckedAt == nil || identity.LastCheckStatus == "" {
		return true
	}
	if identity.LastCheckStatus == CheckStatusUnavailable {
		interval = min(interval, r.retryWait)
	}
	return now.Sub(*identity.LastCheckedAt) >= interval
}

func verdictFor(checkStatus string, failClosed bool) (recheckVerdict, error) {
	if isRefusal(checkStatus) {
		return verdictSlide, ErrSessionRevoked
	}
	switch checkStatus {
	case CheckStatusUnsupported:
		return verdictAbsoluteAge, nil
	case CheckStatusUnavailable:
		if failClosed {
			return verdictSlide, ErrProviderUnavailable
		}
		return verdictSlide, nil
	default:
		return verdictSlide, nil
	}
}

// recheckOutcome is what one re-check transaction did, reported after
// commit.
type recheckOutcome struct {
	status  string
	userID  int
	revoked bool
	audit   []auditEvent
	// asked reports that the plugin was called.
	asked bool
	// stateStored reports that replacement state was committed before the
	// answer transaction, so a later account lock failure cannot lose it.
	stateStored bool
	// loadErr is this node failing to load the plugin (errCheckerLoad).
	loadErr error
	// err is a failure inside the savepoint (applyInSavepoint). For a
	// revoking answer, its stored status and cleared refresh_state rolled
	// back with it; the transaction still commits whatever was stored
	// before the savepoint, a rotated refresh_state included.
	err error
}

// recheck asks the provider about identity and applies the answer. The call
// and its persistence are detached from the request: a client that hangs up
// must not lose a refresh token the provider already rotated. A refresh
// that finds every slot taken counts the provider as unavailable; the
// scheduled pass (waitForSlot) waits for one instead.
//
// The plugin is loaded before the transaction (resolveChecker): loading can
// start the plugin process, which must not hold the identity's lock that
// other refreshes wait on.
func (r *ProviderRecheck) recheck(ctx context.Context, identity *LinkedIdentity, interval time.Duration, waitForSlot bool) (string, bool, error) {
	if waitForSlot {
		select {
		case r.slots <- struct{}{}:
			defer func() { <-r.slots }()
		case <-ctx.Done():
			return "", false, ctx.Err()
		}
	} else {
		select {
		case r.slots <- struct{}{}:
			defer func() { <-r.slots }()
		default:
			slog.WarnContext(ctx, "too many provider re-checks in flight on this node; counting the provider as unavailable",
				"component", "auth", auditInstallationID, identity.InstallationID, auditUserID, identity.UserID)
			return CheckStatusUnavailable, false, nil
		}
	}
	ctx = context.WithoutCancel(ctx)
	checker, loadErr := r.resolveChecker(ctx, identity.InstallationID)
	var out recheckOutcome
	err := r.recheckConn(ctx, identity, interval, checker, loadErr, &out)
	if err != nil {
		if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "55P03" && !out.asked {
			// Another node's check of this identity is still running.
			slog.WarnContext(ctx, "provider re-check is busy on another node; counting the provider as unavailable",
				"component", "auth", auditInstallationID, identity.InstallationID, auditUserID, identity.UserID)
			return CheckStatusUnavailable, false, nil
		}
		return "", false, err
	}
	for _, event := range out.audit {
		auditAuthEvent(ctx, event.event, event.attrs...)
	}
	// The hook drops cached sessions (jellycompat) when login sessions end.
	// API keys and Audiobookshelf sessions are checked against the database,
	// so an unsupported answer's credential bound needs no hook.
	if out.revoked && r.resolver.onSessionsRevoked != nil {
		r.resolver.onSessionsRevoked(ctx, out.userID)
	}
	if out.err != nil {
		return "", false, out.err
	}
	if out.loadErr != nil {
		return "", false, fmt.Errorf("%w: %w", errCheckerLoad, out.loadErr)
	}
	return out.status, out.revoked, nil
}

// resolveChecker loads the installation's CheckAccount client, bounded by
// the call timeout. It answers ErrProviderNotLoaded when this node has no
// provider for the installation.
func (r *ProviderRecheck) resolveChecker(ctx context.Context, installationID int) (AccountChecker, error) {
	if r.source == nil {
		return nil, ErrProviderNotLoaded
	}
	loadCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	checker, err := r.source.AccountChecker(loadCtx, installationID)
	if err != nil && !errors.Is(err, ErrProviderNotLoaded) {
		slog.WarnContext(ctx, "provider re-check could not load the auth plugin", "component", "auth",
			auditInstallationID, installationID, "error", err)
	}
	return checker, err
}

// recheckConn holds a session advisory lock so the independently committed
// call marker and the answer transaction can use the same connection.
func (r *ProviderRecheck) recheckConn(ctx context.Context, identity *LinkedIdentity, interval time.Duration, checker AccountChecker, loadErr error, out *recheckOutcome) error {
	conn, err := r.resolver.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	lockKey := fmt.Sprintf("silo:auth-identity:%d:%s", identity.InstallationID, identity.ExternalSubject)
	locked := false
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), min(r.writeWait, 5*time.Second))
		defer cancel()
		if locked {
			var unlocked bool
			if err := conn.QueryRow(cleanupCtx, `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, lockKey).Scan(&unlocked); err != nil || !unlocked {
				_ = conn.Hijack().Close(cleanupCtx)
				return
			}
		}
		if _, err := conn.Exec(cleanupCtx, `RESET lock_timeout`); err != nil {
			_ = conn.Hijack().Close(cleanupCtx)
			return
		}
		conn.Release()
	}()
	if _, err := conn.Exec(ctx, fmt.Sprintf(`SET lock_timeout = '%dms'`, r.lockWait.Milliseconds())); err != nil {
		return fmt.Errorf("setting lock timeout: %w", err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return fmt.Errorf("locking external identity: %w", err)
	}
	locked = true
	if _, err := conn.Exec(ctx, fmt.Sprintf(`SET lock_timeout = '%dms'`, r.writeWait.Milliseconds())); err != nil {
		return fmt.Errorf("setting lock timeout: %w", err)
	}
	current, err := identityByID(ctx, conn, identity.ID)
	if err != nil {
		if errors.Is(err, ErrIdentityNotFound) {
			return nil
		}
		return err
	}
	out.userID = current.UserID
	if !r.recheckDue(current, interval, time.Now()) {
		// Another refresh checked it while this one waited for the lock.
		out.status = current.LastCheckStatus
		return nil
	}
	var outcomeUnknown bool
	var pendingRefusal string
	if err := conn.QueryRow(ctx, `SELECT check_outcome_unknown, pending_refusal FROM plugin_auth_identities WHERE id = $1`,
		current.ID).Scan(&outcomeUnknown, &pendingRefusal); err != nil {
		return fmt.Errorf("reading provider re-check outcome: %w", err)
	}
	if isRefusal(pendingRefusal) {
		// The provider already refused the account and its revocation rolled
		// back. The plugin may have dropped or spent the token it was refused
		// with, so asking again could only answer unsupported: apply the
		// refusal on record instead.
		out.status = pendingRefusal
		return r.withRecheckIdentity(ctx, conn, current, out, func(tx pgx.Tx, linked *LinkedIdentity) error {
			return r.applyInSavepoint(ctx, tx, linked, pendingRefusal, nil, false, out)
		})
	}
	if errors.Is(loadErr, ErrProviderNotLoaded) {
		// This node cannot ask: the answer is this node's, not the
		// provider's, so nothing is stored for other nodes to act on.
		out.status, err = notLoadedStatus(ctx, conn, current.InstallationID)
		if err != nil || out.status != CheckStatusUnsupported {
			return err
		}
		// The installation is no longer an enabled sign-in provider, so
		// nobody can re-check the account: its API keys and Audiobookshelf
		// sessions get the bound of an unsupported answer.
		return r.withRecheckIdentity(ctx, conn, current, out, func(tx pgx.Tx, linked *LinkedIdentity) error {
			staleAuth, err := r.staleProviderAuth(ctx, tx, linked.ID)
			if err != nil || !staleAuth {
				return err
			}
			return r.applyAnswer(ctx, tx, linked, CheckStatusUnsupported, nil, true, out)
		})
	}
	if loadErr != nil {
		// Node-local too (errCheckerLoad): nothing is stored, and recheck
		// reports the failure after the connection is released.
		out.loadErr = loadErr
		return nil //nolint:nilerr // no answer is stored; recheck returns out.loadErr.
	}
	state, err := r.resolver.loadRefreshState(ctx, conn, current.ID)
	if err != nil {
		return err
	}
	checkStatus, account := r.ask(ctx, conn, current, checker, state)
	out.status, out.asked = checkStatus, true
	// A refusal of state whose previous outcome was lost cannot establish
	// that the stored token is current: the provider may be refusing a token
	// the lost call already rotated. Keep its uncertainty unless the plugin
	// returns replacement state below. Without stored state (LDAP), the call
	// presented no token a lost call could have rotated, so its refusal counts.
	if outcomeUnknown && isRefusal(checkStatus) && len(state.GetFields()) > 0 {
		slog.WarnContext(ctx, "provider refused an account whose previous re-check answer was lost; the refusal may be of a refresh token the provider already rotated, so it counts as unsupported",
			"component", "auth", auditInstallationID, current.InstallationID, auditUserID, current.UserID, auditCheckStatus, checkStatus)
		checkStatus = CheckStatusUnsupported
		out.status = checkStatus
	}
	// A replacement may already be the only token the provider accepts.
	// Commit it before waiting for account locks, with no identity row lock
	// retained while an unlink could hold the account. Cleared state stays
	// in the answer transaction so a refusal clears it with revocation.
	if replacement := account.GetRefreshState(); len(replacement.GetFields()) > 0 {
		if err := r.resolver.storeRefreshState(ctx, conn, current.ID, replacement); err != nil {
			return err
		}
		out.stateStored = true
	}
	err = r.withRecheckIdentity(ctx, conn, current, out, func(tx pgx.Tx, linked *LinkedIdentity) error {
		current = linked
		if checkStatus == CheckStatusActive || isRefusal(checkStatus) {
			// The plugin answered for the account with the state it was given,
			// so that state was current; the answer commits with this
			// transaction or not at all, and check_outcome_unknown stays set
			// until it does. Any other answer may come from a plugin that never
			// presented the stored token (a host failure before the call, a
			// lost call, a plugin that cannot check yet), so it clears the flag
			// only with a refresh_state it returns (storeRefreshState).
			if _, err := tx.Exec(ctx, `UPDATE plugin_auth_identities SET check_outcome_unknown = FALSE WHERE id = $1`, current.ID); err != nil {
				return fmt.Errorf("recording provider re-check outcome: %w", err)
			}
		}
		staleAuth := false
		if checkStatus == CheckStatusUnsupported {
			if staleAuth, err = r.staleProviderAuth(ctx, tx, current.ID); err != nil {
				return err
			}
		}
		return r.applyInSavepoint(ctx, tx, current, checkStatus, account, staleAuth, out)
	})
	if err != nil && isRefusal(checkStatus) {
		// The account or identity lock can fail before the savepoint runs.
		// Keep the known refusal after rollback too: the replacement state
		// may already have discarded the token the provider refused.
		if recordErr := recordPendingRefusal(ctx, conn, current.ID, checkStatus); recordErr != nil {
			return errors.Join(err, recordErr)
		}
	}
	return err
}

// withRecheckIdentity serializes the answer with unlinking. The provider
// call can outlast an unlink, so lock the user first and read the identity
// again before writing state or changing the account's credentials.
func (r *ProviderRecheck) withRecheckIdentity(ctx context.Context, conn *pgxpool.Conn, identity *LinkedIdentity, out *recheckOutcome, apply func(pgx.Tx, *LinkedIdentity) error) error {
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := lockUser(ctx, tx, identity.UserID); err != nil {
			if IsNotFound(err) {
				out.status = ""
				return nil
			}
			return err
		}
		current, err := scanIdentity(tx.QueryRow(ctx, `SELECT `+identityColumns+` FROM plugin_auth_identities WHERE id = $1 FOR UPDATE`, identity.ID))
		if errors.Is(err, ErrIdentityNotFound) {
			out.status = ""
			return nil
		}
		if err != nil {
			return err
		}
		return apply(tx, current)
	})
}

// isRefusal reports whether checkStatus is the provider refusing the
// account, which revokes its credentials.
func isRefusal(checkStatus string) bool {
	switch checkStatus {
	case CheckStatusNotFound, CheckStatusDisabled, CheckStatusNotPermitted:
		return true
	}
	return false
}

// revokingAnswer reports whether applying the answer revokes credentials:
// a refusal, or an unsupported answer for an identity the provider has not
// vouched for within the absolute age (staleAuth).
func revokingAnswer(checkStatus string, staleAuth bool) bool {
	return isRefusal(checkStatus) || checkStatus == CheckStatusUnsupported && staleAuth
}

// applyInSavepoint stores the answer and the refresh_state it rotated or
// cleared, and runs applyAnswer in a savepoint.
//
// A rotated (non-empty) refresh_state is stored before the savepoint for
// every answer: the plugin may already have spent the stored token, so the
// next call must present the new one even when the rest fails. An answer
// that revokes credentials, and a refresh_state it clears, are stored inside
// the savepoint, so they commit only with the revocation: a revocation that
// fails rolls them back, and the next refresh asks again with the kept token
// instead of finding a refusal on record with nothing revoked. Any other
// answer is stored before the savepoint, so a failed role sync keeps it; an
// ACTIVE answer whose role sync fails leaves the check due, so the role is
// synced on the next refresh. Either way a failure is reported to the
// refresh (out.err).
//
// A refusal whose savepoint rolls back is kept as the identity's
// pending_refusal, outside the savepoint, and the next re-check applies it
// without asking again: the refresh_state stored before the savepoint may no
// longer hold the token the provider refused (the OIDC plugin keeps only its
// ID token), so the provider could not refuse it again.
func (r *ProviderRecheck) applyInSavepoint(ctx context.Context, tx pgx.Tx, identity *LinkedIdentity, checkStatus string, account *pluginv1.AuthenticateResponse, staleAuth bool, out *recheckOutcome) error {
	revoking := revokingAnswer(checkStatus, staleAuth)
	state := account.GetRefreshState()
	clearsWithRevocation := revoking && state != nil && len(state.GetFields()) == 0
	if !clearsWithRevocation && !out.stateStored {
		if err := r.resolver.storeRefreshState(ctx, tx, identity.ID, state); err != nil {
			return err
		}
	}
	if !revoking {
		if err := recordIdentityCheck(ctx, tx, identity.ID, checkStatus, account); err != nil {
			return err
		}
	}
	savepoint, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	var applyErr error
	if clearsWithRevocation {
		applyErr = r.resolver.storeRefreshState(ctx, savepoint, identity.ID, state)
	}
	if applyErr == nil && revoking {
		applyErr = recordIdentityCheck(ctx, savepoint, identity.ID, checkStatus, account)
	}
	if applyErr == nil {
		applyErr = r.applyAnswer(ctx, savepoint, identity, checkStatus, account, staleAuth, out)
	}
	if applyErr == nil {
		return savepoint.Commit(ctx)
	}
	out.err = applyErr
	out.revoked, out.audit = false, nil
	if err := savepoint.Rollback(ctx); err != nil {
		return err
	}
	if isRefusal(checkStatus) {
		return recordPendingRefusal(ctx, tx, identity.ID, checkStatus)
	}
	if checkStatus == CheckStatusActive {
		// The role sync did not apply, so a demotion the provider asked
		// for is still pending. Force another check even if the previous
		// answer was due only under the shorter outage retry interval.
		if _, err := tx.Exec(ctx, `UPDATE plugin_auth_identities SET last_checked_at = NULL WHERE id = $1`,
			identity.ID); err != nil {
			return fmt.Errorf("keeping the provider re-check due: %w", err)
		}
	}
	return nil
}

func recordPendingRefusal(ctx context.Context, db dbQuerier, identityID int64, checkStatus string) error {
	if _, err := db.Exec(ctx, `UPDATE plugin_auth_identities SET pending_refusal = $2,
		check_outcome_unknown = FALSE, updated_at = NOW() WHERE id = $1`, identityID, checkStatus); err != nil {
		return fmt.Errorf("recording the provider refusal: %w", err)
	}
	return nil
}

// staleProviderAuth reports whether the provider last vouched for the
// identity (an interactive sign-in or a check that answered active;
// linked_at for an administrator link it never vouched for) longer ago
// than the absolute session age, auth.refresh_token_expiry. An account
// with local password sign-in turned on is never stale: that person signs
// in without the provider, so its credentials are not the provider's to
// bound.
func (r *ProviderRecheck) staleProviderAuth(ctx context.Context, tx pgx.Tx, identityID int64) (bool, error) {
	absoluteAge, err := absoluteSessionAge(ctx, tx)
	if err != nil {
		return false, err
	}
	var stale bool
	if err := tx.QueryRow(ctx, `SELECT COALESCE(i.last_authenticated_at, i.linked_at) <= NOW() - make_interval(secs => $2)
			AND NOT u.local_password_login_enabled
		FROM plugin_auth_identities i JOIN users u ON u.id = i.user_id WHERE i.id = $1`,
		identityID, absoluteAge.Seconds()).Scan(&stale); err != nil {
		return false, fmt.Errorf("reading the last provider authentication: %w", err)
	}
	return stale, nil
}

// absoluteSessionAge is auth.refresh_token_expiry, the absolute age that
// bounds what a provider can no longer re-check.
func absoluteSessionAge(ctx context.Context, db dbQuerier) (time.Duration, error) {
	raw, err := readServerSetting(ctx, db, config.AuthRefreshTokenExpirySettingKey)
	if err != nil {
		return 0, err
	}
	return config.AuthRefreshTokenExpiry(raw), nil
}

// applyAnswer acts on the provider's answer: role sync for an active
// account, revocation of every login session and API key of a refused one
// (only the sessions opened through the identity for a break-glass account,
// or for a network identity that defers to the account's primary provider),
// and of the API keys and Audiobookshelf sessions of an account whose
// provider cannot re-check it and has not vouched for it within the absolute
// age (staleAuth). A deferring network identity changes nothing else: the
// primary provider sets the role and bounds the account's credentials.
func (r *ProviderRecheck) applyAnswer(ctx context.Context, tx pgx.Tx, identity *LinkedIdentity, checkStatus string, account *pluginv1.AuthenticateResponse, staleAuth bool, out *recheckOutcome) error {
	authority, err := primaryAuthorityOf(ctx, tx, identity.UserID, identity.InstallationID)
	if err != nil {
		return err
	}
	switch {
	case checkStatus == CheckStatusActive:
		if authority.defers {
			return nil
		}
		user, err := lockUser(ctx, tx, identity.UserID)
		if err != nil {
			return err
		}
		synced, event, err := syncManagedRole(ctx, tx, user, account.GetManagedRole())
		if err != nil {
			return err
		}
		if event != nil {
			event.attrs = append(event.attrs, auditInstallationID, identity.InstallationID, "method", "recheck")
			out.audit = append(out.audit, *event)
		}
		out.revoked = synced != nil
	case isRefusal(checkStatus):
		user, err := lockUser(ctx, tx, identity.UserID)
		if err != nil {
			return err
		}
		if user.BreakGlass || authority.defers {
			// A break-glass account keeps its local sessions and API keys
			// independent of the provider (linkIdentityTx does not attach
			// them), and an account whose primary provider still vouches for
			// it keeps what that provider opened: the refusal ends only the
			// sessions opened through the identity.
			if _, err := tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at = NOW()
				WHERE identity_id = $1 AND user_id = $2 AND revoked_at IS NULL`, identity.ID, identity.UserID); err != nil {
				return fmt.Errorf("revoking provider sessions: %w", err)
			}
			if _, err := tx.Exec(ctx, `UPDATE device_login_requests SET status = $3, updated_at = NOW()
				WHERE approved_identity_id = $1 AND approved_by_user_id = $2 AND status = $4`,
				identity.ID, identity.UserID, DeviceLoginStatusDenied, DeviceLoginStatusApproved); err != nil {
				return fmt.Errorf("withdrawing provider device sign-in approvals: %w", err)
			}
			out.revoked = true
			out.audit = append(out.audit, auditEvent{"recheck_revoked", []any{
				auditInstallationID, identity.InstallationID, auditUserID, identity.UserID, auditCheckStatus, checkStatus,
				"break_glass", user.BreakGlass, "primary_identity", authority.defers}})
			return nil
		}
		if err := RevokeSignInsInTransaction(ctx, tx, identity.UserID); err != nil {
			return err
		}
		// API keys act as the account without a session to re-check, so a
		// person the provider removed must not keep them.
		if _, err := deleteAPIKeys(ctx, tx, identity.UserID); err != nil {
			return err
		}
		out.revoked = true
		out.audit = append(out.audit, auditEvent{"recheck_revoked", []any{
			auditInstallationID, identity.InstallationID, auditUserID, identity.UserID, auditCheckStatus, checkStatus}})
	case checkStatus == CheckStatusUnsupported:
		if !staleAuth || authority.defers {
			return nil
		}
		// Login sessions already stop sliding (verdictAbsoluteAge); API keys
		// and Audiobookshelf sessions have no such bound, so they end once
		// the person has not signed in through the provider for the
		// absolute age.
		keys, err := deleteAPIKeys(ctx, tx, identity.UserID)
		if err != nil {
			return err
		}
		absSessions, err := tx.Exec(ctx, `UPDATE abs_sessions SET revoked_at = NOW() WHERE user_id = $1 AND revoked_at IS NULL`, identity.UserID)
		if err != nil {
			return fmt.Errorf("revoking Audiobookshelf sessions: %w", err)
		}
		if keys > 0 || absSessions.RowsAffected() > 0 {
			out.audit = append(out.audit, auditEvent{"recheck_credentials_revoked", []any{
				auditInstallationID, identity.InstallationID, auditUserID, identity.UserID, auditCheckStatus, checkStatus,
				"api_keys", keys, "abs_sessions", absSessions.RowsAffected()}})
		}
	case checkStatus == CheckStatusUnavailable:
		slog.WarnContext(ctx, "provider re-check could not reach the provider; retrying at the next refresh",
			"component", "auth", auditInstallationID, identity.InstallationID, auditUserID, identity.UserID)
	}
	return nil
}

// deleteAPIKeys deletes every API key of the account and answers how many
// it deleted.
func deleteAPIKeys(ctx context.Context, tx pgx.Tx, userID int) (int64, error) {
	tag, err := tx.Exec(ctx, `DELETE FROM api_keys WHERE user_id = $1`, userID)
	if err != nil {
		return 0, fmt.Errorf("deleting API keys: %w", err)
	}
	return tag.RowsAffected(), nil
}

// scheduledRecheckBatch bounds the identities one query of the scheduled
// pass reads.
const scheduledRecheckBatch = 100

// idleIdentityCondition selects, for the scheduled pass, the identities
// whose last provider answer is due ($1 the re-check interval, $2 the retry
// wait after an unavailable answer, both in seconds) of an account that is
// not break-glass. Break-glass accounts keep local credentials independent
// of the provider; their provider sessions are re-checked when they
// refresh. At an installation that is an enabled sign-in provider, the
// account must still hold a credential a refresh does not re-check: an API
// key, a live Audiobookshelf session, or a live login session opened
// through the identity that is not refreshing. An identity whose provider
// last answered unsupported is skipped when its account has local password
// sign-in turned on: asking again could only repeat an answer that bounds
// nothing for it (staleProviderAuth). An identity at an enabled primary
// provider is also due while its account has a live login session opened
// through a network identity: those sessions defer to it (primaryAuthorityOf),
// but their refresh re-checks only the network identity.
//
// At an installation that is no longer an enabled sign-in provider nobody
// can be asked, so the pass only bounds the API keys and Audiobookshelf
// sessions of an account the provider has not vouched for within the
// absolute age ($3, auth.refresh_token_expiry in seconds), as for an
// unsupported answer (staleProviderAuth). Login sessions already stop
// sliding at refresh.
var idleIdentityCondition = `
	(i.last_checked_at IS NULL OR i.last_check_status = ''
		OR i.last_checked_at <= NOW() - make_interval(secs => $1)
		OR (i.last_check_status = 'unavailable' AND i.last_checked_at <= NOW() - make_interval(secs => $2)))
	AND EXISTS (SELECT 1 FROM users u WHERE u.id = i.user_id AND NOT u.break_glass
		AND NOT (i.last_check_status = 'unsupported' AND u.local_password_login_enabled)
		AND (EXISTS (SELECT 1 FROM plugin_auth_bindings b JOIN plugin_installations pi ON pi.id = b.plugin_installation_id
				WHERE b.plugin_installation_id = i.plugin_installation_id AND b.enabled AND pi.enabled)
			OR (NOT u.local_password_login_enabled
				AND COALESCE(i.last_authenticated_at, i.linked_at) <= NOW() - make_interval(secs => $3))))
	AND (EXISTS (SELECT 1 FROM api_keys k WHERE k.user_id = i.user_id)
		OR EXISTS (SELECT 1 FROM abs_sessions a WHERE a.user_id = i.user_id AND a.revoked_at IS NULL
			AND (a.expires_at IS NULL OR a.expires_at > NOW()))
		OR EXISTS (SELECT 1 FROM auth_sessions s WHERE s.identity_id = i.id AND s.revoked_at IS NULL AND s.expires_at > NOW()
			AND EXISTS (SELECT 1 FROM plugin_auth_bindings b JOIN plugin_installations pi ON pi.id = b.plugin_installation_id
				WHERE b.plugin_installation_id = i.plugin_installation_id AND b.enabled AND pi.enabled))
		OR EXISTS (SELECT 1 FROM auth_sessions s JOIN plugin_auth_identities n ON n.id = s.identity_id
			WHERE s.user_id = i.user_id AND n.user_id = i.user_id AND n.plugin_installation_id <> i.plugin_installation_id
				AND s.revoked_at IS NULL AND s.expires_at > NOW()
				AND EXISTS (SELECT 1 FROM plugin_auth_bindings b WHERE b.plugin_installation_id = n.plugin_installation_id
					AND ` + plugins.AuthBindingIsNetworkSQL("b.plugin_installation_id", "b.capability_id") + `)
				AND EXISTS (SELECT 1 FROM plugin_auth_bindings b JOIN plugin_installations pi ON pi.id = b.plugin_installation_id
					WHERE b.plugin_installation_id = i.plugin_installation_id AND b.enabled AND pi.enabled
						AND NOT ` + plugins.AuthBindingIsNetworkSQL("b.plugin_installation_id", "b.capability_id") + `)))`

// IdleRecheckDue reports whether the scheduled pass has an identity to
// re-check (RecheckIdleIdentities).
func (r *ProviderRecheck) IdleRecheckDue(ctx context.Context) (bool, error) {
	if r == nil || r.resolver == nil || r.resolver.pool == nil {
		return false, nil
	}
	interval, _, err := r.policy(ctx)
	if err != nil {
		return false, err
	}
	absoluteAge, err := absoluteSessionAge(ctx, r.resolver.pool)
	if err != nil {
		return false, err
	}
	var due bool
	if err := r.resolver.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM plugin_auth_identities i WHERE `+idleIdentityCondition+`)`,
		interval.Seconds(), r.retryWait.Seconds(), absoluteAge.Seconds()).Scan(&due); err != nil {
		return false, fmt.Errorf("finding identities to re-check: %w", err)
	}
	return due, nil
}

// RecheckIdleIdentities is the scheduled provider re-check. A refresh
// re-checks the sessions that keep refreshing; this pass asks the provider
// about the identities whose accounts hold credentials no refresh re-checks
// (idleIdentityCondition), with the same outcomes: a refused account loses
// its login and Audiobookshelf sessions and its API keys. Each check takes
// the identity's advisory lock and re-reads whether it is still due, so
// passes on several nodes, and refreshes, never ask twice. It answers how
// many identities ended in each check status ("failed" counts checks that
// could not complete).
func (r *ProviderRecheck) RecheckIdleIdentities(ctx context.Context) (map[string]int, error) {
	counts := map[string]int{}
	if r == nil || r.resolver == nil || r.resolver.pool == nil {
		return counts, nil
	}
	interval, _, err := r.policy(ctx)
	if err != nil {
		return counts, err
	}
	absoluteAge, err := absoluteSessionAge(ctx, r.resolver.pool)
	if err != nil {
		return counts, err
	}
	var after int64
	for {
		batch, err := r.idleIdentities(ctx, after, interval, absoluteAge)
		if err != nil {
			return counts, err
		}
		for _, identity := range batch {
			after = identity.ID
			checkStatus, _, err := r.recheck(ctx, identity, interval, true)
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return counts, ctxErr
				}
				slog.WarnContext(ctx, "scheduled provider re-check failed", "component", "auth",
					auditInstallationID, identity.InstallationID, auditUserID, identity.UserID, "error", err)
				counts["failed"]++
				continue
			}
			counts[checkStatus]++
		}
		if len(batch) < scheduledRecheckBatch {
			return counts, nil
		}
	}
}

// idleIdentities reads the next batch of identities the scheduled pass
// re-checks, in id order after after.
func (r *ProviderRecheck) idleIdentities(ctx context.Context, after int64, interval, absoluteAge time.Duration) ([]*LinkedIdentity, error) {
	rows, err := r.resolver.pool.Query(ctx, `SELECT `+identityColumns+`
		FROM plugin_auth_identities i WHERE i.id > $4 AND `+idleIdentityCondition+`
		ORDER BY i.id LIMIT $5`, interval.Seconds(), r.retryWait.Seconds(), absoluteAge.Seconds(), after, scheduledRecheckBatch)
	if err != nil {
		return nil, fmt.Errorf("finding identities to re-check: %w", err)
	}
	defer rows.Close()
	var out []*LinkedIdentity
	for rows.Next() {
		identity, err := scanIdentity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, identity)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("finding identities to re-check: %w", err)
	}
	return out, nil
}

// notLoadedStatus decides, from the bindings every node shares, what a
// refresh does when this node has no provider for the installation: an
// installation that is no longer an enabled sign-in provider cannot re-check
// anyone (unsupported: the session stops sliding), while one that is enabled
// but missing here (this node has not rebuilt its providers yet) is
// unavailable. A plugin this node cannot load is errCheckerLoad.
func notLoadedStatus(ctx context.Context, db dbQuerier, installationID int) (string, error) {
	var enabled bool
	if err := db.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM plugin_auth_bindings b JOIN plugin_installations i ON i.id = b.plugin_installation_id
		WHERE b.plugin_installation_id = $1 AND b.enabled AND i.enabled)`, installationID).Scan(&enabled); err != nil {
		return "", fmt.Errorf("reading auth binding: %w", err)
	}
	if enabled {
		return CheckStatusUnavailable, nil
	}
	return CheckStatusUnsupported, nil
}

// ask calls the plugin through checker (nil: the plugin has no
// CheckAccount, unsupported). Unimplemented (a plugin built before
// AuthProviderChecks) is unsupported. Any other failure, an UNSPECIFIED
// answer and a value this build does not know are unavailable. The
// response's external_subject and denial are ignored. A call that fails
// after it may have reached the plugin has an unknown outcome: the
// identity's check_outcome_unknown, committed before the call, stays set.
func (r *ProviderRecheck) ask(ctx context.Context, db dbQuerier, identity *LinkedIdentity, checker AccountChecker, state *structpb.Struct) (checkStatus string, account *pluginv1.AuthenticateResponse) {
	if checker == nil {
		return CheckStatusUnsupported, nil
	}
	if err := r.markOutcomeUnknown(ctx, db, identity.ID); err != nil {
		slog.WarnContext(ctx, "provider re-check could not record the call it was about to make; not calling the plugin", "component", "auth",
			auditInstallationID, identity.InstallationID, "error", err)
		return CheckStatusUnavailable, nil
	}
	callCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	response, err := checker.CheckAccount(callCtx, &pluginv1.CheckAccountRequest{
		ExternalSubject: identity.ExternalSubject,
		RefreshState:    state,
	})
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			return CheckStatusUnsupported, nil
		}
		slog.WarnContext(ctx, "provider re-check failed", "component", "auth",
			auditInstallationID, identity.InstallationID, "error", err)
		return CheckStatusUnavailable, nil
	}
	return checkStatusOf(response.GetStatus()), response.GetAccount()
}

// markOutcomeUnknown commits check_outcome_unknown on the identity before the
// plugin is called. The connection holds the identity's session advisory
// lock, but has not begun the answer transaction, so the marker survives a
// failed call or answer transaction without needing a second connection.
func (r *ProviderRecheck) markOutcomeUnknown(ctx context.Context, db dbQuerier, identityID int64) error {
	markCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	if _, err := db.Exec(markCtx, `UPDATE plugin_auth_identities SET check_outcome_unknown = TRUE WHERE id = $1`, identityID); err != nil {
		return fmt.Errorf("recording provider re-check call: %w", err)
	}
	return nil
}

func checkStatusOf(value pluginv1.CheckAccountStatus) string {
	switch value {
	case pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_ACTIVE:
		return CheckStatusActive
	case pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_NOT_FOUND:
		return CheckStatusNotFound
	case pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_DISABLED:
		return CheckStatusDisabled
	case pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_NOT_PERMITTED:
		return CheckStatusNotPermitted
	case pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_UNSUPPORTED:
		return CheckStatusUnsupported
	default:
		return CheckStatusUnavailable
	}
}

// recordIdentityCheck stamps the answer, which supersedes a pending
// refusal. An active answer also counts as a provider authentication
// (last_authenticated_at) and refreshes what the provider says about the
// person, keeping stored values the provider left empty.
func recordIdentityCheck(ctx context.Context, db dbQuerier, id int64, checkStatus string, account *pluginv1.AuthenticateResponse) error {
	if checkStatus != CheckStatusActive || account == nil {
		account = &pluginv1.AuthenticateResponse{}
	}
	if _, err := db.Exec(ctx, `
		UPDATE plugin_auth_identities
		SET last_checked_at = NOW(), last_check_status = $2,
			last_authenticated_at = CASE WHEN $2 = 'active' THEN NOW() ELSE last_authenticated_at END,
			issuer = COALESCE(NULLIF($3, ''), issuer),
			username = COALESCE(NULLIF($4, ''), username),
			email = COALESCE(NULLIF($5, ''), email),
			display_name = COALESCE(NULLIF($6, ''), display_name),
			pending_refusal = '', updated_at = NOW()
		WHERE id = $1`,
		id, checkStatus, strings.TrimSpace(account.GetIssuer()), strings.TrimSpace(account.GetUsername()),
		strings.TrimSpace(account.GetEmail()), strings.TrimSpace(account.GetDisplayName())); err != nil {
		return fmt.Errorf("recording provider re-check: %w", err)
	}
	return nil
}

// refreshStateAAD binds an identity's encrypted refresh_state to its row.
func refreshStateAAD(identityID int64) string {
	return secret.RowAAD("plugin_auth_identities", "refresh_state", strconv.FormatInt(identityID, 10))
}

// storeRefreshState applies a plugin's refresh_state to the identity: nil
// keeps the stored state, an empty Struct clears it, anything else replaces
// it, encrypted with the server secret. Without a cipher nothing is stored
// (the provider then cannot re-check the account). A stored state is the
// plugin's latest, so it also clears check_outcome_unknown.
func (r *AccountResolver) storeRefreshState(ctx context.Context, db dbQuerier, identityID int64, state *structpb.Struct) error {
	if state == nil {
		return nil
	}
	value := ""
	if len(state.GetFields()) > 0 {
		if r.cipher == nil {
			slog.WarnContext(ctx, "no secret cipher configured; provider refresh state not stored", "component", "auth")
			return nil
		}
		raw, err := protojson.Marshal(state)
		if err != nil {
			return fmt.Errorf("encoding provider refresh state: %w", err)
		}
		if value, err = r.cipher.Encrypt(string(raw), refreshStateAAD(identityID)); err != nil {
			return fmt.Errorf("encrypting provider refresh state: %w", err)
		}
	}
	if _, err := db.Exec(ctx, `UPDATE plugin_auth_identities SET refresh_state = $2, check_outcome_unknown = FALSE, updated_at = NOW() WHERE id = $1`,
		identityID, value); err != nil {
		return fmt.Errorf("storing provider refresh state: %w", err)
	}
	return nil
}

// loadRefreshState returns the identity's stored refresh_state, or nil when
// there is none. A value that no longer decrypts (for example after a
// SECRET_KEY change) is logged and treated as none: the plugin then answers
// that it cannot re-check the account.
func (r *AccountResolver) loadRefreshState(ctx context.Context, db dbQuerier, identityID int64) (*structpb.Struct, error) {
	var stored string
	if err := db.QueryRow(ctx, `SELECT refresh_state FROM plugin_auth_identities WHERE id = $1`, identityID).Scan(&stored); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading provider refresh state: %w", err)
	}
	if stored == "" || r.cipher == nil {
		return nil, nil
	}
	plain, err := r.cipher.Decrypt(stored, refreshStateAAD(identityID))
	if err != nil {
		slog.WarnContext(ctx, "provider refresh state does not decrypt; sending none", "component", "auth",
			"identity_id", identityID, "error", err)
		return nil, nil
	}
	state := &structpb.Struct{}
	if err := protojson.Unmarshal([]byte(plain), state); err != nil {
		slog.WarnContext(ctx, "provider refresh state is unreadable; sending none", "component", "auth",
			"identity_id", identityID, "error", err)
		return nil, nil
	}
	return state, nil
}

// AccountChecker finds the CheckAccount client of an enabled sign-in
// provider (AccountCheckerSource).
func (r *PluginProviderRegistry) AccountChecker(ctx context.Context, installationID int) (AccountChecker, error) {
	for _, registered := range r.Providers() {
		provider, ok := registered.Provider.(*PluginProvider)
		if !ok || provider == nil || provider.InstallationID() != installationID {
			continue
		}
		client, err := provider.client(ctx)
		if err != nil {
			if errors.Is(err, plugins.ErrInstallationDisabled) {
				return nil, ErrProviderNotLoaded
			}
			return nil, err
		}
		checker, ok := client.(AccountChecker)
		if !ok {
			return nil, nil
		}
		return checker, nil
	}
	return nil, ErrProviderNotLoaded
}

// sessionProviderChain is what a device sign-in approved from a session
// inherits from it: the identity the session came from and when the provider
// last vouched for its chain.
type sessionProviderChain struct {
	identityID    *int64
	providerSince *time.Time
}

// sessionProviderChainOf returns the provider chain of the session
// sessionID, for a device sign-in approved from that session. The caller
// holds the account's row lock, serializing this session lock and the device
// approval with account-wide revocation. Empty for a local session.
func sessionProviderChainOf(ctx context.Context, db dbQuerier, sessionID string, userID int) (sessionProviderChain, error) {
	var chain sessionProviderChain
	if sessionID == "" {
		return chain, ErrSessionRevoked
	}
	err := db.QueryRow(ctx, `SELECT identity_id, provider_since FROM auth_sessions
		WHERE id = $1 AND user_id = $2 AND impersonator_user_id IS NULL
			AND revoked_at IS NULL AND expires_at > NOW() FOR UPDATE`, sessionID, userID).Scan(&chain.identityID, &chain.providerSince)
	if errors.Is(err, pgx.ErrNoRows) {
		return sessionProviderChain{}, ErrSessionRevoked
	}
	if err != nil {
		return sessionProviderChain{}, fmt.Errorf("reading session identity: %w", err)
	}
	return chain, nil
}
