package recommendations

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/recommendations/embeddings"
)

const (
	// embeddingBackfillBatchSize is how many items one embedding API call
	// carries.
	embeddingBackfillBatchSize = 10

	// embeddingTextStaleQuotaPerRun bounds how many text-stale items the
	// text-staleness pass re-embeds in one run. It is also the page size of
	// that pass's candidate scan.
	embeddingTextStaleQuotaPerRun = 200

	// maxConsecutiveEmbedFailures ends a run once this many items in a row
	// fail to embed one at a time: the provider is down, not the items. Inputs
	// the provider refuses do not count.
	maxConsecutiveEmbedFailures = 3

	// embedProbeText is embedded once when a run's first refused input comes
	// before anything is stored, to tell a refused input from a provider that
	// refuses every input. The admin connection check sends the same text.
	embedProbeText = "silo connection test"

	// An item whose text the provider rejects is retried shortened to
	// embedRetryRunes, then to embedRetryShortRunes, when the text is longer
	// than embedRetryRunes. Local models with a short input window (Ollama,
	// TEI) reject long texts instead of truncating them.
	embedRetryRunes      = 600
	embedRetryShortRunes = 300
)

// EmbedCounts counts the items one embedding run handled.
type EmbedCounts struct {
	// Embedded counts the items whose vector was stored.
	Embedded int `json:"embedded"`
	// Truncated counts the embedded items the provider accepted only after
	// their text was shortened.
	Truncated int `json:"truncated"`
	// Failed counts items whose embed call or write failed. The next run
	// retries them.
	Failed int `json:"failed"`
	// Skipped counts items whose input the provider refused, as too long or
	// invalid, at every length tried. Items the catch-up pass holds back
	// because the provider refused the same text earlier are not counted.
	Skipped int `json:"skipped"`
}

// EmbedAll embeds items that are missing embeddings or have stale canonical
// text, in two passes:
//
//   - Pass 1 (cheap): drain every missing or model-stale item via the
//     ItemsNeedingEmbedding cursor. This query is a single LEFT JOIN with no
//     item_people LATERAL joins, so active backfill (lots of brand-new items)
//     stays cheap. The cursor advances past each page, so an item that fails to
//     embed or store is retried on the next run rather than stalling the page.
//   - Pass 2 (expensive): only once Pass 1 has drained, page through
//     ListEmbeddingTextCandidates for items embedded with the current model
//     whose canonical text has drifted (for example a cast change), until
//     embeddingTextStaleQuotaPerRun of them are confirmed in Go or the scan
//     ends. Detecting drift rebuilds each row's text in SQL with the
//     item_people LATERAL joins, which is why Pass 1 goes first.
//
// Coverage-first tradeoff: when Pass 1 does not drain (a provider limit or the
// deadline stops it), Pass 2 is skipped, so new items get covered before
// text-changed ones are refreshed.
//
// A run that attempted items and stored none of them returns an error, unless
// the provider only refused their inputs.
func (e *Engine) EmbedAll(ctx context.Context) (EmbedCounts, error) {
	b := e.newBackfill()
	err := b.run(ctx, true)
	return b.counts, err
}

// EmbedMissing runs Pass 1 of EmbedAll only: it embeds items that have no
// embedding or one from another model. The worker runs it every few minutes,
// so newly matched items get embeddings without waiting for the nightly run.
// It skips items whose unchanged text the provider refused since this
// server's last EmbedAll.
func (e *Engine) EmbedMissing(ctx context.Context) (EmbedCounts, error) {
	b := e.newBackfill()
	err := b.run(ctx, false)
	return b.counts, err
}

// NeedsEmbedding reports whether any item has no embedding or one from
// another model.
func (e *Engine) NeedsEmbedding(ctx context.Context) (bool, error) {
	ids, err := e.repo.ItemsNeedingEmbedding(ctx, e.cfg.EmbeddingModel, "", 1)
	if err != nil {
		return false, err
	}
	return len(ids) > 0, nil
}

func (e *Engine) newBackfill() *embedBackfill {
	return &embedBackfill{
		db:      e.repo,
		client:  e.embClient,
		baseURL: e.cfg.EmbeddingBaseURL,
		model:   e.cfg.EmbeddingModel,
		load:    e.loadEmbeddingItems,
		refused: e.refusedEmbeds,
	}
}

// refusedEmbedInputs records the items whose embedding text the provider
// refused at every length, with a hash of that text. Pass 1 skips an item
// while its text still hashes the same, so the catch-up pass does not resend
// a refused text every few minutes. EmbedAll clears the record first and
// retries them all. The record is held in memory: each server's Engine keeps
// its own, and a restart clears it. A nil record remembers nothing.
type refusedEmbedInputs struct {
	mu    sync.Mutex
	texts map[string][sha256.Size]byte
}

func newRefusedEmbedInputs() *refusedEmbedInputs {
	return &refusedEmbedInputs{texts: map[string][sha256.Size]byte{}}
}

func (r *refusedEmbedInputs) add(itemID, text string) {
	if r == nil {
		return
	}
	hash := sha256.Sum256([]byte(text))
	r.mu.Lock()
	defer r.mu.Unlock()
	r.texts[itemID] = hash
}

// has reports whether the provider refused itemID with this text.
func (r *refusedEmbedInputs) has(itemID, text string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	hash, ok := r.texts[itemID]
	r.mu.Unlock()
	return ok && hash == sha256.Sum256([]byte(text))
}

func (r *refusedEmbedInputs) reset() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	clear(r.texts)
}

// loadEmbeddingItems loads items with their cast and crew, which the
// embedding text includes.
func (e *Engine) loadEmbeddingItems(ctx context.Context, ids []string) ([]*models.MediaItem, error) {
	items, err := e.itemRepo.GetByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	peopleMap, err := e.personRepo.ListForItems(ctx, ids)
	if err != nil {
		slog.WarnContext(ctx, "failed to hydrate item people for embeddings", "component", "recommendations", "error", err)
		return items, nil
	}
	for _, item := range items {
		if people, ok := peopleMap[item.ContentID]; ok {
			item.People = people
		}
	}
	return items, nil
}

// embeddingStore is the part of Repo the embedding backfill uses.
type embeddingStore interface {
	embeddingLockReader
	SetEmbeddingLock(ctx context.Context, lock EmbeddingLock) error
	ItemsNeedingEmbedding(ctx context.Context, currentModel, afterID string, limit int) ([]string, error)
	ListEmbeddingTextCandidates(ctx context.Context, afterID, currentModel string, limit int) ([]EmbeddingTextCandidate, error)
	UpsertEmbedding(ctx context.Context, itemID string, embedding []float32, model, canonicalText string) error
}

type embeddingLockReader interface {
	GetEmbeddingLock(ctx context.Context) (*EmbeddingLock, error)
}

// embedBackfill is one embedding run. It is not safe for concurrent use.
type embedBackfill struct {
	db      embeddingStore
	client  embedder
	baseURL string
	model   string
	// load returns the items to embed, with the people their text names.
	load func(ctx context.Context, ids []string) ([]*models.MediaItem, error)
	// refused records the inputs the provider refused, across runs.
	refused *refusedEmbedInputs

	counts EmbedCounts
	// lock is the embedding lock once this run has read or written it.
	lock *EmbeddingLock
	// consecutiveFailures counts the items in a row whose single-item embed
	// failed for a reason other than a refused input.
	consecutiveFailures int
	// lastErr is the newest per-item failure other than a refused input.
	lastErr error
	// probed records that the provider embedded embedProbeText in this run.
	probed bool
}

func (b *embedBackfill) run(ctx context.Context, includeTextStale bool) error {
	if err := checkEmbeddingLockConfig(ctx, b.db, b.baseURL, b.model); err != nil {
		return err
	}
	if includeTextStale {
		// The full run retries every input refused since the last one.
		b.refused.reset()
	}
	if err := b.embedMissing(ctx); err != nil {
		return err
	}
	if includeTextStale {
		if err := b.embedTextStale(ctx); err != nil {
			return err
		}
	}
	if b.counts.Embedded == 0 && b.lastErr != nil {
		return fmt.Errorf("embedding run stored none of the %d items it tried: %w", b.counts.Failed+b.counts.Skipped, b.lastErr)
	}
	return nil
}

// embedMissing is Pass 1: items with no embedding or one from another model.
func (b *embedBackfill) embedMissing(ctx context.Context) error {
	afterID := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ids, err := b.db.ItemsNeedingEmbedding(ctx, b.model, afterID, embeddingBackfillBatchSize)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		afterID = ids[len(ids)-1]

		items, err := b.load(ctx, ids)
		if err != nil {
			return fmt.Errorf("get items for embedding: %w", err)
		}
		toEmbed := make([]*models.MediaItem, 0, len(items))
		texts := make([]string, 0, len(items))
		for _, item := range items {
			text := embeddings.BuildEmbeddingText(item)
			if b.refused.has(item.ContentID, text) {
				continue
			}
			toEmbed = append(toEmbed, item)
			texts = append(texts, text)
		}
		if err := b.embedBatch(ctx, toEmbed, texts); err != nil {
			return err
		}
	}
}

// embedTextStale is Pass 2: items embedded with the current model whose
// canonical text no longer matches BuildEmbeddingText. The SQL copy of the
// text builder can disagree with Go, so each page is re-checked in Go and the
// scan moves past the rows it skips; rows that only look stale in SQL cannot
// hold back the real ones behind them.
func (b *embedBackfill) embedTextStale(ctx context.Context) error {
	quota := embeddingTextStaleQuotaPerRun
	afterID := ""
	for quota > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		candidates, err := b.db.ListEmbeddingTextCandidates(ctx, afterID, b.model, embeddingTextStaleQuotaPerRun)
		if err != nil {
			return err
		}
		if len(candidates) == 0 {
			return nil
		}
		afterID = candidates[len(candidates)-1].MediaItemID

		ids := make([]string, 0, len(candidates))
		stored := make(map[string]EmbeddingTextCandidate, len(candidates))
		for _, candidate := range candidates {
			ids = append(ids, candidate.MediaItemID)
			stored[candidate.MediaItemID] = candidate
		}
		items, err := b.load(ctx, ids)
		if err != nil {
			return fmt.Errorf("get items for embedding: %w", err)
		}
		staleItems := make([]*models.MediaItem, 0, len(items))
		staleTexts := make([]string, 0, len(items))
		for _, item := range items {
			text := embeddings.BuildEmbeddingText(item)
			candidate := stored[item.ContentID]
			if embeddingTextNeedsRefresh(candidate.Model, candidate.CanonicalText, text, b.model) {
				staleItems = append(staleItems, item)
				staleTexts = append(staleTexts, text)
			}
		}
		if len(staleItems) > quota {
			staleItems, staleTexts = staleItems[:quota], staleTexts[:quota]
		}
		if err := b.embedBatch(ctx, staleItems, staleTexts); err != nil {
			return err
		}
		// An item the provider refused uses none of the quota: the walk moves
		// past it, so refused items at the front cannot starve the rest.
		for i, item := range staleItems {
			if !b.refused.has(item.ContentID, staleTexts[i]) {
				quota--
			}
		}
		if len(candidates) < embeddingTextStaleQuotaPerRun {
			return nil
		}
	}
	return nil
}

func embeddingTextNeedsRefresh(storedModel, storedCanonicalText, generatedCanonicalText, currentModel string) bool {
	return storedModel != currentModel || storedCanonicalText != generatedCanonicalText
}

// embedBatch embeds and stores items (parallel items/texts slices), in calls
// of embeddingBackfillBatchSize. A failed batch call falls back to one call
// per item, so one item the provider rejects does not block the rest.
//
// It returns an error that ends the run when the provider will not serve it
// (a limit, rejected credentials, an unreachable host, repeated failures, or
// a refusal of every input), when a vector cannot be stored under the
// embedding lock, or when ctx ends. Other per-item failures are counted and
// the item is retried next run; an item whose input the provider refused is
// recorded in b.refused instead.
func (b *embedBackfill) embedBatch(ctx context.Context, items []*models.MediaItem, texts []string) error {
	for start := 0; start < len(items); start += embeddingBackfillBatchSize {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(start+embeddingBackfillBatchSize, len(items))
		chunkItems, chunkTexts := items[start:end], texts[start:end]

		vectors, err := b.client.Embed(ctx, chunkTexts)
		if err == nil && len(vectors) != len(chunkTexts) {
			err = fmt.Errorf("embedding API returned %d vectors for %d inputs", len(vectors), len(chunkTexts))
		}
		if err != nil {
			if stop := runStopError(ctx, err); stop != nil {
				return stop
			}
			slog.WarnContext(ctx, "batch embed failed, falling back to single-item mode", "component", "recommendations", "error", err, "batch_size", len(chunkItems))
			if err := b.embedEach(ctx, chunkItems, chunkTexts); err != nil {
				return err
			}
			continue
		}

		b.consecutiveFailures = 0
		for i, item := range chunkItems {
			if _, err := b.save(ctx, item, vectors[i], chunkTexts[i]); err != nil {
				return err
			}
		}
	}
	return nil
}

// embedEach embeds and stores items one call at a time.
func (b *embedBackfill) embedEach(ctx context.Context, items []*models.MediaItem, texts []string) error {
	for i, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		vector, limit, err := b.embedOne(ctx, texts[i])
		if err != nil {
			if stop := runStopError(ctx, err); stop != nil {
				return stop
			}
			// A refused input says nothing about the provider: it is skipped
			// and is not a failure, so permanently refused items at the head
			// of the backlog cannot stop every run.
			if embeddings.InputRejected(err) {
				if err := b.checkProviderAcceptsInput(ctx); err != nil {
					b.counts.Failed++
					return err
				}
				b.counts.Skipped++
				b.refused.add(item.ContentID, texts[i])
				slog.WarnContext(ctx, "skipping item, provider refused its text", "component", "recommendations", "item_id", item.ContentID, "error", err)
				continue
			}
			b.lastErr = err
			b.counts.Failed++
			b.consecutiveFailures++
			slog.WarnContext(ctx, "skipping item, embed failed", "component", "recommendations", "item_id", item.ContentID, "error", err)
			// The batch call already failed; when the first item alone fails
			// too and nothing has been stored, the provider is not working.
			if (i == 0 && b.counts.Embedded == 0) || b.consecutiveFailures >= maxConsecutiveEmbedFailures {
				return providerUnavailable(err)
			}
			continue
		}

		b.consecutiveFailures = 0
		// Store the full text, not the shortened one: Pass 2 compares the
		// stored text with the full text and would otherwise re-embed the
		// item on every run.
		saved, err := b.save(ctx, item, vector, texts[i])
		if err != nil {
			return err
		}
		if saved && limit > 0 {
			b.counts.Truncated++
			slog.WarnContext(ctx, "embedded item from shortened text", "component", "recommendations", "item_id", item.ContentID, "runes", utf8.RuneCountInString(texts[i]), "limit", limit)
		}
	}
	return nil
}

// embedOne embeds one text. When the provider rejects it and the text is
// longer than embedRetryRunes, it retries with the text shortened to
// embedRetryRunes, then embedRetryShortRunes. limit is the length that
// succeeded, or 0 for the full text.
func (b *embedBackfill) embedOne(ctx context.Context, text string) (vector []float32, limit int, err error) {
	vector, err = b.embedText(ctx, text)
	if err == nil || !lengthMayBeRefused(err) || runStopError(ctx, err) != nil || utf8.RuneCountInString(text) <= embedRetryRunes {
		return vector, 0, err
	}
	for _, runes := range []int{embedRetryRunes, embedRetryShortRunes} {
		vector, err = b.embedText(ctx, embeddings.TruncateRunes(text, runes))
		if err == nil {
			return vector, runes, nil
		}
		if !lengthMayBeRefused(err) || runStopError(ctx, err) != nil {
			break
		}
	}
	return nil, 0, err
}

// lengthMayBeRefused reports whether err can mean the provider refused the
// input's length: a refused input, or the 5xx a local model such as Ollama
// answers an input longer than its context with. Only then is a shortened
// text worth sending; after any other failure a shortened vector would be
// stored under the full text and never refreshed.
func lengthMayBeRefused(err error) bool {
	return embeddings.InputRejected(err) || strings.Contains(strings.ToLower(err.Error()), "context length")
}

func (b *embedBackfill) embedText(ctx context.Context, text string) ([]float32, error) {
	vectors, err := b.client.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 {
		return nil, fmt.Errorf("embedding API returned %d vectors for 1 input", len(vectors))
	}
	return vectors[0], nil
}

// checkProviderAcceptsInput is called after a refused input. Until the run
// has stored a vector, it embeds embedProbeText once, and returns an error
// that ends the run when that fails too: a provider that refuses every input
// (Gemini answers a bad API key with 400) is down, not the items.
func (b *embedBackfill) checkProviderAcceptsInput(ctx context.Context) error {
	if b.probed || b.counts.Embedded > 0 {
		return nil
	}
	if _, err := b.embedText(ctx, embedProbeText); err != nil {
		if stop := runStopError(ctx, err); stop != nil {
			return stop
		}
		return providerUnavailable(err)
	}
	b.probed = true
	return nil
}

// save stores one item's vector with canonicalText. It returns an error, which
// ends the run, when the vector does not fit the embedding lock. A failed
// write is counted and logged, and saved is false.
func (b *embedBackfill) save(ctx context.Context, item *models.MediaItem, vector []float32, canonicalText string) (saved bool, err error) {
	if err := b.ensureLock(ctx, vector); err != nil {
		return false, fmt.Errorf("embed item %s: %w", item.ContentID, err)
	}
	if err := b.db.UpsertEmbedding(ctx, item.ContentID, vector, b.model, canonicalText); err != nil {
		b.counts.Failed++
		b.lastErr = err
		slog.WarnContext(ctx, "skipping item, store failed", "component", "recommendations", "item_id", item.ContentID, "error", err)
		return false, nil
	}
	b.counts.Embedded++
	return true, nil
}

// ensureLock checks vector against the embedding lock, writing the lock from
// it when none exists. A vector that cannot be stored never writes the lock,
// so a model with unusable output cannot pin the installation to itself.
func (b *embedBackfill) ensureLock(ctx context.Context, vector []float32) error {
	if err := checkStorableVector(vector); err != nil {
		return err
	}
	if b.lock == nil {
		lock, err := b.db.GetEmbeddingLock(ctx)
		if err != nil {
			return fmt.Errorf("load embedding lock: %w", err)
		}
		if lock == nil {
			lock = &EmbeddingLock{
				BaseURL:           b.baseURL,
				Model:             b.model,
				SourceDimensions:  len(vector),
				StorageDimensions: CanonicalEmbeddingDimensions,
			}
			if err := b.db.SetEmbeddingLock(ctx, *lock); err != nil {
				return err
			}
		}
		b.lock = lock
	}
	return b.lock.Validate(b.baseURL, b.model, len(vector))
}

// checkStorableVector rejects a vector the embeddings table cannot hold.
func checkStorableVector(vector []float32) error {
	switch n := len(vector); {
	case n == 0:
		return errors.New("embedding model returned an empty vector")
	case n > CanonicalEmbeddingDimensions:
		return fmt.Errorf("embedding model returns %d dimensions; Silo stores at most %d", n, CanonicalEmbeddingDimensions)
	}
	return nil
}

// checkEmbeddingLockConfig fails when an embedding lock exists for another
// base URL or model.
func checkEmbeddingLockConfig(ctx context.Context, store embeddingLockReader, baseURL, model string) error {
	lock, err := store.GetEmbeddingLock(ctx)
	if err != nil {
		return fmt.Errorf("load embedding lock: %w", err)
	}
	if lock == nil {
		return nil
	}
	return lock.ValidateConfig(baseURL, model)
}

// runStopError returns the error that ends the run after a failed embed call,
// or nil when the run can go on without that call.
func runStopError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	// A provider limit will not improve by splitting the batch.
	if isQuotaError(err) {
		return fmt.Errorf("embedding batch stopped: %w", err)
	}
	if embeddings.Unavailable(err) {
		return providerUnavailable(err)
	}
	return nil
}

func providerUnavailable(err error) error {
	return fmt.Errorf("embedding provider unavailable: %w", err)
}

// isQuotaError identifies provider limits that should stop this backfill run:
// a spent quota, a rate limit that outlasted the retries, or a retry delay too
// long to wait for.
func isQuotaError(err error) bool {
	var limitErr *embeddings.RateLimitError
	if errors.As(err, &limitErr) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "insufficient_quota") ||
		strings.Contains(msg, "exceeded your current quota") ||
		strings.Contains(msg, "billing")
}
