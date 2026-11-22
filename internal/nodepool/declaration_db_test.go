package nodepool

import (
	"errors"
	"slices"
	"testing"
)

// A declaration writes its fields and replaces env_managed with exactly
// their names; recording ownership alone moves neither the admin revision nor
// the pool generation; an administrator update that changes a declared field
// is refused while one that restates it succeeds; a declared URL another
// transcode node has is refused; and a proxy with no row registers itself.
func TestNodeDeclarationsLockFieldsDB(t *testing.T) {
	pool := nodeKeyTestPool(t)
	ctx := t.Context()
	repo, store := NewRepository(pool), NewAdminConfigurationStore(pool)
	accept := func(int64) error { return nil }
	node := createNodeKeyTestNode(t, store, repo, NodeTypeTranscode, nodeKeyTestURL("declare"))
	versions := func() (revision, generation int64) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT admin_revision, (SELECT generation FROM stream_node_pool_generation WHERE singleton) FROM stream_nodes WHERE id = $1`, node.ID).Scan(&revision, &generation); err != nil {
			t.Fatal(err)
		}
		return revision, generation
	}

	group, jobs, accel := "rack-a", 0, "videotoolbox"
	declared, _, err := store.Declare(ctx, node.ID, UpdateNodeInput{Group: &group, MaxJobs: &jobs, HWAccelOverride: &accel})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(declared.EnvManaged, []string{FieldGroup, FieldMaxJobs, FieldHWAccelOverride}) ||
		declared.Group == nil || *declared.Group != group || declared.MaxJobs != nil || *declared.HWAccelOverride != accel {
		t.Fatalf("declared row: env_managed %v, group %v, max_jobs %v", declared.EnvManaged, declared.Group, declared.MaxJobs)
	}

	// Restating a declared value, in the spelling a full-form save sends,
	// goes through; changing one is refused and changes nothing.
	restated, zero := "rack-a", 0
	if _, _, err := store.Update(ctx, node.ID, UpdateNodeInput{Group: &restated, MaxJobs: &zero, HWAccelOverride: &accel}, accept); err != nil {
		t.Fatalf("restating declared values: %v", err)
	}
	other, name := "rack-b", "renamed"
	_, _, err = store.Update(ctx, node.ID, UpdateNodeInput{Group: &other, Name: &name}, accept)
	if locked, ok := errors.AsType[*EnvManagedFieldsError](err); !ok || !slices.Equal(locked.Fields, []string{FieldGroup}) {
		t.Fatalf("changing a declared field: %v, want it refused for group", err)
	}
	if row, _ := repo.GetByID(ctx, node.ID); row.Name == name || *row.Group != group {
		t.Fatal("a refused update left part of its change behind")
	}

	// Dropping a variable unlocks its field, and changing ownership alone
	// is not a configuration change.
	revision, generation := versions()
	undeclared, _, err := store.Declare(ctx, node.ID, UpdateNodeInput{Group: &group, HWAccelOverride: &accel})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(undeclared.EnvManaged, []string{FieldGroup, FieldHWAccelOverride}) {
		t.Fatalf("env_managed %v after max_jobs was dropped", undeclared.EnvManaged)
	}
	if r, g := versions(); r != revision || g != generation {
		t.Fatal("recording which fields a node declares moved the revision or the generation")
	}
	// The running node's periodic re-send of an unchanged declaration is
	// cheap: no revision, no pool generation, the same locks.
	again, _, err := store.Declare(ctx, node.ID, UpdateNodeInput{Group: &group, HWAccelOverride: &accel})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(again.EnvManaged, undeclared.EnvManaged) {
		t.Fatalf("re-sent declaration: env_managed %v", again.EnvManaged)
	}
	if r, g := versions(); r != revision || g != generation {
		t.Fatal("an unchanged declaration moved the revision or the generation")
	}
	limit := 4
	if _, _, err := store.Update(ctx, node.ID, UpdateNodeInput{MaxJobs: &limit}, accept); err != nil {
		t.Fatalf("an undeclared field stays editable: %v", err)
	}

	taken := createNodeKeyTestNode(t, store, repo, NodeTypeTranscode, nodeKeyTestURL("declare-taken"))
	spelled := taken.URL + ":80/"
	if _, _, err := store.Declare(ctx, node.ID, UpdateNodeInput{URL: &spelled}); !errors.Is(err, ErrTranscodeNodeURLTaken) {
		t.Fatalf("declaring another node's URL: %v, want ErrTranscodeNodeURLTaken", err)
	}
}

// A proxy adds its own row only when asked to, is found again by its URL, and
// keeps that address: a transcode node's key cannot declare it under another
// spelling.
func TestProxyDeclarationsDB(t *testing.T) {
	pool := nodeKeyTestPool(t)
	ctx := t.Context()
	repo, store := NewRepository(pool), NewAdminConfigurationStore(pool)
	node := createNodeKeyTestNode(t, store, repo, NodeTypeTranscode, nodeKeyTestURL("declare-beside-proxy"))

	proxyURL := nodeKeyTestURL("declare-proxy")
	public := proxyURL + "-public"
	if _, _, err := store.DeclareProxy(ctx, ProxyIdentity{URL: proxyURL}, UpdateNodeInput{PublicURL: &public}, false); !errors.Is(err, ErrProxyNotRegistered) {
		t.Fatalf("unregistered proxy without registration: %v, want ErrProxyNotRegistered", err)
	}
	proxy, created, err := store.DeclareProxy(ctx, ProxyIdentity{URL: proxyURL}, UpdateNodeInput{PublicURL: &public}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, proxy.ID) })
	if !created || proxy.Type != NodeTypeProxy || !proxy.Enabled || proxy.URL != proxyURL || !slices.Equal(proxy.EnvManaged, []string{FieldPublicURL}) {
		t.Fatalf("self-registered proxy: created %v, %+v", created, proxy)
	}
	// Nor does a proxy add itself at an address a node has under another
	// spelling.
	if _, _, err := store.DeclareProxy(ctx, ProxyIdentity{URL: node.URL + ":80"}, UpdateNodeInput{}, true); !errors.Is(err, ErrNodeIdentityConflict) {
		t.Fatalf("registering at a transcode node's URL: %v, want ErrNodeIdentityConflict", err)
	}
	// A node's key must not take a proxy's address under another spelling.
	shadow := proxyURL + "/"
	if _, _, err := store.Declare(ctx, node.ID, UpdateNodeInput{URL: &shadow}); !errors.Is(err, ErrTranscodeNodeURLTaken) {
		t.Fatalf("declaring a proxy's URL: %v, want ErrTranscodeNodeURLTaken", err)
	}
	again, created, err := store.DeclareProxy(ctx, ProxyIdentity{URL: proxyURL + "/"}, UpdateNodeInput{}, false)
	if err != nil || created || again.ID != proxy.ID || len(again.EnvManaged) != 0 {
		t.Fatalf("restarted proxy: created %v, id %d (want %d), env_managed %v, err %v", created, again.ID, proxy.ID, again.EnvManaged, err)
	}
}
