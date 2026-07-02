package recommendations

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/recommendations/embeddings"
)

// recordingEmbedder is a fake embedder seam used to drive EmbedAll without a
// real embedding API. It records, in order, every text it is asked to embed so
// tests can assert the cheap pass drains before the expensive text-staleness
// pass runs. Returned vectors are deterministic and canonical-width so the
// embedding lock validates without a real model.
type recordingEmbedder struct {
	calls [][]string // one entry per Embed call (a batch)
	texts []string   // flattened, in embed order
}

func (r *recordingEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	batch := make([]string, len(texts))
	copy(batch, texts)
	r.calls = append(r.calls, batch)
	r.texts = append(r.texts, batch...)

	out := make([][]float32, len(texts))
	for i := range texts {
		vec := make([]float32, CanonicalEmbeddingDimensions)
		// A non-zero leading component keeps the vector distinguishable from the
		// seeded zero vectors; the rest stay zero.
		vec[0] = 0.5
		out[i] = vec
	}
	return out, nil
}

var _ embedder = (*recordingEmbedder)(nil)

// firstIndexContaining returns the index of the first recorded text containing
// needle, or -1.
func firstIndexContaining(texts []string, needle string) int {
	for i, txt := range texts {
		if strings.Contains(txt, needle) {
			return i
		}
	}
	return -1
}

// lastIndexContaining returns the index of the last recorded text containing
// needle, or -1.
func lastIndexContaining(texts []string, needle string) int {
	idx := -1
	for i, txt := range texts {
		if strings.Contains(txt, needle) {
			idx = i
		}
	}
	return idx
}

func newEmbedAllTestEngine(t *testing.T, pool *pgxpool.Pool, fake embedder, model string) *Engine {
	t.Helper()
	return &Engine{
		repo:       NewRepo(pool),
		itemRepo:   catalog.NewItemRepository(pool),
		personRepo: catalog.NewPersonRepository(pool),
		embClient:  fake,
		cfg: config.RecommendationsConfig{
			EmbeddingModel:   model,
			EmbeddingBaseURL: "http://embed.test",
		},
		pool: pool,
	}
}

// TestEmbedAllCheapPassDrainsBeforeTextStalePass verifies the coverage-first
// ordering: items with no embedding or a stale model (cheap Pass 1) are all
// embedded before the expensive text-staleness CTE (Pass 2) re-embeds an item
// whose only change is its canonical text.
func TestEmbedAllCheapPassDrainsBeforeTextStalePass(t *testing.T) {
	pool := newEngineTestPool(t)
	snapshotEmbeddingLock(t, pool)
	ctx := context.Background()

	const (
		prefix       = "t7embed-"
		currentModel = "model-current"
		oldModel     = "model-old"
	)
	cleanupRecoMediaItems(t, pool, prefix)
	// Start from a clean lock so the first embed establishes it for currentModel.
	if _, err := pool.Exec(ctx, `DELETE FROM server_settings WHERE key = $1`, embeddingLockSettingKey); err != nil {
		t.Fatalf("clear embedding lock: %v", err)
	}

	// Two cheap rows: one missing entirely, one under an old model. Distinct
	// titles let us find their generated text in the embedder's record.
	missingID := prefix + "1-missing"
	modelStaleID := prefix + "2-modelstale"
	seedRecoMediaItemTitled(t, pool, missingID, "movie", "matched", "CHEAPMISS Origins")
	seedRecoMediaItemTitled(t, pool, modelStaleID, "movie", "matched", "CHEAPMODEL Returns")
	seedRecoEmbedding(t, pool, modelStaleID, oldModel, "old text")

	// One text-stale-only row: embedded under the CURRENT model, but its stored
	// canonical_text does not match what BuildEmbeddingText now produces. Only
	// the expensive CTE can detect this.
	textStaleID := prefix + "3-textstale"
	seedRecoMediaItemTitled(t, pool, textStaleID, "movie", "matched", "TEXTSTALE Reloaded")
	seedRecoEmbedding(t, pool, textStaleID, currentModel, "deliberately stale canonical text")

	fake := &recordingEmbedder{}
	e := newEmbedAllTestEngine(t, pool, fake, currentModel)

	n, err := e.EmbedAll(ctx)
	if err != nil {
		t.Fatalf("EmbedAll: %v", err)
	}

	// All three should have been (re)embedded: 2 cheap + 1 text-stale.
	if n < 3 {
		t.Fatalf("EmbedAll embedded %d items, want >= 3 (2 cheap + 1 text-stale)", n)
	}

	// End state: every row now carries the current model and matching text.
	assertEmbeddingModel(t, pool, missingID, currentModel)
	assertEmbeddingModel(t, pool, modelStaleID, currentModel)
	assertEmbeddingModel(t, pool, textStaleID, currentModel)

	// Ordering: both cheap items must be embedded before the text-stale item,
	// proving Pass 2 (the expensive CTE) only ran after Pass 1 drained.
	lastCheap := lastIndexContaining(fake.texts, "CHEAP")
	firstTextStale := firstIndexContaining(fake.texts, "TEXTSTALE")
	if lastCheap < 0 {
		t.Fatalf("cheap items were never embedded; recorded texts: %v", fake.texts)
	}
	if firstTextStale < 0 {
		t.Fatalf("text-stale item was never embedded; recorded texts: %v", fake.texts)
	}
	if firstTextStale <= lastCheap {
		t.Fatalf("text-stale item embedded at %d before cheap drained at %d; coverage-first ordering violated:\n%v",
			firstTextStale, lastCheap, fake.texts)
	}
}

// TestEmbedAllSkipsTextStaleWhenAlreadyCurrent confirms Pass 2 does not waste an
// embed call on an item whose stored canonical_text already matches the freshly
// generated text (it is neither missing, model-stale, nor text-stale).
func TestEmbedAllSkipsAlreadyCurrentItems(t *testing.T) {
	pool := newEngineTestPool(t)
	snapshotEmbeddingLock(t, pool)
	ctx := context.Background()

	const (
		prefix       = "t7current-"
		currentModel = "model-current"
	)
	cleanupRecoMediaItems(t, pool, prefix)
	if _, err := pool.Exec(ctx, `DELETE FROM server_settings WHERE key = $1`, embeddingLockSettingKey); err != nil {
		t.Fatalf("clear embedding lock: %v", err)
	}

	// Seed an item, then store the embedding using the SAME canonical text the
	// engine will generate, so it is fully up to date.
	currentID := prefix + "1-current"
	seedRecoMediaItemTitled(t, pool, currentID, "movie", "matched", " alreadyCurrent")
	items, err := catalog.NewItemRepository(pool).GetByIDs(ctx, []string{currentID})
	if err != nil || len(items) == 0 {
		t.Fatalf("load seeded item: %v", err)
	}
	wantText := embeddings.BuildEmbeddingText(items[0])
	seedRecoEmbedding(t, pool, currentID, currentModel, wantText)

	fake := &recordingEmbedder{}
	e := newEmbedAllTestEngine(t, pool, fake, currentModel)

	n, err := e.EmbedAll(ctx)
	if err != nil {
		t.Fatalf("EmbedAll: %v", err)
	}
	if n != 0 {
		t.Fatalf("EmbedAll re-embedded %d up-to-date items, want 0", n)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("embedder was called %d times for an up-to-date item, want 0: %v", len(fake.calls), fake.texts)
	}
}

// seedRecoMediaItemTitled inserts an embed-eligible media item via
// ItemRepository.Upsert. Using the repository (rather than a hand-written
// INSERT) guarantees every non-nullable column the scan path reads is
// populated, so the round-trip through GetByIDs in EmbedAll succeeds.
func seedRecoMediaItemTitled(t *testing.T, pool *pgxpool.Pool, contentID, mediaType, status, title string) {
	t.Helper()
	repo := catalog.NewItemRepository(pool)
	if err := repo.Upsert(context.Background(), &models.MediaItem{
		ContentID: contentID,
		Type:      mediaType,
		Title:     title,
		Status:    status,
	}); err != nil {
		t.Fatalf("seed media item %s: %v", contentID, err)
	}
}

func assertEmbeddingModel(t *testing.T, pool *pgxpool.Pool, contentID, wantModel string) {
	t.Helper()
	var model string
	err := pool.QueryRow(context.Background(),
		`SELECT model FROM media_item_embeddings WHERE media_item_id = $1`, contentID).Scan(&model)
	if err != nil {
		t.Fatalf("read embedding model for %s: %v", contentID, err)
	}
	if model != wantModel {
		t.Fatalf("embedding for %s has model %q, want %q", contentID, model, wantModel)
	}
}
