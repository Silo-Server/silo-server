package recommendations

import (
	"context"
	"fmt"

	"log/slog"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/recommendations/embeddings"
)

// isQuotaError returns true if the error indicates an API quota/billing issue
// that won't be resolved by retrying.
func isQuotaError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "insufficient_quota") ||
		strings.Contains(msg, "exceeded your current quota") ||
		strings.Contains(msg, "billing")
}

func embeddingTextNeedsRefresh(storedModel, storedCanonicalText, generatedCanonicalText, currentModel string) bool {
	return storedModel != currentModel || storedCanonicalText != generatedCanonicalText
}

const (
	// embeddingBackfillBatchSize is how many items EmbedAll embeds per API call.
	embeddingBackfillBatchSize = 10

	// embeddingTextStaleQuotaPerRun bounds how many text-stale items the
	// expensive Pass 2 re-embeds in a single EmbedAll run. It caps the cost of
	// the one full-table text-staleness CTE scan per run.
	embeddingTextStaleQuotaPerRun = 200
)

// SimilarItems returns items most similar to the given item. Blends embedding
// similarity (70%) with co-watch Jaccard score (30%), applies a validation
// pipeline, MMR re-ranking, and assigns connection reasons.
func (e *Engine) SimilarItems(ctx context.Context, itemID string, limit int) ([]ScoredItem, error) {
	// 1. Fetch source embedding.
	embedding, err := e.repo.GetEmbedding(ctx, itemID)
	if err != nil {
		return nil, fmt.Errorf("get embedding for item %s: %w", itemID, err)
	}
	if embedding == nil {
		return nil, nil
	}

	// 2. Fetch source metadata for validation pipeline.
	sourceMeta, err := e.repo.GetItemMetadata(ctx, itemID)
	if err != nil {
		slog.WarnContext(ctx, "could not fetch source metadata, skipping validation", "component", "recommendations", "item_id", itemID, "error", err)
	}
	sourceType := ""
	if sourceMeta != nil {
		sourceType = sourceMeta.Type
	}

	// 3. Embedding search (3x limit for filtering headroom). Constrain to the
	// source item's media type so an audiobook never appears in a movie's
	// Similar rail (and vice versa) once audiobook embeddings exist.
	embCandidates, err := e.repo.FindSimilar(ctx, embedding, []string{itemID}, sourceType, limit*3)
	if err != nil {
		return nil, fmt.Errorf("find similar items: %w", err)
	}

	// 4. Co-watch neighbors.
	cowatchPairs, _ := e.repo.GetCowatchNeighbors(ctx, itemID, limit*3)
	cowatchMap := make(map[string]float64, len(cowatchPairs))
	for _, p := range cowatchPairs {
		cowatchMap[p.SimilarItemID] = p.JaccardScore
	}

	// 5. Blend scores (70% embedding, 30% co-watch).
	blended := blendScores(embCandidates, cowatchMap, 0.7, 0.3)

	// 6. Validation pipeline.
	if sourceMeta != nil && len(blended) > 0 {
		blended = e.applyValidation(ctx, sourceMeta, blended)
	}

	// 7. MMR re-ranking.
	candidateIDs := make([]string, len(blended))
	for i, item := range blended {
		candidateIDs[i] = item.MediaItemID
	}
	embMap, _ := e.repo.GetBatchEmbeddings(ctx, candidateIDs)
	result := applyMMR(blended, embMap, e.mmrLambda(LambdaSimilarItems), limit)

	// 8. Connection reasons.
	e.assignReasons(ctx, itemID, sourceMeta, result)

	return result, nil
}

// applyValidation filters and penalizes candidates using the validation pipeline.
func (e *Engine) applyValidation(ctx context.Context, sourceMeta *ItemMetadata, candidates []ScoredItem) []ScoredItem {
	candidateIDs := make([]string, len(candidates))
	for i, item := range candidates {
		candidateIDs[i] = item.MediaItemID
	}

	// Batch fetch candidate genres and titles.
	candidateGenres, _ := e.repo.GetItemAllGenres(ctx, candidateIDs)

	// We need titles for title-pattern detection. Fetch lightweight metadata.
	type titleYear struct {
		title string
		year  int
	}
	candTitles := make(map[string]titleYear)
	rows, err := e.pool.Query(ctx, `
		SELECT content_id, title, COALESCE(year, 0) FROM media_items
		WHERE content_id = ANY($1)`, candidateIDs)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var id, title string
			var year int
			if rows.Scan(&id, &title, &year) == nil {
				candTitles[id] = titleYear{title: title, year: year}
			}
		}
	}

	var validated []ScoredItem
	for _, item := range candidates {
		candMeta := &ItemMetadata{
			Genres: candidateGenres[item.MediaItemID],
		}
		if ty, ok := candTitles[item.MediaItemID]; ok {
			candMeta.Title = ty.title
			candMeta.Year = ty.year
		}

		result := validateCandidate(sourceMeta, candMeta, item)
		if result.rejected {
			continue
		}
		if result.scoreMult < 1.0 {
			item.Score *= result.scoreMult
		}
		validated = append(validated, item)
	}

	return validated
}

// assignReasons computes connection reasons for each result item.
func (e *Engine) assignReasons(ctx context.Context, sourceItemID string, sourceMeta *ItemMetadata, items []ScoredItem) {
	if len(items) == 0 {
		return
	}

	// Default all to similar_content.
	for i := range items {
		items[i].Reason = "similar_content"
	}

	if sourceMeta == nil {
		return
	}

	candIDs := make([]string, len(items))
	for i := range items {
		candIDs[i] = items[i].MediaItemID
	}

	// Batch fetch people for source + candidates.
	allIDs := make([]string, 0, len(candIDs)+1)
	allIDs = append(allIDs, sourceItemID)
	allIDs = append(allIDs, candIDs...)

	allPeople := make(map[string]*itemPeopleData)
	if e.personRepo != nil {
		peopleMap, err := e.personRepo.ListForItems(ctx, allIDs)
		if err == nil {
			for id, people := range peopleMap {
				pd := &itemPeopleData{}
				for _, p := range people {
					switch p.Kind {
					case models.PersonKindDirector:
						pd.directors = append(pd.directors, p.Name)
					case models.PersonKindActor:
						if p.SortOrder <= 5 {
							pd.actors = append(pd.actors, p.Name)
						}
					}
				}
				allPeople[id] = pd
			}
		}
	}

	// Batch fetch candidate studios and genres for reasons.
	candStudios := make(map[string][]string)
	candidateGenres, _ := e.repo.GetItemAllGenres(ctx, candIDs)
	rows, err := e.pool.Query(ctx, `
		SELECT content_id, studios FROM media_items
		WHERE content_id = ANY($1) AND studios IS NOT NULL`, candIDs)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var id string
			var studios []string
			if rows.Scan(&id, &studios) == nil {
				candStudios[id] = studios
			}
		}
	}

	sourcePeople := allPeople[sourceItemID]
	if sourcePeople == nil {
		sourcePeople = &itemPeopleData{}
	}

	for i := range items {
		candPeople := allPeople[items[i].MediaItemID]
		if candPeople == nil {
			candPeople = &itemPeopleData{}
		}
		candGenres := candidateGenres[items[i].MediaItemID]
		reason, detail := computeReason(sourcePeople, candPeople, sourceMeta.Studios, candStudios[items[i].MediaItemID], sourceMeta.Genres, candGenres)
		items[i].Reason = reason
		items[i].ReasonDetail = detail
	}
}

// EmbedItem generates and stores an embedding for a single media item.
func (e *Engine) EmbedItem(ctx context.Context, itemID string) error {
	if err := e.ensureEmbeddingLockConfig(ctx); err != nil {
		return fmt.Errorf("embed item %s: %w", itemID, err)
	}

	items, err := e.itemRepo.GetByIDs(ctx, []string{itemID})
	if err != nil || len(items) == 0 {
		return fmt.Errorf("get item %s: %w", itemID, err)
	}

	// Hydrate cast/crew from item_people for richer embedding text.
	e.hydrateItemPeople(ctx, items)

	text := embeddings.BuildEmbeddingText(items[0])
	vectors, err := e.embClient.Embed(ctx, []string{text})
	if err != nil {
		return fmt.Errorf("embed item %s: %w", itemID, err)
	}
	if len(vectors) == 0 {
		return fmt.Errorf("no embedding returned for item %s", itemID)
	}

	if err := e.ensureEmbeddingLock(ctx, vectors[0]); err != nil {
		return fmt.Errorf("embed item %s: %w", itemID, err)
	}

	return e.repo.UpsertEmbedding(ctx, itemID, vectors[0], e.cfg.EmbeddingModel, text)
}

// EmbedAll embeds items that are missing embeddings or have stale canonical
// text, in two passes:
//
//   - Pass 1 (cheap): drain every missing or model-stale item via the
//     ItemsNeedingEmbedding cursor. This query is a single LEFT JOIN with no
//     item_people LATERAL joins, so active backfill (lots of brand-new items)
//     stays cheap. The cursor advances past each page, so an item that fails to
//     embed or store is simply retried on the next EmbedAll run rather than
//     stalling the page forever.
//   - Pass 2 (expensive): ONLY once Pass 1 has fully drained, make a single
//     ListEmbeddingTextCandidates scan (LIMIT embeddingTextStaleQuotaPerRun) to
//     find items whose embedding model is current but whose canonical text has
//     drifted (e.g. a cast change). Detecting this requires recomputing each
//     row's text via the full item_people LATERAL CTE, so it is run at most once
//     per EmbedAll call. Re-embedding refreshes canonical_text, so handled items
//     drop out of the candidate set on the next run — natural forward progress
//     without a Pass 2 cursor.
//
// Coverage-first tradeoff: in steady state Pass 1 drains every run, so text-stale
// items are re-embedded promptly. Only under pathological continuous heavy
// ingest (Pass 1 never drains within a run) does Pass 2 get skipped — we
// deliberately prioritize getting NEW items covered over re-embedding
// text-changed ones. A periodic "force Pass 2 every Nth run" is a possible
// follow-up, intentionally out of scope here.
func (e *Engine) EmbedAll(ctx context.Context) (int, error) {
	model := e.cfg.EmbeddingModel
	total := 0

	if err := e.ensureEmbeddingLockConfig(ctx); err != nil {
		return total, err
	}

	// Pass 1: drain the cheap missing/model-stale backlog via the cursor.
	afterID := ""
	for {
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		default:
		}

		ids, err := e.repo.ItemsNeedingEmbedding(ctx, model, afterID, embeddingBackfillBatchSize)
		if err != nil {
			return total, err
		}
		if len(ids) == 0 {
			break
		}
		afterID = ids[len(ids)-1]

		items, err := e.itemRepo.GetByIDs(ctx, ids)
		if err != nil {
			return total, fmt.Errorf("get items for embedding: %w", err)
		}
		// Hydrate cast/crew for richer embedding text.
		e.hydrateItemPeople(ctx, items)

		texts := make([]string, len(items))
		for i, item := range items {
			texts[i] = embeddings.BuildEmbeddingText(item)
		}

		// embedBatch only returns quota/billing or context errors; both mean the
		// cheap backlog did not fully drain this run, so we stop here and skip the
		// expensive Pass 2. Any per-item store/embed failure is swallowed inside
		// embedBatch and retried next run (the cursor passes over it).
		embedded, err := e.embedBatch(ctx, items, texts, model)
		total += embedded
		if err != nil {
			return total, err
		}
	}

	// Reaching here means Pass 1 fully drained (every error path above returns).

	select {
	case <-ctx.Done():
		return total, ctx.Err()
	default:
	}

	// Pass 2: one expensive text-staleness scan, bounded by the per-run quota.
	candidates, err := e.repo.ListEmbeddingTextCandidates(ctx, "", model, embeddingTextStaleQuotaPerRun)
	if err != nil {
		return total, err
	}
	if len(candidates) == 0 {
		return total, nil
	}

	ids := make([]string, 0, len(candidates))
	stored := make(map[string]EmbeddingTextCandidate, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.MediaItemID)
		stored[candidate.MediaItemID] = candidate
	}

	items, err := e.itemRepo.GetByIDs(ctx, ids)
	if err != nil {
		return total, fmt.Errorf("get items for embedding: %w", err)
	}
	e.hydrateItemPeople(ctx, items)

	// Pass 1 already cleared missing/model-stale rows, so the remaining
	// candidates are text-stale. Re-confirm in Go (the SQL detection is an
	// approximation of BuildEmbeddingText) before paying for an embed.
	staleItems := make([]*models.MediaItem, 0, len(items))
	staleTexts := make([]string, 0, len(items))
	for _, item := range items {
		text := embeddings.BuildEmbeddingText(item)
		candidate := stored[item.ContentID]
		if embeddingTextNeedsRefresh(candidate.Model, candidate.CanonicalText, text, model) {
			staleItems = append(staleItems, item)
			staleTexts = append(staleTexts, text)
		}
	}

	embedded, err := e.embedBatch(ctx, staleItems, staleTexts, model)
	total += embedded
	if err != nil {
		return total, err
	}

	return total, nil
}

// embedBatch embeds the given items (parallel items/texts slices, same length),
// chunking internally by embeddingBackfillBatchSize so callers can pass any
// size. It returns the number of items successfully stored.
//
// Behavior preserved from the original EmbedAll inner loop:
//   - quota/billing errors return immediately (retrying won't help) and are the
//     only batch-embed errors propagated to the caller;
//   - any other batch embed failure falls back to embedding one item at a time
//     so a single oversized item can't block the rest;
//   - ensureEmbeddingLock runs before each upsert;
//   - a store (upsert) failure for one item is logged and skipped, not fatal.
func (e *Engine) embedBatch(ctx context.Context, items []*models.MediaItem, texts []string, model string) (int, error) {
	total := 0
	for start := 0; start < len(items); start += embeddingBackfillBatchSize {
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		default:
		}

		end := start + embeddingBackfillBatchSize
		if end > len(items) {
			end = len(items)
		}
		chunkItems := items[start:end]
		chunkTexts := texts[start:end]
		if len(chunkItems) == 0 {
			continue
		}

		vectors, err := e.embClient.Embed(ctx, chunkTexts)
		if err != nil {
			// Quota/billing errors won't resolve by retrying — stop immediately.
			if isQuotaError(err) {
				return total, fmt.Errorf("embedding API quota exceeded, check billing: %w", err)
			}

			// Batch failed — fall back to embedding one at a time so a single
			// oversized item doesn't block the entire job.
			slog.WarnContext(ctx, "batch embed failed, falling back to single-item mode", "component", "recommendations", "error", err, "batch_size", len(chunkItems))
			for i, item := range chunkItems {
				if ctx.Err() != nil {
					return total, ctx.Err()
				}
				single := chunkTexts[i]
				vecs, embedErr := e.embClient.Embed(ctx, []string{single})
				if embedErr != nil {
					if isQuotaError(embedErr) {
						return total, fmt.Errorf("embedding API quota exceeded, check billing: %w", embedErr)
					}
					if ctx.Err() != nil {
						return total, ctx.Err()
					}
					slog.WarnContext(ctx, "skipping item, embed failed", "component", "recommendations", "item_id", item.ContentID, "error", embedErr)
					continue
				}
				if len(vecs) > 0 {
					if err := e.ensureEmbeddingLock(ctx, vecs[0]); err != nil {
						return total, fmt.Errorf("embed item %s: %w", item.ContentID, err)
					}
					if storeErr := e.repo.UpsertEmbedding(ctx, item.ContentID, vecs[0], model, single); storeErr != nil {
						slog.WarnContext(ctx, "skipping item, store failed", "component", "recommendations", "item_id", item.ContentID, "error", storeErr)
						continue
					}
					total++
				}
			}
			continue
		}

		for i, item := range chunkItems {
			if i >= len(vectors) {
				break
			}
			if err := e.ensureEmbeddingLock(ctx, vectors[i]); err != nil {
				return total, fmt.Errorf("embed item %s: %w", item.ContentID, err)
			}
			if err := e.repo.UpsertEmbedding(ctx, item.ContentID, vectors[i], model, chunkTexts[i]); err != nil {
				slog.WarnContext(ctx, "skipping item, store failed", "component", "recommendations", "item_id", item.ContentID, "error", err)
				continue
			}
			total++
		}
	}

	return total, nil
}

// hydrateItemPeople populates the People field on media items from item_people.
func (e *Engine) hydrateItemPeople(ctx context.Context, items []*models.MediaItem) {
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.ContentID
	}

	peopleMap, err := e.personRepo.ListForItems(ctx, ids)
	if err != nil {
		slog.WarnContext(ctx, "failed to hydrate item people for embeddings", "component", "recommendations", "error", err)
		return
	}

	for _, item := range items {
		if people, ok := peopleMap[item.ContentID]; ok {
			item.People = people
		}
	}
}

func (e *Engine) ensureEmbeddingLock(ctx context.Context, vector []float32) error {
	sourceDimensions := len(vector)

	lock, err := e.repo.GetEmbeddingLock(ctx)
	if err != nil {
		return fmt.Errorf("load embedding lock: %w", err)
	}
	if lock == nil {
		return e.repo.SetEmbeddingLock(ctx, EmbeddingLock{
			BaseURL:           e.cfg.EmbeddingBaseURL,
			Model:             e.cfg.EmbeddingModel,
			SourceDimensions:  sourceDimensions,
			StorageDimensions: CanonicalEmbeddingDimensions,
		})
	}

	if err := lock.Validate(e.cfg.EmbeddingBaseURL, e.cfg.EmbeddingModel, sourceDimensions); err != nil {
		return err
	}
	return nil
}

func (e *Engine) ensureEmbeddingLockConfig(ctx context.Context) error {
	lock, err := e.repo.GetEmbeddingLock(ctx)
	if err != nil {
		return fmt.Errorf("load embedding lock: %w", err)
	}
	if lock == nil {
		return nil
	}
	if err := lock.ValidateConfig(e.cfg.EmbeddingBaseURL, e.cfg.EmbeddingModel); err != nil {
		return err
	}
	return nil
}
