package metadata

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Outcomes recorded in metadata_enrichment_state.
const (
	enrichmentOutcomeFound  = "found"
	enrichmentOutcomeEmpty  = "empty"
	enrichmentOutcomeFailed = "failed"
)

// enrichmentCandidate is an item the bulk enrichment pass should look up.
type enrichmentCandidate struct {
	ContentID string
	Type      string
	// Language is the item's canonical metadata language, "" when unset.
	Language string
	// ProviderIDs are the item's durable provider IDs.
	ProviderIDs map[string]string
}

// enrichmentCandidateQuery selects the items one provider has not answered for
// yet, or whose last empty or failed answer is due for another look.
type enrichmentCandidateQuery struct {
	Provider string
	// LookupKeys are the provider-ID keys the provider can look an item up
	// by; an item needs at least one of them.
	LookupKeys []string
	// MovieFolders and SeriesFolders are the libraries whose movie and series
	// provider chains include the provider.
	MovieFolders  []int
	SeriesFolders []int
	// After is the content ID the previous page ended on; "" starts over.
	After string
	Limit int
}

// enrichmentOutcome is what one lookup found. NextCheckAt is nil for a found
// item, which the pass never looks up again.
type enrichmentOutcome struct {
	ContentID   string
	Outcome     string
	NextCheckAt *time.Time
}

// enrichmentStateStore keeps the bulk enrichment pass's record of which items
// each enrichment provider has answered for.
type enrichmentStateStore interface {
	Candidates(ctx context.Context, query enrichmentCandidateQuery) ([]enrichmentCandidate, error)
	CountCandidates(ctx context.Context, query enrichmentCandidateQuery) (int, error)
	RecordOutcomes(ctx context.Context, provider string, outcomes []enrichmentOutcome) error
	// RecordFound marks each provider as having answered for the item, from
	// a match or refresh that ran the provider in its chain.
	RecordFound(ctx context.Context, contentID string, providers []string) error
}

// enrichmentStateRepository is the Postgres enrichmentStateStore.
type enrichmentStateRepository struct {
	pool *pgxpool.Pool
}

func newEnrichmentStateRepository(pool *pgxpool.Pool) *enrichmentStateRepository {
	return &enrichmentStateRepository{pool: pool}
}

// enrichmentCandidatePredicate selects matched movies and series in a library
// that runs the provider, carrying a lookup ID, and not settled for the
// provider: never answered, or answered empty or failed and due again.
// $1 provider, $2 lookup keys, $3 movie folders, $4 series folders.
const enrichmentCandidatePredicate = `
	mi.status = 'matched'
	AND (
		(mi.type = 'movie' AND EXISTS (
			SELECT 1 FROM media_item_libraries mil
			WHERE mil.content_id = mi.content_id AND mil.media_folder_id = ANY($3::int[])))
		OR (mi.type = 'series' AND EXISTS (
			SELECT 1 FROM media_item_libraries mil
			WHERE mil.content_id = mi.content_id AND mil.media_folder_id = ANY($4::int[])))
	)
	AND EXISTS (
		SELECT 1 FROM media_item_provider_ids pid
		WHERE pid.content_id = mi.content_id AND pid.provider = ANY($2::text[]))
	AND NOT EXISTS (
		SELECT 1 FROM metadata_enrichment_state es
		WHERE es.content_id = mi.content_id AND es.provider = $1
		  AND (es.next_check_at IS NULL OR es.next_check_at > now()))`

// Candidates returns the next page of items in content ID order, each with
// its durable provider IDs.
func (r *enrichmentStateRepository) Candidates(ctx context.Context, query enrichmentCandidateQuery) ([]enrichmentCandidate, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT mi.content_id, mi.type, COALESCE(mi.default_metadata_language, ''),
		       COALESCE((SELECT jsonb_object_agg(pid.provider, pid.provider_id)
		                 FROM media_item_provider_ids pid
		                 WHERE pid.content_id = mi.content_id), '{}'::jsonb)
		FROM media_items mi
		WHERE `+enrichmentCandidatePredicate+`
		  AND mi.content_id > $5
		ORDER BY mi.content_id
		LIMIT $6`,
		query.Provider, query.LookupKeys, query.MovieFolders, query.SeriesFolders, query.After, query.Limit)
	if err != nil {
		return nil, fmt.Errorf("query enrichment candidates: %w", err)
	}
	defer rows.Close()

	var candidates []enrichmentCandidate
	for rows.Next() {
		var candidate enrichmentCandidate
		if err := rows.Scan(&candidate.ContentID, &candidate.Type, &candidate.Language, &candidate.ProviderIDs); err != nil {
			return nil, fmt.Errorf("scan enrichment candidate: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate enrichment candidates: %w", err)
	}
	return candidates, nil
}

// CountCandidates counts every item Candidates would page through.
func (r *enrichmentStateRepository) CountCandidates(ctx context.Context, query enrichmentCandidateQuery) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `
		SELECT count(*) FROM media_items mi WHERE `+enrichmentCandidatePredicate,
		query.Provider, query.LookupKeys, query.MovieFolders, query.SeriesFolders).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count enrichment candidates: %w", err)
	}
	return count, nil
}

// RecordOutcomes stores what the provider answered for each item. An item
// deleted since it was selected is skipped rather than failing the batch.
func (r *enrichmentStateRepository) RecordOutcomes(ctx context.Context, provider string, outcomes []enrichmentOutcome) error {
	if len(outcomes) == 0 {
		return nil
	}
	contentIDs := make([]string, 0, len(outcomes))
	results := make([]string, 0, len(outcomes))
	nextChecks := make([]*time.Time, 0, len(outcomes))
	for _, outcome := range outcomes {
		contentIDs = append(contentIDs, outcome.ContentID)
		results = append(results, outcome.Outcome)
		nextChecks = append(nextChecks, outcome.NextCheckAt)
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO metadata_enrichment_state (content_id, provider, outcome, checked_at, next_check_at)
		SELECT o.content_id, $1, o.outcome, now(), o.next_check_at
		FROM unnest($2::text[], $3::text[], $4::timestamptz[]) AS o(content_id, outcome, next_check_at)
		WHERE EXISTS (SELECT 1 FROM media_items mi WHERE mi.content_id = o.content_id)
		ON CONFLICT (content_id, provider) DO UPDATE SET
			outcome = EXCLUDED.outcome,
			checked_at = EXCLUDED.checked_at,
			next_check_at = EXCLUDED.next_check_at`,
		provider, contentIDs, results, nextChecks)
	if err != nil {
		return fmt.Errorf("record enrichment outcomes: %w", err)
	}
	return nil
}

// RecordFound marks each provider as having answered for the item.
func (r *enrichmentStateRepository) RecordFound(ctx context.Context, contentID string, providers []string) error {
	if contentID == "" || len(providers) == 0 {
		return nil
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO metadata_enrichment_state (content_id, provider, outcome, checked_at, next_check_at)
		SELECT $1, p.provider, 'found', now(), NULL
		FROM unnest($2::text[]) AS p(provider)
		WHERE EXISTS (SELECT 1 FROM media_items mi WHERE mi.content_id = $1)
		ON CONFLICT (content_id, provider) DO UPDATE SET
			outcome = 'found',
			checked_at = now(),
			next_check_at = NULL`,
		contentID, providers)
	if err != nil {
		return fmt.Errorf("record enrichment found: %w", err)
	}
	return nil
}
