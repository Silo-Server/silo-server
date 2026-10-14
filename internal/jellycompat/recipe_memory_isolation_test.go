package jellycompat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/noderecipe"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

type memoryPublicationBarrier struct {
	recipeNodePutter
	phase   string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *memoryPublicationBarrier) wait(ctx context.Context, phase string) error {
	if phase != s.phase {
		return nil
	}
	s.once.Do(func() { close(s.entered) })
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *memoryPublicationBarrier) PublishWithUndo(ctx context.Context, work string, card playback.RecipeCard, accepted ...*noderecipe.AcceptedPublication) (noderecipe.Publication, error) {
	pub, err := s.recipeNodePutter.PublishWithUndo(ctx, work, card, accepted...)
	if err != nil {
		return pub, err
	}
	wrapped := &memoryBarrierReceipt{Publication: pub, store: s}
	if err = s.wait(ctx, "publish"); err != nil {
		return wrapped, err
	}
	if s.phase == "undo" {
		return wrapped, errors.New("injected publication rejection")
	}
	return wrapped, nil
}
func (s *memoryPublicationBarrier) ReconcileAccepted(ctx context.Context, work string, accepted *noderecipe.AcceptedPublication) error {
	if err := s.wait(ctx, "ack"); err != nil {
		return err
	}
	return s.recipeNodePutter.ReconcileAccepted(ctx, work, accepted)
}

type memoryBarrierReceipt struct {
	noderecipe.Publication
	store *memoryPublicationBarrier
}

func (p *memoryBarrierReceipt) Commit(ctx context.Context) error {
	if err := p.store.wait(ctx, "commit"); err != nil {
		return err
	}
	return p.Publication.Commit(ctx)
}
func (p *memoryBarrierReceipt) Undo(ctx context.Context) error {
	if err := p.store.wait(ctx, "undo"); err != nil {
		return err
	}
	return p.Publication.Undo(ctx)
}

func TestMemoryRecipePublicationDoesNotBlockUnrelatedSessions(t *testing.T) {
	for _, phase := range []string{"publish", "ack", "undo", "commit"} {
		t.Run(phase, func(t *testing.T) {
			state := NewPlaybackSessionStore(time.Hour, nil)
			state.Put(PlaybackSession{ID: "publishing", UpstreamSessionID: "work"})
			state.Put(PlaybackSession{ID: "unrelated", ClientName: "before"})
			recipes := &memoryPublicationBarrier{recipeNodePutter: &stubRecipeNodeStore{}, phase: phase, entered: make(chan struct{}), release: make(chan struct{})}
			released := sync.OnceFunc(func() { close(recipes.release) })
			defer released()
			finished := make(chan error, 1)
			go func() {
				finished <- state.PublishRecipe(t.Context(), "publishing", "work", playback.RecipeCard{SessionID: "work"}, recipes)
			}()
			select {
			case <-recipes.entered:
			case <-time.After(time.Second):
				t.Fatal("publication did not reach blocked receipt operation")
			}
			unrelated := make(chan error, 1)
			go func() {
				if _, ok := state.Get("unrelated"); !ok {
					unrelated <- errors.New("unrelated read lost row")
					return
				}
				unrelated <- state.Update("unrelated", func(row *PlaybackSession) error { row.ClientName = "after"; return nil })
			}()
			select {
			case err := <-unrelated:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(300 * time.Millisecond):
				t.Error("unrelated read/write blocked behind receipt I/O")
			}
			released()
			select {
			case err := <-finished:
				if (err != nil) != (phase == "undo") {
					t.Fatalf("publication result: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("publication failed to finish")
			}
			select {
			case err := <-unrelated:
				if err != nil {
					t.Fatal(err)
				}
			default:
			}
		})
	}
}

func waitMemoryMutationRefs(t *testing.T, state *PlaybackSessionStore, id string, want int) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		state.sessionMutationMu.Lock()
		gate := state.sessionMutations[id]
		refs := 0
		if gate != nil {
			refs = gate.refs
		}
		state.sessionMutationMu.Unlock()
		if refs >= want {
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatalf("session %s has %d mutation owners/waiters, want %d", id, refs, want)
		}
	}
}
func assertMemoryGatesReleased(t *testing.T, state *PlaybackSessionStore) {
	t.Helper()
	state.sessionMutationMu.Lock()
	defer state.sessionMutationMu.Unlock()
	if len(state.sessionMutations) != 0 {
		t.Fatalf("leaked %d session gates", len(state.sessionMutations))
	}
	state.recipeWorkMu.Lock()
	defer state.recipeWorkMu.Unlock()
	if len(state.recipeWorks) != 0 {
		t.Fatalf("leaked %d work gates", len(state.recipeWorks))
	}
}
func TestMemoryRecipeLifecycleMutationsSerializeWithReceipt(t *testing.T) {
	for _, phase := range []string{"publish", "ack", "undo", "commit"} {
		for _, operation := range []string{"update", "delete", "terminal", "put", "negotiated", "replace", "expiry", "pending-expiry", "terminal-expiry", "finalizable", "claim", "release", "complete", "stage", "get-expiry"} {
			t.Run(phase+"/"+operation, func(t *testing.T) {
				start := time.Now()
				var elapsed atomic.Int64
				state := NewPlaybackSessionStore(time.Hour, func() time.Time { return start.Add(time.Duration(elapsed.Load())) })
				prior := playback.RecipeCard{SessionID: "work", InputPath: "/prior"}
				state.Put(PlaybackSession{ID: "session", CompatToken: "token", UpstreamSessionID: "work", Recipe: &prior, RecipePublicationID: "prior", TranscodeStarted: true, ClientName: "before"})
				storage := &stubRecipeNodeStore{cards: map[string]playback.RecipeCard{"work": prior}}
				recipes := &memoryPublicationBarrier{recipeNodePutter: storage, phase: phase, entered: make(chan struct{}), release: make(chan struct{})}
				release := sync.OnceFunc(func() { close(recipes.release) })
				defer release()
				result := make(chan error, 1)
				go func() {
					result <- state.PublishRecipe(t.Context(), "session", "work", playback.RecipeCard{SessionID: "work", InputPath: "/candidate"}, recipes)
				}()
				select {
				case <-recipes.entered:
				case <-time.After(time.Second):
					t.Fatal("missing receipt barrier")
				}
				done := make(chan error, 1)
				go func() {
					var err error
					switch operation {
					case "update":
						err = state.Update("session", func(row *PlaybackSession) error {
							row.ClientName = "after"
							row.UpstreamSessionID = "new-work"
							return nil
						})
					case "delete":
						state.Delete("session")
					case "terminal":
						err = state.HideFromRouting("session", "token")
					case "stage":
						_, err = state.StageTerminal("session", "token", watchsync.ScrobbleEvent{}, true)
					case "get-expiry":
						elapsed.Store(int64(2 * time.Hour))
						_, _ = state.Get("session")
					case "put":
						state.Put(PlaybackSession{ID: "session", CompatToken: "replacement", UpstreamSessionID: "new-work"})
					case "negotiated":
						state.PutNegotiated(PlaybackSession{ID: "session", CompatToken: "replacement", UpstreamSessionID: "new-work"})
					case "replace":
						state.replaceByCompatToken("token", []PlaybackSession{{ID: "replacement", CompatToken: "token"}}, nil)
					case "expiry":
						elapsed.Store(int64(2 * time.Hour))
						state.deleteExpired()
					case "pending-expiry":
						elapsed.Store(int64(2 * time.Hour))
						_, err = state.ListPendingTerminals(t.Context(), 10)
					case "terminal-expiry":
						elapsed.Store(int64(2 * time.Hour))
						state.terminalIDsByCompatToken("token")
					case "finalizable":
						_, _ = state.GetFinalizable("session", "token")
					case "claim":
						_, err = state.ClaimTerminal("session", "token", start.Add(time.Minute))
						if errors.Is(err, ErrSessionNotFound) {
							err = nil
						}
					case "release":
						state.ReleaseTerminalClaim("session", "token", start, 1, false)
					case "complete":
						state.CompleteTerminal("session", "token", start, 1)
					}
					done <- err
				}()
				waitMemoryMutationRefs(t, state, "session", 2)
				select {
				case err := <-done:
					t.Fatalf("same-session mutation escaped receipt ownership: %v", err)
				default:
				}
				unrelated := make(chan struct{})
				go func() { state.Put(PlaybackSession{ID: "other"}); close(unrelated) }()
				select {
				case <-unrelated:
				case <-time.After(time.Second):
					t.Fatal("waiting lifecycle writer held global map lock")
				}
				release()
				select {
				case err := <-result:
					expiring := strings.Contains(operation, "expiry")
					if phase == "undo" && !expiring && err == nil {
						t.Fatal("publication rejection lost")
					}
					if phase != "undo" && !expiring && err != nil {
						t.Fatal(err)
					}
				case <-time.After(time.Second):
					t.Fatal("publication did not finish")
				}
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(time.Second):
					t.Fatal("lifecycle writer deadlocked")
				}
				state.mu.RLock()
				row, exists := state.sessions["session"]
				state.mu.RUnlock()
				switch operation {
				case "update":
					if row.ClientName != "after" || row.UpstreamSessionID != "new-work" {
						t.Fatalf("lost lifecycle metadata: %+v", row)
					}
				case "delete", "replace", "expiry", "pending-expiry", "terminal-expiry", "get-expiry":
					if exists {
						t.Fatal("deleted/expired authority resurrected")
					}
				case "terminal", "stage":
					if !row.Terminal {
						t.Fatal("terminal authority resurrected")
					}
				case "put", "negotiated":
					if row.CompatToken != "replacement" || row.UpstreamSessionID != "new-work" {
						t.Fatal("replacement authority overwritten")
					}
				}
				assertMemoryGatesReleased(t, state)
			})
		}
	}
}

func TestMemorySessionMutationCancellationAndBulkRevalidation(t *testing.T) {
	state := NewPlaybackSessionStore(time.Hour, nil)
	state.Put(PlaybackSession{ID: "a", CompatToken: "target"})
	release, err := state.lockSessionMutation(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	released := sync.OnceFunc(release)
	defer released()
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		unlock, err := state.lockSessionMutation(ctx, "a")
		if err == nil {
			unlock()
		}
		result <- err
	}()
	waitMemoryMutationRefs(t, state, "a", 2)
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	bulk := make(chan []string, 1)
	go func() {
		bulk <- state.replaceByCompatToken("target", []PlaybackSession{{ID: "replacement", CompatToken: "target"}}, nil)
	}()
	waitMemoryMutationRefs(t, state, "a", 2)
	state.Put(PlaybackSession{ID: "b", CompatToken: "target"})
	state.Put(PlaybackSession{ID: "unrelated", ClientName: "preserved"})
	released()
	select {
	case ids := <-bulk:
		if len(ids) != 3 {
			t.Fatalf("bulk missed concurrent matching insert: %v", ids)
		}
	case <-time.After(time.Second):
		t.Fatal("bulk gate retry deadlocked")
	}
	if _, ok := state.Get("a"); ok {
		t.Fatal("old row remains")
	}
	if _, ok := state.Get("b"); ok {
		t.Fatal("concurrent matching row escaped atomic replacement")
	}
	if row, ok := state.Get("unrelated"); !ok || row.ClientName != "preserved" {
		t.Fatal("unrelated row affected")
	}
	assertMemoryGatesReleased(t, state)
}

func memoryRecipeRedis(t *testing.T) *noderecipe.Store {
	t.Helper()
	endpoint := os.Getenv("SILO_TEST_REDIS_URL")
	if endpoint == "" {
		t.Skip("requires actual Redis test fixture")
	}
	options, err := redis.ParseURL(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(t.Context()).Err(); err != nil {
		t.Fatal(err)
	}
	return noderecipe.NewStore(client, time.Hour)
}

func TestMemoryRecipeRedisReceiptsPreserveRollbackAndSuccessors(t *testing.T) {
	for _, phase := range []string{"publish", "ack", "commit", "undo"} {
		for _, successor := range []bool{false, true} {
			if successor && phase != "undo" {
				continue
			}
			t.Run(fmt.Sprintf("%s/successor=%t", phase, successor), func(t *testing.T) {
				state := NewPlaybackSessionStore(time.Hour, nil)
				storage := memoryRecipeRedis(t)
				work := "memory-receipt-" + uuid.NewString()
				t.Cleanup(func() { _ = storage.Delete(context.Background(), work) })
				state.Put(PlaybackSession{ID: work, CompatToken: "token", UpstreamSessionID: work})
				prior := playback.NewRecipeCard(7, "profile", 42, "https://worker.example.test", playback.TranscodeOpts{SessionID: work, InputPath: "/prior"})
				if err := state.PublishRecipe(t.Context(), work, work, prior, storage); err != nil {
					t.Fatal(err)
				}
				before, _ := state.Get(work)
				candidate := prior
				candidate.InputPath = "/candidate"
				barrier := &memoryPublicationBarrier{recipeNodePutter: storage, phase: phase, entered: make(chan struct{}), release: make(chan struct{})}
				release := sync.OnceFunc(func() { close(barrier.release) })
				defer release()
				result := make(chan error, 1)
				go func() { result <- state.PublishRecipe(t.Context(), work, work, candidate, barrier) }()
				select {
				case <-barrier.entered:
				case <-time.After(time.Second):
					t.Fatal("missing Redis receipt barrier")
				}
				unrelated := make(chan struct{})
				go func() { state.Put(PlaybackSession{ID: "other"}); _, _ = state.Get("other"); close(unrelated) }()
				select {
				case <-unrelated:
				case <-time.After(time.Second):
					t.Fatal("Redis receipt holds global map lock")
				}
				if successor {
					replacement := candidate
					replacement.InputPath = "/independent-successor"
					if err := storage.Put(t.Context(), work, replacement); err != nil {
						t.Fatal(err)
					}
				}
				terminal := make(chan error, 1)
				go func() { terminal <- state.HideFromRouting(work, "token") }()
				waitMemoryMutationRefs(t, state, work, 2)
				release()
				err := <-result
				if (err != nil) != (phase == "undo") {
					t.Fatalf("publication outcome %v", err)
				}
				if err := <-terminal; err != nil {
					t.Fatal(err)
				}
				row, ok := state.GetFinalizable(work, "token")
				if !ok || !row.Terminal {
					t.Fatal("terminal lifecycle did not win after receipt completed")
				}
				got, ok := storage.Get(t.Context(), work)
				if !ok {
					t.Fatal("recipe lost")
				}
				want := candidate.InputPath
				if phase == "undo" {
					want = prior.InputPath
					if row.RecipePublicationID != before.RecipePublicationID {
						t.Fatal("failed publication changed accepted receipt")
					}
				}
				if successor {
					want = "/independent-successor"
				}
				if got.InputPath != want {
					t.Fatalf("receipt restored/removed wrong authority: %q want %q", got.InputPath, want)
				}
				assertMemoryGatesReleased(t, state)
			})
		}
	}
}

func TestMemoryRecipeExpiredDuringRedisRetiresExactCandidate(t *testing.T) {
	for _, successor := range []bool{false, true} {
		t.Run(fmt.Sprint(successor), func(t *testing.T) {
			start := time.Now()
			var elapsed atomic.Int64
			state := NewPlaybackSessionStore(time.Hour, func() time.Time { return start.Add(time.Duration(elapsed.Load())) })
			storage := memoryRecipeRedis(t)
			work := "memory-expiry-" + uuid.NewString()
			t.Cleanup(func() { _ = storage.Delete(context.Background(), work) })
			state.Put(PlaybackSession{ID: work, UpstreamSessionID: work})
			card := playback.NewRecipeCard(7, "profile", 42, "https://worker.example.test", playback.TranscodeOpts{SessionID: work, InputPath: "/candidate"})
			barrier := &memoryPublicationBarrier{recipeNodePutter: storage, phase: "publish", entered: make(chan struct{}), release: make(chan struct{})}
			release := sync.OnceFunc(func() { close(barrier.release) })
			defer release()
			result := make(chan error, 1)
			go func() { result <- state.PublishRecipe(t.Context(), work, work, card, barrier) }()
			select {
			case <-barrier.entered:
			case <-time.After(time.Second):
				t.Fatal("missing Redis preparation barrier")
			}
			if successor {
				next := card
				next.InputPath = "/successor"
				if err := storage.Put(t.Context(), work, next); err != nil {
					t.Fatal(err)
				}
			}
			elapsed.Store(int64(2 * time.Hour))
			release()
			if err := <-result; !errors.Is(err, ErrSessionNotFound) {
				t.Fatalf("expired candidate accepted: %v", err)
			}
			got, ok := storage.Get(t.Context(), work)
			if successor {
				if !ok || got.InputPath != "/successor" {
					t.Fatal("exact retirement removed successor")
				}
			} else if ok {
				t.Fatal("expired rejected candidate survived")
			}
			if _, ok := state.Get(work); ok {
				t.Fatal("expired session survived")
			}
			assertMemoryGatesReleased(t, state)
		})
	}
}

func TestMemoryRecipeAcknowledgementOutageKeepsAcceptedReceipt(t *testing.T) {
	state := NewPlaybackSessionStore(time.Hour, nil)
	storage := memoryRecipeRedis(t)
	work := "memory-lostack-" + uuid.NewString()
	t.Cleanup(func() { _ = storage.Delete(context.Background(), work) })
	state.Put(PlaybackSession{ID: work, UpstreamSessionID: work})
	card := playback.NewRecipeCard(7, "profile", 42, "https://worker.example.test", playback.TranscodeOpts{SessionID: work, InputPath: "/accepted"})
	unavailable := &unavailablePublicationAckStore{Store: storage}
	if err := state.PublishRecipe(t.Context(), work, work, card, unavailable); err != nil {
		t.Fatalf("known accepted publication converted to rejection: %v", err)
	}
	row, ok := state.Get(work)
	if !ok || !row.TranscodeStarted || row.RecipePublicationID == "" {
		t.Fatal("accepted receipt lost")
	}
	if err := storage.ReconcileAccepted(t.Context(), work, acceptedRecipe(row)); err != nil {
		t.Fatalf("retained receipt cannot reconcile: %v", err)
	}
	if got, ok := storage.Get(t.Context(), work); !ok || got.InputPath != "/accepted" {
		t.Fatal("accepted candidate lost")
	}
	assertMemoryGatesReleased(t, state)
}

type blockedMemoryRedisCommand struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*blockedMemoryRedisCommand) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h *blockedMemoryRedisCommand) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (h *blockedMemoryRedisCommand) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, command redis.Cmder) error {
		if command.Name() == "eval" || command.Name() == "evalsha" {
			first := false
			h.once.Do(func() { first = true; close(h.entered) })
			if first {
				select {
				case <-h.release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		return next(ctx, command)
	}
}
func TestMemoryRecipeBlockedRedisCommandAllowsUnrelatedSession(t *testing.T) {
	endpoint := os.Getenv("SILO_TEST_REDIS_URL")
	if endpoint == "" {
		t.Skip("requires actual Redis test fixture")
	}
	options, err := redis.ParseURL(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	hook := &blockedMemoryRedisCommand{entered: make(chan struct{}), release: make(chan struct{})}
	client.AddHook(hook)
	release := sync.OnceFunc(func() { close(hook.release) })
	defer release()
	recipes := noderecipe.NewStore(client, time.Hour)
	state := NewPlaybackSessionStore(time.Hour, nil)
	work := "memory-command-" + uuid.NewString()
	t.Cleanup(func() { _ = recipes.Delete(context.Background(), work) })
	state.Put(PlaybackSession{ID: work, UpstreamSessionID: work})
	state.Put(PlaybackSession{ID: "unrelated", ClientName: "before"})
	card := playback.NewRecipeCard(7, "profile", 42, "https://worker.example.test", playback.TranscodeOpts{SessionID: work, InputPath: "/candidate"})
	result := make(chan error, 1)
	go func() { result <- state.PublishRecipe(t.Context(), work, work, card, recipes) }()
	select {
	case <-hook.entered:
	case <-time.After(time.Second):
		t.Fatal("publication did not reach actual Redis command")
	}
	progress := make(chan error, 1)
	go func() {
		if _, ok := state.Get("unrelated"); !ok {
			progress <- errors.New("unrelated row lost")
			return
		}
		progress <- state.Update("unrelated", func(row *PlaybackSession) error { row.ClientName = "after"; return nil })
	}()
	select {
	case err := <-progress:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Error("actual Redis command blocked unrelated read/update")
	}
	release()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Redis publication did not finish")
	}
	row, ok := state.Get("unrelated")
	if !ok || row.ClientName != "after" {
		t.Fatal("unrelated progress lost")
	}
	assertMemoryGatesReleased(t, state)
}

func TestMemorySessionPendingScanCancellationKeepsRows(t *testing.T) {
	start := time.Now()
	var elapsed atomic.Int64
	state := NewPlaybackSessionStore(time.Hour, func() time.Time { return start.Add(time.Duration(elapsed.Load())) })
	state.Put(PlaybackSession{ID: "expired", CompatToken: "owner"})
	release, err := state.lockSessionMutation(t.Context(), "expired")
	if err != nil {
		t.Fatal(err)
	}
	released := sync.OnceFunc(release)
	defer released()
	elapsed.Store(int64(2 * time.Hour))
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() { _, err := state.ListPendingTerminals(ctx, 10); result <- err }()
	waitMemoryMutationRefs(t, state, "expired", 2)
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("scan cancellation lost: %v", err)
	}
	state.mu.RLock()
	_, exists := state.sessions["expired"]
	state.mu.RUnlock()
	if !exists {
		t.Fatal("canceled scan mutated authority")
	}
	state.Put(PlaybackSession{ID: "unrelated"})
	released()
	removed := state.deleteExpired()
	if removed["expired"] != "owner" {
		t.Fatal("canceled scan prevented later expiry")
	}
	assertMemoryGatesReleased(t, state)
}
