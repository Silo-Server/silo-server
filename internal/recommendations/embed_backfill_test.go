package recommendations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"syscall"
	"testing"
	"unicode/utf8"

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
	pages     []string
	stored    map[string]storedTestEmbedding
	upsertErr error
	// marked records each MarkProfilesStaleForItems call's items.
	marked [][]string
	// markedAll counts MarkAllProfilesStale calls.
	markedAll int
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

func (f *fakeEmbeddingStore) UpsertEmbedding(_ context.Context, itemID string, embedding []float32, model, canonicalText string) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.stored[itemID] = storedTestEmbedding{model: model, text: canonicalText, dims: len(embedding)}
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

func TestEmbedBatchStopsOnProviderLimit(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"daily quota", &embeddings.RateLimitError{DailyQuota: true}},
		{"quota exhausted", &embeddings.RateLimitError{QuotaExhausted: true}},
		{"temporary retries exhausted", &embeddings.RateLimitError{}},
		{"excessive retry delay", &embeddings.RateLimitError{RetryDeferred: true}},
		{"wrapped limit", fmt.Errorf("wrapped: %w", &embeddings.RateLimitError{})},
		{"OpenAI quota text", errors.New("embedding API returned 429: insufficient_quota")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			b := newTestBackfill(newFakeEmbeddingStore(), quotaTestEmbedder(func(context.Context, []string) ([][]float32, error) {
				calls++
				return nil, tc.err
			}), nil)
			items := []*models.MediaItem{{ContentID: "test-one"}, {ContentID: "test-two"}}
			err := b.embedBatch(context.Background(), items, []string{"one", "two"})
			if calls != 1 || b.counts.Embedded != 0 || !errors.Is(err, tc.err) {
				t.Fatalf("batch stop: calls=%d counts=%+v error=%v", calls, b.counts, err)
			}
			if strings.Contains(err.Error(), "check billing") {
				t.Fatal("batch added an unsupported billing diagnosis")
			}
		})
	}
}

func TestEmbedBatchStopsFallbackOnProviderLimit(t *testing.T) {
	calls := 0
	limitErr := &embeddings.RateLimitError{DailyQuota: true}
	b := newTestBackfill(newFakeEmbeddingStore(), quotaTestEmbedder(func(context.Context, []string) ([][]float32, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("batch input too large")
		}
		return nil, limitErr
	}), nil)
	items := []*models.MediaItem{{ContentID: "test-one"}, {ContentID: "test-two"}}
	err := b.embedBatch(context.Background(), items, []string{"one", "two"})
	if !errors.Is(err, limitErr) || calls != 2 || b.counts.Embedded != 0 {
		t.Fatalf("fallback stop: calls=%d counts=%+v error=%v", calls, b.counts, err)
	}
}

// A model whose vectors the table cannot hold must never become the locked
// model: the run fails and writes neither the lock nor any row.
func TestBackfillUnstorableVectorWritesNoLock(t *testing.T) {
	for _, dims := range []int{0, CanonicalEmbeddingDimensions + 1, 4096} {
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

// The profiles with signals on stored items are marked stale, in batches of
// signalsStaleBatch items and once more for the rest when the run ends: a
// refresh that ran before the items had vectors left them out.
func TestBackfillMarksProfilesStaleForStoredItems(t *testing.T) {
	calls := 0
	store := newFakeEmbeddingStore(testIDs("item-", 2*signalsStaleBatch+200)...)
	b := newTestBackfill(store, vectorsOf(768, &calls), nil)
	if err := b.run(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	marked := 0
	for _, ids := range store.marked {
		marked += len(ids)
	}
	if len(store.marked) != 3 || marked != b.counts.Embedded || marked != 2*signalsStaleBatch+200 || store.markedAll != 0 {
		t.Fatalf("marks = %d calls for %d items (%d whole-server), want 3 calls for every one of the %d stored", len(store.marked), marked, store.markedAll, b.counts.Embedded)
	}

	// With signals in a user store outside Postgres, the item-based mark
	// cannot see them, so the run marks every profile instead.
	outside := newFakeEmbeddingStore(testIDs("item-", 25)...)
	b = newTestBackfill(outside, vectorsOf(768, &calls), nil)
	b.signalsOutsidePostgres = true
	if err := b.run(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if outside.markedAll != 1 || len(outside.marked) != 0 {
		t.Fatalf("store outside Postgres: %d whole-server marks and %d item marks, want one whole-server mark", outside.markedAll, len(outside.marked))
	}
}

// A provider that answers 200 with a malformed body fails the run before any
// lock or row is written.
func TestBackfillMalformedResponsesFailTheRun(t *testing.T) {
	for name, entry := range map[string]func(index int) string{
		"no data":      nil,
		"null vectors": func(index int) string { return fmt.Sprintf(`{"embedding":null,"index":%d}`, index) },
		"one short": func(index int) string {
			if index == 0 {
				return ""
			}
			return fmt.Sprintf(`{"embedding":[0.5,0.5],"index":%d}`, index)
		},
	} {
		t.Run(name, func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var req struct {
					Input []string `json:"input"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				var entries []string
				for i := range req.Input {
					if entry != nil {
						if e := entry(i); e != "" {
							entries = append(entries, e)
						}
					}
				}
				_, _ = fmt.Fprintf(w, `{"data":[%s]}`, strings.Join(entries, ","))
			}))
			defer srv.Close()

			store := newFakeEmbeddingStore(testIDs("item-", 5)...)
			b := newTestBackfill(store, embeddings.NewClient(embeddings.ClientConfig{BaseURL: srv.URL, Model: backfillTestModel}), nil)
			err := b.run(context.Background(), true)
			if err == nil || !strings.Contains(err.Error(), "embedding provider unavailable") {
				t.Fatalf("error = %v, want the run to fail", err)
			}
			if store.lock != nil || len(store.stored) != 0 || requests != 2 {
				t.Fatalf("lock=%+v stored=%d requests=%d", store.lock, len(store.stored), requests)
			}
		})
	}
}

func TestBackfillFailsFastWhenTheProviderIsUnavailable(t *testing.T) {
	for name, providerErr := range map[string]error{
		"unauthorized":       &embeddings.StatusError{API: "embedding", StatusCode: 401, Body: "bad key"},
		"forbidden":          &embeddings.StatusError{API: "embedding", StatusCode: 403},
		"connection refused": fmt.Errorf("embedding request failed: %w", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}),
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			store := newFakeEmbeddingStore(testIDs("item-", 30)...)
			b := newTestBackfill(store, quotaTestEmbedder(func(context.Context, []string) ([][]float32, error) {
				calls++
				return nil, providerErr
			}), nil)
			err := b.run(context.Background(), true)
			if calls != 1 || !errors.Is(err, providerErr) || !strings.HasPrefix(err.Error(), "embedding provider unavailable: ") {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
		})
	}
}

func TestBackfillStopsWhenFallbackItemsKeepFailing(t *testing.T) {
	broken := errors.New("embedding API returned 500: model crashed")

	t.Run("first item fails with nothing stored", func(t *testing.T) {
		calls := 0
		store := newFakeEmbeddingStore(testIDs("item-", 30)...)
		b := newTestBackfill(store, quotaTestEmbedder(func(context.Context, []string) ([][]float32, error) {
			calls++
			return nil, broken
		}), nil)
		err := b.run(context.Background(), true)
		if calls != 2 || !errors.Is(err, broken) || !strings.Contains(err.Error(), "embedding provider unavailable") {
			t.Fatalf("calls=%d counts=%+v error=%v", calls, b.counts, err)
		}
	})

	t.Run("three in a row", func(t *testing.T) {
		calls := 0
		store := newFakeEmbeddingStore(testIDs("item-", 30)...)
		b := newTestBackfill(store, quotaTestEmbedder(func(_ context.Context, texts []string) ([][]float32, error) {
			calls++
			// The batch and every item after the first fail.
			if len(texts) > 1 || texts[0] != "item-0000" {
				return nil, broken
			}
			return [][]float32{testVector(8)}, nil
		}), nil)
		err := b.run(context.Background(), true)
		if calls != 5 || b.counts.Embedded != 1 || b.counts.Failed != 3 || !errors.Is(err, broken) || !strings.Contains(err.Error(), "embedding provider unavailable") {
			t.Fatalf("calls=%d counts=%+v error=%v", calls, b.counts, err)
		}
	})

	t.Run("an occasional bad item does not stop the run", func(t *testing.T) {
		store := newFakeEmbeddingStore(testIDs("item-", 20)...)
		b := newTestBackfill(store, quotaTestEmbedder(func(_ context.Context, texts []string) ([][]float32, error) {
			for _, text := range texts {
				if text == "item-0003" || text == "item-0012" {
					return nil, &embeddings.StatusError{API: "embedding", StatusCode: 400, Body: "invalid input"}
				}
			}
			out := make([][]float32, len(texts))
			for i := range out {
				out[i] = testVector(8)
			}
			return out, nil
		}), nil)
		if err := b.run(context.Background(), true); err != nil {
			t.Fatal(err)
		}
		if b.counts.Embedded != 18 || b.counts.Skipped != 2 || b.counts.Failed != 0 {
			t.Fatalf("counts=%+v", b.counts)
		}
	})
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

// A refused input says nothing about the provider: it is skipped and never
// counts as a failure, so refused items at the head of the backlog cannot
// stop every run. A provider that refuses every input (Gemini answers a bad
// key with 400) still stops the run, because it refuses the probe text too.
func TestBackfillSkipsRefusedInputs(t *testing.T) {
	t.Run("refused items at the head of the backlog", func(t *testing.T) {
		calls := 0
		ids := testIDs("item-", 30)
		bad := map[string]bool{}
		for _, id := range ids[:maxConsecutiveEmbedFailures+1] {
			bad[id] = true
		}
		store := newFakeEmbeddingStore(ids...)
		b := newTestBackfill(store, refusingEmbedder(func(text string) bool { return bad[text] }, &calls), nil)
		if err := b.run(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		// The first batch falls back to single calls, with one probe after
		// the first refusal; the other two batches succeed.
		if b.counts.Embedded != 26 || b.counts.Skipped != 4 || b.counts.Failed != 0 || calls != 14 {
			t.Fatalf("counts=%+v calls=%d", b.counts, calls)
		}
		for id := range bad {
			if _, ok := store.stored[id]; ok || !b.refused.has(id, id) {
				t.Fatalf("refused %s: stored=%v recorded=%v", id, ok, b.refused.has(id, id))
			}
		}
	})

	t.Run("a provider that refuses every input stops the run", func(t *testing.T) {
		calls := 0
		b := newTestBackfill(newFakeEmbeddingStore(testIDs("item-", 30)...), refusingEmbedder(func(string) bool { return true }, &calls), nil)
		err := b.run(context.Background(), false)
		// The batch, the first item, and the probe.
		if calls != 3 || b.counts.Failed != 1 || b.counts.Skipped != 0 || !errors.Is(err, testRefusal) || !strings.HasPrefix(err.Error(), "embedding provider unavailable: ") {
			t.Fatalf("calls=%d counts=%+v error=%v", calls, b.counts, err)
		}
		// The item is not recorded as refused, so the next run tries it again.
		if b.refused.has("item-0000", "item-0000") {
			t.Fatal("recorded an item the provider was down for")
		}
	})
}

// When only refused items are left, the catch-up pass completes, then holds
// them back until their text changes or a full run retries them, so it does
// not resend them or record a run every 15 minutes.
func TestEmbedMissingHoldsBackRefusedInputs(t *testing.T) {
	calls := 0
	store := newFakeEmbeddingStore("bad-1", "bad-2")
	client := refusingEmbedder(func(text string) bool { return text == "bad-1" || text == "bad-2" }, &calls)
	refused := newRefusedEmbedInputs()
	run := func(full bool, overviews map[string]string) (EmbedCounts, error) {
		calls = 0
		b := newTestBackfill(store, client, overviews)
		b.refused = refused
		err := b.run(context.Background(), full)
		return b.counts, err
	}

	counts, err := run(false, nil)
	// The batch, each item alone, and one probe.
	if err != nil || counts.Skipped != 2 || counts.Embedded != 0 || calls != 4 {
		t.Fatalf("first pass: counts=%+v calls=%d error=%v", counts, calls, err)
	}
	if isIdleRun(newEmbeddingsResult(counts, err, true), err) {
		t.Fatal("the first pass that refused items is not recorded")
	}

	counts, err = run(false, nil)
	if err != nil || counts != (EmbedCounts{}) || calls != 0 || !isIdleRun(newEmbeddingsResult(counts, err, true), err) {
		t.Fatalf("second pass: counts=%+v calls=%d error=%v", counts, calls, err)
	}

	counts, err = run(false, map[string]string{"bad-1": "A corrected overview."})
	if err != nil || counts.Embedded != 1 || counts.Skipped != 0 || calls != 1 {
		t.Fatalf("pass after bad-1's text changed: counts=%+v calls=%d error=%v", counts, calls, err)
	}

	counts, err = run(true, nil)
	if err != nil || counts.Skipped != 1 || counts.Embedded != 0 || calls != 3 {
		t.Fatalf("full run: counts=%+v calls=%d error=%v", counts, calls, err)
	}
	if counts, err = run(false, nil); err != nil || calls != 0 {
		t.Fatalf("pass after the full run: counts=%+v calls=%d error=%v", counts, calls, err)
	}
}

func TestBackfillRunThatStoresNothingFails(t *testing.T) {
	calls := 0
	store := newFakeEmbeddingStore(testIDs("item-", 12)...)
	store.upsertErr = errors.New("database is read-only")
	b := newTestBackfill(store, vectorsOf(8, &calls), nil)
	err := b.run(context.Background(), true)
	if !errors.Is(err, store.upsertErr) || !strings.Contains(err.Error(), "stored none of the 12 items") || b.counts.Failed != 12 {
		t.Fatalf("counts=%+v error=%v", b.counts, err)
	}

	empty := newTestBackfill(newFakeEmbeddingStore(), vectorsOf(8, &calls), nil)
	if err := empty.run(context.Background(), true); err != nil {
		t.Fatalf("a run with nothing to do failed: %v", err)
	}
}

// overLengthEmbedder rejects any input longer than limit runes the way the
// given error does, and records every input length it is sent.
func overLengthEmbedder(limit int, reject error, lengths *[]int) quotaTestEmbedder {
	return func(_ context.Context, texts []string) ([][]float32, error) {
		out := make([][]float32, len(texts))
		for i, text := range texts {
			*lengths = append(*lengths, utf8.RuneCountInString(text))
			if utf8.RuneCountInString(text) > limit {
				return nil, reject
			}
			out[i] = testVector(8)
		}
		return out, nil
	}
}

func TestBackfillRetriesLongTextsShortened(t *testing.T) {
	overview := strings.Repeat("é", 1000)
	overviews := map[string]string{"long": overview}
	fullText := embeddings.BuildEmbeddingText(testItem("long", overviews))

	for _, tc := range []struct {
		name    string
		limit   int
		reject  error
		lengths []int
	}{
		{"accepted at 600 runes", 700, errors.New("embedding API returned 500: input length exceeds the context length"), []int{utf8.RuneCountInString(fullText), 600}},
		{"accepted at 300 runes", 400, &embeddings.StatusError{API: "embedding", StatusCode: 413}, []int{utf8.RuneCountInString(fullText), 600, 300}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var lengths []int
			store := newFakeEmbeddingStore("long")
			b := newTestBackfill(store, overLengthEmbedder(tc.limit, tc.reject, &lengths), overviews)
			if err := b.run(context.Background(), false); err != nil {
				t.Fatal(err)
			}
			// The first length is the failed batch call.
			if got := fmt.Sprint(lengths[1:]); got != fmt.Sprint(tc.lengths) {
				t.Fatalf("single-item input lengths = %s, want %v", got, tc.lengths)
			}
			stored := store.stored["long"]
			if b.counts.Embedded != 1 || b.counts.Truncated != 1 || stored.text != fullText {
				t.Fatalf("counts=%+v stored text has %d runes, want the full %d", b.counts, utf8.RuneCountInString(stored.text), utf8.RuneCountInString(fullText))
			}
			// The next text-staleness check sees the item as current.
			if embeddingTextNeedsRefresh(stored.model, stored.text, embeddings.BuildEmbeddingText(testItem("long", overviews)), backfillTestModel) {
				t.Fatal("an item embedded from shortened text looks stale to the next run")
			}
		})
	}

	// A local model's context-length 5xx at every length is a refused input,
	// not a provider failure, even for the first item of a run: the probe
	// text passes, so the item is skipped and remembered and the run goes on.
	t.Run("context length at every length, first item", func(t *testing.T) {
		var lengths []int
		store := newFakeEmbeddingStore("long", "z-ok")
		reject := errors.New("embedding API returned 500: input length exceeds the context length")
		b := newTestBackfill(store, overLengthEmbedder(100, reject, &lengths), overviews)
		if err := b.run(context.Background(), false); err != nil {
			t.Fatalf("run stopped on a refused input: %v", err)
		}
		if b.counts.Skipped != 1 || b.counts.Embedded != 1 || b.counts.Failed != 0 || !b.refused.has("long", fullText) {
			t.Fatalf("counts=%+v refused recorded=%v", b.counts, b.refused.has("long", fullText))
		}
	})

	t.Run("rejected at every length", func(t *testing.T) {
		var lengths []int
		// "a-ok" sorts first, so the run has stored an item when "long"
		// fails.
		store := newFakeEmbeddingStore("a-ok", "long")
		reject := &embeddings.StatusError{API: "embedding", StatusCode: 400, Body: "too long"}
		b := newTestBackfill(store, overLengthEmbedder(100, reject, &lengths), overviews)
		if err := b.run(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		// The batch call sends both texts, then "a-ok" alone, then "long"
		// at full length, 600 and 300 runes.
		if b.counts.Skipped != 1 || b.counts.Embedded != 1 || b.counts.Failed != 0 || len(lengths) != 6 {
			t.Fatalf("counts=%+v lengths=%v", b.counts, lengths)
		}
	})

	t.Run("short texts and failures other than length are not retried shortened", func(t *testing.T) {
		for name, err := range map[string]error{
			"short text":         errors.New("embedding API returned 500: crashed"),
			"limit":              &embeddings.RateLimitError{},
			"server error":       &embeddings.StatusError{API: "embedding", StatusCode: 503, Body: "unavailable"},
			"malformed response": errors.New("embedding API returned 0 vectors for 1 input"),
		} {
			calls := 0
			text := fullText
			if name == "short text" {
				text = "short"
			}
			b := newTestBackfill(newFakeEmbeddingStore(), quotaTestEmbedder(func(context.Context, []string) ([][]float32, error) {
				calls++
				return nil, err
			}), nil)
			if _, _, got := b.embedOne(context.Background(), text); !errors.Is(got, err) || calls != 1 {
				t.Fatalf("%s: calls=%d error=%v", name, calls, got)
			}
		}
	})
}

// Rows that only look stale in SQL must not hold back real stale rows behind
// them, and the run re-embeds at most the per-run quota.
func TestBackfillTextStalePassPagesPastFalsePositives(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		falsePositives, real int
		wantEmbedded         int
		wantPages            int
	}{
		// 420 false positives fill two full pages and part of a third.
		{"real rows behind 420 false positives", 420, 30, 30, 3},
		// The quota fills on the third page, so no fourth page is read.
		{"quota stops the scan", 300, 250, embeddingTextStaleQuotaPerRun, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeEmbeddingStore()
			for _, id := range testIDs("a-false-", tc.falsePositives) {
				// The stored text equals the Go text: SQL drift flagged it.
				store.candidates = append(store.candidates, EmbeddingTextCandidate{MediaItemID: id, Model: backfillTestModel, CanonicalText: id})
			}
			for _, id := range testIDs("b-real-", tc.real) {
				store.candidates = append(store.candidates, EmbeddingTextCandidate{MediaItemID: id, Model: backfillTestModel, CanonicalText: "old text"})
			}
			calls := 0
			b := newTestBackfill(store, vectorsOf(8, &calls), nil)
			if err := b.run(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			if b.counts.Embedded != tc.wantEmbedded || len(store.pages) != tc.wantPages {
				t.Fatalf("embedded %d over %d pages (%v), want %d over %d", b.counts.Embedded, len(store.pages), store.pages, tc.wantEmbedded, tc.wantPages)
			}
			for id := range store.stored {
				if !strings.HasPrefix(id, "b-real-") {
					t.Fatalf("re-embedded %s, which is current", id)
				}
			}
			if store.pages[0] != "" || store.pages[1] != store.candidates[embeddingTextStaleQuotaPerRun-1].MediaItemID {
				t.Fatalf("page cursors = %v", store.pages)
			}
		})
	}
}

// Stale rows the provider refuses use none of the text-refresh quota, so a
// full quota of them at the front of the walk cannot keep a later stale row
// from being re-embedded.
func TestBackfillTextStalePassWalksPastRefusedRows(t *testing.T) {
	store := newFakeEmbeddingStore()
	bad := map[string]bool{}
	for _, id := range testIDs("a-refused-", embeddingTextStaleQuotaPerRun) {
		bad[id] = true
		store.candidates = append(store.candidates, EmbeddingTextCandidate{MediaItemID: id, Model: backfillTestModel, CanonicalText: "old text"})
	}
	store.candidates = append(store.candidates, EmbeddingTextCandidate{MediaItemID: "b-stale", Model: backfillTestModel, CanonicalText: "old text"})
	calls := 0
	b := newTestBackfill(store, refusingEmbedder(func(text string) bool { return bad[text] }, &calls), nil)
	if err := b.run(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.stored["b-stale"]; !ok || b.counts.Embedded != 1 || b.counts.Skipped != embeddingTextStaleQuotaPerRun {
		t.Fatalf("counts=%+v, stored b-stale=%v; want the stale row re-embedded past the refused ones", b.counts, ok)
	}
}

func TestEmbedMissingSkipsTheTextStalePass(t *testing.T) {
	calls := 0
	store := newFakeEmbeddingStore("missing")
	store.candidates = []EmbeddingTextCandidate{{MediaItemID: "stale", Model: backfillTestModel, CanonicalText: "old"}}
	b := newTestBackfill(store, vectorsOf(8, &calls), nil)
	if err := b.run(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if len(store.pages) != 0 || b.counts.Embedded != 1 {
		t.Fatalf("pages=%v counts=%+v", store.pages, b.counts)
	}
}

func TestBackfillStopsOnALockForAnotherModel(t *testing.T) {
	calls := 0
	store := newFakeEmbeddingStore("item")
	store.lock = &EmbeddingLock{BaseURL: "http://embed.test", Model: "other-model", SourceDimensions: 8}
	b := newTestBackfill(store, vectorsOf(8, &calls), nil)
	err := b.run(context.Background(), true)
	if err == nil || !strings.Contains(err.Error(), "reset required") || calls != 0 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}
