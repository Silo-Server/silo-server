package noderecipe

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func legacyPublicationFixture(t *testing.T) (*Store, *redis.Client, string, string, *AcceptedPublication) {
	t.Helper()
	store, client, work := publicationTestStore(t)
	card := playback.RecipeCard{SessionID: work, InputPath: "/approved/legacy", TranscodeNodeURL: "https://legacy.example.test"}
	if err := store.PutTTL(t.Context(), work, card, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	_, version, err := store.RecipeVersion(t.Context(), work)
	if err != nil {
		t.Fatal(err)
	}
	// PutTTL before publication receipts wrote the same record, version and
	// authority stamps, but did not create this marker.
	if err = client.Del(t.Context(), publicationUndoKeyPrefix+version).Err(); err != nil {
		t.Fatal(err)
	}
	return store, client, work, version, &AcceptedPublication{Scope: "compat-" + work, Card: &card, Started: true}
}

func publicationValuesAndExpiries(t *testing.T, client *redis.Client, keys []string) ([]any, []time.Duration) {
	t.Helper()
	values, err := client.MGet(t.Context(), keys...).Result()
	if err != nil {
		t.Fatal(err)
	}
	expiries := make([]time.Duration, len(keys))
	for i, key := range keys {
		expiries[i], err = client.PExpireTime(t.Context(), key).Result()
		if err != nil {
			t.Fatal(err)
		}
	}
	return values, expiries
}

func TestRedisPublicationRebuildsLegacyAuthorityWithoutChangingExpiry(t *testing.T) {
	store, client, work, old, accepted := legacyPublicationFixture(t)
	keys := []string{store.key(work), publicationVersionKey(work), nodeAuthorityRecordGenerationKey(work), nodeAuthorityRecordDigestKey(work)}
	serverTime, err := client.Time(t.Context()).Result()
	if err != nil {
		t.Fatal(err)
	}
	// Distinct expiries catch a recovery that silently gives every sidecar
	// the recipe's longer remaining lifetime.
	for i, key := range keys[1:] {
		if err := client.PExpireAt(t.Context(), key, serverTime.Add(time.Duration(8-i)*time.Second)).Err(); err != nil {
			t.Fatal(err)
		}
	}
	before, expires := publicationValuesAndExpiries(t, client, keys)
	if err := store.ReconcileAccepted(t.Context(), work, accepted); err != nil {
		t.Fatal("exact legacy authority cannot be reconstructed", err)
	}
	after, afterExpires := publicationValuesAndExpiries(t, client, keys)
	if !reflect.DeepEqual(expires, afterExpires) || before[0] != after[0] || before[2] != after[2] || before[3] != after[3] || before[1] == after[1] {
		t.Fatalf("reconstruction changed bytes/expiry or retained old token: before=%v after=%v expiry=%v/%v", before, after, expires, afterExpires)
	}
	fresh, ok := after[1].(string)
	if !ok || !validPublicationVersion(fresh) {
		t.Fatal("invalid fresh baseline token")
	}
	if marker, err := client.Get(t.Context(), publicationUndoKeyPrefix+old).Result(); err != nil || marker != "retired" {
		t.Fatal("unknown old publication was not retired", marker, err)
	}
	if marker, err := client.Get(t.Context(), publicationUndoKeyPrefix+fresh).Result(); err != nil || marker != "committed" {
		t.Fatal("fresh authoritative baseline missing", marker, err)
	}
	if expiry, err := client.PExpireTime(t.Context(), publicationUndoKeyPrefix+fresh).Result(); err != nil || expiry != expires[1] {
		t.Fatal("baseline marker changed the original version expiry", expiry, err)
	}
	_, terminalExpiry := publicationValuesAndExpiries(t, client, []string{publicationUndoKeyPrefix + old})
	afterTime, err := client.Time(t.Context()).Result()
	if err != nil || terminalExpiry[0].Milliseconds() < serverTime.Add(publicationPrepareTTL).UnixMilli() || terminalExpiry[0].Milliseconds() > afterTime.Add(publicationPrepareTTL).UnixMilli() {
		t.Fatal("old terminal exclusion is not bounded to the preparation window", terminalExpiry, err)
	}
	if owners, err := client.Exists(t.Context(), publicationOwnerKeyPrefix+work).Result(); err != nil || owners != 0 {
		t.Fatal("baseline reconstruction created an owner", owners, err)
	}
	if err := store.ReconcileAccepted(t.Context(), work, accepted); err != nil {
		t.Fatal("reconciliation retry failed", err)
	}
	retried, retryExpiry := publicationValuesAndExpiries(t, client, keys)
	_, terminalRetryExpiry := publicationValuesAndExpiries(t, client, []string{publicationUndoKeyPrefix + old})
	if !reflect.DeepEqual(after, retried) || !reflect.DeepEqual(afterExpires, retryExpiry) || !reflect.DeepEqual(terminalExpiry, terminalRetryExpiry) {
		t.Fatal("reconciliation retry rotated or extended an existing record")
	}
}

func TestRedisPublicationLegacyRebuildKeepsRevokedAuthority(t *testing.T) {
	store, client, work, _, accepted := legacyPublicationFixture(t)
	if err := store.RevokeNode(t.Context(), accepted.Card.TranscodeNodeURL); err != nil {
		t.Fatal(err)
	}
	keys := []string{store.key(work), nodeAuthorityRecordGenerationKey(work), nodeAuthorityRecordDigestKey(work)}
	before, expires := publicationValuesAndExpiries(t, client, keys)
	if err := store.ReconcileAccepted(t.Context(), work, accepted); err != nil {
		t.Fatal(err)
	}
	after, afterExpires := publicationValuesAndExpiries(t, client, keys)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(expires, afterExpires) {
		t.Fatal("reconstruction changed revoked authority")
	}
	if _, found := store.Get(t.Context(), work); found {
		t.Fatal("reconstruction renewed a revoked authority generation")
	}
}

func TestRedisPublicationLegacyRebuildRejectsUnprovenState(t *testing.T) {
	for _, kind := range []string{"nil", "empty-scope", "not-started", "cleared", "different-card", "modern-id", "owner", "invalid-version", "missing-recipe"} {
		t.Run(kind, func(t *testing.T) {
			store, client, work, version, accepted := legacyPublicationFixture(t)
			switch kind {
			case "nil":
				accepted = nil
			case "empty-scope":
				accepted.Scope = ""
			case "not-started":
				accepted.Started = false
			case "cleared":
				accepted.Card = nil
			case "different-card":
				accepted.Card.InputPath = "/approved/different"
			case "modern-id":
				accepted.ID = version
			case "owner":
				if err := client.Set(t.Context(), publicationOwnerKeyPrefix+work, version, time.Second).Err(); err != nil {
					t.Fatal(err)
				}
			case "invalid-version":
				if err := client.Set(t.Context(), publicationVersionKey(work), "invalid", time.Second).Err(); err != nil {
					t.Fatal(err)
				}
			case "missing-recipe":
				if err := client.Del(t.Context(), store.key(work)).Err(); err != nil {
					t.Fatal(err)
				}
			}
			keys := []string{store.key(work), publicationVersionKey(work), nodeAuthorityRecordGenerationKey(work), nodeAuthorityRecordDigestKey(work), publicationUndoKeyPrefix + version, publicationOwnerKeyPrefix + work}
			before, expires := publicationValuesAndExpiries(t, client, keys)
			if err := store.ReconcileAccepted(t.Context(), work, accepted); !errors.Is(err, ErrPublicationUncertain) {
				t.Fatal("unproven state was reconstructed", err)
			}
			after, afterExpires := publicationValuesAndExpiries(t, client, keys)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(expires, afterExpires) {
				t.Fatal("unknown state was changed")
			}
		})
	}
}

func TestRedisPublicationLegacyRebuildFencesLostModernReceipt(t *testing.T) {
	store, client, work := publicationTestStore(t)
	card := playback.RecipeCard{SessionID: work, InputPath: "/approved/identical"}
	if err := store.Put(t.Context(), work, card); err != nil {
		t.Fatal(err)
	}
	accepted := &AcceptedPublication{Scope: "compat-" + work, Card: &card, Started: true}
	token, err := publicationVersion()
	if err != nil {
		t.Fatal(err)
	}
	now, err := client.Time(t.Context()).Result()
	if err != nil {
		t.Fatal(err)
	}
	old := &publication{store: store, work: work, version: token, prepareDeadline: now.Add(300 * time.Millisecond).UnixMilli()}
	evidence, err := acceptedPublicationEvidence(work, accepted)
	if err != nil {
		t.Fatal(err)
	}
	args := append([]any{token, publicationPrepareTTL.Milliseconds(), publicationUndoKeyPrefix, "unused-restore"}, evidence...)
	args = append(args, old.prepareDeadline)
	if n, err := prepareRecipePublicationScript.Run(t.Context(), client, old.keys(), args...).Int(); err != nil || n != 1 {
		t.Fatal("modern preparation failed", err)
	}
	data, err := marshalCard(card)
	if err != nil {
		t.Fatal(err)
	}
	writeArgs := []any{data, (2 * time.Second).Milliseconds(), nodeAuthorityRecordDigest(data), token, "1", publicationPrepareTTL.Milliseconds()}
	writeKeys := append(old.keys(), "")
	if err := publishRecipeScript.Run(t.Context(), client, writeKeys, writeArgs...).Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.Del(t.Context(), old.ownerKey(), old.undoKey()).Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.ReconcileAccepted(t.Context(), work, accepted); err != nil {
		t.Fatal("could not reconstruct exact accepted baseline", err)
	}
	_, fresh, err := store.RecipeVersion(t.Context(), work)
	if err != nil || fresh == old.ID() {
		t.Fatal("unknown modern token was treated as accepted", err)
	}
	if err := old.Commit(t.Context()); !errors.Is(err, ErrPublicationLost) {
		t.Fatal("unknown old candidate was committed", err)
	}
	if err := old.Undo(t.Context()); err != nil {
		t.Fatal(err)
	}
	checkDelayed := func() {
		t.Helper()
		if n, err := prepareRecipePublicationScript.Run(t.Context(), client, old.keys(), args...).Int(); err != nil || n != 0 {
			t.Fatal("delayed old preparation revived unknown candidate", err)
		}
		if err := publishRecipeScript.Run(t.Context(), client, writeKeys, writeArgs...).Err(); err == nil {
			t.Fatal("delayed old publication revived unknown candidate")
		}
		got, current, err := store.RecipeVersion(t.Context(), work)
		if err != nil || got == nil || !reflect.DeepEqual(*got, card) || current != fresh {
			t.Fatal("old command changed reconstructed baseline", err)
		}
	}
	checkDelayed()
	// Expiry at the known original deadline is already safe. Even without a
	// terminal marker, the original commands retain their expired admission.
	if err := client.PExpireAt(t.Context(), old.undoKey(), time.UnixMilli(old.prepareDeadline)).Err(); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		exists, err := client.Exists(t.Context(), old.undoKey()).Result()
		if err != nil {
			t.Fatal(err)
		}
		if exists == 0 {
			break
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("old terminal marker did not expire")
		}
	}
	checkDelayed()
}
