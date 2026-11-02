package catalog

import (
	"testing"
	"time"
)

func TestPersonalCollectionReadMemo(t *testing.T) {
	var memo personalCollectionReadMemo[int]
	key := personalCollectionReadKey{userID: 1, collectionID: "c", accessKey: "a"}
	now := time.Now()

	if _, ok := memo.get(key, 1, now); ok {
		t.Fatal("an empty memo remembered a value")
	}
	memo.put(key, 1, 7, now)
	if got, ok := memo.get(key, 1, now.Add(personalCollectionReadMemoTTL-time.Second)); !ok || got != 7 {
		t.Fatalf("get = %d, %v; want 7, true", got, ok)
	}
	if _, ok := memo.get(key, 2, now); ok {
		t.Fatal("a value was served for another revision")
	}
	if _, ok := memo.get(personalCollectionReadKey{userID: 1, collectionID: "c", accessKey: "b"}, 1, now); ok {
		t.Fatal("a value was served for another viewer access")
	}
	if _, ok := memo.get(key, 1, now.Add(personalCollectionReadMemoTTL)); ok {
		t.Fatal("an expired value was served")
	}
}

func TestPersonalCollectionReadMemoStaysBounded(t *testing.T) {
	var memo personalCollectionReadMemo[struct{}]
	now := time.Now()
	for i := range personalCollectionReadMemoMaxEntries {
		memo.put(personalCollectionReadKey{userID: i}, 1, struct{}{}, now)
	}
	// A full memo of live entries forgets a tenth of them rather than grow.
	memo.put(personalCollectionReadKey{userID: -1}, 1, struct{}{}, now)
	if want := personalCollectionReadMemoMaxEntries * 9 / 10; len(memo.entries) != want {
		t.Fatalf("memo holds %d entries, want %d", len(memo.entries), want)
	}
	if _, ok := memo.get(personalCollectionReadKey{userID: -1}, 1, now); !ok {
		t.Fatal("the new entry was not kept")
	}
	// Expired entries are dropped first, keeping live ones.
	clear(memo.entries)
	for i := range personalCollectionReadMemoMaxEntries - 1 {
		memo.put(personalCollectionReadKey{userID: i}, 1, struct{}{}, now.Add(-personalCollectionReadMemoTTL))
	}
	memo.put(personalCollectionReadKey{userID: -2}, 1, struct{}{}, now)
	memo.put(personalCollectionReadKey{userID: -3}, 1, struct{}{}, now)
	if len(memo.entries) != 2 {
		t.Fatalf("memo holds %d entries, want the 2 live ones", len(memo.entries))
	}
	// One expired entry in a full memo still leaves a tenth of headroom, so
	// the next new keys do not each sweep the whole memo.
	clear(memo.entries)
	memo.put(personalCollectionReadKey{userID: -4}, 1, struct{}{}, now.Add(-personalCollectionReadMemoTTL))
	for i := range personalCollectionReadMemoMaxEntries - 1 {
		memo.put(personalCollectionReadKey{userID: i}, 1, struct{}{}, now)
	}
	memo.put(personalCollectionReadKey{userID: -5}, 1, struct{}{}, now)
	if want := personalCollectionReadMemoMaxEntries * 9 / 10; len(memo.entries) != want {
		t.Fatalf("memo holds %d entries, want %d", len(memo.entries), want)
	}
	if _, ok := memo.get(personalCollectionReadKey{userID: -5}, 1, now); !ok {
		t.Fatal("the new entry was not kept")
	}
}
