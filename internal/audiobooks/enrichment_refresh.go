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

// adminRefreshLease bounds a single-book claim taken for an admin refresh,
// held from before the book's tag read until its provider step finishes. An
// abandoned one frees the book for the sweep afterwards.
const adminRefreshLease = 30 * time.Minute

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

// RequeueUnmatchedInLibrary is RequeueUnmatched for every audiobook in
// library folderID, in one statement. It returns how many books it made due.
func (e *Enricher) RequeueUnmatchedInLibrary(ctx context.Context, folderID int) (int, error) {
	if e == nil || e.pool == nil || folderID <= 0 {
		return 0, nil
	}
	tag, err := e.pool.Exec(ctx, `
		WITH target AS (
			SELECT mi.content_id
			FROM media_item_libraries mil
			JOIN media_items mi ON mi.content_id = mil.content_id
			WHERE mil.media_folder_id = $1
			  AND mi.type = 'audiobook'
			  AND mi.match_ignored_at IS NULL
			  AND NOT `+catalog.AudiobookProviderMatchedWhere("mi.content_id")+`
			ORDER BY mi.content_id
			FOR UPDATE OF mi
		)
		INSERT INTO audiobook_enrichment_state (content_id, updated_at)
		SELECT content_id, now() FROM target
		ON CONFLICT (content_id) DO UPDATE SET `+requeueStateSet+`
		WHERE audiobook_enrichment_state.lease_until IS NULL
		   OR audiobook_enrichment_state.lease_until <= now()
	`, folderID)
	if err != nil {
		return 0, fmt.Errorf("making library %d audiobooks due for enrichment: %w", folderID, err)
	}
	return int(tag.RowsAffected()), nil
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

// An admin refresh of one audiobook claims the book first (ClaimRefresh), so
// the enrichment sweep leaves it alone from before the refresh's tag read
// until its provider step has finished (Refresh) or the claim is released
// (ReleaseRefresh). A Reset to Provider clears the match inside the scan
// transaction that writes the re-read tags (ResetMatchTx), under the same
// claim.

// ClaimRefresh claims one audiobook for an admin refresh, whatever its retry
// schedule. It returns an empty token for a book that is not an audiobook or
// whose match an admin ignored: such a book gets no provider step. A delegated
// curator's authorizer on ctx must allow every library that holds the book.
// A new state row for a matched book records success, so a claim never makes
// it look untried to the sweep.
func (e *Enricher) ClaimRefresh(ctx context.Context, contentID string) (token string, err error) {
	if e == nil || e.pool == nil || contentID == "" {
		return "", nil
	}
	err = pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		var ignored, matched bool
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
		if err := catalog.GuardMetadataCurationItems(ctx, tx, contentID); err != nil {
			return err
		}
		if ignored {
			return nil
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO audiobook_enrichment_state (content_id, outcome, completed_at, claim_token, lease_until, updated_at)
			VALUES ($1, CASE WHEN $2 THEN 'success' END, CASE WHEN $2 THEN now() END,
			        gen_random_uuid()::text, now() + make_interval(secs => $3::double precision), now())
			ON CONFLICT (content_id) DO UPDATE SET
				claim_token = EXCLUDED.claim_token,
				lease_until = EXCLUDED.lease_until,
				updated_at  = now()
			WHERE audiobook_enrichment_state.lease_until IS NULL
			   OR audiobook_enrichment_state.lease_until <= now()
			RETURNING claim_token
		`, contentID, matched, adminRefreshLease.Seconds()).Scan(&token)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAudiobookEnrichmentBusy
		}
		if err != nil {
			return fmt.Errorf("claiming audiobook for refresh: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// ReleaseRefresh returns a refresh claim that Refresh did not complete. A
// claim an outcome already cleared is not touched.
func (e *Enricher) ReleaseRefresh(ctx context.Context, contentID, token string) {
	if e == nil || token == "" {
		return
	}
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	e.releaseClaim(releaseCtx, enrichmentItemRow{ContentID: contentID, ClaimToken: token})
}

// Refresh is the provider step of an admin refresh under a claim from
// ClaimRefresh, after the book's tags were read again. A matched book fetches
// the records its stored IDs name again, under the matching rules, and keeps
// its match when no provider returns them. An unmatched book is matched now,
// as one sweep item would be. The book's content ID, files, and library
// membership never change.
func (e *Enricher) Refresh(ctx context.Context, contentID, token string) error {
	if e == nil || e.pool == nil || token == "" {
		return nil
	}
	var item enrichmentItemRow
	var matched bool
	err := e.pool.QueryRow(ctx, `
		SELECT mi.content_id, s.claim_token, `+catalog.AudiobookProviderMatchedWhere("mi.content_id")+`, `+enrichmentItemColumns+`
		FROM media_items mi
		JOIN audiobook_enrichment_state s ON s.content_id = mi.content_id
		WHERE mi.content_id = $1 AND s.claim_token = $2 AND s.lease_until > now()
	`, contentID, token).Scan(append([]any{&item.ContentID, &item.ClaimToken, &matched}, item.columnTargets()...)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAudiobookEnrichmentClaimLost
	}
	if err != nil {
		return fmt.Errorf("loading claimed audiobook: %w", err)
	}
	if e.providerIDs != nil {
		rows, err := e.providerIDs.GetByContentID(ctx, contentID)
		if err != nil {
			return fmt.Errorf("loading audiobook provider IDs: %w", err)
		}
		item.ProviderIDs = providerIDMapFromRows(rows)
	}
	if matched {
		return e.refreshMatched(ctx, item)
	}
	return e.enrichItem(ctx, item)
}

// RefreshItem claims one audiobook, runs Refresh, and releases the claim, for
// a caller that has already read the book's tags.
func (e *Enricher) RefreshItem(ctx context.Context, contentID string) error {
	token, err := e.ClaimRefresh(ctx, contentID)
	if err != nil || token == "" {
		return err
	}
	defer e.ReleaseRefresh(ctx, contentID, token)
	return e.Refresh(ctx, contentID, token)
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

// ResetMatchTx clears an audiobook's provider match for Reset to Provider, in
// tx, under the claim token from ClaimRefresh: its provider IDs and work ids, the fields
// and credits a match owns, artwork a provider supplied, and its field locks.
// Tag-owned fields are cleared too, so the tags applied next in the same
// transaction are written as on an unmatched book, and the enrichment state
// returns to untried while the claim stays held. Clearing a cached poster
// queues its revision for collection (media_items_artwork_revision_gc_update).
// The content ID, files, series row, progress, and favorites stay. The
// scanner calls it from the book's write transaction, after locking the item
// (scanner.WithAudiobookTagRereadReset).
func (e *Enricher) ResetMatchTx(ctx context.Context, tx pgx.Tx, contentID, token string) error {
	if e == nil || tx == nil || token == "" {
		return nil
	}
	if err := e.state.AssertClaimTx(ctx, tx, contentID, token); err != nil {
		return err
	}
	if err := catalog.GuardMetadataCurationItems(ctx, tx, contentID); err != nil {
		return err
	}
	// The tags applied next store the tagged ASIN again.
	if _, err := tx.Exec(ctx, `DELETE FROM media_item_provider_ids WHERE content_id = $1`, contentID); err != nil {
		return fmt.Errorf("clearing audiobook provider IDs: %w", err)
	}
	if err := catalog.DeleteWorkClaimsTx(ctx, tx, contentID); err != nil {
		return err
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
		UPDATE audiobook_enrichment_state SET `+requeueStateSet+`
		WHERE content_id = $1 AND claim_token = $2
	`, contentID, token); err != nil {
		return fmt.Errorf("resetting audiobook enrichment state: %w", err)
	}
	return nil
}
