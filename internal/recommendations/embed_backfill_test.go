package recommendations

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/recommendations/embeddings"
)

type quotaTestEmbedder func(context.Context, []string) ([][]float32, error)

func (f quotaTestEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return f(ctx, texts)
}

const backfillTestModel = "test-model"

type storedTestEmbedding struct {
	model, text string
	dims        int
}

// fakeEmbeddingStore is an in-memory embeddingStore.
type fakeEmbeddingStore struct {
	lock       *EmbeddingLock
	lockWrites int
	// missing lists, sorted, the items Pass 1 finds until they are stored.
	missing []string
	// candidates lists, sorted by ID, the rows the Pass 2 SQL flags.
	candidates []EmbeddingTextCandidate
	// pages records the afterID of each Pass 2 page request.
	pages  []string
	stored map[string]storedTestEmbedding
	// failItem names an item whose row fails to store, and with it any
	// write that includes it.
	failItem string
	// marked records each MarkProfilesStaleForItems call's items.
	marked [][]string
	// markedAll counts MarkAllProfilesStale calls.
	markedAll int
}

func (f *fakeEmbeddingStore) HasEmbeddingsFromModel(_ context.Context, model string) (bool, error) {
	for _, e := range f.stored {
		if e.model == model {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeEmbeddingStore) MarkAllProfilesStale(context.Context) (int64, error) {
	f.markedAll++
	return 1, nil
}

func (f *fakeEmbeddingStore) MarkProfilesStaleForItems(_ context.Context, itemIDs []string) (int64, error) {
	f.marked = append(f.marked, slices.Clone(itemIDs))
	return int64(len(itemIDs)), nil
}

func newFakeEmbeddingStore(missing ...string) *fakeEmbeddingStore {
	sort.Strings(missing)
	return &fakeEmbeddingStore{missing: missing, stored: map[string]storedTestEmbedding{}}
}

func (f *fakeEmbeddingStore) GetEmbeddingLock(context.Context) (*EmbeddingLock, error) {
	if f.lock == nil {
		return nil, nil
	}
	lock := *f.lock
	return &lock, nil
}

func (f *fakeEmbeddingStore) SetEmbeddingLock(_ context.Context, lock EmbeddingLock) error {
	f.lockWrites++
	f.lock = &lock
	return nil
}

func (f *fakeEmbeddingStore) ItemsNeedingEmbedding(_ context.Context, _, afterID string, limit int) ([]string, error) {
	var ids []string
	for _, id := range f.missing {
		if _, ok := f.stored[id]; ok {
			continue
		}
		if id > afterID && len(ids) < limit {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (f *fakeEmbeddingStore) ListEmbeddingTextCandidates(_ context.Context, afterID, _ string, limit int) ([]EmbeddingTextCandidate, error) {
	f.pages = append(f.pages, afterID)
	var page []EmbeddingTextCandidate
	for _, c := range f.candidates {
		if c.MediaItemID > afterID && len(page) < limit {
			page = append(page, c)
		}
	}
	return page, nil
}

func (f *fakeEmbeddingStore) UpsertEmbedding(ctx context.Context, itemID string, embedding []float32, model, canonicalText string) error {
	return f.UpsertEmbeddings(ctx, model, []EmbeddingWrite{{ItemID: itemID, Embedding: embedding, CanonicalText: canonicalText}})
}

func (f *fakeEmbeddingStore) UpsertEmbeddings(_ context.Context, model string, writes []EmbeddingWrite) error {
	for _, w := range writes {
		if w.ItemID == f.failItem {
			return fmt.Errorf("store %s failed", w.ItemID)
		}
	}
	for _, w := range writes {
		f.stored[w.ItemID] = storedTestEmbedding{model: model, text: w.CanonicalText, dims: len(w.Embedding)}
	}
	return nil
}

// testItem is the item the fake loader returns for id: a movie titled id,
// whose embedding text is id, unless overviews names a long text for it.
func testItem(id string, overviews map[string]string) *models.MediaItem {
	item := &models.MediaItem{ContentID: id, Type: "movie", Title: id}
	if overview, ok := overviews[id]; ok {
		item.Genres = []string{"Drama"}
		item.Overview = overview
	}
	return item
}

func newTestBackfill(store *fakeEmbeddingStore, client embedder, overviews map[string]string) *embedBackfill {
	return &embedBackfill{
		db:      store,
		client:  client,
		baseURL: "http://embed.test",
		model:   backfillTestModel,
		load: func(_ context.Context, ids []string) ([]*models.MediaItem, error) {
			items := make([]*models.MediaItem, 0, len(ids))
			for _, id := range ids {
				items = append(items, testItem(id, overviews))
			}
			return items, nil
		},
		refused: newRefusedEmbedInputs(),
	}
}

func testVector(dims int) []float32 {
	vector := make([]float32, dims)
	vector[0] = 1
	return vector
}

// vectorsOf returns an embedder that answers every text with a dims-wide
// vector and counts its calls.
func vectorsOf(dims int, calls *int) quotaTestEmbedder {
	return func(_ context.Context, texts []string) ([][]float32, error) {
		*calls++
		out := make([][]float32, len(texts))
		for i := range texts {
			out[i] = testVector(dims)
		}
		return out, nil
	}
}

func testIDs(prefix string, n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s%04d", prefix, i)
	}
	return ids
}

// A model whose vectors the table cannot hold must never become the locked
// model: the run fails and writes neither the lock nor any row.
func TestBackfillUnstorableVectorWritesNoLock(t *testing.T) {
	for _, dims := range []int{0, CanonicalEmbeddingDimensions + 1} {
		t.Run(fmt.Sprint(dims), func(t *testing.T) {
			calls := 0
			store := newFakeEmbeddingStore(testIDs("item-", 3)...)
			b := newTestBackfill(store, quotaTestEmbedder(func(_ context.Context, texts []string) ([][]float32, error) {
				calls++
				out := make([][]float32, len(texts))
				for i := range out {
					out[i] = make([]float32, dims)
				}
				return out, nil
			}), nil)
			err := b.run(context.Background(), true)
			if err == nil || store.lockWrites != 0 || store.lock != nil || len(store.stored) != 0 {
				t.Fatalf("unstorable %d-d vectors: error=%v lock writes=%d stored=%d", dims, err, store.lockWrites, len(store.stored))
			}
			if dims > 0 && !strings.Contains(err.Error(), fmt.Sprintf("returns %d dimensions; Silo stores at most %d", dims, CanonicalEmbeddingDimensions)) {
				t.Fatalf("error = %v, want the model's dimensions named", err)
			}
		})
	}
}

func TestBackfillWritesTheLockFromTheFirstStorableVector(t *testing.T) {
	calls := 0
	store := newFakeEmbeddingStore(testIDs("item-", 25)...)
	b := newTestBackfill(store, vectorsOf(768, &calls), nil)
	if err := b.run(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if store.lockWrites != 1 || store.lock.SourceDimensions != 768 || store.lock.Model != backfillTestModel || b.counts.Embedded != 25 || calls != 3 {
		t.Fatalf("lock=%+v writes=%d counts=%+v calls=%d", store.lock, store.lockWrites, b.counts, calls)
	}
}

// When storing a chunk fails, its items are stored one at a time: only the
// item whose row fails is lost, counted as failed for the next run, and only
// the stored items mark their profiles stale.
func TestBackfillStoresItemsOneAtATimeWhenAChunkWriteFails(t *testing.T) {
	calls := 0
	ids := testIDs("item-", 4)
	store := newFakeEmbeddingStore(ids...)
	store.failItem = ids[1]
	b := newTestBackfill(store, vectorsOf(768, &calls), nil)
	if err := b.run(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	stored := []string{ids[0], ids[2], ids[3]}
	if b.counts.Embedded != 3 || b.counts.Failed != 1 || len(store.stored) != 3 {
		t.Fatalf("counts = %+v with %d stored; want the three good items stored and one failure", b.counts, len(store.stored))
	}
	if len(store.marked) != 1 || !slices.Equal(store.marked[0], stored) {
		t.Fatalf("profiles marked stale for %v; want %v", store.marked, stored)
	}
}

// Vectors a catalog import stored carry no lock, and with every item embedded
// no run stores one to write it from. A run that finds them and no lock embeds
// the probe text once and writes the lock; without them it writes none.
func TestBackfillLocksImportedEmbeddings(t *testing.T) {
	calls := 0
	store := newFakeEmbeddingStore()
	store.stored["imported"] = storedTestEmbedding{model: backfillTestModel, text: "imported"}
	b := newTestBackfill(store, vectorsOf(768, &calls), nil)
	if err := b.run(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || store.lockWrites != 1 || store.lock.Model != backfillTestModel || store.lock.SourceDimensions != 768 {
		t.Fatalf("calls=%d lock writes=%d lock=%+v; want one probe and a lock for the imported model", calls, store.lockWrites, store.lock)
	}

	calls = 0
	empty := newFakeEmbeddingStore()
	b = newTestBackfill(empty, vectorsOf(768, &calls), nil)
	if err := b.run(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || empty.lockWrites != 0 {
		t.Fatalf("no imported vectors: calls=%d lock writes=%d, want none", calls, empty.lockWrites)
	}
}

var testRefusal = &embeddings.StatusError{API: "embedding", StatusCode: 400, Body: "invalid input"}

// refusingEmbedder answers a call with testRefusal when bad reports one of
// its texts, and counts its calls.
func refusingEmbedder(bad func(text string) bool, calls *int) quotaTestEmbedder {
	return func(_ context.Context, texts []string) ([][]float32, error) {
		*calls++
		for _, text := range texts {
			if bad(text) {
				return nil, testRefusal
			}
		}
		out := make([][]float32, len(texts))
		for i := range out {
			out[i] = testVector(8)
		}
		return out, nil
	}
}

// An embeddings reset deletes the lock on every server, and each server's
// record of refused inputs was made under the old lock, so the next catch-up
// pass on any server retries them instead of holding them back until the
// nightly run.
func TestEmbedMissingRetriesRefusedInputsAfterTheLockChanges(t *testing.T) {
	calls := 0
	store := newFakeEmbeddingStore("bad-1")
	store.lock = &EmbeddingLock{BaseURL: "http://embed.test", Model: backfillTestModel, SourceDimensions: 8, StorageDimensions: CanonicalEmbeddingDimensions}
	client := refusingEmbedder(func(text string) bool { return text == "bad-1" }, &calls)
	refused := newRefusedEmbedInputs()
	run := func() EmbedCounts {
		t.Helper()
		calls = 0
		b := newTestBackfill(store, client, nil)
		b.refused = refused
		if err := b.run(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		return b.counts
	}

	if counts := run(); counts.Skipped != 1 {
		t.Fatalf("first pass: counts=%+v", counts)
	}
	if counts := run(); counts.Skipped != 0 || calls != 0 {
		t.Fatalf("second pass under the same lock: counts=%+v calls=%d; want bad-1 held back", counts, calls)
	}
	store.lock = nil // an embeddings reset on another server
	if counts := run(); counts.Skipped != 1 || calls == 0 {
		t.Fatalf("pass after the reset: counts=%+v calls=%d; want bad-1 retried", counts, calls)
	}
}

// The lock follows the model, not the provider: the same model under a
// vendor prefix through another address embeds and stores its vectors under
// the lock's name, and another model stops the run.
func TestBackfillHonorsTheLockModel(t *testing.T) {
	for _, tc := range []struct {
		name, lockModel string
		wantStored      bool
	}{
		{name: "another model", lockModel: "other-model"},
		{name: "same model through another provider", lockModel: "vendor/" + backfillTestModel, wantStored: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			store := newFakeEmbeddingStore("item")
			store.lock = &EmbeddingLock{BaseURL: "http://other.test", Model: tc.lockModel, SourceDimensions: 8, StorageDimensions: CanonicalEmbeddingDimensions}
			b := newTestBackfill(store, vectorsOf(8, &calls), nil)
			err := b.run(context.Background(), true)
			if !tc.wantStored {
				if err == nil || !strings.Contains(err.Error(), "reset required") || calls != 0 {
					t.Fatalf("calls=%d error=%v", calls, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := store.stored["item"].model; got != tc.lockModel {
				t.Fatalf("stored model = %q, want %q", got, tc.lockModel)
			}
		})
	}
}

// lostAfterLock is a clusterLock whose session closes after its first checks
// calls to Check.
type lostAfterLock struct {
	fakeClusterLock
	checks *int
}

func (l lostAfterLock) Check(context.Context) error {
	if *l.checks <= 0 {
		return errJobLockLost
	}
	*l.checks--
	return nil
}

// A run whose cluster lock session closes stores nothing more: an embeddings
// reset or another server's run may hold the lock by then.
func TestBackfillStopsStoringWhenItsLockIsLost(t *testing.T) {
	t.Run("batches", func(t *testing.T) {
		calls, checks := 0, 1
		store := newFakeEmbeddingStore(testIDs("item-", 25)...)
		b := newTestBackfill(store, vectorsOf(768, &calls), nil)
		b.held = lostAfterLock{checks: &checks}
		if err := b.run(context.Background(), true); !errors.Is(err, errJobLockLost) {
			t.Fatalf("run error = %v, want errJobLockLost", err)
		}
		if b.counts.Embedded != embeddingBackfillBatchSize || calls != 2 {
			t.Fatalf("counts=%+v calls=%d, want one batch stored and the second dropped", b.counts, calls)
		}
	})
	t.Run("one item at a time", func(t *testing.T) {
		checks := 1
		store := newFakeEmbeddingStore(testIDs("item-", 3)...)
		var client quotaTestEmbedder = func(_ context.Context, texts []string) ([][]float32, error) {
			if len(texts) > 1 {
				return nil, errors.New("batch refused")
			}
			return [][]float32{testVector(768)}, nil
		}
		b := newTestBackfill(store, client, nil)
		b.held = lostAfterLock{checks: &checks}
		if err := b.run(context.Background(), true); !errors.Is(err, errJobLockLost) {
			t.Fatalf("run error = %v, want errJobLockLost", err)
		}
		if b.counts.Embedded != 1 {
			t.Fatalf("counts=%+v, want only the first item stored", b.counts)
		}
	})
}
