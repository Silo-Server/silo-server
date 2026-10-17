package workercontrolcore

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

func bindingRedis(t *testing.T) (*RedisStore, *redis.Client) {
	t.Helper()
	address := os.Getenv("SILO_WORKER_TEST_REDIS")
	if address == "" {
		t.Skip("requires disposable Redis SILO_WORKER_TEST_REDIS")
	}
	client := redis.NewClient(&redis.Options{Addr: address})
	t.Cleanup(func() { _ = client.Close() })
	store := NewRedisStore(client, "silo:binding-test:"+uuid.NewString()+":")
	t.Cleanup(func() {
		keys, err := client.Keys(context.Background(), store.prefix+"*").Result()
		if err != nil {
			t.Error(err)
		} else if len(keys) > 0 {
			if err := client.Del(context.Background(), keys...).Err(); err != nil {
				t.Error(err)
			}
		}
	})
	return store, client
}

func boundCandidate(t *testing.T, store *RedisStore, value string) workerwire.Assignment {
	t.Helper()
	a, err := store.Begin(t.Context(), workerwire.Assignment{Version: 1, NodeID: 7, WorkID: "artifact", Path: preparePath})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindInputs(t.Context(), a.NodeID, a.WorkID, a.Generation, map[string]string{"source:media": value}); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestRedisBindingsFollowLiveAndCandidateGenerationLifetime(t *testing.T) {
	store, client := bindingRedis(t)
	bindingsKey := store.prefix + "{7}:input-bindings"
	field := func(a workerwire.Assignment) string { return a.WorkID + "\x1f" + strconv.FormatUint(a.Generation, 10) }
	wantBinding := func(a workerwire.Assignment, value string) {
		t.Helper()
		bindings, err := NewRedisStore(client, store.prefix).InputBindings(t.Context(), a.NodeID, a.WorkID, a.Generation)
		if err != nil || bindings["source:media"] != value {
			t.Fatalf("generation%d binding=%v err=%v", a.Generation, bindings, err)
		}
	}
	wantAbsent := func(a workerwire.Assignment) {
		t.Helper()
		if exists, err := client.HExists(t.Context(), bindingsKey, field(a)).Result(); err != nil || exists {
			t.Fatalf("obsolete generation%d binding retained: exists%v err%v", a.Generation, exists, err)
		}
		if _, err := store.InputBindings(t.Context(), a.NodeID, a.WorkID, a.Generation); !errors.Is(err, ErrFenced) {
			t.Fatalf("obsolete generation%d binding still readable: %v", a.Generation, err)
		}
	}
	live := boundCandidate(t, store, "live")
	if err := store.Activate(t.Context(), 7, "artifact", live.Generation); err != nil {
		t.Fatal(err)
	}
	// Metadata has the same lifetime as its live generation; a 72-hour or
	// longer artifact must not depend on a separate 24-hour receipt expiry.
	if ttl, err := client.PTTL(t.Context(), bindingsKey).Result(); err != nil || ttl != -1 {
		t.Fatalf("generation-owned bindings have an independent lifetime: ttl%s err%v", ttl, err)
	}
	wantBinding(live, "live")
	failed := boundCandidate(t, store, "failed")
	if err := store.Cancel(t.Context(), 7, "artifact", failed.Generation); err != nil {
		t.Fatal(err)
	}
	wantAbsent(failed)
	wantBinding(live, "live")
	obsolete := boundCandidate(t, store, "obsolete")
	replacement := boundCandidate(t, store, "replacement")
	wantAbsent(obsolete)
	wantBinding(live, "live")
	wantBinding(replacement, "replacement")
	if err := store.Activate(t.Context(), 7, "artifact", replacement.Generation); err != nil {
		t.Fatal(err)
	}
	wantAbsent(live)
	wantBinding(replacement, "replacement")
	if err := store.StopIfGeneration(t.Context(), 7, "artifact", live.Generation); !errors.Is(err, ErrFenced) {
		t.Fatalf("delayed stop accepted: %v", err)
	}
	wantBinding(replacement, "replacement")
	pending := boundCandidate(t, store, "pending")
	if err := store.StopIfGeneration(t.Context(), 7, "artifact", pending.Generation); err != nil {
		t.Fatal(err)
	}
	wantAbsent(replacement)
	wantAbsent(pending)
	if count, err := client.HLen(t.Context(), bindingsKey).Result(); err != nil || count != 0 {
		t.Fatalf("stopped generation metadata leaked: count%d err%v", count, err)
	}
	final := boundCandidate(t, store, "final")
	if err := store.Stop(t.Context(), 7, "artifact"); err != nil {
		t.Fatal(err)
	}
	wantAbsent(final)
}

func TestRedisReadyPreparedOutputGrantHasGenerationOwnedReceipt(t *testing.T) {
	store, client := bindingRedis(t)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	node := Node{ID: 7, URL: "https://worker.example", PublicKey: public, Enabled: true}
	a, err := store.Begin(t.Context(), workerwire.Assignment{Version: 1, NodeID: node.ID, WorkID: "artifact", Path: preparePath})
	if err != nil {
		t.Fatal(err)
	}
	original, worker := strings.Repeat("a", 64), strings.Repeat("b", 64)
	if err := store.BindInputs(t.Context(), node.ID, a.WorkID, a.Generation, map[string]string{receiptOriginal: original, receiptWorker: worker}); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err := store.Activate(t.Context(), node.ID, a.WorkID, a.Generation); err != nil {
		t.Fatal(err)
	}
	if ttl, err := client.PTTL(t.Context(), store.prefix+"{7}:input-bindings").Result(); err != nil || ttl != -1 {
		t.Fatalf("receipt expires independently of ready output: ttl%s err%v", ttl, err)
	}
	const secret = "output-grant-test-only-secret"
	service, err := New(Options{Origin: "https://core.example", PrivateKey: private, Nodes: testNodes{[]Node{node}}, Store: NewRedisStore(client, store.prefix), OutputAuthorities: NewMemoryOutputAuthorities(), VerifyToken: func(_ context.Context, token string) (*streamtoken.Claims, error) {
		return streamtoken.Verify(token, secret)
	}})
	if err != nil {
		t.Fatal(err)
	}
	token, err := streamtoken.Sign(streamtoken.Claims{OriginCredential: streamtoken.OriginCredential{OriginLoginSessionID: "origin-login"}, DownloadArtifactID: a.WorkID, TranscodeNode: node.URL, RoutingExecutionNodeID: node.ID, PlayMethod: streamtoken.PlayMethodToneMapDownload, DownloadExecutionFingerprint: original, DownloadArtifactSize: 10, UserID: 5, MediaFileID: 9}, secret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.IssueOutputGrant(t.Context(), token, node.URL, secret); err != nil {
		t.Fatalf("ready prepared output lost durable receipt across core restart: %v", err)
	}
}
