package recommendations

import (
	"context"
	"fmt"

	"log/slog"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// SimilarItems returns items most similar to the given item for a profile.
// Blends embedding similarity (70%) with co-watch Jaccard score (30%),
// applies a validation pipeline and MMR re-ranking. Titles the profile marked
// Not interested are left out before ranking, so they do not take the list's
// places. The items carry no connection reason; SimilarItemsWithReasons adds
// them.
func (e *Engine) SimilarItems(ctx context.Context, userID int, profileID, itemID string, limit int, filter catalog.AccessFilter) ([]ScoredItem, error) {
	result, _, err := e.rankSimilar(ctx, userID, profileID, itemID, limit, filter)
	return result, err
}

// SimilarItemsWithReasons is SimilarItems with each item's connection reason
// set. Only the v1 similar list answers reasons, and they cost three more
// reads (credits, genres, studios), so the other lists skip them.
func (e *Engine) SimilarItemsWithReasons(ctx context.Context, userID int, profileID, itemID string, limit int, filter catalog.AccessFilter) ([]ScoredItem, error) {
	result, sourceMeta, err := e.rankSimilar(ctx, userID, profileID, itemID, limit, filter)
	if err != nil {
		return nil, err
	}
	e.assignReasons(ctx, itemID, sourceMeta, result)
	return result, nil
}

// rankSimilar ranks the items similar to itemID and returns them with the
// source metadata the connection reasons compare against.
func (e *Engine) rankSimilar(ctx context.Context, userID int, profileID, itemID string, limit int, filter catalog.AccessFilter) ([]ScoredItem, *ItemMetadata, error) {
	marks, err := e.signalReader().NotInterestedMarks(ctx, userID, profileID)
	if err != nil {
		return nil, nil, fmt.Errorf("get not interested marks: %w", err)
	}
	excludeIDs := make([]string, 0, len(marks)+1)
	excludeIDs = append(excludeIDs, itemID)
	for _, m := range marks {
		excludeIDs = append(excludeIDs, m.MediaItemID)
	}

	// 1. Fetch the source embedding, and the source metadata for the
	// validation pipeline.
	embedding, sourceMeta, err := e.repo.GetSimilarSource(ctx, itemID)
	if err != nil {
		return nil, nil, err
	}
	if embedding == nil {
		return nil, nil, nil
	}
	sourceType := sourceMeta.Type

	// 2. Embedding search (3x limit for filtering headroom). Constrain to the
	// source item's media type so an audiobook never appears in a movie's
	// Similar rail (and vice versa) once audiobook embeddings exist. Titles
	// the viewer cannot see are left out before ranking, so they do not take
	// the list's places. The candidates come with the details validation and
	// MMR read.
	found, err := e.repo.FindSimilarCandidates(ctx, embedding, excludeIDs, sourceType, limit*3, filter)
	if err != nil {
		return nil, nil, fmt.Errorf("find similar items: %w", err)
	}
	embCandidates := make([]ScoredItem, len(found))
	details := make(map[string]CandidateDetails, len(found))
	for i, c := range found {
		embCandidates[i] = c.ScoredItem
		details[c.MediaItemID] = c.Details
	}

	// 3. Co-watch neighbors of the source item's media type that the viewer
	// can see: co-watch counts ebook reading too, the rail keeps to one type
	// like the embedding search, and hidden neighbors must not take places in
	// the ranked list. A source of unknown type keeps every type. Co-watch
	// only adds to the embedding candidates, so neighbors that cannot be
	// checked are left out rather than failing the list.
	cowatchPairs, _ := e.repo.GetCowatchNeighbors(ctx, itemID, limit*3)
	cowatchMap, err := e.cowatchScores(ctx, cowatchPairs, filter, cowatchMediaTypes(sourceType, nil))
	if err != nil {
		slog.WarnContext(ctx, "filtering co-watch neighbors failed; leaving them out", "component", "recommendations", "item_id", itemID, "error", err)
		cowatchMap = map[string]float64{}
	}

	// 4. Blend scores (70% embedding, 30% co-watch), leaving out the marked
	// titles co-watch brought in.
	blended := blendScores(embCandidates, cowatchMap, 0.7, 0.3)
	if len(marks) > 0 {
		marked := make(map[string]struct{}, len(marks))
		for _, m := range marks {
			marked[m.MediaItemID] = struct{}{}
		}
		blended = excludeScoredItems(blended, marked)
	}

	// 5. Validation pipeline, after reading the details of the co-watch
	// neighbors the embedding search did not return.
	var cowatchOnly []string
	for _, item := range blended {
		if _, ok := details[item.MediaItemID]; !ok {
			cowatchOnly = append(cowatchOnly, item.MediaItemID)
		}
	}
	if len(cowatchOnly) > 0 {
		cowatchDetails, err := e.repo.GetCandidateDetails(ctx, cowatchOnly)
		if err != nil {
			slog.WarnContext(ctx, "reading co-watch candidate details failed; ranking them without", "component", "recommendations", "item_id", itemID, "error", err)
		}
		for id, d := range cowatchDetails {
			details[id] = d
		}
	}
	if len(blended) > 0 {
		blended = applyValidation(sourceMeta, blended, details)
	}

	// 6. MMR re-ranking.
	embMap := make(map[string][]float32, len(details))
	for id, d := range details {
		if d.Embedding != nil {
			embMap[id] = d.Embedding
		}
	}
	return applyMMR(blended, embMap, e.mmrLambda(), limit), sourceMeta, nil
}

// applyValidation filters and penalizes candidates using the validation
// pipeline, reading each candidate's title, year and genres from details.
func applyValidation(sourceMeta *ItemMetadata, candidates []ScoredItem, details map[string]CandidateDetails) []ScoredItem {
	var validated []ScoredItem
	for _, item := range candidates {
		candMeta := details[item.MediaItemID].Meta

		result := validateCandidate(sourceMeta, &candMeta, item)
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
		if err := rows.Err(); err != nil {
			slog.WarnContext(ctx, "reading candidate studios failed; assigning reasons without them", "component", "recommendations", "error", err)
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
	_, err := checkEmbeddingLockConfig(ctx, e.repo, e.cfg.EmbeddingModel)
	return err
}
