package nodepool

import (
	"errors"
	"testing"
)

// source_access is stored configuration: a transcode node takes local or
// core, a change moves the admin revision and the pool generation, and a
// proxy can only hold local, which the store refuses before the database's
// check constraint would.
func TestSourceAccessIsTranscodeConfigurationDB(t *testing.T) {
	pool := nodeKeyTestPool(t)
	ctx := t.Context()
	repo, store := NewRepository(pool), NewAdminConfigurationStore(pool)
	accept := func(int64) error { return nil }

	node := createNodeKeyTestNode(t, store, repo, NodeTypeTranscode, nodeKeyTestURL("source-access"))
	if node.SourceAccess != SourceAccessLocal {
		t.Fatalf("new transcode node source_access = %q, want %q", node.SourceAccess, SourceAccessLocal)
	}
	versions := func() (revision, generation int64) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT admin_revision, (SELECT generation FROM stream_node_pool_generation WHERE singleton) FROM stream_nodes WHERE id = $1`, node.ID).Scan(&revision, &generation); err != nil {
			t.Fatal(err)
		}
		return revision, generation
	}
	revision, generation := versions()
	core := "CORE"
	updated, previous, err := store.Update(ctx, node.ID, UpdateNodeInput{SourceAccess: &core}, accept)
	if err != nil {
		t.Fatal(err)
	}
	if updated.SourceAccess != SourceAccessCore || previous.SourceAccess != SourceAccessLocal {
		t.Fatalf("update: now %q, before %q", updated.SourceAccess, previous.SourceAccess)
	}
	if r, g := versions(); r == revision || g == generation {
		t.Fatalf("source_access change moved revision %v and generation %v, want both", r != revision, g != generation)
	}
	revision, generation = versions()
	if _, _, err := store.Update(ctx, node.ID, UpdateNodeInput{SourceAccess: &core}, accept); err != nil {
		t.Fatal(err)
	}
	if r, g := versions(); r != revision || g != generation {
		t.Fatal("a no-change update moved the revision or the generation")
	}
	bogus := "nfs"
	if _, _, err := store.Update(ctx, node.ID, UpdateNodeInput{SourceAccess: &bogus}, accept); !errors.Is(err, ErrInvalidNodeInput) {
		t.Fatalf("unknown mode: %v, want ErrInvalidNodeInput", err)
	}

	created, err := store.Create(ctx, CreateNodeInput{Name: "source-access-core", Type: NodeTypeTranscode, URL: nodeKeyTestURL("source-access-core"), SourceAccess: SourceAccessCore})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, created.ID) })
	if created.SourceAccess != SourceAccessCore {
		t.Fatalf("created with core, stored %q", created.SourceAccess)
	}

	proxy := createNodeKeyTestNode(t, store, repo, NodeTypeProxy, nodeKeyTestURL("source-access-proxy"))
	if _, _, err := store.Update(ctx, proxy.ID, UpdateNodeInput{SourceAccess: &core}, accept); !errors.Is(err, ErrInvalidNodeInput) {
		t.Fatalf("proxy set to core: %v, want ErrInvalidNodeInput", err)
	}
	local := SourceAccessLocal
	if _, _, err := store.Update(ctx, proxy.ID, UpdateNodeInput{SourceAccess: &local}, accept); err != nil {
		t.Fatalf("proxy set to local: %v", err)
	}
	if _, err := store.Create(ctx, CreateNodeInput{Name: "proxy-core", Type: NodeTypeProxy, URL: nodeKeyTestURL("proxy-core"), SourceAccess: SourceAccessCore}); !errors.Is(err, ErrInvalidNodeInput) {
		t.Fatalf("proxy created with core: %v, want ErrInvalidNodeInput", err)
	}
	// The constraint holds without the store's check.
	if _, err := pool.Exec(ctx, `UPDATE stream_nodes SET source_access = 'core' WHERE id = $1`, proxy.ID); err == nil {
		t.Fatal("the database let a proxy read sources through the core")
	}
}
