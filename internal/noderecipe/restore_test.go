package noderecipe

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestRedisRecipeRestorePreservesAbsoluteExpiry(t *testing.T) {
	store, client, work := publicationTestStore(t)
	card := playback.RecipeCard{SessionID: work, InputPath: "/approved/predecessor", TranscodeNodeURL: "https://worker.example"}
	if err := store.PutTTL(t.Context(), work, card, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	restoration, err := store.CaptureRestore(t.Context(), work, card)
	if err != nil {
		t.Fatal(err)
	}
	expiry := restoration.ExpiresAt()
	original, err := client.PExpireTime(t.Context(), store.key(work)).Result()
	if err != nil || expiry.UnixMilli() > original.Milliseconds()+1 {
		t.Fatalf("capture extended predecessor expiry: %v %v", expiry, err)
	}
	if err := store.Delete(t.Context(), work); err != nil {
		t.Fatal(err)
	}
	// The durable attempt may expire before the original Redis record.
	expiry = expiry.Add(-time.Second)
	if err := restoration.Restore(t.Context(), expiry); err != nil {
		t.Fatal(err)
	}
	after, err := client.PExpireTime(t.Context(), store.key(work)).Result()
	if err != nil || after.Milliseconds() != expiry.UnixMilli() {
		t.Fatalf("restore expiry=%v want=%v err=%v", after, expiry, err)
	}
	if restored, ok := store.Get(t.Context(), work); !ok || restored.InputPath != card.InputPath {
		t.Fatal("acknowledged restore did not retain predecessor")
	}
	// Ordinary callers still receive their requested TTL and the default
	// publication retains its two-minute admission window.
	if err := store.Delete(t.Context(), work); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTTL(t.Context(), work, card, 20*time.Second); err != nil {
		t.Fatal(err)
	}
	if ttl := client.PTTL(t.Context(), store.key(work)).Val(); ttl < 19*time.Second {
		t.Fatalf("ordinary PutTTL changed: %v", ttl)
	}
	if err := store.Delete(t.Context(), work); err != nil {
		t.Fatal(err)
	}
	receipt, err := store.PublishWithUndo(t.Context(), work, card)
	if err != nil {
		t.Fatal(err)
	}
	serverTime, err := client.Time(t.Context()).Result()
	if err != nil {
		t.Fatal(err)
	}
	remaining := time.UnixMilli(receipt.(*publication).prepareDeadline).Sub(serverTime)
	if remaining < publicationPrepareTTL-time.Second || remaining > publicationPrepareTTL {
		t.Fatalf("ordinary publication admission changed: %v", remaining)
	}
	if err := receipt.Retire(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRedisRecipeRestoreCaptureRejectsChangedOrExpiredAuthority(t *testing.T) {
	store, _, work := publicationTestStore(t)
	card := playback.RecipeCard{SessionID: work, InputPath: "/approved/predecessor"}
	if err := store.Put(t.Context(), work, card); err != nil {
		t.Fatal(err)
	}
	changed := card
	changed.InputPath = "/approved/successor"
	if _, err := store.CaptureRestore(t.Context(), work, changed); err == nil {
		t.Fatal("captured a different predecessor")
	}
	restoration, err := store.CaptureRestore(t.Context(), work, card)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(t.Context(), work); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CaptureRestore(t.Context(), work, card); err == nil {
		t.Fatal("captured missing authority")
	}
	if err := restoration.Restore(t.Context(), time.Now().Add(-time.Second)); err == nil {
		t.Fatal("restored expired authority")
	}
	if _, found := store.Get(t.Context(), work); found {
		t.Fatal("expired restoration created authority")
	}
}

func TestRedisRecipeRestoreCannotRestampRevokedNode(t *testing.T) {
	store, _, work := publicationTestStore(t)
	card := playback.RecipeCard{SessionID: work, InputPath: "/approved/predecessor", TranscodeNodeURL: "https://" + work + ".example"}
	if err := store.Put(t.Context(), work, card); err != nil {
		t.Fatal(err)
	}
	restoration, err := store.CaptureRestore(t.Context(), work, card)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeNode(t.Context(), card.TranscodeNodeURL); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(t.Context(), work); err != nil {
		t.Fatal(err)
	}
	if err := restoration.Restore(t.Context(), restoration.ExpiresAt()); err == nil {
		t.Error("restoration blessed a revoked predecessor with the new epoch")
	}
	if _, found := store.Get(t.Context(), work); found {
		t.Fatal("revoked predecessor became reconstructable")
	}
}

func TestRedisRecipeRestoreCapturePreservesAuthorityBoundaries(t *testing.T) {
	for _, mode := range []string{"revoke-before-capture", "node-id-revoked", "shorter-sidecars", "legacy", "legacy-revoked"} {
		t.Run(mode, func(t *testing.T) {
			store, client, work := publicationTestStore(t)
			card := playback.RecipeCard{SessionID: work, InputPath: "/approved/predecessor", TranscodeNodeURL: "https://" + work + ".example"}
			if mode == "node-id-revoked" {
				card.RoutingExecutionNodeID = int(time.Now().UnixNano()%1_000_000_000) + 1
			}
			if err := store.Put(t.Context(), work, card); err != nil {
				t.Fatal(err)
			}
			if mode == "legacy" || mode == "legacy-revoked" {
				if err := client.Del(t.Context(), nodeAuthorityRecordGenerationKey(work), nodeAuthorityRecordDigestKey(work)).Err(); err != nil {
					t.Fatal(err)
				}
			}
			if _, found, err := store.GetStatus(t.Context(), work); err != nil || !found {
				t.Fatalf("pre-capture read failed: found=%v err=%v", found, err)
			}
			revoked := mode == "revoke-before-capture" || mode == "node-id-revoked" || mode == "legacy-revoked"
			if revoked {
				var err error
				if mode == "node-id-revoked" {
					err = store.RevokeNodeID(t.Context(), card.RoutingExecutionNodeID)
				} else {
					err = store.RevokeNode(t.Context(), card.TranscodeNodeURL)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			var sidecarExpiry time.Duration
			if mode == "shorter-sidecars" {
				if err := client.PExpire(t.Context(), nodeAuthorityRecordGenerationKey(work), 1500*time.Millisecond).Err(); err != nil {
					t.Fatal(err)
				}
				if err := client.PExpire(t.Context(), nodeAuthorityRecordDigestKey(work), 2500*time.Millisecond).Err(); err != nil {
					t.Fatal(err)
				}
				sidecarExpiry = client.PExpireTime(t.Context(), nodeAuthorityRecordGenerationKey(work)).Val()
			}
			restoration, err := store.CaptureRestore(t.Context(), work, card)
			if revoked {
				if err == nil {
					t.Fatal("capture blessed an already revoked predecessor")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "shorter-sidecars" && restoration.ExpiresAt().UnixMilli() > sidecarExpiry.Milliseconds()+1 {
				t.Fatal("capture extended a shorter authority sidecar")
			}
			if err := store.Delete(t.Context(), work); err != nil {
				t.Fatal(err)
			}
			if err := restoration.Restore(t.Context(), restoration.ExpiresAt()); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{store.key(work), nodeAuthorityRecordGenerationKey(work), nodeAuthorityRecordDigestKey(work)} {
				if expiry := client.PExpireTime(t.Context(), key).Val(); expiry.Milliseconds() != restoration.ExpiresAt().UnixMilli() {
					t.Fatalf("restored authority expiry=%v want=%v", expiry, restoration.ExpiresAt())
				}
			}
		})
	}
}
