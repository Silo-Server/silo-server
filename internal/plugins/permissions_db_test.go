package plugins_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
	"github.com/Silo-Server/silo-server/internal/plugins"
	"github.com/Silo-Server/silo-server/internal/secret"
)

func subscriber(events ...string) plugins.Capability {
	subscriptions := make([]any, 0, len(events))
	for _, event := range events {
		subscriptions = append(subscriptions, event)
	}
	return plugins.Capability{Type: "event_consumer.v1", ID: "events", Metadata: map[string]any{"subscriptions": subscriptions}}
}

// TestPluginPermissionGrantsPostgres covers the plugin permission grant and
// its host API key against Postgres: installing approves the events the
// plugin subscribes to and a replacement never widens that; the boot pass
// carries existing installations over; an approval mints a key acting as the
// approving admin, keeps it on a repeat, rotates it when the scopes change
// (create, save, then revoke); a replacement that narrows or drops the
// scopes deletes the key at once; a key revoked on the API keys page, one that no longer opens, or one whose owner
// lost the admin role leaves the plugin pending and the next approval
// replaces it; an approval checks what it approves inside its transaction,
// so an update landing after the read is refused; a failure before the
// approval commits leaves the previous key, still named by the grant, as the
// only one; a stored key opens only for its own installation; and
// uninstalling deletes the key with the installation and audits it.
func TestPluginPermissionGrantsPostgres(t *testing.T) {
	f := newPermissionsFixture(t)
	t.Run("installing approves subscribed events and a replacement never widens them", f.installApprovesEventsAndReplaceNeverWidens)
	t.Run("boot pass carries over installations without a grant", f.bootPassCarriesOverInstallations)
	t.Run("approval issues, keeps and rotates the host API key", f.approvalIssuesKeepsAndRotatesKey)
	t.Run("an update between reading and approving is refused", f.updateBetweenReadAndApprovalIsRefused)
	t.Run("a key whose owner lost the admin role is pending and replaced", f.demotedOwnersKeyIsPendingAndReplaced)
	t.Run("a failure before the approval commits leaves only the previous key", f.failedRevokeLeavesOnlyThePreviousKey)
	t.Run("a revoked key leaves the plugin pending and narrowed scopes revoke it", f.revokedKeyIsPendingAndDroppedScopesRevoke)
	t.Run("a stored key opens only for its own installation", f.storedKeyOpensOnlyForItsInstallation)
	t.Run("uninstalling deletes the host API key with the installation", f.uninstallDeletesKey)
}

type permissionsFixture struct {
	pool        *pgxpool.Pool
	store       *plugins.InstallationStore
	apiKeys     *auth.APIKeyRepository
	cipher      *secret.Cipher
	permissions *plugins.PermissionService
	changes     int
	stamp       string
}

func newPermissionsFixture(t *testing.T) *permissionsFixture {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	cipher, err := secret.New(bytes.Repeat([]byte{7}, secret.MinMasterKeyLen))
	if err != nil {
		t.Fatal(err)
	}
	f := &permissionsFixture{
		pool:    pool,
		store:   plugins.NewInstallationStore(pool),
		apiKeys: auth.NewAPIKeyRepository(pool),
		cipher:  cipher,
		stamp:   time.Now().UTC().Format("20060102150405.000000000"),
	}
	f.permissions = plugins.NewPermissionService(f.store, cipher, auth.PluginHostAPIKeys{Keys: f.apiKeys}, func(context.Context) { f.changes++ })
	return f
}

func (f *permissionsFixture) admin(t *testing.T, name string) int {
	t.Helper()
	var id int
	username := "plugin-permissions-" + name + "-" + f.stamp
	if err := f.pool.QueryRow(t.Context(), `INSERT INTO users (username, email, password_hash, role) VALUES ($1, $2, 'x', 'admin') RETURNING id`,
		username, username+"@example.test").Scan(&id); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id) })
	return id
}

func (f *permissionsFixture) install(t *testing.T, name string, scopes []string, capabilities ...plugins.Capability) *plugins.Installation {
	t.Helper()
	installation, err := f.store.Create(t.Context(), plugins.CreateInstallationInput{
		PluginID: "silo.test.permissions." + name + "-" + f.stamp, Version: "1.0.0",
		InstallPath: filepath.Join(t.TempDir(), "plugin"), Enabled: true, UpdatePolicy: "manual",
		Capabilities: capabilities, HostAPIScopes: scopes,
	})
	if err != nil {
		t.Fatalf("create installation: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE id = $1`, installation.ID)
	})
	return installation
}

// replace rewrites an installation's capabilities and scopes, as an upgrade does.
func (f *permissionsFixture) replace(t *testing.T, id int, scopes []string, capabilities ...plugins.Capability) {
	t.Helper()
	if err := f.store.Update(t.Context(), id, plugins.UpdateInstallationInput{Capabilities: capabilities, HostAPIScopes: scopes}); err != nil {
		t.Fatalf("replace installation: %v", err)
	}
}

func (f *permissionsFixture) read(t *testing.T, id int) *plugins.PluginPermissions {
	t.Helper()
	got, err := f.permissions.Permissions(t.Context(), id)
	if err != nil {
		t.Fatalf("read permissions: %v", err)
	}
	return got
}

func (f *permissionsFixture) approve(t *testing.T, actor, id int) *plugins.PluginPermissions {
	t.Helper()
	got, err := f.permissions.Approve(t.Context(), actor, id, nil)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	return got
}

func (f *permissionsFixture) keyExists(t *testing.T, id int64) bool {
	t.Helper()
	var exists bool
	if err := f.pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM api_keys WHERE id = $1)`, id).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

// pluginKeys lists the IDs of the plugin's host API keys on any account.
func (f *permissionsFixture) pluginKeys(t *testing.T, installation *plugins.Installation) []int64 {
	t.Helper()
	rows, err := f.pool.Query(t.Context(), `SELECT id FROM api_keys WHERE label = $1 ORDER BY id`, "Plugin: "+installation.PluginID)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

// failingRevoke is the real issuer with RevokeTx failing, as a crash or a
// lost connection between pointing the grant at a new key and deleting the
// old one would.
type failingRevoke struct{ plugins.HostAPIKeyIssuer }

var errInjectedRevoke = errors.New("injected revoke failure")

func (failingRevoke) RevokeTx(context.Context, pgx.Tx, int64) (func(), error) {
	return nil, errInjectedRevoke
}

func (f *permissionsFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func (f *permissionsFixture) installApprovesEventsAndReplaceNeverWidens(t *testing.T) {
	installation := f.install(t, "widen", nil, subscriber("scan.completed", "catalog.library.changed"))
	got := f.read(t, installation.ID)
	if got.Pending || !slices.Equal(got.Grant.ApprovedEventAccess, []string{"admin"}) || got.Grant.ApprovedAt != nil {
		t.Fatalf("after install: pending=%v grant=%+v", got.Pending, got.Grant)
	}

	f.replace(t, installation.ID, []string{auth.ScopeAdminMonitorRead}, subscriber("scan.completed", "notification.created"))
	got = f.read(t, installation.ID)
	if !got.Pending || !slices.Equal(got.Grant.ApprovedEventAccess, []string{"admin"}) {
		t.Fatalf("after an upgrade asking for more: pending=%v grant=%+v", got.Pending, got.Grant)
	}
	approved, err := f.store.ApprovedEventAccess(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(approved[installation.ID], []string{"admin"}) {
		t.Fatalf("dispatcher sees %v, want [admin] until an administrator approves", approved[installation.ID])
	}
}

func (f *permissionsFixture) bootPassCarriesOverInstallations(t *testing.T) {
	installation := f.install(t, "boot", nil, subscriber("notification.created", "session.started"))
	f.exec(t, `DELETE FROM plugin_permission_grants WHERE plugin_installation_id = $1`, installation.ID)
	if got := f.read(t, installation.ID); got.Grant != nil || !got.Pending {
		t.Fatalf("without a grant: pending=%v grant=%+v", got.Pending, got.Grant)
	}
	if _, err := f.store.EnsurePermissionGrants(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := f.read(t, installation.ID)
	if got.Pending || !slices.Equal(got.Grant.ApprovedEventAccess, []string{"admin", "user_data"}) {
		t.Fatalf("after the boot pass: pending=%v grant=%+v", got.Pending, got.Grant)
	}
	f.exec(t, `UPDATE plugin_permission_grants SET approved_event_access = '{}' WHERE plugin_installation_id = $1`, installation.ID)
	if _, err := f.store.EnsurePermissionGrants(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := f.read(t, installation.ID); len(got.Grant.ApprovedEventAccess) != 0 {
		t.Fatalf("a second boot pass rewrote an existing grant: %+v", got.Grant)
	}
}

func (f *permissionsFixture) approvalIssuesKeepsAndRotatesKey(t *testing.T) {
	ctx := t.Context()
	first, second := f.admin(t, "first"), f.admin(t, "second")
	installation := f.install(t, "keys", []string{auth.ScopeAdminMonitorRead, "admin:not-a-scope"}, subscriber("session.started"))
	if _, err := f.permissions.HostAPICredential(ctx, installation.ID); !errors.Is(err, pluginhost.ErrHostAPINotGranted) {
		t.Fatalf("credential before approval: %v, want ErrHostAPINotGranted", err)
	}
	if !f.read(t, installation.ID).Pending {
		t.Fatalf("an installation asking for scopes isn't pending before approval")
	}

	approved := f.approve(t, first, installation.ID)
	if approved.Pending || approved.Grant.APIKeyID == nil || *approved.Grant.APIKeyOwnerUserID != first ||
		!slices.Equal(approved.Grant.APIKeyScopes, []string{auth.ScopeAdminMonitorRead}) || *approved.Grant.ApprovedByUserID != first {
		t.Fatalf("after approval: %+v", approved.Grant)
	}
	if !slices.Equal(approved.Required.UnsupportedScopes, []string{"admin:not-a-scope"}) {
		t.Fatalf("unsupported scopes = %v", approved.Required.UnsupportedScopes)
	}
	issued := *approved.Grant.APIKeyID
	credential, err := f.permissions.HostAPICredential(ctx, installation.ID)
	if err != nil {
		t.Fatalf("credential after approval: %v", err)
	}
	key, err := f.apiKeys.GetByKey(ctx, credential.Token)
	if err != nil || key.ID != issued || key.UserID != first || !slices.Equal(credential.Scopes, []string{auth.ScopeAdminMonitorRead}) {
		t.Fatalf("credential = %+v (key %+v, %v), want the issued key acting as the approving admin", credential, key, err)
	}
	if f.changes == 0 {
		t.Fatalf("an approval did not announce a lifecycle change")
	}

	// The approval's check sees exactly what a read showed while nothing
	// changed in between: the API's validator is a digest of it.
	seen := f.read(t, installation.ID)
	again, err := f.permissions.Approve(ctx, second, installation.ID, func(current *plugins.PluginPermissions) error {
		if !reflect.DeepEqual(current, seen) {
			return fmt.Errorf("the approval saw %+v, the read %+v", current, seen)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if *again.Grant.APIKeyID != issued || !f.keyExists(t, issued) {
		t.Fatalf("a repeat approval replaced the key: %+v", again.Grant)
	}

	f.replace(t, installation.ID, []string{auth.ScopeAdminMonitorRead, auth.ScopeAdminSessionsControl}, subscriber("session.started"))
	if got := f.read(t, installation.ID); !got.Pending || *got.Grant.APIKeyID != issued {
		t.Fatalf("an upgrade asking for another scope: pending=%v grant=%+v", got.Pending, got.Grant)
	}
	if still, err := f.permissions.HostAPICredential(ctx, installation.ID); err != nil || still.Token != credential.Token {
		t.Fatalf("the plugin lost its approved key while the upgrade is pending: %v", err)
	}
	rotated := f.approve(t, second, installation.ID)
	if rotated.Pending || *rotated.Grant.APIKeyID == issued || *rotated.Grant.APIKeyOwnerUserID != second || f.keyExists(t, issued) {
		t.Fatalf("rotation: grant=%+v, old key exists=%v", rotated.Grant, f.keyExists(t, issued))
	}
}

func (f *permissionsFixture) updateBetweenReadAndApprovalIsRefused(t *testing.T) {
	ctx := t.Context()
	approver := f.admin(t, "stale")
	installation := f.install(t, "stale", []string{auth.ScopeAdminMonitorRead}, subscriber("session.started"))
	seen := f.read(t, installation.ID)
	// An automatic update lands after the administrator read the panel.
	f.replace(t, installation.ID, []string{auth.ScopeAdminUsers}, subscriber("session.started", "notification.created"))

	errStale := errors.New("not what the administrator read")
	var checked *plugins.PluginPermissions
	_, err := f.permissions.Approve(ctx, approver, installation.ID, func(current *plugins.PluginPermissions) error {
		checked = current
		if !slices.Equal(current.Required.RequiredScopes, seen.Required.RequiredScopes) ||
			!slices.Equal(current.Required.EventAccess, seen.Required.EventAccess) {
			return errStale
		}
		return nil
	})
	if !errors.Is(err, errStale) {
		t.Fatalf("approving after an update: %v, want the check's error", err)
	}
	if checked == nil || !slices.Equal(checked.Required.RequiredScopes, []string{auth.ScopeAdminUsers}) {
		t.Fatalf("the check saw %+v, want the updated requirements", checked)
	}
	got := f.read(t, installation.ID)
	if !got.Pending || got.Grant.APIKeyID != nil || got.Grant.ApprovedAt != nil || len(f.pluginKeys(t, installation)) != 0 {
		t.Fatalf("a refused approval changed the grant: %+v, keys %v", got.Grant, f.pluginKeys(t, installation))
	}
	if !slices.Equal(got.Grant.ApprovedEventAccess, []string{"admin"}) {
		t.Fatalf("a refused approval widened event access: %v", got.Grant.ApprovedEventAccess)
	}
}

func (f *permissionsFixture) demotedOwnersKeyIsPendingAndReplaced(t *testing.T) {
	ctx := t.Context()
	first, second := f.admin(t, "demoted"), f.admin(t, "successor")
	installation := f.install(t, "demoted", []string{auth.ScopeAdminMonitorRead})
	issued := *f.approve(t, first, installation.ID).Grant.APIKeyID

	f.exec(t, `UPDATE users SET role = 'user' WHERE id = $1`, first)
	got := f.read(t, installation.ID)
	if !got.Pending || got.Grant.HostAPIKeyUsable() || len(got.Grant.EffectiveHostAPIScopes()) != 0 || *got.Grant.APIKeyID != issued {
		t.Fatalf("after demoting the key's owner: pending=%v grant=%+v", got.Pending, got.Grant)
	}
	if _, err := f.permissions.HostAPICredential(ctx, installation.ID); !errors.Is(err, pluginhost.ErrHostAPINotGranted) {
		t.Fatalf("credential of a demoted owner's key: %v, want ErrHostAPINotGranted", err)
	}
	pending, err := f.permissions.PendingByInstallation(ctx)
	if err != nil || !pending[installation.ID] {
		t.Fatalf("PendingByInstallation = %v, %v; want pending", pending[installation.ID], err)
	}

	replaced := f.approve(t, second, installation.ID)
	if replaced.Pending || *replaced.Grant.APIKeyID == issued || *replaced.Grant.APIKeyOwnerUserID != second || f.keyExists(t, issued) {
		t.Fatalf("re-approval kept the demoted owner's key: grant=%+v, old key exists=%v", replaced.Grant, f.keyExists(t, issued))
	}

	// A disabled owner is no administrator either.
	f.exec(t, `UPDATE users SET enabled = false WHERE id = $1`, second)
	if got := f.read(t, installation.ID); !got.Pending || got.Grant.HostAPIKeyUsable() {
		t.Fatalf("after disabling the key's owner: pending=%v grant=%+v", got.Pending, got.Grant)
	}
}

func (f *permissionsFixture) failedRevokeLeavesOnlyThePreviousKey(t *testing.T) {
	ctx := t.Context()
	approver := f.admin(t, "crash")
	installation := f.install(t, "crash", []string{auth.ScopeAdminMonitorRead})
	issued := *f.approve(t, approver, installation.ID).Grant.APIKeyID
	failing := plugins.NewPermissionService(f.store, f.cipher, failingRevoke{auth.PluginHostAPIKeys{Keys: f.apiKeys}}, nil)

	// Rotation: the new key is minted and the grant points at it before the
	// old key's deletion fails.
	f.replace(t, installation.ID, []string{auth.ScopeAdminMonitorRead, auth.ScopeAdminSessionsControl})
	if _, err := failing.Approve(ctx, approver, installation.ID, nil); !errors.Is(err, errInjectedRevoke) {
		t.Fatalf("rotation with a failing revoke: %v, want the injected failure", err)
	}
	got := f.read(t, installation.ID)
	if keys := f.pluginKeys(t, installation); !slices.Equal(keys, []int64{issued}) || got.Grant.APIKeyID == nil || *got.Grant.APIKeyID != issued {
		t.Fatalf("after a failed rotation: keys %v, grant %+v; want only the previous key, still named by the grant", keys, got.Grant)
	}
	if credential, err := f.permissions.HostAPICredential(ctx, installation.ID); err != nil || !slices.Equal(credential.Scopes, []string{auth.ScopeAdminMonitorRead}) {
		t.Fatalf("the plugin lost its key to a failed rotation: %+v, %v", credential, err)
	}

	if err := f.store.Delete(ctx, installation.ID); err != nil {
		t.Fatal(err)
	}
	if keys := f.pluginKeys(t, installation); len(keys) != 0 {
		t.Fatalf("keys outlived the uninstall: %v", keys)
	}
}

func (f *permissionsFixture) revokedKeyIsPendingAndDroppedScopesRevoke(t *testing.T) {
	ctx := t.Context()
	approver := f.admin(t, "revoke")
	installation := f.install(t, "revoke", []string{auth.ScopeAdminMonitorRead}, subscriber("session.started"))
	issued := *f.approve(t, approver, installation.ID).Grant.APIKeyID

	// Revoked on the API keys page: the plugin is pending again.
	if err := f.apiKeys.DeleteByAdminConditional(ctx, issued, auth.APIKeyPrecondition{Any: true}); err != nil {
		t.Fatal(err)
	}
	if got := f.read(t, installation.ID); !got.Pending || got.Grant.APIKeyID != nil {
		t.Fatalf("after revoking the key: pending=%v grant=%+v", got.Pending, got.Grant)
	}
	if _, err := f.permissions.HostAPICredential(ctx, installation.ID); !errors.Is(err, pluginhost.ErrHostAPINotGranted) {
		t.Fatalf("credential after revocation: %v, want ErrHostAPINotGranted", err)
	}
	reissued := f.approve(t, approver, installation.ID)
	if reissued.Grant.APIKeyID == nil {
		t.Fatalf("approval after revocation issued no key: %+v", reissued.Grant)
	}

	// A replacement that asks for fewer scopes deletes the wider key at once,
	// without waiting for an approval; the plugin is pending for what it
	// still asks for.
	f.replace(t, installation.ID, []string{auth.ScopeAdminMonitorRead, auth.ScopeAdminSessionsControl}, subscriber("session.started"))
	wide := *f.approve(t, approver, installation.ID).Grant.APIKeyID
	f.replace(t, installation.ID, []string{auth.ScopeAdminMonitorRead}, subscriber("session.started"))
	narrowed := f.read(t, installation.ID)
	if !narrowed.Pending || narrowed.Grant.APIKeyID != nil || len(narrowed.Grant.ApprovedHostAPIScopes) != 0 || f.keyExists(t, wide) {
		t.Fatalf("after a narrowing replacement: pending=%v grant=%+v, wide key exists=%v", narrowed.Pending, narrowed.Grant, f.keyExists(t, wide))
	}
	if _, err := f.permissions.HostAPICredential(ctx, installation.ID); !errors.Is(err, pluginhost.ErrHostAPINotGranted) {
		t.Fatalf("credential after a narrowing replacement: %v, want ErrHostAPINotGranted", err)
	}
	// Dropping every scope leaves it keyless and not pending.
	kept := *f.approve(t, approver, installation.ID).Grant.APIKeyID
	f.replace(t, installation.ID, []string{}, subscriber("session.started"))
	dropped := f.read(t, installation.ID)
	if dropped.Pending || dropped.Grant.APIKeyID != nil || f.keyExists(t, kept) {
		t.Fatalf("a replacement asking for no scopes kept its key: pending=%v grant=%+v", dropped.Pending, dropped.Grant)
	}
}

func (f *permissionsFixture) storedKeyOpensOnlyForItsInstallation(t *testing.T) {
	ctx := t.Context()
	approver := f.admin(t, "transplant")
	owner := f.install(t, "owner", []string{auth.ScopeAdminMonitorRead})
	other := f.install(t, "other", []string{auth.ScopeAdminMonitorRead})
	f.approve(t, approver, owner.ID)
	if _, err := f.permissions.HostAPICredential(ctx, other.ID); !errors.Is(err, pluginhost.ErrHostAPINotGranted) {
		t.Fatalf("another installation's credential: %v, want ErrHostAPINotGranted", err)
	}
	// A database writer copying the grant's key onto another row gains
	// nothing: the ciphertext is bound to its installation.
	const copyKey = `UPDATE plugin_permission_grants o SET api_key_id = g.api_key_id, api_key_ciphertext = g.api_key_ciphertext
		FROM plugin_permission_grants g WHERE g.plugin_installation_id = $1 AND o.plugin_installation_id = $2`
	f.exec(t, copyKey, owner.ID, other.ID)
	if credential, err := f.permissions.HostAPICredential(ctx, other.ID); !errors.Is(err, pluginhost.ErrHostAPINotGranted) || credential.Token != "" {
		t.Fatalf("a transplanted key opened for another installation: %v", err)
	}
	f.exec(t, `UPDATE plugin_permission_grants SET api_key_id = NULL, api_key_ciphertext = NULL WHERE plugin_installation_id = $1`, other.ID)

	// A key that no longer opens, as after a SECRET_KEY change, shows as
	// pending and the next approval replaces it.
	unreadable := *f.approve(t, approver, other.ID).Grant.APIKeyID
	f.exec(t, `UPDATE plugin_permission_grants o SET api_key_ciphertext = g.api_key_ciphertext
		FROM plugin_permission_grants g WHERE g.plugin_installation_id = $1 AND o.plugin_installation_id = $2`, owner.ID, other.ID)
	if !f.read(t, other.ID).Pending {
		t.Fatalf("an installation whose key doesn't open isn't pending")
	}
	replaced := f.approve(t, approver, other.ID)
	if *replaced.Grant.APIKeyID == unreadable || f.keyExists(t, unreadable) || replaced.Pending {
		t.Fatalf("approval kept a key that doesn't open: %+v", replaced.Grant)
	}
	if _, err := f.permissions.HostAPICredential(ctx, other.ID); err != nil {
		t.Fatalf("credential after replacing the unreadable key: %v", err)
	}
}

func (f *permissionsFixture) uninstallDeletesKey(t *testing.T) {
	approver := f.admin(t, "uninstall")
	installation := f.install(t, "uninstall", []string{auth.ScopeAdminMonitorRead})
	keyID := *f.approve(t, approver, installation.ID).Grant.APIKeyID
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM activity_log WHERE target_type = $1 AND target_id = $2`,
			auditmutation.TargetAPIKey, strconv.FormatInt(keyID, 10))
	})
	lc := &auditmutation.LogContext{Request: httptest.NewRequest(http.MethodDelete,
		fmt.Sprintf("/api/v2/admin/plugins/installations/%d", installation.ID), nil), Started: time.Now(), UserID: &approver}
	var ctx context.Context
	capture := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { ctx = r.Context() }) //nolint:fatcontext // captures the one request context the middleware builds
	clientip.Middleware(clientip.NewResolver(nil))(capture).ServeHTTP(httptest.NewRecorder(), lc.Request.WithContext(auditmutation.SetLogContext(t.Context(), lc)))
	if err := f.permissions.DeleteInstallation(ctx, approver, installation.ID); err != nil {
		t.Fatal(err)
	}
	if f.keyExists(t, keyID) {
		t.Fatalf("the host API key outlived its plugin")
	}
	if !slices.ContainsFunc(lc.Committed, func(e auditmutation.Entry) bool {
		return e.Action == auditmutation.ActionAPIKeyDeleted && e.TargetID == strconv.FormatInt(keyID, 10)
	}) {
		t.Fatalf("uninstalling audited %+v, want the host API key's deletion", lc.Committed)
	}
	if _, err := f.permissions.Approve(t.Context(), approver, installation.ID, nil); !errors.Is(err, plugins.ErrInstallationNotFound) {
		t.Fatalf("approving an uninstalled plugin: %v, want ErrInstallationNotFound", err)
	}
}
