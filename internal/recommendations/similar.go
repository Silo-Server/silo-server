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
		slog.Warn("could not fetch source metadata, skipping validation", "item_id", itemID, "error", err)
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

// EmbedAll embeds all items that are missing embeddings or have stale canonical text.
func (e *Engine) EmbedAll(ctx context.Context) (int, error) {
	model := e.cfg.EmbeddingModel
	total := 0
	batchSize := 10
	afterID := ""

	if err := e.ensureEmbeddingLockConfig(ctx); err != nil {
		return total, err
	}

	for {
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		default:
		}

		candidates, err := e.repo.ListEmbeddingTextCandidates(ctx, afterID, model, batchSize)
		if err != nil {
			return total, err
		}
		if len(candidates) == 0 {
			break
		}
		afterID = candidates[len(candidates)-1].MediaItemID

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

		// Hydrate cast/crew for richer embedding text.
		e.hydrateItemPeople(ctx, items)

		texts := make([]string, len(items))
		staleItems := make([]*models.MediaItem, 0, len(items))
		staleTexts := make([]string, 0, len(items))
		for i, item := range items {
			texts[i] = embeddings.BuildEmbeddingText(item)
			candidate := stored[item.ContentID]
			if embeddingTextNeedsRefresh(candidate.Model, candidate.CanonicalText, texts[i], model) {
				staleItems = append(staleItems, item)
				staleTexts = append(staleTexts, texts[i])
			}
		}
		if len(staleItems) == 0 {
			continue
		}

		vectors, err := e.embClient.Embed(ctx, staleTexts)
		if err != nil {
			// Quota/billing errors won't resolve by retrying — stop immediately.
			if isQuotaError(err) {
				return total, fmt.Errorf("embedding API quota exceeded, check billing: %w", err)
			}

			// Batch failed — fall back to embedding one at a time so a single
			// oversized item doesn't block the entire job.
			slog.Warn("batch embed failed, falling back to single-item mode", "error", err, "batch_size", len(staleItems))
			for i, item := range staleItems {
				if ctx.Err() != nil {
					return total, ctx.Err()
				}
				single := staleTexts[i]
				vecs, embedErr := e.embClient.Embed(ctx, []string{single})
				if embedErr != nil {
					if isQuotaError(embedErr) {
						return total, fmt.Errorf("embedding API quota exceeded, check billing: %w", embedErr)
					}
					if ctx.Err() != nil {
						return total, ctx.Err()
					}
					slog.Warn("skipping item, embed failed", "item_id", item.ContentID, "error", embedErr)
					continue
				}
				if len(vecs) > 0 {
					if err := e.ensureEmbeddingLock(ctx, vecs[0]); err != nil {
						return total, fmt.Errorf("embed item %s: %w", item.ContentID, err)
					}
					if storeErr := e.repo.UpsertEmbedding(ctx, item.ContentID, vecs[0], model, single); storeErr != nil {
						slog.Warn("skipping item, store failed", "item_id", item.ContentID, "error", storeErr)
						continue
					}
					total++
				}
			}
			continue
		}

		for i, item := range staleItems {
			if i >= len(vectors) {
				break
			}
			if err := e.ensureEmbeddingLock(ctx, vectors[i]); err != nil {
				return total, fmt.Errorf("embed item %s: %w", item.ContentID, err)
			}
			if err := e.repo.UpsertEmbedding(ctx, item.ContentID, vectors[i], model, staleTexts[i]); err != nil {
				slog.Warn("skipping item, store failed", "item_id", item.ContentID, "error", err)
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
		slog.Warn("failed to hydrate item people for embeddings", "error", err)
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
