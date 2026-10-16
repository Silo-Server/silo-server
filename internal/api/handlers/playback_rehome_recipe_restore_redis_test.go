package handlers

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderecipe"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

type recipeRestoreNamespacedRecipe struct {
	store  *noderecipe.Store
	prefix string
}

func (s recipeRestoreNamespacedRecipe) CaptureRestore(ctx context.Context, work string, card playback.RecipeCard) (noderecipe.Restoration, error) {
	return s.store.CaptureRestore(ctx, s.prefix+work, card)
}

func (s recipeRestoreNamespacedRecipe) Enabled() bool { return s.store.Enabled() }
func (s recipeRestoreNamespacedRecipe) Get(ctx context.Context, work string) (*playback.RecipeCard, bool) {
	return s.store.Get(ctx, s.prefix+work)
}
func (s recipeRestoreNamespacedRecipe) Put(ctx context.Context, work string, card playback.RecipeCard) error {
	return s.store.Put(ctx, s.prefix+work, card)
}
func (s recipeRestoreNamespacedRecipe) Delete(ctx context.Context, work string) error {
	return s.store.Delete(ctx, s.prefix+work)
}

func recipeRestoreReadRedisCommand(r *bufio.Reader) ([]byte, []string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, nil, err
	}
	if len(line) == 0 || line[0] != '*' {
		return nil, nil, errors.New("expected RESP array")
	}
	n, err := strconv.Atoi(strings.TrimSpace(line[1:]))
	if err != nil {
		return nil, nil, err
	}
	raw := []byte(line)
	args := make([]string, 0, n)
	for i := 0; i < n; i++ {
		head, e := r.ReadString('\n')
		if e != nil {
			return nil, nil, e
		}
		if len(head) == 0 || head[0] != '$' {
			return nil, nil, errors.New("expected RESP bulk")
		}
		size, e := strconv.Atoi(strings.TrimSpace(head[1:]))
		if e != nil {
			return nil, nil, e
		}
		body := make([]byte, size+2)
		if _, e = io.ReadFull(r, body); e != nil {
			return nil, nil, e
		}
		raw = append(raw, head...)
		raw = append(raw, body...)
		args = append(args, string(body[:size]))
	}
	return raw, args, nil
}

// Hold exactly the bounded recipe publication packet before Redis sees it.
// The caller times out; releasing the gate later still executes the bytes on
// the original live upstream socket and observes Redis's response.
func recipeRestoreDelayedRecipeClient(t *testing.T, direct *redis.Client, key string) (*redis.Client, func(), <-chan struct{}, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	executed := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	var seen atomic.Int32
	var wg sync.WaitGroup
	t.Cleanup(func() { unblock(); _ = ln.Close(); wg.Wait() })
	go func() {
		for {
			down, e := ln.Accept()
			if e != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { _ = down.Close() }()
				up, e := net.Dial("tcp", direct.Options().Addr)
				if e != nil {
					t.Error(e)
					return
				}
				defer func() { _ = up.Close() }()
				var gate atomic.Bool
				go func() {
					b := make([]byte, 32768)
					for {
						n, e := up.Read(b)
						if n > 0 {
							if gate.Swap(false) {
								close(executed)
							}
							if _, werr := down.Write(b[:n]); werr != nil {
								return
							}
						}
						if e != nil {
							return
						}
					}
				}()
				r := bufio.NewReader(down)
				for {
					raw, args, e := recipeRestoreReadRedisCommand(r)
					if e != nil {
						return
					}
					held := len(args) > 10 && strings.EqualFold(args[0], "evalsha") && args[2] == "7" && args[3] == key
					if held {
						if seen.Add(1) != 1 {
							t.Error("unexpected retry")
						}
						<-release
						gate.Store(true)
					}
					if _, e = up.Write(raw); e != nil {
						t.Error(e)
						return
					}
					if held {
						select {
						case <-executed:
						case <-t.Context().Done():
						}
						return
					}
				}
			}()
		}
	}()
	opts := *direct.Options()
	opts.Addr = ln.Addr().String()
	opts.ReadTimeout = 40 * time.Millisecond
	opts.WriteTimeout = time.Second
	opts.MaxRetries = -1
	client := redis.NewClient(&opts)
	t.Cleanup(func() { _ = client.Close() })
	return client, unblock, executed, &seen
}

func TestRejectedRehomeRouteDelayedRestoreCannotReviveStoppedAttempt(t *testing.T) {
	url := os.Getenv("SILO_TEST_REDIS_URL")
	if url == "" {
		t.Skip("requires task Redis SILO_TEST_REDIS_URL")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	direct := redis.NewClient(opts)
	t.Cleanup(func() { _ = direct.Close() })
	prefix := "recipeRestore-rejected-restore-" + uuid.NewString() + ":"
	recipes := recipeRestoreNamespacedRecipe{store: noderecipe.NewStore(direct, time.Minute), prefix: prefix}
	t.Cleanup(func() { _ = recipes.Delete(context.Background(), rehomeTestTransport) })
	var starts, deletes atomic.Int32
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes.Add(1)
			if err := recipes.Delete(r.Context(), strings.TrimPrefix(r.URL.Path, "/transcode/")); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var req transcodenode.TranscodeStartRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		starts.Add(1)
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(transcodenode.TranscodeStartResponse{SessionID: req.SessionID, ThrottleSeconds: req.ThrottleSeconds})
	}))
	t.Cleanup(node.Close)
	f := newRehomeFixture(t, []*nodepool.Node{pooledNode(1, unreachableNodeURL(t)), pooledNode(2, node.URL)}, "ffmpeg")
	cfg := f.handler.playbackConfig()
	cfg.Routing.VideoTranscodeExecution = config.PlaybackExecutionWorkerOnly
	f.handler.PlaybackConfig = func() config.PlaybackConfig { return cfg }
	card, _ := verifiedStreamCardFromToken(f.token, f.session.ID, f.handler.JWTSecret)
	if card == nil {
		t.Fatal("missing recipe")
	}
	// Initial direct Put also loads the production publication script.
	if err := recipes.Put(t.Context(), rehomeTestTransport, *card); err != nil {
		t.Fatal(err)
	}
	proxy, release, executed, seen := recipeRestoreDelayedRecipeClient(t, direct, noderecipe.KeyPrefix+prefix+rehomeTestTransport)
	f.handler.NodeRecipeStore = recipeRestoreNamespacedRecipe{store: noderecipe.NewStore(proxy, time.Minute), prefix: prefix}
	base := rehomeDurableStore(t, f)
	f.handler.PlanStoreV3 = &rehomeFaultStore{MemoryPlanStoreV3: base, fault: "rejected"}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err = f.handler.moveTranscodeOffNodeV3(ctx, f.session.ID, playback.TranscodeRoute{NodeURL: f.deadURL, TransportID: rehomeTestTransport}, nil, 3)
	if !errors.Is(err, errRehomeOutcomeUnknown) || seen.Load() != 1 || starts.Load() != 1 || deletes.Load() != 1 {
		t.Fatalf("did not reach timed-out restoration: err=%v seen=%d POST=%d DELETE=%d", err, seen.Load(), starts.Load(), deletes.Load())
	}
	// Production STOP's authoritative row write and shared recipe deletion
	// complete after the restoration's WithLiveRoute returned/released its row.
	if _, _, err := base.StopAttempt(ctx, f.session.ID, uuid.NewString(), nil); err != nil {
		t.Fatal(err)
	}
	if err := recipes.Delete(ctx, rehomeTestTransport); err != nil {
		t.Fatal(err)
	}
	if _, ok := recipes.Get(ctx, rehomeTestTransport); ok {
		t.Fatal("recipe survived acknowledged stop before delayed write")
	}
	release()
	select {
	case <-executed:
	case <-ctx.Done():
		t.Fatal("Redis did not execute delayed packet", ctx.Err())
	}
	stopped, err := base.GetAttempt(ctx, f.session.ID)
	if err != nil || stopped.StoppedAt == nil {
		t.Fatal("authoritative stop missing", err)
	}
	if got, ok := recipes.Get(ctx, rehomeTestTransport); ok {
		t.Fatalf("timed-out restoration executed after acknowledged STOP+DELETE and revived tokenless recipe %q on %q", got.InputPath, got.TranscodeNodeURL)
	}
}

func TestFailedRehomeStartDelayedRestoreCannotReviveStoppedAttempt(t *testing.T) {
	url := os.Getenv("SILO_TEST_REDIS_URL")
	if url == "" {
		t.Skip("requires task Redis SILO_TEST_REDIS_URL")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	direct := redis.NewClient(opts)
	t.Cleanup(func() { _ = direct.Close() })
	prefix := "recipeRestore-rejected-restore-" + uuid.NewString() + ":"
	recipes := recipeRestoreNamespacedRecipe{store: noderecipe.NewStore(direct, time.Minute), prefix: prefix}
	t.Cleanup(func() { _ = recipes.Delete(context.Background(), rehomeTestTransport) })
	var starts, deletes atomic.Int32
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes.Add(1)
			if err := recipes.Delete(r.Context(), strings.TrimPrefix(r.URL.Path, "/transcode/")); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var req transcodenode.TranscodeStartRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		starts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(transcodenode.TranscodeStartResponse{SessionID: req.SessionID, ThrottleSeconds: req.ThrottleSeconds})
	}))
	t.Cleanup(node.Close)
	f := newRehomeFixture(t, []*nodepool.Node{pooledNode(1, unreachableNodeURL(t)), pooledNode(2, node.URL)}, "ffmpeg")
	cfg := f.handler.playbackConfig()
	cfg.Routing.VideoTranscodeExecution = config.PlaybackExecutionWorkerOnly
	f.handler.PlaybackConfig = func() config.PlaybackConfig { return cfg }
	card, _ := verifiedStreamCardFromToken(f.token, f.session.ID, f.handler.JWTSecret)
	if card == nil {
		t.Fatal("missing recipe")
	}
	// Initial direct Put also loads the production publication script.
	if err := recipes.Put(t.Context(), rehomeTestTransport, *card); err != nil {
		t.Fatal(err)
	}
	proxy, release, executed, seen := recipeRestoreDelayedRecipeClient(t, direct, noderecipe.KeyPrefix+prefix+rehomeTestTransport)
	f.handler.NodeRecipeStore = recipeRestoreNamespacedRecipe{store: noderecipe.NewStore(proxy, time.Minute), prefix: prefix}
	base := rehomeDurableStore(t, f)
	f.handler.PlanStoreV3 = base
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err = f.handler.moveTranscodeOffNodeV3(ctx, f.session.ID, playback.TranscodeRoute{NodeURL: f.deadURL, TransportID: rehomeTestTransport}, nil, 3)
	if err == nil || seen.Load() != 1 || starts.Load() != 1 || deletes.Load() != 1 {
		t.Fatalf("did not reach timed-out restoration: err=%v seen=%d POST=%d DELETE=%d", err, seen.Load(), starts.Load(), deletes.Load())
	}
	// Production STOP's authoritative row write and shared recipe deletion
	// complete after the failed-start rollback returned/released its lifecycle.
	if _, _, err := base.StopAttempt(ctx, f.session.ID, uuid.NewString(), nil); err != nil {
		t.Fatal(err)
	}
	if err := recipes.Delete(ctx, rehomeTestTransport); err != nil {
		t.Fatal(err)
	}
	if _, ok := recipes.Get(ctx, rehomeTestTransport); ok {
		t.Fatal("recipe survived acknowledged stop before delayed write")
	}
	release()
	select {
	case <-executed:
	case <-ctx.Done():
		t.Fatal("Redis did not execute delayed packet", ctx.Err())
	}
	stopped, err := base.GetAttempt(ctx, f.session.ID)
	if err != nil || stopped.StoppedAt == nil {
		t.Fatal("authoritative stop missing", err)
	}
	if got, ok := recipes.Get(ctx, rehomeTestTransport); ok {
		t.Fatalf("timed-out restoration executed after acknowledged STOP+DELETE and revived tokenless recipe %q on %q", got.InputPath, got.TranscodeNodeURL)
	}
}
