package nodepool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
)

func createKeyTestNode(t *testing.T, repo *Repository, nodeType string) *Node {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%s-%d", nodeType, time.Now().UnixNano())
	node, err := repo.Create(ctx, CreateNodeInput{Name: "node-key-" + suffix, Type: nodeType, URL: "http://node-key-" + suffix})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(context.Background(), node.ID) })
	return node
}

// Issuing stores only ciphertext, flips key_issued, leaves the admin revision
// alone, and refuses proxies and missing rows.
func TestRepositoryIssuesTranscodeNodeKeys(t *testing.T) {
	pool := newNodeTestPool(t)
	ctx := context.Background()
	repo := NewRepository(pool)
	cipher := testDataCipher(t)
	node := createKeyTestNode(t, repo, NodeTypeTranscode)
	if node.KeyIssued {
		t.Fatal("a new node reports a key")
	}
	revision := func() int64 {
		t.Helper()
		var r int64
		if err := pool.QueryRow(ctx, `SELECT admin_revision FROM stream_nodes WHERE id = $1`, node.ID).Scan(&r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	before := revision()

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
	if stored == key || strings.Contains(stored, key) {
		t.Fatal("the key is stored in plaintext")
	}
	if got, err := DecryptNodeKey(cipher, node.ID, stored); err != nil || got != key {
		t.Fatalf("stored ciphertext opens to %q, %v", got, err)
	}
	if after := revision(); after != before {
		t.Fatalf("admin_revision moved from %d to %d on a key issue", before, after)
	}

	read, err := repo.GetByID(ctx, node.ID)
	if err != nil || !read.KeyIssued {
		t.Fatalf("GetByID after issue: key_issued=%v, %v", read != nil && read.KeyIssued, err)
	}
	encoded, err := json.Marshal(read)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), key) || strings.Contains(string(encoded), stored) || strings.Contains(string(encoded), "node_key") || strings.Contains(string(encoded), "key_issued") {
		t.Fatalf("a node row serializes key material: %s", encoded)
	}

	rotated, err := repo.IssueNodeKey(ctx, cipher, node.ID)
	if err != nil || rotated == key {
		t.Fatalf("rotation = %v; the key must change", err)
	}

	proxy := createKeyTestNode(t, repo, NodeTypeProxy)
	if _, err := repo.IssueNodeKey(ctx, cipher, proxy.ID); !errors.Is(err, ErrNodeKeyNotIssuable) {
		t.Fatalf("proxy issue = %v, want ErrNodeKeyNotIssuable", err)
	}
	if _, err := repo.IssueNodeKey(ctx, cipher, 2_000_000_000); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("missing node issue = %v, want ErrNodeNotFound", err)
	}
}

// The registry holds disabled nodes (they keep serving their streams), drops
// deleted ones within one refresh, adopts rotations, and marks keyless ones.
func TestKeyRegistryLoadsEveryTranscodeNodeFromTheDatabase(t *testing.T) {
	pool := newNodeTestPool(t)
	ctx := context.Background()
	repo := NewRepository(pool)
	cipher := testDataCipher(t)
	enabled := createKeyTestNode(t, repo, NodeTypeTranscode)
	disabled := createKeyTestNode(t, repo, NodeTypeTranscode)
	keyless := createKeyTestNode(t, repo, NodeTypeTranscode)
	proxy := createKeyTestNode(t, repo, NodeTypeProxy)
	enabledKey, err := repo.IssueNodeKey(ctx, cipher, enabled.ID)
	if err != nil {
		t.Fatal(err)
	}
	disabledKey, err := repo.IssueNodeKey(ctx, cipher, disabled.ID)
	if err != nil {
		t.Fatal(err)
	}
	off := false
	if _, err := repo.Update(ctx, disabled.ID, UpdateNodeInput{Enabled: &off}); err != nil {
		t.Fatal(err)
	}

	registry := NewKeyRegistry(repo, cipher)
	if err := registry.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	keys := registry.Keyring(func() string { return "shared-secret" })
	for _, tc := range []struct {
		node *Node
		want string
		err  error
	}{
		{enabled, enabledKey, nil},
		{disabled, disabledKey, nil},
		{keyless, "", nodeauth.ErrNoNodeKey},
		{proxy, "shared-secret", nil},
	} {
		if got, err := keys.KeyFor(tc.node.SignTarget(), tc.node.URL); got != tc.want || !errors.Is(err, tc.err) {
			t.Errorf("KeyFor(%s) = %q, %v; want %q, %v", tc.node.Name, got, err, tc.want, tc.err)
		}
	}
	if got, err := registry.NodeKey(disabled.ID); err != nil || got != disabledKey {
		t.Fatalf("disabled NodeKey = %q, %v; want its key", got, err)
	}

	rotated, err := repo.IssueNodeKey(ctx, cipher, enabled.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, disabled.ID); err != nil {
		t.Fatal(err)
	}
	if err := registry.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := registry.NodeKey(enabled.ID); got != rotated {
		t.Fatal("rotation not adopted within one refresh")
	}
	if _, err := registry.NodeKey(disabled.ID); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("deleted NodeKey = %v, want ErrNodeNotFound", err)
	}
	if _, err := keys.KeyFor(nodeauth.TargetTranscode, disabled.URL); !errors.Is(err, nodeauth.ErrUnknownNode) {
		t.Fatalf("deleted KeyFor = %v, want ErrUnknownNode", err)
	}
}
