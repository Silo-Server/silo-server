package noderecipe

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/playback"
)

type recipeRestoreRestoreGate struct {
	entered chan struct{}
	release chan struct{}
	done    chan struct{}
	once    sync.Once
}

func recipeRestoreNewRestoreGate() *recipeRestoreRestoreGate {
	return &recipeRestoreRestoreGate{entered: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
}
func (g *recipeRestoreRestoreGate) unblock() { g.once.Do(func() { close(g.release) }) }

type recipeRestoreRestoreWire struct {
	prepare, publish, retire atomic.Int32
	deadline                 atomic.Int64
	holdPrepare, holdPublish *recipeRestoreRestoreGate
	retireUnavailable        bool
}

//nolint:funlen,gocyclo,nestif // Keep this ordered failure-injection scenario and its exact authority assertions in one scope.
func recipeRestoreRestoreClient(t *testing.T, direct *redis.Client, work string, wire *recipeRestoreRestoreWire, retries int, readTimeout time.Duration) *redis.Client {
	t.Helper()
	for _, script := range []*redis.Script{prepareRecipePublicationScript, publishRecipeScript, retireRecipePublicationScript, commitRecipePublicationScript} {
		if err := script.Load(t.Context(), direct).Err(); err != nil {
			t.Fatal(err)
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	t.Cleanup(func() {
		if wire.holdPrepare != nil {
			wire.holdPrepare.unblock()
		}
		if wire.holdPublish != nil {
			wire.holdPublish.unblock()
		}
		_ = ln.Close()
		wg.Wait()
	})
	go func() {
		defer wg.Done()
		for {
			down, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { _ = down.Close() }()
				up, err := net.Dial("tcp", direct.Options().Addr)
				if err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = up.Close() }()
				var responseGate atomic.Pointer[recipeRestoreRestoreGate]
				go func() {
					b := make([]byte, 32768)
					for {
						n, err := up.Read(b)
						if n > 0 {
							if g := responseGate.Swap(nil); g != nil {
								close(g.done)
							}
							if _, e := down.Write(b[:n]); e != nil {
								return
							}
						}
						if err != nil {
							return
						}
					}
				}()
				r := bufio.NewReader(down)
				for {
					raw, args, err := publicationReadCommand(r)
					if err != nil {
						return
					}
					var held *recipeRestoreRestoreGate
					if len(args) > 3 && strings.EqualFold(args[0], "evalsha") && args[3] == KeyPrefix+work {
						switch args[1] {
						case prepareRecipePublicationScript.Hash():
							deadline, err := strconv.ParseInt(args[len(args)-1], 10, 64)
							if err != nil {
								t.Error(err)
								return
							}
							wire.deadline.Store(deadline)
							if wire.prepare.Add(1) == 1 {
								held = wire.holdPrepare
							}
						case publishRecipeScript.Hash():
							if wire.publish.Add(1) == 1 {
								held = wire.holdPublish
							}
						case retireRecipePublicationScript.Hash():
							wire.retire.Add(1)
							if wire.retireUnavailable {
								_, _ = down.Write([]byte("-ERR injected retirement unavailable\r\n"))
								continue
							}
						}
					}
					if held != nil {
						close(held.entered)
						<-held.release
						responseGate.Store(held)
					}
					if _, err := up.Write(raw); err != nil {
						return
					}
					if held != nil {
						select {
						case <-held.done:
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
	opts.ReadTimeout = readTimeout
	opts.WriteTimeout = time.Second
	opts.MaxRetries = retries
	opts.MinRetryBackoff = time.Millisecond
	opts.MaxRetryBackoff = time.Millisecond
	opts.ContextTimeoutEnabled = true
	client := redis.NewClient(&opts)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func recipeRestoreWaitRestoreGate(t *testing.T, gate <-chan struct{}) {
	t.Helper()
	select {
	case <-gate:
	case <-time.After(3 * time.Second):
		t.Fatal("expected Redis packet gate was not observed")
	}
}

func TestRedisRecipeRestoreRetirementOutageKeepsRowOwned(t *testing.T) {
	for _, stage := range []string{"prepare", "publish", "canceled-publish"} {
		t.Run(stage, func(t *testing.T) {
			directStore, direct, work := publicationTestStore(t)
			card := playback.RecipeCard{SessionID: work, InputPath: "/approved/old", TranscodeNodeURL: "https://" + work + ".example"}
			if err := directStore.Put(t.Context(), work, card); err != nil {
				t.Fatal(err)
			}
			gate := recipeRestoreNewRestoreGate()
			wire := &recipeRestoreRestoreWire{retireUnavailable: true}
			if stage == "prepare" {
				wire.holdPrepare = gate
			} else {
				wire.holdPublish = gate
			}
			client := recipeRestoreRestoreClient(t, direct, work, wire, -1, 35*time.Millisecond)
			store := NewStore(client, time.Minute)
			restoration, err := store.CaptureRestore(t.Context(), work, card)
			if err != nil {
				t.Fatal(err)
			}
			if err := directStore.Delete(t.Context(), work); err != nil {
				t.Fatal(err)
			}
			plans := playback.NewMemoryPlanStoreV3()
			record := playback.AttemptRecordV3{SessionID: work, PlaybackAttemptID: work, CurrentPlanID: "old-plan", ExpiresAt: time.Now().Add(time.Minute), FrozenRecipe: playback.ExecutableRecipeV3{TranscodeNodeURL: card.TranscodeNodeURL, TranscodeTransportID: work}}
			if err := plans.SaveAttempt(t.Context(), record); err != nil {
				t.Fatal(err)
			}
			owned, err := plans.AcquireRouteSession(t.Context(), work)
			if err != nil {
				t.Fatal(err)
			}
			defer owned.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 800*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := owned.WithLiveRoute(ctx, record, func(owned context.Context) error { return restoration.Restore(owned, restoration.ExpiresAt()) })
				done <- err
			}()
			recipeRestoreWaitRestoreGate(t, gate.entered)
			if stage == "canceled-publish" {
				cancel()
			}
			stopCtx, stopCancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			_, _, err = plans.StopAttempt(stopCtx, work, uuid.NewString(), nil)
			stopCancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("STOP passed unresolved restoration before its admission deadline: %v", err)
			}
			select {
			case err = <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("restoration failed to settle within bounded admission")
			}
			if err == nil || wire.retire.Load() == 0 {
				t.Fatalf("unavailable retirement was not exercised: restore=%v retire=%d", err, wire.retire.Load())
			}
			now, err := direct.Time(t.Context()).Result()
			if err != nil {
				t.Fatal(err)
			}
			if now.UnixMilli() < wire.deadline.Load() {
				t.Fatalf("row released while original packet remained admissible: now=%d deadline=%d", now.UnixMilli(), wire.deadline.Load())
			}
			if _, _, err := plans.StopAttempt(t.Context(), work, uuid.NewString(), nil); err != nil {
				t.Fatal(err)
			}
			if err := directStore.Delete(t.Context(), work); err != nil {
				t.Fatal(err)
			}
			gate.unblock()
			recipeRestoreWaitRestoreGate(t, gate.done)
			if got, ok := directStore.Get(t.Context(), work); ok {
				t.Fatalf("delayed %s after settled STOP recreated %q", stage, got.InputPath)
			}
		})
	}
}

func TestRedisRecipeRestoreDuplicatePacketsCannotReviveStoppedRecipe(t *testing.T) {
	directStore, direct, work := publicationTestStore(t)
	card := playback.RecipeCard{SessionID: work, InputPath: "/approved/old", TranscodeNodeURL: "https://" + work + ".example"}
	if err := directStore.Put(t.Context(), work, card); err != nil {
		t.Fatal(err)
	}
	wire := &recipeRestoreRestoreWire{holdPrepare: recipeRestoreNewRestoreGate(), holdPublish: recipeRestoreNewRestoreGate()}
	client := recipeRestoreRestoreClient(t, direct, work, wire, 2, 35*time.Millisecond)
	restoration, err := NewStore(client, time.Minute).CaptureRestore(t.Context(), work, card)
	if err != nil {
		t.Fatal(err)
	}
	if err := directStore.Delete(t.Context(), work); err != nil {
		t.Fatal(err)
	}
	if err := restoration.Restore(t.Context(), restoration.ExpiresAt()); err != nil {
		t.Fatal(err)
	}
	if wire.prepare.Load() < 2 || wire.publish.Load() < 2 {
		t.Fatalf("automatic retries were not observed prepare=%d publish=%d", wire.prepare.Load(), wire.publish.Load())
	}
	if got, ok := directStore.Get(t.Context(), work); !ok || got.InputPath != card.InputPath {
		t.Fatal("successful restoration missing before stop")
	}
	if err := directStore.Delete(t.Context(), work); err != nil {
		t.Fatal(err)
	}
	wire.holdPrepare.unblock()
	recipeRestoreWaitRestoreGate(t, wire.holdPrepare.done)
	wire.holdPublish.unblock()
	recipeRestoreWaitRestoreGate(t, wire.holdPublish.done)
	if got, ok := directStore.Get(t.Context(), work); ok {
		t.Fatalf("successful original duplicate packets revived stopped recipe %q", got.InputPath)
	}
}

func TestRedisRecipeRestoreDelayedPublicationRespectsOriginalExpiry(t *testing.T) {
	directStore, direct, work := publicationTestStore(t)
	card := playback.RecipeCard{SessionID: work, InputPath: "/approved/old", TranscodeNodeURL: "https://" + work + ".example"}
	if err := directStore.PutTTL(t.Context(), work, card, 350*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	abs, err := direct.PExpireTime(t.Context(), directStore.key(work)).Result()
	if err != nil {
		t.Fatal(err)
	}
	wire := &recipeRestoreRestoreWire{holdPublish: recipeRestoreNewRestoreGate()}
	client := recipeRestoreRestoreClient(t, direct, work, wire, -1, time.Second)
	restoration, err := NewStore(client, time.Minute).CaptureRestore(t.Context(), work, card)
	if err != nil {
		t.Fatal(err)
	}
	if err := directStore.Delete(t.Context(), work); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- restoration.Restore(t.Context(), restoration.ExpiresAt()) }()
	recipeRestoreWaitRestoreGate(t, wire.holdPublish.entered)
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		now, err := direct.Time(t.Context()).Result()
		if err != nil {
			t.Fatal(err)
		}
		if now.UnixMilli() >= abs.Milliseconds() {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("original Redis expiry did not arrive")
		}
	}
	wire.holdPublish.unblock()
	recipeRestoreWaitRestoreGate(t, wire.holdPublish.done)
	if err := <-done; err == nil {
		t.Fatal("publication accepted after original authority expiry")
	}
	if got, ok := directStore.Get(t.Context(), work); ok {
		t.Fatalf("expired delayed publication recreated %q", got.InputPath)
	}
}
