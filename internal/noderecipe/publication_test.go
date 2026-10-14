package noderecipe

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func publicationTestStore(t *testing.T) (*Store, *redis.Client, string) {
	t.Helper()
	endpoint := os.Getenv("SILO_TEST_REDIS_URL")
	if endpoint == "" {
		t.Skip("requires task Redis SILO_TEST_REDIS_URL")
	}
	opts, err := redis.ParseURL(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })
	store := NewStore(client, time.Minute)
	work := "publication-undo-" + uuid.NewString()
	t.Cleanup(func() { _ = store.Delete(context.Background(), work) })
	return store, client, work
}

func TestRedisPublicationUndoPreservesSuccessors(t *testing.T) {
	for _, identical := range []bool{false, true} {
		t.Run(map[bool]string{false: "different", true: "identical"}[identical], func(t *testing.T) {
			store, _, work := publicationTestStore(t)
			candidate := playback.RecipeCard{SessionID: work, InputPath: "/approved/candidate", TranscodeNodeURL: "https://worker.example.test"}
			undo, err := store.PublishWithUndo(t.Context(), work, candidate)
			if err != nil {
				t.Fatal(err)
			}
			successor := candidate
			if !identical {
				successor.InputPath = "/approved/successor"
			}
			if err = store.Put(t.Context(), work, successor); err != nil {
				t.Fatal(err)
			}
			_, version, err := store.RecipeVersion(t.Context(), work)
			if err != nil {
				t.Fatal(err)
			}
			if err = undo.Undo(t.Context()); err != nil {
				t.Fatal(err)
			}
			got, current, err := store.RecipeVersion(t.Context(), work)
			if err != nil || got == nil || !reflect.DeepEqual(*got, successor) || current != version {
				t.Fatalf("undo replaced successor: got=%+v err=%v", got, err)
			}
		})
	}
}

func TestRedisPublicationUndoRestoresExactPriorAuthorityAndExpiry(t *testing.T) {
	store, client, work := publicationTestStore(t)
	prior := playback.RecipeCard{SessionID: work, InputPath: "/approved/prior", TranscodeNodeURL: "https://prior.example.test"}
	if err := store.PutTTL(t.Context(), work, prior, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	keys := []string{store.key(work), nodeAuthorityRecordGenerationKey(work), nodeAuthorityRecordDigestKey(work)}
	before, err := client.MGet(t.Context(), keys...).Result()
	if err != nil {
		t.Fatal(err)
	}
	expires, err := client.PExpireTime(t.Context(), store.key(work)).Result()
	if err != nil {
		t.Fatal(err)
	}
	_, oldVersion, err := store.RecipeVersion(t.Context(), work)
	if err != nil {
		t.Fatal(err)
	}
	candidate := prior
	candidate.InputPath = "/approved/candidate"
	candidate.TranscodeNodeURL = "https://new.example.test"
	receipt, err := store.PublishWithUndo(t.Context(), work, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RevokeNode(t.Context(), prior.TranscodeNodeURL); err != nil {
		t.Fatal(err)
	}
	if err = receipt.Undo(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, err := client.MGet(t.Context(), keys...).Result()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("changed authority bytes: before=%v after=%v err=%v", before, after, err)
	}
	restoredExpiry, err := client.PExpireTime(t.Context(), store.key(work)).Result()
	if err != nil || restoredExpiry != expires {
		t.Fatalf("undo extended old expiry: before=%v after=%v err=%v", expires, restoredExpiry, err)
	}
	_, newVersion, err := store.RecipeVersion(t.Context(), work)
	if err != nil || oldVersion == newVersion {
		t.Fatal("restoration reused stale publication token", err)
	}
	if _, ok := store.Get(t.Context(), work); ok {
		t.Fatal("undo resurrected authority revoked between publish and undo")
	}
	if err = receipt.Undo(t.Context()); err != nil {
		t.Fatal("undo is not idempotent", err)
	}
}

func TestRedisPublicationUndoRemovesNewOrExpiredPrior(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "expired"}[expired], func(t *testing.T) {
			store, client, work := publicationTestStore(t)
			card := playback.RecipeCard{SessionID: work, InputPath: "/approved/media", TranscodeNodeURL: "https://worker.example.test"}
			var priorExpiry time.Time
			if expired {
				if err := store.PutTTL(t.Context(), work, card, 120*time.Millisecond); err != nil {
					t.Fatal(err)
				}
				milliseconds, err := client.PExpireTime(t.Context(), store.key(work)).Result()
				if err != nil {
					t.Fatal(err)
				}
				priorExpiry = time.UnixMilli(int64(milliseconds / time.Millisecond))
			}
			receipt, err := store.PublishWithUndo(t.Context(), work, card)
			if err != nil {
				t.Fatal(err)
			}
			if expired {
				// Wait for the captured absolute prior expiry, while the candidate stays live.
				for time.Now().Before(priorExpiry) {
					time.Sleep(time.Millisecond)
				}
			}
			if err = receipt.Undo(t.Context()); err != nil {
				t.Fatal(err)
			}
			if exists, err := client.Exists(t.Context(), store.key(work), publicationVersionKey(work), nodeAuthorityRecordGenerationKey(work), nodeAuthorityRecordDigestKey(work)).Result(); err != nil || exists != 0 {
				t.Fatal("undo retained expired/new recipe or sidecars", exists, err)
			}
		})
	}
}

func TestPublicationUndoDisabledAndCanceled(t *testing.T) {
	for _, store := range []*Store{nil, NewStore(nil, 0)} {
		receipt, err := store.PublishWithUndo(t.Context(), "work", playback.RecipeCard{})
		if err != nil {
			t.Fatal(err)
		}
		if err = receipt.Undo(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err = receipt.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	store, _, work := publicationTestStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	receipt, err := store.PublishWithUndo(ctx, work, playback.RecipeCard{SessionID: work})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("canceled publication succeeded", err)
	}
	if receipt == nil {
		t.Fatal("failed write returned no conditional undo")
	}
	if err = receipt.Undo(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// Lose a successful script response after Redis has actually committed it.
// This differs from cancellation before dispatch, when there is nothing to undo.
type lostPublicationResponse struct {
	armed  bool
	target string
}

func (*lostPublicationResponse) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h *lostPublicationResponse) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		err := next(ctx, cmd)
		if err == nil && h.armed && cmd.Name() == "evalsha" && cmd.Args()[1] == h.target {
			h.armed = false
			return context.DeadlineExceeded
		}
		return err
	}
}
func (*lostPublicationResponse) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func TestRedisPublicationUndoRecoversLostCommittedWriteResponse(t *testing.T) {
	store, client, work := publicationTestStore(t)
	prior := playback.RecipeCard{SessionID: work, InputPath: "/approved/prior", TranscodeNodeURL: "https://prior.example.test"}
	if err := store.Put(t.Context(), work, prior); err != nil {
		t.Fatal(err)
	}
	// Put warms the same publication Lua script before arming this response fault.
	hook := &lostPublicationResponse{armed: true, target: publishRecipeScript.Hash()}
	client.AddHook(hook)
	candidate := prior
	candidate.InputPath = "/approved/candidate"
	receipt, err := store.PublishWithUndo(t.Context(), work, candidate)
	if !errors.Is(err, context.DeadlineExceeded) || receipt == nil {
		t.Fatal("lost response did not retain undo capability", err)
	}
	actual, ok := store.Get(t.Context(), work)
	if !ok || actual.InputPath != candidate.InputPath {
		t.Fatal("fault did not follow a real Redis commit")
	}
	if err = receipt.Undo(t.Context()); err != nil {
		t.Fatal(err)
	}
	restored, ok := store.Get(t.Context(), work)
	if !ok || !reflect.DeepEqual(*restored, prior) {
		t.Fatalf("lost-response undo did not restore actual prior record: %+v", restored)
	}
}

func TestRedisPublicationCommitCleansBoundedReceipt(t *testing.T) {
	store, client, work := publicationTestStore(t)
	card := playback.RecipeCard{SessionID: work, InputPath: "/approved/media"}
	receipt, err := store.PublishWithUndo(t.Context(), work, card)
	if err != nil {
		t.Fatal(err)
	}
	key := receipt.(*publication).undoKey()
	ttl, err := client.PTTL(t.Context(), key).Result()
	if err != nil || ttl <= 0 || ttl > publicationPrepareTTL {
		t.Fatal("undo receipt is not bounded", ttl, err)
	}
	if err = receipt.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if marker, err := client.Get(t.Context(), key).Result(); err != nil || marker != "committed" {
		t.Fatal("committed publication retained prior recipe bytes", err)
	}
	if got, ok := store.Get(t.Context(), work); !ok || got.InputPath != card.InputPath {
		t.Fatal("commit changed published recipe")
	}
}

func TestRedisPublicationUndoFencesAWriteThatArrivesAfterCancellation(t *testing.T) {
	store, client, work := publicationTestStore(t)
	if err := publishRecipeScript.Load(t.Context(), client).Err(); err != nil {
		t.Fatal(err)
	}
	// Dispatch cancellation before the write leaves the process, then simulate a
	// previously sent command arriving at Redis after Undo has already returned.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	card := playback.RecipeCard{SessionID: work, InputPath: "/approved/late-candidate"}
	receipt, err := store.PublishWithUndo(ctx, work, card)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err = receipt.Undo(t.Context()); err != nil {
		t.Fatal(err)
	}
	p := receipt.(*publication)
	data, err := marshalCard(card)
	if err != nil {
		t.Fatal(err)
	}
	err = publishRecipeScript.Run(t.Context(), client, append(p.keys(), ""), data, store.ttl.Milliseconds(), nodeAuthorityRecordDigest(data), p.version, "1", publicationPrepareTTL.Milliseconds()).Err()
	if err == nil {
		t.Fatal("late write accepted a missing preparation receipt")
	}
	if _, ok := store.Get(t.Context(), work); ok {
		t.Fatal("late canceled write resurrected reconstruction authority")
	}
}

func TestRedisPublicationRequiresAnUnexpiredPreparation(t *testing.T) {
	store, client, work := publicationTestStore(t)
	token, err := publicationVersion()
	if err != nil {
		t.Fatal(err)
	}
	receipt := &publication{store: store, work: work, version: token}
	if err = client.Set(t.Context(), receipt.undoKey(), "prepared", 20*time.Millisecond).Err(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		exists, err := client.Exists(t.Context(), receipt.undoKey()).Result()
		if err != nil {
			t.Fatal(err)
		}
		if exists == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("preparation did not expire")
		}
		time.Sleep(time.Millisecond)
	}
	card := playback.RecipeCard{SessionID: work, InputPath: "/approved/expired-candidate"}
	data, err := marshalCard(card)
	if err != nil {
		t.Fatal(err)
	}
	err = publishRecipeScript.Run(t.Context(), client, append(receipt.keys(), ""), data, store.ttl.Milliseconds(), nodeAuthorityRecordDigest(data), receipt.version, "1", publicationPrepareTTL.Milliseconds()).Err()
	if err == nil {
		t.Fatal("delayed write accepted an expired preparation receipt")
	}
	if _, ok := store.Get(t.Context(), work); ok {
		t.Fatal("expired preparation created reconstruction authority")
	}
}

func TestRedisPublicationRetainsUndoForTheCandidatesLifetime(t *testing.T) {
	store, client, work := publicationTestStore(t)
	store.ttl = 5 * time.Minute
	receipt, err := store.PublishWithUndo(t.Context(), work, playback.RecipeCard{SessionID: work, InputPath: "/approved/media"})
	if err != nil {
		t.Fatal(err)
	}
	candidateExpiry, err := client.PExpireTime(t.Context(), store.key(work)).Result()
	if err != nil {
		t.Fatal(err)
	}
	undoExpiry, err := client.PExpireTime(t.Context(), receipt.(*publication).undoKey()).Result()
	if err != nil || undoExpiry != candidateExpiry {
		t.Fatal("undo expires before the candidate it must compensate", err)
	}
	if err = receipt.Undo(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRedisPublicationBusyCannotChangeAnotherOwner(t *testing.T) {
	store, client, work := publicationTestStore(t)
	card := playback.RecipeCard{SessionID: work, InputPath: "/approved/first"}
	first, err := store.PublishWithUndo(t.Context(), work, card)
	if err != nil {
		t.Fatal(err)
	}
	card.InputPath = "/approved/second"
	second, err := store.PublishWithUndo(t.Context(), work, card)
	if !errors.Is(err, ErrPublicationBusy) {
		t.Fatal("overlap was allowed to publish", err)
	}
	if err = second.Undo(t.Context()); err != nil {
		t.Fatal(err)
	}
	owner, err := client.Get(t.Context(), first.(*publication).ownerKey()).Result()
	if err != nil || owner != first.(*publication).version {
		t.Fatal("failed competitor released the live owner's fence", err)
	}
	if got, ok := store.Get(t.Context(), work); !ok || got.InputPath != "/approved/first" {
		t.Fatal("failed competitor changed the recipe")
	}
	if err = first.Undo(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRedisPublicationExpiredOwnerFailuresDoNotResurrectCandidate(t *testing.T) {
	for _, priorExists := range []bool{false, true} {
		for _, firstUndoFirst := range []bool{false, true} {
			name := "absent"
			if priorExists {
				name = "committed-prior"
			}
			if firstUndoFirst {
				name += "/first-undo-first"
			} else {
				name += "/second-undo-first"
			}
			t.Run(name, func(t *testing.T) {
				store, client, work := publicationTestStore(t)
				card := playback.RecipeCard{SessionID: work, InputPath: "/approved/prior", TranscodeNodeURL: "https://worker.example.test"}
				if priorExists {
					if err := store.Put(t.Context(), work, card); err != nil {
						t.Fatal(err)
					}
				}
				accepted := &AcceptedPublication{Scope: work}
				if priorExists {
					prior := card
					accepted.Card = &prior
					accepted.ID = "committed-prior"
					accepted.Started = true
				}
				card.InputPath = "/approved/failed-first"
				first, err := store.PublishWithUndo(t.Context(), work, card, accepted)
				if err != nil {
					t.Fatal(err)
				}
				if err = client.PExpire(t.Context(), first.(*publication).ownerKey(), time.Millisecond).Err(); err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(time.Second)
				for {
					n, err := client.Exists(t.Context(), first.(*publication).ownerKey()).Result()
					if err != nil {
						t.Fatal(err)
					}
					if n == 0 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("owner did not expire")
					}
					time.Sleep(time.Millisecond)
				}
				card.InputPath = "/approved/failed-second"
				second, err := store.PublishWithUndo(t.Context(), work, card, accepted)
				if err != nil {
					t.Fatal(err)
				}
				order := []Publication{second, first}
				if firstUndoFirst {
					order = []Publication{first, second}
				}
				for _, receipt := range order {
					if err = receipt.Undo(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
				got, found := store.Get(t.Context(), work)
				if !priorExists && found {
					t.Fatalf("two failures revived authority: %+v", got)
				}
				if priorExists && (!found || got.InputPath != "/approved/prior") {
					t.Fatalf("two failures lost committed predecessor: %+v", got)
				}
			})
		}
	}
}

func TestRedisPublicationCommitAcknowledgementLossIsIdempotent(t *testing.T) {
	store, client, work := publicationTestStore(t)
	receipt, err := store.PublishWithUndo(t.Context(), work, playback.RecipeCard{SessionID: work, InputPath: "/approved/accepted"})
	if err != nil {
		t.Fatal(err)
	}
	if err = commitRecipePublicationScript.Load(t.Context(), client).Err(); err != nil {
		t.Fatal(err)
	}
	client.AddHook(&lostPublicationResponse{armed: true, target: commitRecipePublicationScript.Hash()})
	if err = receipt.Commit(t.Context()); err != nil {
		t.Fatal("lost commit response was confused with ownership loss", err)
	}
	if err = receipt.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = receipt.Undo(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, ok := store.Get(t.Context(), work); !ok || got.InputPath != "/approved/accepted" {
		t.Fatal("uncertain commit compensation removed accepted authority")
	}
}

func TestRedisPublicationExpiredOwnerCannotCommitOverRecoveredSuccessor(t *testing.T) {
	store, client, work := publicationTestStore(t)
	card := playback.RecipeCard{SessionID: work, InputPath: "/approved/abandoned"}
	accepted := &AcceptedPublication{Scope: work}
	first, err := store.PublishWithUndo(t.Context(), work, card, accepted)
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Del(t.Context(), first.(*publication).ownerKey()).Err(); err != nil {
		t.Fatal(err)
	}
	card.InputPath = "/approved/successor"
	second, err := store.PublishWithUndo(t.Context(), work, card, accepted)
	if err != nil {
		t.Fatal(err)
	}
	if err = first.Commit(t.Context()); !errors.Is(err, ErrPublicationLost) {
		t.Fatal("expired owner committed over recovered successor", err)
	}
	if err = first.Undo(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = second.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, ok := store.Get(t.Context(), work); !ok || got.InputPath != card.InputPath {
		t.Fatal("late expired owner changed the accepted successor")
	}
}

func TestRedisPublicationRecoveryRequiresExactAuthoritativeEvidence(t *testing.T) {
	store, client, work := publicationTestStore(t)
	before := &AcceptedPublication{Scope: "compat-row", Started: false}
	card := playback.RecipeCard{SessionID: work, InputPath: "/approved/undecided"}
	first, err := store.PublishWithUndo(t.Context(), work, card, before)
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Del(t.Context(), first.(*publication).ownerKey()).Err(); err != nil {
		t.Fatal(err)
	}
	different := card
	different.InputPath = "/approved/mismatched"
	for _, accepted := range []*AcceptedPublication{nil, {Scope: "wrong-row"}, {Scope: "compat-row", ID: first.ID(), Started: false}, {Scope: "compat-row", ID: first.ID(), Started: true, Card: &different}} {
		if err = store.ReconcileAccepted(t.Context(), work, accepted); !errors.Is(err, ErrPublicationUncertain) {
			t.Fatal("unproven evidence resolved a pending publication", err)
		}
		got, version, err := store.RecipeVersion(t.Context(), work)
		if err != nil || got == nil || got.InputPath != card.InputPath || version != first.ID() {
			t.Fatal("unproven recovery changed candidate", err)
		}
	}
	accepted := &AcceptedPublication{Scope: "compat-row", ID: first.ID(), Started: true, Card: &card}
	if err = store.ReconcileAccepted(t.Context(), work, accepted); err != nil {
		t.Fatal(err)
	}
	if err = first.Undo(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, ok := store.Get(t.Context(), work); !ok || got.InputPath != card.InputPath {
		t.Fatal("accepted recovery allowed late undo")
	}
}

func TestRedisPublicationMissingReceiptDoesNotGuessAcceptedState(t *testing.T) {
	store, client, work := publicationTestStore(t)
	card := playback.RecipeCard{SessionID: work, InputPath: "/approved/prior"}
	if err := store.Put(t.Context(), work, card); err != nil {
		t.Fatal(err)
	}
	_, version, err := store.RecipeVersion(t.Context(), work)
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Del(t.Context(), publicationUndoKeyPrefix+version).Err(); err != nil {
		t.Fatal(err)
	}
	accepted := &AcceptedPublication{Scope: work, ID: version, Card: &card, Started: true}
	if err = store.ReconcileAccepted(t.Context(), work, accepted); !errors.Is(err, ErrPublicationUncertain) {
		t.Fatal("missing receipt was assumed committed", err)
	}
	got, current, err := store.RecipeVersion(t.Context(), work)
	if err != nil || got == nil || current != version {
		t.Fatal("unknown publication was mutated", err)
	}
}

func TestLegacyAdoptedPublicationCanBeReplaced(t *testing.T) {
	store, client, work := publicationTestStore(t)
	card := playback.RecipeCard{SessionID: work, InputPath: "/approved/legacy"}
	if err := store.Put(t.Context(), work, card); err != nil {
		t.Fatal(err)
	}
	if err := client.Del(t.Context(), publicationVersionKey(work)).Err(); err != nil {
		t.Fatal(err)
	}
	_, _, err := store.RecipeVersion(t.Context(), work)
	if err != nil {
		t.Fatal(err)
	}
	prior := card
	card.InputPath = "/approved/replacement"
	receipt, err := store.PublishWithUndo(t.Context(), work, card, &AcceptedPublication{Scope: work, Card: &prior, Started: true})
	if err != nil {
		t.Fatal("adopted legacy publication cannot be replaced", err)
	}
	if err = receipt.Undo(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRecipeVersionDoesNotAdoptPendingPublicationWithMissingVersion(t *testing.T) {
	store, client, work := publicationTestStore(t)
	candidate := playback.RecipeCard{SessionID: work, InputPath: "/approved/pending"}
	receipt, err := store.PublishWithUndo(t.Context(), work, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Del(t.Context(), publicationVersionKey(work)).Err(); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.RecipeVersion(t.Context(), work); err == nil {
		t.Fatal("pending receipt was adopted as a committed legacy recipe")
	}
	if owner, err := client.Get(t.Context(), publicationOwnerKeyPrefix+work).Result(); err != nil || owner != receipt.ID() {
		t.Fatal("legacy adoption changed the pending publication owner", err)
	}
	if marker, err := client.Get(t.Context(), publicationUndoKeyPrefix+receipt.ID()).Result(); err != nil || marker == "committed" {
		t.Fatal("legacy adoption finalized an unresolved candidate", err)
	}
	if err = receipt.Undo(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRedisPublicationRetireOnlyRemovesExactRejectedCandidate(t *testing.T) {
	for _, successor := range []string{"none", "different", "identical", "legacy-same-version"} {
		t.Run(successor, func(t *testing.T) {
			store, client, work := publicationTestStore(t)
			prior := playback.RecipeCard{SessionID: work, InputPath: "/approved/prior"}
			if err := store.Put(t.Context(), work, prior); err != nil {
				t.Fatal(err)
			}
			candidate := prior
			candidate.InputPath = "/approved/rejected"
			receipt, err := store.PublishWithUndo(t.Context(), work, candidate)
			if err != nil {
				t.Fatal(err)
			}
			expectedVersion := receipt.ID()
			if successor != "none" {
				next := candidate
				if successor != "identical" {
					next.InputPath = "/approved/successor"
				}
				if successor == "legacy-same-version" {
					data, e := marshalCard(next)
					if e != nil {
						t.Fatal(e)
					}
					if e = client.Set(t.Context(), store.key(work), data, time.Minute).Err(); e != nil {
						t.Fatal(e)
					}
				} else if err = store.Put(t.Context(), work, next); err != nil {
					t.Fatal(err)
				}
				_, expectedVersion, err = store.RecipeVersion(t.Context(), work)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err = receipt.Retire(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err = receipt.Retire(t.Context()); err != nil {
				t.Fatal("late retirement retry failed", err)
			}
			got, found := store.Get(t.Context(), work)
			if successor == "none" {
				if found {
					t.Fatalf("retirement restored prior or retained rejected recipe: %+v", got)
				}
			} else {
				_, version, e := store.RecipeVersion(t.Context(), work)
				if e != nil || !found || version != expectedVersion || successor != "identical" && got.InputPath != "/approved/successor" {
					t.Fatalf("retirement changed successor: got=%+v version=%s error=%v", got, version, e)
				}
			}
		})
	}
}
func TestRedisPublicationRetirePreservesCommittedCandidate(t *testing.T) {
	store, _, work := publicationTestStore(t)
	card := playback.RecipeCard{SessionID: work, InputPath: "/approved/accepted"}
	receipt, err := store.PublishWithUndo(t.Context(), work, card)
	if err != nil {
		t.Fatal(err)
	}
	if err = receipt.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = receipt.Retire(t.Context()); !errors.Is(err, ErrPublicationLost) {
		t.Fatal("committed recipe was treated as rejected", err)
	}
	if got, found := store.Get(t.Context(), work); !found || got.InputPath != card.InputPath {
		t.Fatal("retirement deleted an accepted publication")
	}
}
func TestRedisPublicationRetireRecoversLostReplyAndFencesLateOwner(t *testing.T) {
	store, client, work := publicationTestStore(t)
	card := playback.RecipeCard{SessionID: work, InputPath: "/approved/rejected"}
	receipt, err := store.PublishWithUndo(t.Context(), work, card)
	if err != nil {
		t.Fatal(err)
	}
	if err = retireRecipePublicationScript.Load(t.Context(), client).Err(); err != nil {
		t.Fatal(err)
	}
	hook := &lostPublicationResponse{armed: true, target: retireRecipePublicationScript.Hash()}
	client.AddHook(hook)
	if err = receipt.Retire(t.Context()); err != nil {
		t.Fatal("retirement did not reconcile actual lost reply", err)
	}
	if hook.armed {
		t.Fatal("retirement response fault did not follow Redis execution")
	}
	if _, found := store.Get(t.Context(), work); found {
		t.Fatal("rejected candidate survived actual retirement")
	}
	successor, err := store.PublishWithUndo(t.Context(), work, card)
	if err != nil {
		t.Fatal(err)
	}
	if err = receipt.Retire(t.Context()); err != nil {
		t.Fatal(err)
	}
	if owner, e := client.Get(t.Context(), publicationOwnerKeyPrefix+work).Result(); e != nil || owner != successor.ID() {
		t.Fatal("late retirement released successor owner", e)
	}
	data, err := marshalCard(card)
	if err != nil {
		t.Fatal(err)
	}
	old := receipt.(*publication)
	if err = publishRecipeScript.Run(t.Context(), client, append(old.keys(), nodeAuthorityGenerationKeyForCard(card)), data, time.Minute.Milliseconds(), nodeAuthorityRecordDigest(data), old.ID(), "1", publicationPrepareTTL.Milliseconds()).Err(); err == nil {
		t.Fatal("late publication resurrected retired owner")
	}
	_, version, err := store.RecipeVersion(t.Context(), work)
	if err != nil || version != successor.ID() {
		t.Fatal("late publisher replaced successor", err)
	}
	if err = successor.Undo(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRedisPublicationTerminalPreparationCannotBeReusedAfterDeadline(t *testing.T) {
	for _, finish := range []string{"undo", "retire", "commit", "reconcile"} {
		t.Run(finish, func(t *testing.T) {
			store, client, work := publicationTestStore(t)
			token, err := publicationVersion()
			if err != nil {
				t.Fatal(err)
			}
			now, err := client.Time(t.Context()).Result()
			if err != nil {
				t.Fatal(err)
			}
			receipt := &publication{store: store, work: work, version: token, prepareDeadline: now.Add(300 * time.Millisecond).UnixMilli()}
			prior := &AcceptedPublication{Scope: work}
			evidence, err := acceptedPublicationEvidence(work, prior)
			if err != nil {
				t.Fatal(err)
			}
			args := append([]any{token, publicationPrepareTTL.Milliseconds(), publicationUndoKeyPrefix, "restore-probe"}, evidence...)
			args = append(args, receipt.prepareDeadline)
			if n, e := prepareRecipePublicationScript.Run(t.Context(), client, receipt.keys(), args...).Int(); e != nil || n != 1 {
				t.Fatal("could not prepare fixture", e)
			}
			candidate := playback.RecipeCard{SessionID: work, InputPath: "/approved/rejected-deadline"}
			data, err := marshalCard(candidate)
			if err != nil {
				t.Fatal(err)
			}
			writeArgs := []any{data, (2 * time.Second).Milliseconds(), nodeAuthorityRecordDigest(data), token, "1", publicationPrepareTTL.Milliseconds()}
			keys := append(receipt.keys(), "")
			if err = publishRecipeScript.Run(t.Context(), client, keys, writeArgs...).Err(); err != nil {
				t.Fatal(err)
			}
			switch finish {
			case "undo":
				err = receipt.Undo(t.Context())
			case "retire":
				err = receipt.Retire(t.Context())
			case "commit":
				err = receipt.Commit(t.Context())
			case "reconcile":
				err = store.ReconcileAccepted(t.Context(), work, prior)
			}
			if err != nil {
				t.Fatal(err)
			}
			next := candidate
			next.InputPath = "/approved/successor-after-" + finish
			if err = store.Put(t.Context(), work, next); err != nil {
				t.Fatal(err)
			}
			_, version, err := store.RecipeVersion(t.Context(), work)
			if err != nil {
				t.Fatal(err)
			}
			// Marker expiry at the admission deadline is a legal bounded cleanup. Once
			// gone, the deadline itself must reject BOTH delayed original commands.
			if err = client.PExpireAt(t.Context(), receipt.undoKey(), time.UnixMilli(receipt.prepareDeadline)).Err(); err != nil {
				t.Fatal(err)
			}
			bound := time.Now().Add(time.Second)
			for {
				n, e := client.Exists(t.Context(), receipt.undoKey()).Result()
				if e != nil {
					t.Fatal(e)
				}
				if n == 0 {
					break
				}
				if time.Now().After(bound) {
					t.Fatal("terminal marker did not expire")
				}
				time.Sleep(time.Millisecond)
			}
			if n, e := prepareRecipePublicationScript.Run(t.Context(), client, receipt.keys(), args...).Int(); e != nil || n != 0 {
				t.Fatal("late original prepare reused expired terminal attempt", e)
			}
			if err = publishRecipeScript.Run(t.Context(), client, keys, writeArgs...).Err(); err == nil {
				t.Fatal("late original publication reused expired terminal attempt")
			}
			got, current, err := store.RecipeVersion(t.Context(), work)
			if err != nil || got == nil || got.InputPath != next.InputPath || current != version {
				t.Fatal("late command pair replaced successor", err)
			}
		})
	}
}
func TestRedisPublicationUndoBeforeUncertainPrepareRetiresItsAdmissionID(t *testing.T) {
	store, client, work := publicationTestStore(t)
	token, err := publicationVersion()
	if err != nil {
		t.Fatal(err)
	}
	now, err := client.Time(t.Context()).Result()
	if err != nil {
		t.Fatal(err)
	}
	receipt := &publication{store: store, work: work, version: token, prepareDeadline: now.Add(publicationPrepareTTL).UnixMilli()}
	if err = receipt.Undo(t.Context()); err != nil {
		t.Fatal(err)
	}
	evidence, err := acceptedPublicationEvidence(work, nil)
	if err != nil {
		t.Fatal(err)
	}
	args := append([]any{token, publicationPrepareTTL.Milliseconds(), publicationUndoKeyPrefix, "restored"}, evidence...)
	args = append(args, receipt.prepareDeadline)
	if n, e := prepareRecipePublicationScript.Run(t.Context(), client, receipt.keys(), args...).Int(); e != nil || n != 0 {
		t.Fatal("delayed uncertain Prepare reused a retired admission ID", e)
	}
	if expiry, e := client.PExpireTime(t.Context(), receipt.undoKey()).Result(); e != nil || expiry.Milliseconds() < receipt.prepareDeadline {
		t.Fatal("uncertain prepare retirement expires before its admission window", e)
	}
}
