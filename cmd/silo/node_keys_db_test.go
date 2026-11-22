package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/nodecontrol"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/secret"
)

// registryTestCluster is one admin-writing API server (A), a proxy that
// follows node_pool_changed, and an API server (B) that shares no event bus
// with A, so only its reconciliation pass can tell it about A's writes.
type registryTestCluster struct {
	serverA                     *handlers.NodeHandler
	registryA, proxy, registryB *nodepool.KeyRegistry
}

func newRegistryTestCluster(t *testing.T, pool *pgxpool.Pool, cipher *secret.Cipher) registryTestCluster {
	t.Helper()
	ctx := t.Context()
	repo := nodepool.NewRepository(pool)
	store := nodepool.NewAdminConfigurationStore(pool)
	bus := newMemoryEventBus()
	newRegistry := func() *nodepool.KeyRegistry {
		registry := nodepool.NewKeyRegistry(repo, cipher)
		if err := registry.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		registry.Start(ctx)
		return registry
	}
	c := registryTestCluster{registryA: newRegistry(), registryB: newRegistry()}
	c.serverA = handlers.NewNodeHandler(repo, nil, nil, nil, bus, nil, nil)
	c.serverA.SetConfigurationStore(store)
	c.serverA.SetNodeKeyRegistry(c.registryA)
	c.serverA.SetNodeKeyIssuance(func(ctx context.Context, id int) (string, error) {
		return repo.IssueNodeKey(ctx, cipher, id)
	})
	c.proxy = startProxyNodeKeyRegistry(ctx, pool, cipher, bus)
	serverB := handlers.NewNodeHandler(repo, nil, nil, nil, nil, nil, nil)
	serverB.SetConfigurationStore(store)
	serverB.SetNodeKeyRegistry(c.registryB)
	serverB.StartConfigurationReconciliation(ctx)
	return c
}

// awaitRegistry waits until ready holds: within one reconciliation period (5
// seconds) and well inside the 60-second periodic reload that would otherwise
// hide a missing trigger.
func awaitRegistry(t *testing.T, what string, ready func() bool) {
	t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for !ready() {
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", what)
		case <-tick.C:
		}
	}
}

func registryURL(registry *nodepool.KeyRegistry, id int) string {
	identity, _ := registry.Identity(id)
	return identity.URL
}

func registryHolds(registry *nodepool.KeyRegistry, id int) bool {
	_, ok := registry.Identity(id)
	return ok
}

// Admin node writes reach every credential registry: the registry of the API
// server that took the write before it answers, a proxy's through
// node_pool_changed, and another API server's through its reconciliation
// pass. A deleted node's key stops working on the writing server at once; a
// URL change keeps the key, signable at the new URL at once; a disabled node
// keeps its key.
func TestNodeWritesReachEveryCredentialRegistryDB(t *testing.T) {
	pool := multinodeTestPool(t)
	ctx := t.Context()
	cipher, err := secret.New([]byte("node-registry-test-master-key-0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	c := newRegistryTestCluster(t, pool, cipher)
	anyRevision := func(int64) error { return nil }
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)

	node, err := c.serverA.CreateAdminNode(ctx, nodepool.CreateNodeInput{
		Name: "registry-" + suffix, Type: nodepool.NodeTypeTranscode, URL: "http://registry-a-" + suffix + ".invalid",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = nodepool.NewRepository(pool).Delete(context.Background(), node.ID) })
	if !registryHolds(c.registryA, node.ID) {
		t.Fatal("server A's registry does not hold the node it just created")
	}
	awaitRegistry(t, "the proxy to load the new node", func() bool { return registryHolds(c.proxy, node.ID) })
	awaitRegistry(t, "server B to load the new node", func() bool { return registryHolds(c.registryB, node.ID) })

	key, err := c.serverA.IssueAdminNodeKey(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !c.registryA.HasKey(node.ID) {
		t.Fatal("server A's registry does not hold the key it just issued")
	}
	awaitRegistry(t, "the proxy to load the key", func() bool { return c.proxy.HasKey(node.ID) })

	movedURL := "http://registry-b-" + suffix + ".invalid"
	if _, err := c.serverA.UpdateAdminNode(ctx, node.ID, nodepool.UpdateNodeInput{URL: &movedURL}, anyRevision); err != nil {
		t.Fatal(err)
	}
	if got := registryURL(c.registryA, node.ID); got != movedURL {
		t.Fatalf("server A's registry has the node at %q, want %q", got, movedURL)
	}
	if got, err := c.registryA.Keyring(nil).KeyFor(nodeauth.TargetTranscode, movedURL); err != nil || got != key {
		t.Fatalf("signing at the new URL: key matches=%v, err=%v", got == key, err)
	}
	awaitRegistry(t, "the proxy to move the node", func() bool { return registryURL(c.proxy, node.ID) == movedURL })
	awaitRegistry(t, "server B to move the node", func() bool { return registryURL(c.registryB, node.ID) == movedURL })

	off := false
	if _, err := c.serverA.UpdateAdminNode(ctx, node.ID, nodepool.UpdateNodeInput{Enabled: &off}, anyRevision); err != nil {
		t.Fatal(err)
	}
	if !c.registryA.HasKey(node.ID) {
		t.Fatal("disabling the node dropped its key; a disabled node keeps serving its streams")
	}

	if err := c.serverA.DeleteAdminNode(ctx, node.ID, anyRevision); err != nil {
		t.Fatal(err)
	}
	if registryHolds(c.registryA, node.ID) {
		t.Fatal("server A's registry still holds the node after the delete returned")
	}
	if _, err := c.registryA.Keyring(nil).KeyFor(nodeauth.TargetTranscode, movedURL); !errors.Is(err, nodeauth.ErrUnknownNode) {
		t.Fatalf("signing for the deleted node = %v, want ErrUnknownNode", err)
	}
	request := httptest.NewRequest(http.MethodGet, nodecontrol.PathConfig, nil)
	nodeauthtest.SignFromNode(t, request, node.ID, key)
	recorder := httptest.NewRecorder()
	nodecontrol.RequireNode(c.registryA)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the deleted node's request reached the handler")
	})).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("deleted node's request: %d, want 401", recorder.Code)
	}
	awaitRegistry(t, "the proxy to drop the deleted node", func() bool { return !registryHolds(c.proxy, node.ID) })
	awaitRegistry(t, "server B to drop the deleted node", func() bool { return !registryHolds(c.registryB, node.ID) })
}
