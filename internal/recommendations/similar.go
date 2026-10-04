package recommendations

import (
	"context"
	"fmt"

	"log/slog"

	"github.com/Silo-Server/silo-server/internal/models"
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
	result := applyMMR(blended, embMap, e.mmrLambda(), limit)

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

func (e *Engine) ensureEmbeddingLockConfig(ctx context.Context) error {
	return checkEmbeddingLockConfig(ctx, e.repo, e.cfg.EmbeddingBaseURL, e.cfg.EmbeddingModel)
}
