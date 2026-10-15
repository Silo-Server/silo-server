package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/secret"
)

type fakeNodeKeys struct {
	ids []int
	key string
	err error
}

func (f *fakeNodeKeys) IssueAdminNodeKey(_ context.Context, id int) (string, error) {
	f.ids = append(f.ids, id)
	return f.key, f.err
}

func TestAdminNodeKeyIssue(t *testing.T) {
	f := &fakeNodeKeys{key: "snk1_17_synthetic-node-key"}
	deps := pilotDeps(nil, nil)
	deps.AdminNodeKeys = f
	h := NewHandler(deps)
	deps.AdminNodeKeys = nil
	missing := NewHandler(deps)
	path := Prefix + "/admin/nodes/17/key"

	requireProblem(t, do(t, h, "POST", path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, "POST", path, "", bearer(memberToken)), TypePermissionDenied)
	if len(f.ids) != 0 {
		t.Fatal("unauthorized issue reached the service")
	}
	rec := do(t, h, "POST", path, "", bearer(adminToken))
	if rec.Code != http.StatusCreated || len(f.ids) != 1 || f.ids[0] != 17 {
		t.Fatal(rec.Code, rec.Body.String(), f.ids)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store for a credential", rec.Header().Get("Cache-Control"))
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["node_key"] != f.key || len(body) != 1 {
		t.Fatalf("body %s, %v", rec.Body.String(), err)
	}

	f.key = ""
	for _, tc := range []struct {
		err    error
		status int
	}{
		{nodepool.ErrNodeKeyNotIssuable, http.StatusConflict},
		{nodepool.ErrNodeNotFound, http.StatusNotFound},
		{handlers.ErrAdminNodesUnavailable, http.StatusServiceUnavailable},
		{errors.New("private-store"), http.StatusInternalServerError},
	} {
		f.err = tc.err
		rec = do(t, h, "POST", path, "", bearer(adminToken))
		if rec.Code != tc.status || strings.Contains(rec.Body.String(), "private-store") || strings.Contains(rec.Body.String(), "node_key") {
			t.Fatal(tc.err, rec.Code, rec.Body.String())
		}
	}
	before := len(f.ids)
	if rec = do(t, h, "POST", Prefix+"/admin/nodes/0/key", "", bearer(adminToken)); rec.Code != http.StatusUnprocessableEntity || len(f.ids) != before {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, missing, "POST", path, "", bearer(adminToken)), TypeDependencyUnavailable)
}

func TestAdminNodeKeyIssueThroughTheNodeStore(t *testing.T) {
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
	repo := nodepool.NewRepository(pool)
	cipher, err := secret.New([]byte("admin-node-key-test-master-key-0123456"))
	if err != nil {
		t.Fatal(err)
	}
	create := func(nodeType string) *nodepool.Node {
		t.Helper()
		suffix := fmt.Sprintf("%s-%d", nodeType, time.Now().UnixNano())
		node, err := repo.Create(ctx, nodepool.CreateNodeInput{Name: "admin-key-" + suffix, Type: nodeType, URL: "http://admin-key-" + suffix})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = repo.Delete(context.Background(), node.ID) })
		return node
	}
	transcode, proxy := create(nodepool.NodeTypeTranscode), create(nodepool.NodeTypeProxy)

	registry := nodepool.NewKeyRegistry(repo, cipher)
	if err := registry.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	keys := registry.Keyring(func() string { return "shared-secret" })
	nodes := handlers.NewNodeHandler(repo, nil, nil, nil, nil, nil, keys)
	nodes.SetNodeKeyIssuance(func(ctx context.Context, id int) (string, error) {
		return repo.IssueNodeKey(ctx, cipher, id)
	}, registry)
	deps := pilotDeps(nil, nil)
	deps.AdminNodeKeys = nodes
	deps.AdminNodesRead = nodes
	h := NewHandler(deps)

	keyIssued := func() bool {
		t.Helper()
		rec := do(t, h, "GET", Prefix+"/admin/nodes?limit=200", "", bearer(adminToken))
		var page struct {
			Items []struct {
				ID        ID   `json:"id"`
				KeyIssued bool `json:"key_issued"`
			} `json:"items"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || rec.Code != http.StatusOK {
			t.Fatal(rec.Code, rec.Body.String(), err)
		}
		for _, item := range page.Items {
			if item.ID == IDFromInt(int64(transcode.ID)) {
				return item.KeyIssued
			}
		}
		t.Fatal("test node missing from the first page of nodes")
		return false
	}
	if keyIssued() {
		t.Fatal("a new node reports key_issued")
	}
	if _, err := keys.KeyFor(nodeauth.TargetTranscode, transcode.URL); !errors.Is(err, nodeauth.ErrNoNodeKey) {
		t.Fatalf("keyless node signs: %v", err)
	}

	issue := func() string {
		t.Helper()
		rec := do(t, h, "POST", fmt.Sprintf("%s/admin/nodes/%d/key", Prefix, transcode.ID), "", bearer(adminToken))
		var body struct {
			NodeKey string `json:"node_key"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusCreated {
			t.Fatal(rec.Code, rec.Body.String(), err)
		}
		return body.NodeKey
	}
	key := issue()
	if id, err := nodepool.ParseNodeKey(key); err != nil || id != transcode.ID {
		t.Fatalf("issued key names node %d: %v", id, err)
	}
	if !keyIssued() {
		t.Fatal("key_issued did not flip after issuing")
	}
	// The handler reloaded this server's registry: requests to the node sign
	// with the new key at once, and the node accepts them with that key.
	signed := httptest.NewRequest(http.MethodGet, "/status", nil)
	if err := nodeauth.SignTranscode(signed, transcode.URL, keys); err != nil {
		t.Fatal(err)
	}
	if err := nodeauth.VerifySigned(signed, key); err != nil {
		t.Fatalf("a request signed for the node does not verify with its issued key: %v", err)
	}
	if got, err := registry.NodeKey(transcode.ID); err != nil || got != key {
		t.Fatalf("registry NodeKey = %v, want the issued key", err)
	}

	rotated := issue()
	if rotated == key {
		t.Fatal("a second issue did not rotate the key")
	}
	if got, _ := registry.NodeKey(transcode.ID); got != rotated {
		t.Fatal("the registry kept the old key after rotation")
	}

	requireProblem(t, do(t, h, "POST", fmt.Sprintf("%s/admin/nodes/%d/key", Prefix, proxy.ID), "", bearer(adminToken)), TypeConflict)
	requireProblem(t, do(t, h, "POST", Prefix+"/admin/nodes/2000000000/key", "", bearer(adminToken)), TypeNotFound)
}
