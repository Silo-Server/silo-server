package nodecontrol_test

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/nodecontrol"
	"github.com/Silo-Server/silo-server/internal/nodecontrol/nodecontroltest"
	"github.com/Silo-Server/silo-server/internal/noderecipe"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// A force reload revokes the node's recipes in the real recipe store before
// it calls the node (internal/api/handlers/admin_node_reload.go). Resolve
// reads that store, so after either revocation a node can neither start a
// progressive remux nor rebuild a lost HLS transcode it was serving.
func TestResolveRefusesAfterAForceReloadRevokesTheNode(t *testing.T) {
	rawURL := os.Getenv("SILO_TEST_REDIS_URL")
	if rawURL == "" {
		t.Skip("SILO_TEST_REDIS_URL not set")
	}
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		t.Fatalf("parse SILO_TEST_REDIS_URL: %v", err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })

	unique := uuid.NewString()
	parsed, err := strconv.ParseInt(unique[:7], 16, 64)
	if err != nil {
		t.Fatal(err)
	}
	nodeID := int(parsed) + 1
	nodeURL := "http://node-" + unique
	store := noderecipe.NewStore(client, time.Minute)

	newCore := func(t *testing.T) *nodecontrol.Client {
		return nodecontroltest.Resolver(t, nodeID, nodecontrol.Options{
			Nodes:        nodeURLs{urls: map[int]string{nodeID: nodeURL}},
			StreamSecret: func() string { return resolveSecret },
			Recipes:      store,
		})
	}
	resolve := func(t *testing.T, client *nodecontrol.Client, purpose, transportID, token string) (int, string) {
		t.Helper()
		_, err := client.Resolve(t.Context(), nodecontrol.ResolveRequest{TransportID: transportID, Purpose: purpose, StreamToken: token})
		if err == nil {
			return http.StatusOK, ""
		}
		if nodecontrol.Status(err) == 0 {
			t.Fatalf("resolve: %v", err)
		}
		return nodecontrol.Status(err), nodecontrol.Reason(err)
	}
	put := func(t *testing.T, transportID string, card playback.RecipeCard) {
		t.Helper()
		if err := store.Put(context.Background(), transportID, card); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Delete(context.Background(), transportID) })
	}

	// The admin force reload revokes both keys. Each revocation is checked on
	// its own here, against the card shape it withdraws: the store binds a
	// card that carries the row id by the id, and any other card by its URL.
	// A progressive remux always carries the id.
	t.Run("node id", func(t *testing.T) {
		client := newCore(t)
		remux := remuxClaims(nodeID, "/media/movie.mkv")
		remux.TranscodeNode = nodeURL
		remux.TranscodeTransportID = "remux-" + unique
		put(t, remux.TranscodeTransportID, playback.RecipeCardFromClaims(&remux))
		remuxToken := signClaims(t, remux, resolveSecret)
		hls := nativeCard("viewer-id-"+unique, "hls-id-"+unique)
		hls.TranscodeNodeURL = nodeURL
		hls.RoutingExecutionNodeID = nodeID
		put(t, hls.TranscodeTransportID, hls)

		if status, reason := resolve(t, client, nodecontrol.ResolvePurposeRemux, remux.TranscodeTransportID, remuxToken); status != http.StatusOK {
			t.Fatalf("remux before the revocation = %d %q", status, reason)
		}
		if status, reason := resolve(t, client, nodecontrol.ResolvePurposeHLS, hls.TranscodeTransportID, ""); status != http.StatusOK {
			t.Fatalf("HLS before the revocation = %d %q", status, reason)
		}
		if err := store.RevokeNodeID(context.Background(), nodeID); err != nil {
			t.Fatal(err)
		}
		if status, reason := resolve(t, client, nodecontrol.ResolvePurposeRemux, remux.TranscodeTransportID, remuxToken); status != http.StatusGone || reason != nodecontrol.ResolveStopped {
			t.Fatalf("remux after the revocation = %d %q, want 410 %q", status, reason, nodecontrol.ResolveStopped)
		}
		if status, reason := resolve(t, client, nodecontrol.ResolvePurposeHLS, hls.TranscodeTransportID, ""); status != http.StatusNotFound || reason != nodecontrol.ResolveNotFound {
			t.Fatalf("HLS after the revocation = %d %q, want 404 %q", status, reason, nodecontrol.ResolveNotFound)
		}
	})
	// A transcode moved to this node keeps presenting the token minted for
	// the node it left; the stored card naming this node is what lets it
	// rebuild, and revoking this node withdraws it like any other.
	t.Run("moved transport", func(t *testing.T) {
		client := newCore(t)
		minted := nativeCard("viewer-moved-"+unique, "hls-moved-"+unique)
		minted.RoutingExecutionNodeID = nodeID + 1
		token := signClaims(t, minted.ToClaims(), resolveSecret)
		moved := minted
		moved.TranscodeNodeURL = nodeURL
		moved.RoutingExecutionNodeID = nodeID
		put(t, moved.TranscodeTransportID, moved)

		if status, reason := resolve(t, client, nodecontrol.ResolvePurposeHLS, moved.TranscodeTransportID, token); status != http.StatusOK {
			t.Fatalf("HLS before the revocation = %d %q", status, reason)
		}
		if err := store.RevokeNodeID(context.Background(), nodeID); err != nil {
			t.Fatal(err)
		}
		if status, reason := resolve(t, client, nodecontrol.ResolvePurposeHLS, moved.TranscodeTransportID, token); status != http.StatusForbidden || reason != nodecontrol.ResolveWrongRoute {
			t.Fatalf("HLS after the revocation = %d %q, want 403 %q", status, reason, nodecontrol.ResolveWrongRoute)
		}
	})
	t.Run("node URL", func(t *testing.T) {
		client := newCore(t)
		hls := nativeCard("viewer-url-"+unique, "hls-url-"+unique)
		hls.TranscodeNodeURL = nodeURL
		put(t, hls.TranscodeTransportID, hls)

		if status, reason := resolve(t, client, nodecontrol.ResolvePurposeHLS, hls.TranscodeTransportID, ""); status != http.StatusOK {
			t.Fatalf("HLS before the revocation = %d %q", status, reason)
		}
		if err := store.RevokeNode(context.Background(), nodeURL); err != nil {
			t.Fatal(err)
		}
		if status, reason := resolve(t, client, nodecontrol.ResolvePurposeHLS, hls.TranscodeTransportID, ""); status != http.StatusNotFound || reason != nodecontrol.ResolveNotFound {
			t.Fatalf("HLS after the revocation = %d %q, want 404 %q", status, reason, nodecontrol.ResolveNotFound)
		}
	})
}
