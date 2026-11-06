package plugins

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/capability"
	publicevents "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/events"

	"github.com/Silo-Server/silo-server/internal/pluginhost"
	"github.com/Silo-Server/silo-server/internal/secret"
)

// RequiredPermissions is what an installation asks an administrator to
// approve, derived from its stored capabilities and host_api_scopes.
type RequiredPermissions struct {
	// EventAccess is the non-public access classes (admin, user_data) of the
	// catalog events its event_consumer.v1 capabilities subscribe to.
	EventAccess []string
	// RequestedScopes is the manifest's host_api_scopes.
	RequestedScopes []string
	// RequiredScopes is the requested scopes this server can issue.
	RequiredScopes []string
	// UnsupportedScopes is the requested scopes this server doesn't know.
	// They are never granted.
	UnsupportedScopes []string
}

// DeriveRequiredPermissions derives what an installation with these
// capabilities and host_api_scopes needs approved. knownScopes is the
// server's API key scope catalog. Every list is sorted and unique.
func DeriveRequiredPermissions(capabilities []*Capability, hostAPIScopes, knownScopes []string) RequiredPermissions {
	requested := sortedUnique(hostAPIScopes)
	out := RequiredPermissions{
		EventAccess:       requiredEventAccess(capabilities),
		RequestedScopes:   requested,
		RequiredScopes:    []string{},
		UnsupportedScopes: []string{},
	}
	for _, scope := range requested {
		if slices.Contains(knownScopes, scope) {
			out.RequiredScopes = append(out.RequiredScopes, scope)
		} else {
			out.UnsupportedScopes = append(out.UnsupportedScopes, scope)
		}
	}
	return out
}

// requiredEventAccess returns the non-public access classes of the catalog
// events the event_consumer.v1 capabilities subscribe to, sorted and unique.
// Names outside the catalog are never delivered, so they ask for nothing.
func requiredEventAccess(capabilities []*Capability) []string {
	classes := []string{}
	for _, record := range capabilities {
		if record == nil || record.Type != capability.EventConsumer {
			continue
		}
		for _, name := range subscriptionsOf(record) {
			access, ok := publicevents.AccessOf(name)
			if ok && access != publicevents.AccessPublic {
				classes = append(classes, string(access))
			}
		}
	}
	return sortedUnique(classes)
}

// eventAccessApproved reports whether an event of this access class may be
// delivered to an installation whose grant approves approved. Public and
// plugin events need no approval.
func eventAccessApproved(access publicevents.Access, approved []string) bool {
	return access == publicevents.AccessPublic || slices.Contains(approved, string(access))
}

// PermissionGrant is what an administrator approved for one installation.
type PermissionGrant struct {
	InstallationID        int
	ApprovedEventAccess   []string
	ApprovedHostAPIScopes []string
	// ApprovedByUserID and ApprovedAt are nil for a grant no administrator
	// approved explicitly: the event access recorded when the plugin was
	// installed, or carried over from before grants existed.
	ApprovedByUserID *int
	ApprovedAt       *time.Time
	// APIKeyID is nil when no key is issued, including after the key was
	// revoked on the API keys page or went with its owner's account.
	APIKeyID          *int64
	APIKeyOwnerUserID *int
	APIKeyScopes      []string
	UpdatedAt         time.Time

	apiKeyCiphertext *string
	// keyOwnerAdmin reports whether the key's account is still an enabled
	// administrator. A key acts as its owner, so one whose owner was demoted
	// or disabled can't do what was approved.
	keyOwnerAdmin bool
	// keyUnreadable marks a stored key this server can't decrypt, such as
	// one sealed under an earlier SECRET_KEY.
	keyUnreadable bool
}

// HostAPIKeyUsable reports whether the grant's key can act for the plugin:
// it exists, this server can open it, and its owner is still an enabled
// administrator. An approval replaces a key that isn't usable.
func (g *PermissionGrant) HostAPIKeyUsable() bool {
	return g != nil && g.APIKeyID != nil && !g.keyUnreadable && g.keyOwnerAdmin
}

// EffectiveHostAPIScopes is the scopes the installation can use now: the
// approved scopes while their key is usable, and none otherwise.
func (g *PermissionGrant) EffectiveHostAPIScopes() []string {
	if !g.HostAPIKeyUsable() {
		return []string{}
	}
	return nonNilStrings(g.ApprovedHostAPIScopes)
}

func (g *PermissionGrant) approvedEventAccess() []string {
	if g == nil {
		return []string{}
	}
	return nonNilStrings(g.ApprovedEventAccess)
}

// PermissionsPending reports whether required asks for anything grant
// doesn't approve: an event access class, or a host API scope its usable key
// doesn't carry.
func PermissionsPending(required RequiredPermissions, grant *PermissionGrant) bool {
	return !isSubset(required.EventAccess, grant.approvedEventAccess()) ||
		!isSubset(required.RequiredScopes, grant.EffectiveHostAPIScopes())
}

// pgExecer is the statement surface of a pool or transaction.
type pgExecer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// ensurePermissionGrant records the installation's current event access as
// approved, with no host API scopes, unless it already has a grant. It runs
// with every capability write, so installing a plugin is consent to the
// events it subscribes to, and a replacement never widens an existing grant.
func ensurePermissionGrant(ctx context.Context, q pgExecer, installationID int, capabilities []*Capability) error {
	if _, err := q.Exec(ctx, `INSERT INTO plugin_permission_grants (plugin_installation_id, approved_event_access)
		VALUES ($1, $2) ON CONFLICT (plugin_installation_id) DO NOTHING`,
		installationID, requiredEventAccess(capabilities)); err != nil {
		return fmt.Errorf("recording plugin permission grant: %w", err)
	}
	return nil
}

// narrowHostAPIKey keeps a replacement from leaving the plugin a key wider
// than its new manifest asks for. When the key the grant names carries a
// scope outside scopes, it deletes the key and clears the approved scopes in
// tx: the installation is keyless, and pending while it still asks for
// scopes, until an administrator approves again. A replacement that keeps or
// widens the scopes leaves the key alone, and a wider requirement waits for
// approval. The key's deletion is not audited; no administrator acts here.
func narrowHostAPIKey(ctx context.Context, tx pgx.Tx, installationID int, scopes []string) error {
	grant, err := lockPermissionGrant(ctx, tx, installationID)
	if err != nil {
		return err
	}
	if grant == nil || grant.APIKeyID == nil || isSubset(grant.APIKeyScopes, scopes) {
		return nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM api_keys WHERE id = $1`, *grant.APIKeyID); err != nil {
		return fmt.Errorf("deleting the narrowed plugin host API key: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE plugin_permission_grants
		SET approved_host_api_scopes = '{}', api_key_id = NULL, api_key_ciphertext = NULL, updated_at = NOW()
		WHERE plugin_installation_id = $1`, installationID); err != nil {
		return fmt.Errorf("clearing the narrowed plugin host API scopes: %w", err)
	}
	slog.InfoContext(ctx, "plugin replacement asks for fewer host API scopes; its key was deleted until an administrator approves again",
		"component", "plugins", "installation_id", installationID)
	return nil
}

// EnsurePermissionGrants gives every installation without a grant one that
// approves the event access its capabilities ask for now. Installations from
// before grants existed keep the events they already received. The server
// runs it at boot, before the catalog auto-update can replace a plugin, so an
// update never reaches an installation that has no grant to hold it back.
// The reserved builtin row is skipped. It returns how many grants it wrote.
func (s *InstallationStore) EnsurePermissionGrants(ctx context.Context) (int, error) {
	rows, err := s.pool.Query(ctx, `SELECT i.id FROM plugin_installations i
		WHERE i.kind <> $1 AND NOT EXISTS (
			SELECT 1 FROM plugin_permission_grants g WHERE g.plugin_installation_id = i.id)
		ORDER BY i.id`, KindBuiltin)
	if err != nil {
		return 0, fmt.Errorf("listing plugin installations without permission grants: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err != nil {
		return 0, fmt.Errorf("listing plugin installations without permission grants: %w", err)
	}
	written := 0
	for _, id := range ids {
		capabilities, err := s.ListCapabilities(ctx, id)
		if err != nil {
			return written, err
		}
		tag, err := s.pool.Exec(ctx, `INSERT INTO plugin_permission_grants (plugin_installation_id, approved_event_access)
			SELECT id, $2 FROM plugin_installations WHERE id = $1
			ON CONFLICT (plugin_installation_id) DO NOTHING`, id, requiredEventAccess(capabilities))
		if err != nil {
			return written, fmt.Errorf("recording plugin permission grant: %w", err)
		}
		written += int(tag.RowsAffected())
	}
	return written, nil
}

// permissionGrantSelect reads grants with their key's owner and scopes. The
// owner counts as an administrator only with the admin role on an enabled
// account (models.RoleAdmin), what the admin routes require of a key.
const permissionGrantSelect = `SELECT g.plugin_installation_id, g.approved_event_access, g.approved_host_api_scopes,
		g.approved_by_user_id, g.approved_at, g.api_key_id, g.api_key_ciphertext, g.updated_at,
		k.user_id, COALESCE(k.scopes, '{}'::text[]), COALESCE(u.role = 'admin' AND u.enabled, false)
	FROM plugin_permission_grants g
	LEFT JOIN api_keys k ON k.id = g.api_key_id
	LEFT JOIN users u ON u.id = k.user_id`

func scanPermissionGrant(row pgx.Row) (*PermissionGrant, error) {
	var grant PermissionGrant
	if err := row.Scan(&grant.InstallationID, &grant.ApprovedEventAccess, &grant.ApprovedHostAPIScopes,
		&grant.ApprovedByUserID, &grant.ApprovedAt, &grant.APIKeyID, &grant.apiKeyCiphertext, &grant.UpdatedAt,
		&grant.APIKeyOwnerUserID, &grant.APIKeyScopes, &grant.keyOwnerAdmin); err != nil {
		return nil, err
	}
	return &grant, nil
}

// lockPermissionGrant locks, in tx, the key the installation's grant names
// and then the grant, and returns the grant (nil when it has none). Every
// API key deletion locks the key before the grant that names it, since the
// key's ON DELETE SET NULL writes the grant, so this order can't deadlock
// with one. errPermissionGrantChanged means the grant named another key once
// locked; the caller rolls back and starts again (retryOnGrantChange).
func lockPermissionGrant(ctx context.Context, tx pgx.Tx, installationID int) (*PermissionGrant, error) {
	var named *int64
	err := tx.QueryRow(ctx, `SELECT api_key_id FROM plugin_permission_grants WHERE plugin_installation_id = $1`, installationID).Scan(&named)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("reading plugin permission grant: %w", err)
	}
	if named != nil {
		// A key deleted meanwhile has no row to lock; the grant then names
		// none, and the comparison below starts again.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM api_keys WHERE id = $1 FOR UPDATE`, *named); err != nil {
			return nil, fmt.Errorf("locking plugin host API key: %w", err)
		}
	}
	grant, err := scanPermissionGrant(tx.QueryRow(ctx, permissionGrantSelect+` WHERE g.plugin_installation_id = $1 FOR UPDATE OF g`, installationID))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("locking plugin permission grant: %w", err)
	}
	var locked *int64
	if grant != nil {
		locked = grant.APIKeyID
	}
	if !sameKeyID(named, locked) {
		return nil, errPermissionGrantChanged
	}
	return grant, nil
}

func sameKeyID(a, b *int64) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

// grantLockAttempts bounds how often an approval or uninstall starts again
// because the grant's key changed under it. Each restart means another
// writer committed, so running out takes a sustained stream of them.
const grantLockAttempts = 5

// retryOnGrantChange runs one transaction attempt until it doesn't return
// errPermissionGrantChanged, at most grantLockAttempts times.
func retryOnGrantChange(attempt func() error) error {
	var err error
	for range grantLockAttempts {
		if err = attempt(); !errors.Is(err, errPermissionGrantChanged) {
			return err
		}
	}
	return err
}

// PermissionGrant returns the installation's grant, or nil when it has none.
func (s *InstallationStore) PermissionGrant(ctx context.Context, installationID int) (*PermissionGrant, error) {
	grant, err := scanPermissionGrant(s.pool.QueryRow(ctx, permissionGrantSelect+` WHERE g.plugin_installation_id = $1`, installationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading plugin permission grant: %w", err)
	}
	return grant, nil
}

// ListPermissionGrants returns every grant keyed by installation.
func (s *InstallationStore) ListPermissionGrants(ctx context.Context) (map[int]*PermissionGrant, error) {
	rows, err := s.pool.Query(ctx, permissionGrantSelect)
	if err != nil {
		return nil, fmt.Errorf("listing plugin permission grants: %w", err)
	}
	defer rows.Close()
	grants := map[int]*PermissionGrant{}
	for rows.Next() {
		grant, err := scanPermissionGrant(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning plugin permission grant: %w", err)
		}
		grants[grant.InstallationID] = grant
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing plugin permission grants: %w", err)
	}
	return grants, nil
}

// ApprovedEventAccess returns the event access classes each installation's
// grant approves. An installation without a grant receives only public and
// plugin events.
func (s *InstallationStore) ApprovedEventAccess(ctx context.Context) (map[int][]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT plugin_installation_id, approved_event_access FROM plugin_permission_grants`)
	if err != nil {
		return nil, fmt.Errorf("listing approved plugin event access: %w", err)
	}
	defer rows.Close()
	approved := map[int][]string{}
	for rows.Next() {
		var id int
		var access []string
		if err := rows.Scan(&id, &access); err != nil {
			return nil, fmt.Errorf("scanning approved plugin event access: %w", err)
		}
		approved[id] = access
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing approved plugin event access: %w", err)
	}
	return approved, nil
}

// permissionApproval is one explicit approval to store.
type permissionApproval struct {
	installationID int
	eventAccess    []string
	hostAPIScopes  []string
	approvedBy     int
	// apiKeyID and apiKeyCiphertext name the key to keep; nil for none.
	apiKeyID         *int64
	apiKeyCiphertext *string
	// previousAPIKeyID is the key the locked grant named. The write lands
	// only while it still does, which matters only when there was no grant
	// row to lock and a concurrent install inserted one.
	previousAPIKeyID *int64
}

// errPermissionGrantChanged means the grant's key changed while an approval
// or uninstall held no lock on it.
var errPermissionGrantChanged = errors.New("plugin permission grant changed")

func savePermissionApproval(ctx context.Context, tx pgx.Tx, a permissionApproval) error {
	tag, err := tx.Exec(ctx, `INSERT INTO plugin_permission_grants AS g
			(plugin_installation_id, approved_event_access, approved_host_api_scopes, approved_by_user_id, approved_at, api_key_id, api_key_ciphertext, updated_at)
		VALUES ($1, $2, $3, $4, NOW(), $5, $6, NOW())
		ON CONFLICT (plugin_installation_id) DO UPDATE SET
			approved_event_access = EXCLUDED.approved_event_access,
			approved_host_api_scopes = EXCLUDED.approved_host_api_scopes,
			approved_by_user_id = EXCLUDED.approved_by_user_id,
			approved_at = EXCLUDED.approved_at,
			api_key_id = EXCLUDED.api_key_id,
			api_key_ciphertext = EXCLUDED.api_key_ciphertext,
			updated_at = EXCLUDED.updated_at
		WHERE g.api_key_id IS NOT DISTINCT FROM $7`,
		a.installationID, nonNilStrings(a.eventAccess), nonNilStrings(a.hostAPIScopes), a.approvedBy,
		a.apiKeyID, a.apiKeyCiphertext, a.previousAPIKeyID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			// The installation was uninstalled meanwhile.
			return ErrInstallationNotFound
		}
		return fmt.Errorf("saving plugin permission approval: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errPermissionGrantChanged
	}
	return nil
}

// hostAPIKeyAAD binds a stored host API key to its installation and key row,
// so a ciphertext moved to another grant, or kept after its key was
// replaced, does not open.
func hostAPIKeyAAD(installationID int, apiKeyID int64) string {
	return secret.RowAAD("plugin_permission_grants", "api_key_ciphertext",
		strconv.Itoa(installationID)+":"+strconv.FormatInt(apiKeyID, 10))
}

// HostAPIKeyIssuer mints and deletes the API keys issued to plugins inside
// the caller's transaction, so a key and the grant that names it commit or
// roll back together. The auth package implements it; plugins can't import
// auth. Each mutation returns committed, which the caller runs only after
// its transaction commits (it publishes the audit entry).
type HostAPIKeyIssuer interface {
	// KnownScopes is the server's API key scope catalog.
	KnownScopes() []string
	// LockAccounts takes in tx the account locks IssueTx and RevokeTx take
	// for actorID. A caller runs it before it locks a key or a grant, so its
	// locks follow the accounts, key, grant order of every key deletion.
	LockAccounts(ctx context.Context, tx pgx.Tx, actorID int) error
	// IssueTx mints a key with exactly scopes on actorID's own account,
	// acting as actorID.
	IssueTx(ctx context.Context, tx pgx.Tx, actorID int, label string, scopes []string) (id int64, token string, committed func(), err error)
	// RevokeTx deletes a key. A key that is already gone is not an error.
	RevokeTx(ctx context.Context, tx pgx.Tx, id int64) (committed func(), err error)
}

// PluginPermissions is one installation's required and approved permissions.
type PluginPermissions struct {
	InstallationID int
	Required       RequiredPermissions
	// Grant is nil for an installation that has none.
	Grant   *PermissionGrant
	Pending bool
}

// PermissionService approves what plugins receive and may call, issues the
// scoped host API key an approval carries, and answers plugins asking for it.
type PermissionService struct {
	installations *InstallationStore
	cipher        *secret.Cipher
	keys          HostAPIKeyIssuer
	// onChange runs after an approval so every API server rebuilds its event
	// subscriber index. It is Service.OnLifecycleChange, which announces the
	// change and restarts nothing.
	onChange func(context.Context)
}

// NewPermissionService creates the service. onChange may be nil.
func NewPermissionService(installations *InstallationStore, cipher *secret.Cipher, keys HostAPIKeyIssuer, onChange func(context.Context)) *PermissionService {
	return &PermissionService{installations: installations, cipher: cipher, keys: keys, onChange: onChange}
}

// pgQuerier is the read surface of a pool or transaction.
type pgQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// required loads an installation an administrator may act on and what it
// asks for now, through q.
// Errors: ErrInstallationNotFound, ErrBuiltinInstallationImmutable.
func (p *PermissionService) required(ctx context.Context, q pgQuerier, installationID int) (*Installation, RequiredPermissions, error) {
	installation, err := getInstallation(ctx, q, installationID)
	if err != nil {
		return nil, RequiredPermissions{}, err
	}
	if installation.IsBuiltin() {
		return nil, RequiredPermissions{}, ErrBuiltinInstallationImmutable
	}
	capabilities, err := listCapabilities(ctx, q, installationID)
	if err != nil {
		return nil, RequiredPermissions{}, err
	}
	return installation, DeriveRequiredPermissions(capabilities, installation.HostAPIScopes, p.keys.KnownScopes()), nil
}

func (p *PermissionService) pluginPermissions(installationID int, required RequiredPermissions, grant *PermissionGrant) *PluginPermissions {
	p.checkKey(grant)
	return &PluginPermissions{InstallationID: installationID, Required: required, Grant: grant, Pending: PermissionsPending(required, grant)}
}

// Permissions returns the installation's required and approved permissions.
// Errors: ErrInstallationNotFound, ErrBuiltinInstallationImmutable.
func (p *PermissionService) Permissions(ctx context.Context, installationID int) (*PluginPermissions, error) {
	_, required, err := p.required(ctx, p.installations.pool, installationID)
	if err != nil {
		return nil, err
	}
	grant, err := p.installations.PermissionGrant(ctx, installationID)
	if err != nil {
		return nil, err
	}
	return p.pluginPermissions(installationID, required, grant), nil
}

// checkKey marks a grant whose stored key doesn't open, so the installation
// shows as pending and an approval replaces the key.
func (p *PermissionService) checkKey(grant *PermissionGrant) {
	if grant != nil && grant.APIKeyID != nil {
		grant.keyUnreadable = !p.opens(grant)
	}
}

// PendingByInstallation reports, for every installation that isn't builtin,
// whether it asks for permissions its grant doesn't approve.
func (p *PermissionService) PendingByInstallation(ctx context.Context) (map[int]bool, error) {
	installations, err := p.installations.List(ctx)
	if err != nil {
		return nil, err
	}
	consumers, err := ListCapabilitiesOfType(ctx, p.installations.pool, capability.EventConsumer)
	if err != nil {
		return nil, err
	}
	byInstallation := map[int][]*Capability{}
	for _, consumer := range consumers {
		byInstallation[consumer.InstallationID] = append(byInstallation[consumer.InstallationID], consumer)
	}
	grants, err := p.installations.ListPermissionGrants(ctx)
	if err != nil {
		return nil, err
	}
	known := p.keys.KnownScopes()
	pending := make(map[int]bool, len(installations))
	for _, installation := range installations {
		if installation.IsBuiltin() {
			continue
		}
		required := DeriveRequiredPermissions(byInstallation[installation.ID], installation.HostAPIScopes, known)
		pending[installation.ID] = p.pluginPermissions(installation.ID, required, grants[installation.ID]).Pending
	}
	return pending, nil
}

// ApprovalCheck vets an approval inside its transaction, against the
// permissions as they stand once the grant is locked. An error aborts the
// approval and Approve returns it unchanged.
type ApprovalCheck func(current *PluginPermissions) error

// Approve approves everything the installation asks for now, as actorID.
// check, when non-nil, runs inside the approval's transaction; the API uses
// it to refuse an approval of anything other than what the administrator
// read.
//
// When the installation asks for host API scopes and has no usable key with
// exactly those, a key is minted on actorID's account. Minting it, pointing
// the grant at it, and deleting the key it replaces commit in one
// transaction, as does deleting the key when no scopes are asked for, so the
// grant always names the one live key issued to the plugin. Repeating an
// approval keeps a usable key.
// Errors: ErrInstallationNotFound, ErrBuiltinInstallationImmutable, and
// whatever check returns.
func (p *PermissionService) Approve(ctx context.Context, actorID, installationID int, check ApprovalCheck) (*PluginPermissions, error) {
	var committed []func()
	err := retryOnGrantChange(func() error {
		var err error
		committed, err = p.approve(ctx, actorID, installationID, check)
		return err
	})
	if err != nil {
		return nil, err
	}
	for _, run := range committed {
		run()
	}
	p.changed(ctx)
	return p.Permissions(ctx, installationID)
}

// approve is one attempt at Approve in its own transaction. It returns the
// hooks to run once that transaction has committed.
func (p *PermissionService) approve(ctx context.Context, actorID, installationID int, check ApprovalCheck) ([]func(), error) {
	tx, err := p.installations.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning plugin permission approval: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanupCtx)
	}()

	// Accounts, then the grant's key, then the grant: the order every key
	// deletion locks them in.
	if err := p.keys.LockAccounts(ctx, tx, actorID); err != nil {
		return nil, err
	}
	grant, err := lockPermissionGrant(ctx, tx, installationID)
	if err != nil {
		return nil, err
	}
	installation, required, err := p.required(ctx, tx, installationID)
	if err != nil {
		return nil, err
	}
	// Built even without a check: it marks a stored key that doesn't open,
	// which decides whether the key is kept.
	current := p.pluginPermissions(installationID, required, grant)
	if check != nil {
		if err := check(current); err != nil {
			return nil, err
		}
	}

	approval := permissionApproval{
		installationID: installationID,
		eventAccess:    required.EventAccess,
		hostAPIScopes:  required.RequiredScopes,
		approvedBy:     actorID,
	}
	if grant != nil {
		approval.previousAPIKeyID = grant.APIKeyID
	}
	keep := grant.HostAPIKeyUsable() && len(required.RequiredScopes) > 0 &&
		slices.Equal(sortedUnique(grant.APIKeyScopes), required.RequiredScopes)
	var committed []func()
	switch {
	case keep:
		approval.apiKeyID, approval.apiKeyCiphertext = grant.APIKeyID, grant.apiKeyCiphertext
	case len(required.RequiredScopes) > 0:
		if p.cipher == nil {
			return nil, errors.New("plugin host API keys need the server data cipher")
		}
		id, token, issued, err := p.keys.IssueTx(ctx, tx, actorID, "Plugin: "+installation.PluginID, required.RequiredScopes)
		if err != nil {
			return nil, fmt.Errorf("issuing plugin host API key: %w", err)
		}
		sealed, err := p.cipher.Encrypt(token, hostAPIKeyAAD(installationID, id))
		if err != nil {
			return nil, fmt.Errorf("sealing plugin host API key: %w", err)
		}
		approval.apiKeyID, approval.apiKeyCiphertext = &id, &sealed
		committed = append(committed, issued)
	}
	if err := savePermissionApproval(ctx, tx, approval); err != nil {
		return nil, err
	}
	if approval.previousAPIKeyID != nil && !keep {
		// The grant no longer names it, so its ON DELETE SET NULL writes
		// nothing.
		revoked, err := p.keys.RevokeTx(ctx, tx, *approval.previousAPIKeyID)
		if err != nil {
			return nil, fmt.Errorf("revoking the replaced plugin host API key: %w", err)
		}
		committed = append(committed, revoked)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing plugin permission approval: %w", err)
	}
	return committed, nil
}

// DeleteInstallation uninstalls the plugin as InstallationStore.Delete does,
// deleting its host API key through the issuer, as actorID, so the key's
// deletion is audited like one on the API keys page.
// Errors: ErrInstallationNotFound, ErrBuiltinInstallationImmutable.
func (p *PermissionService) DeleteInstallation(ctx context.Context, actorID, installationID int) error {
	return p.installations.delete(ctx, installationID, &hostAPIKeyRevoker{keys: p.keys, actorID: actorID})
}

// opens reports whether the grant's stored key still decrypts. One sealed
// under an earlier SECRET_KEY doesn't, and an approval replaces it.
func (p *PermissionService) opens(grant *PermissionGrant) bool {
	if p.cipher == nil || grant.APIKeyID == nil || grant.apiKeyCiphertext == nil {
		return false
	}
	_, err := p.cipher.Decrypt(*grant.apiKeyCiphertext, hostAPIKeyAAD(grant.InstallationID, *grant.APIKeyID))
	return err == nil
}

func (p *PermissionService) changed(ctx context.Context) {
	if p.onChange != nil {
		p.onChange(ctx)
	}
}

// HostAPICredential returns the host API key issued to the installation. It
// is the pluginhost.HostAPICredentialSource of the API host; installationID
// comes from the host, never from the plugin.
func (p *PermissionService) HostAPICredential(ctx context.Context, installationID int) (pluginhost.HostAPICredential, error) {
	if installationID <= 0 {
		return pluginhost.HostAPICredential{}, fmt.Errorf("%w: a test run of a plugin has no host API key", pluginhost.ErrHostAPINotGranted)
	}
	installation, err := p.installations.GetByID(ctx, installationID)
	if err != nil {
		return pluginhost.HostAPICredential{}, err
	}
	grant, err := p.installations.PermissionGrant(ctx, installationID)
	if err != nil {
		return pluginhost.HostAPICredential{}, err
	}
	if grant == nil || grant.APIKeyID == nil || grant.apiKeyCiphertext == nil {
		reason := "an administrator hasn't approved this plugin's host API scopes"
		switch {
		case len(installation.HostAPIScopes) == 0:
			reason = "the plugin's manifest declares no host_api_scopes"
		case grant != nil && len(grant.ApprovedHostAPIScopes) > 0:
			reason = "this plugin's host API key was revoked; an administrator must approve its permissions again"
		}
		return pluginhost.HostAPICredential{}, fmt.Errorf("%w: %s", pluginhost.ErrHostAPINotGranted, reason)
	}
	if !grant.keyOwnerAdmin {
		return pluginhost.HostAPICredential{}, fmt.Errorf("%w: this plugin's host API key acts as an account that is no longer an enabled administrator; an administrator must approve its permissions again", pluginhost.ErrHostAPINotGranted)
	}
	if p.cipher == nil {
		return pluginhost.HostAPICredential{}, errors.New("plugin host API keys need the server data cipher")
	}
	token, err := p.cipher.Decrypt(*grant.apiKeyCiphertext, hostAPIKeyAAD(installationID, *grant.APIKeyID))
	if err != nil {
		// Sealed under another SECRET_KEY, or not this installation's. An
		// approval issues a key this server can open.
		slog.WarnContext(ctx, "plugin host API key does not open", "component", "plugins", "installation_id", installationID, "error", err)
		return pluginhost.HostAPICredential{}, fmt.Errorf("%w: this plugin's host API key can't be read on this server; an administrator must approve its permissions again", pluginhost.ErrHostAPINotGranted)
	}
	return pluginhost.HostAPICredential{Token: token, Scopes: sortedUnique(grant.APIKeyScopes)}, nil
}

func capabilityPointers(capabilities []Capability) []*Capability {
	out := make([]*Capability, len(capabilities))
	for i := range capabilities {
		out[i] = &capabilities[i]
	}
	return out
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func sortedUnique(values []string) []string {
	out := slices.Clone(nonNilStrings(values))
	slices.Sort(out)
	return slices.Compact(out)
}

func isSubset(values, of []string) bool {
	for _, value := range values {
		if !slices.Contains(of, value) {
			return false
		}
	}
	return true
}
