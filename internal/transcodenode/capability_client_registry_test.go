package transcodenode_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

// Proxies serve /hw-capabilities too, and the API reads both kinds. Through the
// credential registry, a read of a proxy must carry the shared secret and a
// read of a transcode node its own key, each as the caller names its target;
// the wrong target for a URL is refused before anything is sent.
func TestFetchHWCapabilitiesSignsForTheNamedTargetThroughTheRegistry(t *testing.T) {
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
	cipher, err := secret.New([]byte("transcodenode-capability-registry-test-master-key"))
	if err != nil {
		t.Fatal(err)
	}

	const sharedSecret = "capability-registry-shared-secret"
	var nodeKey string
	var proxyRequests, transcodeRequests atomic.Int32
	worker := func(requests *atomic.Int32, key func() string) *httptest.Server {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			if nodeauth.VerifySigned(r, key()) != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"resolved":"none"}`))
		}))
		t.Cleanup(server.Close)
		return server
	}
	proxy := worker(&proxyRequests, func() string { return sharedSecret })
	transcode := worker(&transcodeRequests, func() string { return nodeKey })

	repo := nodepool.NewRepository(pool)
	create := func(name, nodeType, nodeURL string) *nodepool.Node {
		t.Helper()
		node, err := repo.Create(ctx, nodepool.CreateNodeInput{Name: name + "-" + nodeURL, Type: nodeType, URL: nodeURL})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = repo.Delete(context.Background(), node.ID) })
		return node
	}
	create("capability-fetch-proxy", nodepool.NodeTypeProxy, proxy.URL)
	transcodeNode := create("capability-fetch-transcode", nodepool.NodeTypeTranscode, transcode.URL)
	if nodeKey, err = repo.IssueNodeKey(ctx, cipher, transcodeNode.ID); err != nil {
		t.Fatal(err)
	}
	registry := nodepool.NewKeyRegistry(repo, cipher)
	if err := registry.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	keys := registry.Keyring(func() string { return sharedSecret })

	for _, test := range []struct {
		name    string
		url     string
		target  nodeauth.Target
		calls   *atomic.Int32
		wantErr bool
	}{
		{name: "proxy with the shared secret", url: proxy.URL, target: nodeauth.TargetProxy, calls: &proxyRequests},
		{name: "transcode node with its own key", url: transcode.URL, target: nodeauth.TargetTranscode, calls: &transcodeRequests},
		{name: "proxy URL as a transcode node", url: proxy.URL, target: nodeauth.TargetTranscode, calls: &proxyRequests, wantErr: true},
		{name: "transcode URL as a proxy", url: transcode.URL, target: nodeauth.TargetProxy, calls: &transcodeRequests, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := test.calls.Load()
			info, status, err := transcodenode.FetchHWCapabilities(ctx, nil, test.url, test.target, keys)
			if test.wantErr {
				if err == nil {
					t.Fatalf("fetch = (%d, nil), want a signing refusal", status)
				}
				if test.calls.Load() != before {
					t.Fatal("a refused target still reached the worker")
				}
				return
			}
			if err != nil || status != http.StatusOK || info.Resolved != playback.HWAccelNone {
				t.Fatalf("fetch = (%+v, %d, %v), want the worker's report", info, status, err)
			}
		})
	}
}
