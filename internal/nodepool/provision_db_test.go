package nodepool

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/secret"
)

type provisionFixture struct {
	t      *testing.T
	pool   *pgxpool.Pool
	repo   *Repository
	store  *AdminConfigurationStore
	cipher *secret.Cipher
	// fresh is an id the identity sequence has not handed out.
	fresh int
}

func newProvisionFixture(t *testing.T) *provisionFixture {
	pool := nodeKeyTestPool(t)
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, watermark, err := identityWatermark(t.Context(), tx)
	_ = tx.Rollback(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	f := &provisionFixture{t: t, pool: pool, repo: NewRepository(pool), store: NewAdminConfigurationStore(pool), cipher: testDataCipher(t), fresh: watermark + 1}
	// Leave no row the deployment manages for the next test.
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `UPDATE stream_nodes SET deployment_managed = false WHERE deployment_managed`)
	})
	return f
}

func (f *provisionFixture) key(id int) string {
	key, err := GenerateNodeKey(id)
	if err != nil {
		f.t.Fatal(err)
	}
	return key
}

func (f *provisionFixture) provision(nodes ...ProvisionedNode) (ProvisionResult, error) {
	return ProvisionTranscodeNodes(f.t.Context(), f.pool, f.cipher, nodes)
}

func (f *provisionFixture) storedKey(id int) (ciphertext, key string) {
	if err := f.pool.QueryRow(f.t.Context(), `SELECT node_key_ciphertext FROM stream_nodes WHERE id = $1`, id).Scan(&ciphertext); err != nil {
		f.t.Fatal(err)
	}
	key, err := DecryptNodeKey(f.cipher, id, ciphertext)
	if err != nil {
		f.t.Fatal(err)
	}
	return ciphertext, key
}

// A declaration creates its rows with their keys and owns them: a repeat
// writes nothing, a changed key rotates, the admin API cannot issue a key or
// delete, nothing allocates a declared id, and an empty declaration hands the
// rows back.
func TestProvisionTranscodeNodesDB(t *testing.T) {
	f := newProvisionFixture(t)
	ctx := t.Context()
	id := f.fresh
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM stream_nodes WHERE id = $1`, id) })
	declared := ProvisionedNode{ID: id, Name: "provisioned", URL: nodeKeyTestURL("provisioned"), Key: f.key(id)}

	result, err := f.provision(declared)
	if err != nil || len(result.Created) != 1 || result.Created[0] != id {
		t.Fatalf("create: %+v, %v", result, err)
	}
	node, err := f.repo.GetByID(ctx, id)
	if err != nil || node.Type != NodeTypeTranscode || !node.Enabled || !node.KeyIssued || !node.DeploymentManaged ||
		node.Name != declared.Name || node.URL != declared.URL {
		t.Fatalf("created row: %+v, %v", node, err)
	}
	ciphertext, key := f.storedKey(id)
	if key != declared.Key {
		t.Fatal("the stored key is not the declared one")
	}
	if admin := createNodeKeyTestNode(t, f.store, f.repo, NodeTypeTranscode, nodeKeyTestURL("after")); admin.ID <= id {
		t.Fatalf("an admin create took id %d, at or below the declared %d", admin.ID, id)
	}

	if result, err = f.provision(declared); err != nil || result.Changed() {
		t.Fatalf("repeat: %+v, %v", result, err)
	}
	if again, _ := f.storedKey(id); again != ciphertext {
		t.Fatal("a repeat rewrote the ciphertext")
	}
	if _, err = f.repo.IssueNodeKey(ctx, f.cipher, id); !errors.Is(err, ErrNodeDeploymentManaged) {
		t.Fatalf("issue on a managed node: %v", err)
	}
	if err = f.store.Delete(ctx, id, func(int64) error { return nil }); !errors.Is(err, ErrNodeDeploymentManaged) {
		t.Fatalf("delete of a managed node: %v", err)
	}

	rotated := declared
	rotated.Key = f.key(id)
	if result, err = f.provision(rotated); err != nil || len(result.Rotated) != 1 {
		t.Fatalf("rotate: %+v, %v", result, err)
	}
	if _, key = f.storedKey(id); key != rotated.Key {
		t.Fatal("rotation kept the old key")
	}

	if result, err = f.provision(); err != nil || len(result.Released) != 1 {
		t.Fatalf("release: %+v, %v", result, err)
	}
	if err = f.store.Delete(ctx, id, func(int64) error { return nil }); err != nil {
		t.Fatalf("delete after release: %v", err)
	}
}

// A declaration never takes a proxy row, a keyless row, a row with another
// key, a deleted node's id or a proxy's address; it adopts a row that already
// holds the declared key. A refused declaration writes nothing.
func TestProvisionTranscodeNodesRefusesOtherRowsDB(t *testing.T) {
	f := newProvisionFixture(t)
	ctx := t.Context()
	refused := func(label string, node ProvisionedNode, want string) {
		t.Helper()
		_, err := f.provision(node)
		var provErr *ProvisionError
		if !errors.As(err, &provErr) || provErr.ID != node.ID || !strings.Contains(provErr.Detail, want) {
			t.Fatalf("%s: want a refusal mentioning %q, got %v", label, want, err)
		}
	}
	declare := func(id int) ProvisionedNode {
		return ProvisionedNode{ID: id, Name: "declared", URL: nodeKeyTestURL("declared"), Key: f.key(id)}
	}

	proxy := createNodeKeyTestNode(t, f.store, f.repo, NodeTypeProxy, nodeKeyTestURL("proxy"))
	refused("proxy row", declare(proxy.ID), "proxy")
	admin := createNodeKeyTestNode(t, f.store, f.repo, NodeTypeTranscode, nodeKeyTestURL("admin"))
	refused("keyless row", declare(admin.ID), "no key")
	issued, err := f.repo.IssueNodeKey(ctx, f.cipher, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	refused("admin key", declare(admin.ID), "different key")
	gone := createNodeKeyTestNode(t, f.store, f.repo, NodeTypeTranscode, nodeKeyTestURL("gone"))
	if err = f.repo.Delete(ctx, gone.ID); err != nil {
		t.Fatal(err)
	}
	refused("reused id", declare(gone.ID), "named a node before")
	unused := gone.ID + 1000
	spelled := declare(unused)
	spelled.URL = strings.Replace(proxy.URL, "http://", "HTTP://", 1) + ":80/"
	refused("proxy address", spelled, "URL")
	if _, err = f.repo.GetByID(ctx, unused); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("a refused declaration created node %d: %v", unused, err)
	}

	adopted := declare(admin.ID)
	adopted.Key = issued
	if result, err := f.provision(adopted); err != nil || len(result.Adopted) != 1 || result.Adopted[0] != admin.ID {
		t.Fatalf("adopt: %+v, %v", result, err)
	}
	if _, err = f.repo.IssueNodeKey(ctx, f.cipher, admin.ID); !errors.Is(err, ErrNodeDeploymentManaged) {
		t.Fatalf("issue on an adopted node: %v", err)
	}
}
