package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// An admin's Refresh Metadata reaches an audiobook's provider match only
// through the functions in this file, never through the general metadata
// service, whose matching and item rebinding know none of the audiobook rules
// (docs/architecture/audiobook-metadata.md).

// ErrAudiobookEnrichmentBusy reports that the enrichment sweep holds the
// book's claim, so an admin refresh cannot match it at the same time.
var ErrAudiobookEnrichmentBusy = errors.New("the audiobook enrichment sweep is matching this book; try again when it finishes")

// errMatchedRecordUnavailable is a matched book whose providers returned no
// usable record for its stored IDs. The match and its fields are left as they
// were.
var errMatchedRecordUnavailable = errors.New("no provider returned the matched record")

// adminRefreshLease bounds a single-book claim taken for an admin refresh.
// The refresh asks one book's providers, so a short lease suffices, and an
// abandoned one frees the book for the sweep soon.
const adminRefreshLease = 10 * time.Minute

// RequeueUnmatched makes an unmatched audiobook due for the next enrichment
// sweep: its earlier no_match, failed, or skipped outcome and retry counts are
// cleared, so the sweep tries it again under the audiobook rules. A matched
// book, a book whose match an admin ignored, and a book the sweep holds now
// are left alone. It reports whether the book was made due.
func (e *Enricher) RequeueUnmatched(ctx context.Context, contentID string) (bool, error) {
	if e == nil || e.pool == nil || contentID == "" {
		return false, nil
	}
	var requeued bool
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		eligible, err := lockUnmatchedForRefresh(ctx, tx, contentID)
		if err != nil || !eligible {
			return err
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO audiobook_enrichment_state (content_id, updated_at)
			VALUES ($1, now())
			ON CONFLICT (content_id) DO UPDATE SET `+requeueStateSet+`
			WHERE audiobook_enrichment_state.lease_until IS NULL
			   OR audiobook_enrichment_state.lease_until <= now()
		`, contentID)
		if err != nil {
			return fmt.Errorf("making audiobook due for enrichment: %w", err)
		}
		requeued = tag.RowsAffected() > 0
		return nil
	})
	return requeued, err
}

// requeueStateSet returns a state row to "never tried": the sweep's pending
// branch (outcome IS NULL) claims it on its next batch.
const requeueStateSet = `
	outcome          = NULL,
	no_matches       = 0,
	failures         = 0,
	last_error_class = NULL,
	last_error       = NULL,
	next_attempt_at  = NULL,
	completed_at     = NULL,
	updated_at       = now()`

// lockUnmatchedForRefresh locks the book's media_items row, in the order
// claimBatch takes it, and reports whether the book is an audiobook with no
// provider match that an admin has not marked as ignored.
func lockUnmatchedForRefresh(ctx context.Context, tx pgx.Tx, contentID string) (bool, error) {
	var eligible bool
	err := tx.QueryRow(ctx, `
		SELECT mi.match_ignored_at IS NULL AND NOT `+catalog.AudiobookProviderMatchedWhere("mi.content_id")+`
		FROM media_items mi
		WHERE mi.content_id = $1 AND mi.type = 'audiobook'
		FOR UPDATE OF mi
	`, contentID).Scan(&eligible)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading audiobook match state: %w", err)
	}
	return eligible, nil
}

// RefreshItem is an admin's provider refresh of one audiobook. A matched book
// fetches the records its stored IDs name again, under the matching rules,
// and keeps its match when no provider returns them. An unmatched book is
// matched now, as one sweep item would be. A book whose match an admin
// ignored is left alone. The book's content ID, files, and library membership
// never change.
func (e *Enricher) RefreshItem(ctx context.Context, contentID string) error {
	if e == nil || e.pool == nil || contentID == "" {
		return nil
	}
	item, matched, ok, err := e.claimForRefresh(ctx, contentID)
	if err != nil || !ok {
		return err
	}
	defer func() {
		// Every outcome clears the claim; this returns it after a cancel or an
		// error that recorded nothing. A cleared claim is not touched.
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		e.releaseClaim(releaseCtx, item)
	}()
	if matched {
		return e.refreshMatched(ctx, item)
	}
	return e.enrichItem(ctx, item)
}

// claimForRefresh claims one audiobook for an admin refresh, whatever its
// retry schedule, and loads it as the sweep would. ok is false for a book that
// is not an audiobook or whose match an admin ignored. A new state row for a
// matched book records success, so a claim never makes it look untried to the
// sweep.
func (e *Enricher) claimForRefresh(ctx context.Context, contentID string) (item enrichmentItemRow, matched, ok bool, err error) {
	err = pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		var ignored bool
		err := tx.QueryRow(ctx, `
			SELECT mi.match_ignored_at IS NOT NULL, `+catalog.AudiobookProviderMatchedWhere("mi.content_id")+`
			FROM media_items mi
			WHERE mi.content_id = $1 AND mi.type = 'audiobook'
			FOR UPDATE OF mi
		`, contentID).Scan(&ignored, &matched)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading audiobook match state: %w", err)
		}
		if ignored {
			return nil
		}
		err = tx.QueryRow(ctx, `
			WITH claimed AS (
				INSERT INTO audiobook_enrichment_state (content_id, outcome, completed_at, claim_token, lease_until, updated_at)
				VALUES ($1, CASE WHEN $2 THEN 'success' END, CASE WHEN $2 THEN now() END,
				        gen_random_uuid()::text, now() + make_interval(secs => $3::double precision), now())
				ON CONFLICT (content_id) DO UPDATE SET
					claim_token = EXCLUDED.claim_token,
					lease_until = EXCLUDED.lease_until,
					updated_at  = now()
				WHERE audiobook_enrichment_state.lease_until IS NULL
				   OR audiobook_enrichment_state.lease_until <= now()
				RETURNING content_id, claim_token
			)
			SELECT mi.content_id, c.claim_token, `+enrichmentItemColumns+`
			FROM claimed c
			JOIN media_items mi ON mi.content_id = c.content_id
		`, contentID, matched, adminRefreshLease.Seconds()).Scan(
			append([]any{&item.ContentID, &item.ClaimToken}, item.columnTargets()...)...)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAudiobookEnrichmentBusy
		}
		if err != nil {
			return fmt.Errorf("claiming audiobook for refresh: %w", err)
		}
		ok = true
		return nil
	})
	if err != nil || !ok {
		return enrichmentItemRow{}, false, false, err
	}
	if e.providerIDs != nil {
		rows, err := e.providerIDs.GetByContentID(ctx, contentID)
		if err != nil {
			return item, matched, true, fmt.Errorf("loading audiobook provider IDs: %w", err)
		}
		item.ProviderIDs = providerIDMapFromRows(rows)
	}
	return item, matched, true, nil
}

// refreshMatched fetches a matched book's record again and writes it under
// the field ownership rules. When no provider returns it, or one fails, the
// book keeps its match and fields and its state is not changed.
func (e *Enricher) refreshMatched(ctx context.Context, item enrichmentItemRow) error {
	if item.FolderID == 0 {
		return fmt.Errorf("audiobook %s is in no library", item.ContentID)
	}
	providers, err := e.resolveChain(ctx, item.FolderID)
	if err != nil {
		return fmt.Errorf("resolving audiobook chain for folder %d: %w", item.FolderID, err)
	}
	if len(providers) == 0 {
		return fmt.Errorf("library %d has no audiobook metadata provider", item.FolderID)
	}
	providers = e.gate.wrap(providers)
	for _, p := range providers {
		item.Chain = append(item.Chain, p.Slug())
	}
	if until, paused := e.gate.allPaused(providers); paused {
		return fmt.Errorf("audiobook metadata providers are paused until %s after rate limiting", until.UTC().Format(time.RFC3339))
	}
	match := e.refetchMatch(ctx, item, providers)
	if !match.metadata.HasMetadata {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(match.providerErrs) > 0 {
			return fmt.Errorf("%w: %w", errMatchedRecordUnavailable, errors.Join(match.providerErrs...))
		}
		return errMatchedRecordUnavailable
	}
	if err := e.persist(ctx, item, match.ids, match.metadata); err != nil {
		return fmt.Errorf("persisting refreshed audiobook %s: %w", item.ContentID, err)
	}
	e.enqueueRemoteArtwork(ctx, item.ContentID, match.metadata)
	slog.InfoContext(ctx, "audiobook enrichment: refreshed matched record", "component", "audiobooks",
		"content_id", item.ContentID)
	return nil
}

// ResetMatch clears an audiobook's provider match for Reset to Provider: its
// provider IDs, the fields and credits a match owns, artwork a provider
// supplied, and its field locks. Tag-owned fields are cleared too, so the tag
// read that follows writes them as on an unmatched book, and the enrichment
// state returns to untried. A book whose match an admin ignored is left
// alone; ignored reports it. The content ID, files, series row, progress, and
// favorites stay.
func (e *Enricher) ResetMatch(ctx context.Context, contentID string) (ignored bool, err error) {
	if e == nil || e.pool == nil || contentID == "" {
		return false, nil
	}
	err = pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT match_ignored_at IS NOT NULL FROM media_items
			WHERE content_id = $1 AND type = 'audiobook'
			FOR UPDATE
		`, contentID).Scan(&ignored)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("audiobook %s not found", contentID)
		}
		if err != nil || ignored {
			return err
		}
		var leased bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM audiobook_enrichment_state
				WHERE content_id = $1 AND lease_until > now()
				FOR UPDATE
			)
		`, contentID).Scan(&leased); err != nil {
			return fmt.Errorf("reading audiobook enrichment claim: %w", err)
		}
		if leased {
			return ErrAudiobookEnrichmentBusy
		}
		// The tag read that follows stores the tagged ASIN again.
		if _, err := tx.Exec(ctx, `DELETE FROM media_item_provider_ids WHERE content_id = $1`, contentID); err != nil {
			return fmt.Errorf("clearing audiobook provider IDs: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM item_people WHERE content_id = $1 AND kind IN (7, 8)
		`, contentID); err != nil {
			return fmt.Errorf("clearing audiobook credits: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE media_items SET
				sort_title = '', original_title = '',
				year = 0, overview = '', tagline = '', release_date = NULL,
				genres = '{}', studios = '{}', content_rating = '',
				tmdb_id = '', tvdb_id = '', imdb_id = '',
				poster_path          = CASE WHEN poster_source_path LIKE 'http%' THEN '' ELSE poster_path END,
				poster_thumbhash     = CASE WHEN poster_source_path LIKE 'http%' THEN '' ELSE poster_thumbhash END,
				poster_source_path   = CASE WHEN poster_source_path LIKE 'http%' THEN '' ELSE poster_source_path END,
				backdrop_path = '', backdrop_source_path = '', backdrop_thumbhash = '',
				logo_path = '', logo_source_path = '',
				locked_fields = '{}',
				status = '', matched_at = NULL,
				updated_at = now()
			WHERE content_id = $1
		`, contentID); err != nil {
			return fmt.Errorf("clearing audiobook match fields: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO audiobook_enrichment_state (content_id, updated_at)
			VALUES ($1, now())
			ON CONFLICT (content_id) DO UPDATE SET `+requeueStateSet,
			contentID); err != nil {
			return fmt.Errorf("resetting audiobook enrichment state: %w", err)
		}
		return nil
	})
	return ignored, err
}
