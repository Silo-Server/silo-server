package downloadprepare

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/secret"
)

// The preparer signs against a re-encoded node URL ("/%77orker" becomes
// "/worker"). With the real credential registry that must still find the
// transcode node registered under the escaped spelling, and sign with its own
// key, even when a proxy row owns the decoded spelling.
func TestHTTPPreparerSignsAnEscapedNodeURLWithTheTranscodeNodesKey(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var columns int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'stream_nodes' AND column_name = 'node_key_ciphertext'`).Scan(&columns); err != nil || columns != 1 {
		t.Skip("test database has not applied the node key migration")
	}
	cipher, err := secret.New([]byte("download-prepare-registry-test-master-key"))
	if err != nil {
		t.Fatal(err)
	}

	var nodeKey string
	server := httptest.NewServer(http.StripPrefix("/worker", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if nodeauth.VerifySigned(r, nodeKey) != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Length", "42")
	})))
	defer server.Close()

	repo := nodepool.NewRepository(pool)
	create := func(name, nodeType, nodeURL string) *nodepool.Node {
		t.Helper()
		node, err := repo.Create(ctx, nodepool.CreateNodeInput{Name: name + "-" + server.URL, Type: nodeType, URL: nodeURL})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = repo.Delete(context.Background(), node.ID) })
		return node
	}
	transcode := create("escaped-transcode", nodepool.NodeTypeTranscode, server.URL+"/%77orker")
	create("decoded-proxy", nodepool.NodeTypeProxy, server.URL+"/worker")
	if nodeKey, err = repo.IssueNodeKey(ctx, cipher, transcode.ID); err != nil {
		t.Fatal(err)
	}
	registry := nodepool.NewKeyRegistry(repo, cipher)
	if err := registry.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	keys := registry.Keyring(func() string { return "shared-secret" })

	result, err := HTTPPreparer{}.Stat(ctx, transcode.URL, keys, "artifact-1")
	if err != nil || result.FileSize != 42 {
		t.Fatalf("Stat = (%+v, %v), want a request signed with the transcode node's own key", result, err)
	}
}
