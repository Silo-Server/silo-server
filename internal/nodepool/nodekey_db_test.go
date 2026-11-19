package nodepool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func nodeKeyTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// nodeKeyTestURL is an address no other test row has.
func nodeKeyTestURL(label string) string {
	return "http://node-key-" + label + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func createNodeKeyTestNode(t *testing.T, store *AdminConfigurationStore, repo *Repository, nodeType, nodeURL string) *Node {
	t.Helper()
	node, err := store.Create(t.Context(), CreateNodeInput{Name: "node-key-test", Type: nodeType, URL: nodeURL})
	if err != nil {
		t.Fatalf("create %s node at %s: %v", nodeType, nodeURL, err)
	}
	t.Cleanup(func() { _ = repo.Delete(context.Background(), node.ID) })
	return node
}

// Issuing stores only ciphertext, flips key_issued, moves neither the admin
// revision nor the pool generation, refuses proxies and missing rows, and the
// registry loads the key whether or not the node is enabled.
func TestIssueNodeKeyDB(t *testing.T) {
	pool := nodeKeyTestPool(t)
	ctx := t.Context()
	repo, store, cipher := NewRepository(pool), NewAdminConfigurationStore(pool), testDataCipher(t)
	node := createNodeKeyTestNode(t, store, repo, NodeTypeTranscode, nodeKeyTestURL("transcode"))
	proxy := createNodeKeyTestNode(t, store, repo, NodeTypeProxy, nodeKeyTestURL("proxy"))
	if node.KeyIssued {
		t.Fatal("a new node reports a key")
	}
	off := false
	if _, _, err := store.Update(ctx, node.ID, UpdateNodeInput{Enabled: &off}, func(int64) error { return nil }); err != nil {
		t.Fatal(err)
	}
	versions := func() (revision, generation int64) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT admin_revision, (SELECT generation FROM stream_node_pool_generation WHERE singleton) FROM stream_nodes WHERE id = $1`, node.ID).Scan(&revision, &generation); err != nil {
			t.Fatal(err)
		}
		return revision, generation
	}
	revision, generation := versions()

	key, err := repo.IssueNodeKey(ctx, cipher, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if id, err := ParseNodeKey(key); err != nil || id != node.ID {
		t.Fatalf("issued key names node %d (%v), want %d", id, err, node.ID)
	}
	var stored string
	if err := pool.QueryRow(ctx, `SELECT node_key_ciphertext FROM stream_nodes WHERE id = $1`, node.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, key) {
		t.Fatal("the key is stored in plaintext")
	}
	if r, g := versions(); r != revision || g != generation {
		t.Fatalf("issuing a key moved admin_revision %d->%d or the pool generation %d->%d", revision, r, generation, g)
	}
	read, err := repo.GetByID(ctx, node.ID)
	if err != nil || !read.KeyIssued {
		t.Fatalf("GetByID after issue: key_issued=%v, %v", read != nil && read.KeyIssued, err)
	}
	if encoded, err := json.Marshal(read); err != nil || strings.Contains(string(encoded), key) || strings.Contains(string(encoded), stored) || strings.Contains(string(encoded), "key_issued") {
		t.Fatalf("a node row serializes key state: %s, %v", encoded, err)
	}

	registry := NewKeyRegistry(repo, cipher)
	if err := registry.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := registry.NodeKey(node.ID); err != nil || got != key {
		t.Fatalf("registry NodeKey for the disabled node = %v, want its key", err)
	}
	rotated, err := repo.IssueNodeKey(ctx, cipher, node.ID)
	if err != nil || rotated == key {
		t.Fatalf("rotation = %v; the key must change", err)
	}
	if err := registry.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := registry.NodeKey(node.ID); got != rotated {
		t.Fatal("the registry kept the old key after one refresh")
	}

	if _, err := repo.IssueNodeKey(ctx, cipher, proxy.ID); !errors.Is(err, ErrNodeKeyNotIssuable) {
		t.Fatalf("proxy issue = %v, want ErrNodeKeyNotIssuable", err)
	}
	if _, err := repo.IssueNodeKey(ctx, cipher, 2_000_000_000); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("missing node issue = %v, want ErrNodeNotFound", err)
	}
}

// Two transcode nodes cannot share an address under different spellings, on
// create or on a URL change. A proxy may, an identical create still resolves
// as a retry, and a row that already collides stays editable.
func TestTranscodeNodeURLsAreUniqueDB(t *testing.T) {
	pool := nodeKeyTestPool(t)
	ctx := t.Context()
	repo, store := NewRepository(pool), NewAdminConfigurationStore(pool)
	address := nodeKeyTestURL("dup")
	first := createNodeKeyTestNode(t, store, repo, NodeTypeTranscode, address)
	respelled := strings.Replace(address, "http://", "HTTP://", 1) + ":80/"

	if _, err := store.Create(ctx, CreateNodeInput{Name: "node-key-test", Type: NodeTypeTranscode, URL: respelled}); !errors.Is(err, ErrTranscodeNodeURLTaken) {
		t.Fatalf("second transcode node at %s = %v, want ErrTranscodeNodeURLTaken", respelled, err)
	}
	if retry, err := store.Create(ctx, CreateNodeInput{Name: "node-key-test", Type: NodeTypeTranscode, URL: address}); err != nil || retry.ID != first.ID {
		t.Fatalf("an identical create = %v, %v; want the existing node", retry, err)
	}
	createNodeKeyTestNode(t, store, repo, NodeTypeProxy, respelled)

	noGuard := func(int64) error { return nil }
	second := createNodeKeyTestNode(t, store, repo, NodeTypeTranscode, nodeKeyTestURL("other"))
	if _, _, err := store.Update(ctx, second.ID, UpdateNodeInput{URL: &respelled}, noGuard); !errors.Is(err, ErrTranscodeNodeURLTaken) {
		t.Fatalf("moving a transcode node onto another's address = %v, want ErrTranscodeNodeURLTaken", err)
	}

	// A collision written before this check existed.
	if _, err := pool.Exec(ctx, `UPDATE stream_nodes SET url = $2 WHERE id = $1`, second.ID, address+"/"); err != nil {
		t.Fatal(err)
	}
	same, renamed := address+"/", "renamed"
	if _, _, err := store.Update(ctx, second.ID, UpdateNodeInput{Name: &renamed, URL: &same}, noGuard); err != nil {
		t.Fatalf("editing a node that already shares its address = %v", err)
	}
}
