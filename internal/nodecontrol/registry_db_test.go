package nodecontrol

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/secret"
)

// RequireNode over the real credential registry, as the API router mounts it:
// a disabled transcode node keeps its access, and a proxy row, a transcode
// node without a key and a deleted node are refused.
func TestRequireNodeWithTheCredentialRegistryDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ctx := t.Context()
	repo := nodepool.NewRepository(pool)
	cipher, err := secret.New([]byte("node-control-test-master-key-0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	create := func(label, nodeType string) *nodepool.Node {
		t.Helper()
		node, err := repo.Create(ctx, nodepool.CreateNodeInput{Name: "control-" + label, Type: nodeType, URL: "http://control-" + label + "-" + suffix})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = repo.Delete(context.Background(), node.ID) })
		return node
	}
	issue := func(node *nodepool.Node) string {
		t.Helper()
		key, err := repo.IssueNodeKey(ctx, cipher, node.ID)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	disabled := create("disabled", nodepool.NodeTypeTranscode)
	disabledKey := issue(disabled)
	off := false
	if _, err := repo.Update(ctx, disabled.ID, nodepool.UpdateNodeInput{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	proxy := create("proxy", nodepool.NodeTypeProxy)
	keyless := create("keyless", nodepool.NodeTypeTranscode)
	deleted := create("deleted", nodepool.NodeTypeTranscode)
	deletedKey := issue(deleted)

	registry := nodepool.NewKeyRegistry(repo, cipher)
	if err := registry.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, deleted.ID); err != nil {
		t.Fatal(err)
	}
	if err := registry.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	handler := RequireNode(registry)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := NodeID(r.Context()); !ok {
			t.Error("no node id in the context")
		}
	}))
	serve := func(id int, key string) int {
		r := httptest.NewRequest(http.MethodGet, PathConfig, nil)
		nodeauthtest.SignFromNode(t, r, id, key)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	if got := serve(disabled.ID, disabledKey); got != http.StatusOK {
		t.Fatalf("disabled transcode node: %d, want 200", got)
	}
	for name, got := range map[string]int{
		"proxy row":      serve(proxy.ID, disabledKey),
		"keyless node":   serve(keyless.ID, disabledKey),
		"deleted node":   serve(deleted.ID, deletedKey),
		"wrong node key": serve(disabled.ID, deletedKey),
	} {
		if got != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", name, got)
		}
	}
}
