package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/secret"
)

// registryKeyringFixtureV3 registers one proxy and one keyed transcode node in
// the test database and returns the real credential registry's keyring over
// them, with the transcode node's key and the shared secret.
func registryKeyringFixtureV3(t *testing.T, proxyURL, transcodeURL, sharedSecret string) (nodeauth.Keyring, string) {
	t.Helper()
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
	cipher, err := secret.New([]byte("playback-v3-capability-target-test-master-key"))
	if err != nil {
		t.Fatal(err)
	}
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
	create("capability-proxy", nodepool.NodeTypeProxy, proxyURL)
	transcode := create("capability-transcode", nodepool.NodeTypeTranscode, transcodeURL)
	nodeKey, err := repo.IssueNodeKey(ctx, cipher, transcode.ID)
	if err != nil {
		t.Fatal(err)
	}
	registry := nodepool.NewKeyRegistry(repo, cipher)
	if err := registry.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	return registry.Keyring(func() string { return sharedSecret }), nodeKey
}

// capabilityServerV3 answers /hw-capabilities with info for requests signed
// with the key secret returns, and 401 otherwise, as a worker does.
func capabilityServerV3(t *testing.T, secret func() string, info playback.HWAccelInfo) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hw-capabilities" || nodeauth.VerifySigned(r, secret()) != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, info)
	}))
	t.Cleanup(server.Close)
	return server
}

// Progressive remux executed on a transcode node is relayed by a proxy, and the
// planner learns that a proxy can relay it from the proxy's own capability
// report. With the credential registry, that read must be signed for a proxy:
// signed for a transcode node, the keyring refuses the proxy's URL, the relay
// feature is never seen, and every remux falls back to proxy execution.
func TestProgressiveRemuxPlanningReadsProxyRelayThroughTheCredentialRegistryV3(t *testing.T) {
	const sharedSecret = "capability-target-shared-secret"
	var transcodeKey string
	relay := capabilityServerV3(t, func() string { return sharedSecret }, playback.HWAccelInfo{
		TransportFeatures: []string{playback.TransportFeatureProgressiveRemuxRelayV1},
	})
	transcode := capabilityServerV3(t, func() string { return transcodeKey }, playback.HWAccelInfo{
		TransportFeatures: []string{playback.TransportFeatureProgressiveRemuxExecutionV1},
		Transformations: []playback.TransformationV3{{
			Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3,
			RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3,
		}},
	})
	keys, nodeKey := registryKeyringFixtureV3(t, relay.URL, transcode.URL, sharedSecret)
	transcodeKey = nodeKey

	local := playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{{
		Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3,
	}})
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.NodeKeys = keys
	handler.NodeRecipeStore = &recordingRecipeCardStoreV3{}
	handler.NodePlanner = enumeratingNodePlannerV3{urls: []string{transcode.URL}, proxyURLs: []string{relay.URL}}
	policy := config.DefaultPlaybackRoutingPolicy()
	policy.RemuxExecution = config.PlaybackExecutionWorkerOnly
	handler.PlaybackConfig = func() config.PlaybackConfig { return config.PlaybackConfig{Routing: policy} }

	if !handler.progressiveRemuxRelayAvailableV3(t.Context()) {
		t.Fatal("the proxy's progressive_remux_relay_v1 was not seen through the credential registry")
	}
	registry := handler.progressiveRemuxPlanningRegistryWithInputsV3(t.Context(), local, true)
	if !registry.Available(playback.TransformationAudioToAACV3) {
		t.Fatal("transcode-executed progressive remux was left out of planning")
	}
	eligible := handler.identityProxyRelayEligibilityV3(t.Context())
	if !eligible(&nodepool.Node{URL: relay.URL, Type: nodepool.NodeTypeProxy}) {
		t.Fatal("route selection refused the relay-capable proxy")
	}

	// Transport time reads the selected route's nodes by their role.
	proxyEntry, err := handler.lookupRemoteCapabilitiesV3(t.Context(), nodeauth.TargetProxy, relay.URL, false)
	if err != nil || !slices.Contains(proxyEntry.transportFeatures, playback.TransportFeatureProgressiveRemuxRelayV1) {
		t.Fatalf("selected proxy capabilities = (%v, %v), want the relay feature", proxyEntry.transportFeatures, err)
	}
	transcodeEntry, err := handler.lookupRemoteCapabilitiesV3(t.Context(), nodeauth.TargetTranscode, transcode.URL, false)
	if err != nil || !slices.Contains(transcodeEntry.transportFeatures, playback.TransportFeatureProgressiveRemuxExecutionV1) {
		t.Fatalf("selected transcode node capabilities = (%v, %v), want the execution feature", transcodeEntry.transportFeatures, err)
	}

	// The cache is keyed by role too: the proxy's inventory never answers a
	// transcode lookup of the same URL, which the keyring refuses to sign.
	if _, err := handler.lookupRemoteCapabilitiesV3(t.Context(), nodeauth.TargetTranscode, relay.URL, false); err == nil {
		t.Fatal("a transcode lookup of the proxy's URL was answered")
	}
}
