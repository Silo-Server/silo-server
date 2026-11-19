package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/bookid"
)

// ApplyWorkClaimsTx writes the work ids metadata answers claimed for an
// audiobook or ebook to media_item_work_ids, in the caller's transaction, which
// must hold the item's row lock. Each answer replaces its source's claims: the
// keys it holds are upserted, and the item's other rows from that source are
// deleted unless the answer's work_lookup_failed names the key's catalog.
// Rows from another source stay. Other item types are left alone.
func ApplyWorkClaimsTx(ctx context.Context, tx pgx.Tx, contentID, itemType string, claims []bookid.WorkClaims) error {
	if !bookid.HasWorkIDs(itemType) || len(claims) == 0 {
		return nil
	}
	if strings.TrimSpace(contentID) == "" {
		return errors.New("content_id is required")
	}
	for _, answer := range claims {
		source := strings.TrimSpace(answer.Source)
		if source == "" {
			continue
		}
		keep := make([]string, 0, len(answer.Claims)+len(answer.LookupFailed))
		for _, claim := range answer.Claims {
			key, isWork := bookid.WorkKey(claim.Provider)
			value := strings.TrimSpace(claim.ProviderID)
			if !isWork || value == "" {
				continue
			}
			keep = append(keep, key)
			if _, err := tx.Exec(ctx, `
				INSERT INTO media_item_work_ids (content_id, provider, provider_id, evidence, source, created_at, updated_at)
				VALUES ($1, $2, $3, $4, $5, NOW(), NOW())
				ON CONFLICT (content_id, provider) DO UPDATE
				SET provider_id = EXCLUDED.provider_id,
				    evidence = EXCLUDED.evidence,
				    source = EXCLUDED.source,
				    updated_at = NOW()
				WHERE (media_item_work_ids.provider_id, media_item_work_ids.evidence, media_item_work_ids.source)
				      IS DISTINCT FROM (EXCLUDED.provider_id, EXCLUDED.evidence, EXCLUDED.source)
			`, contentID, key, value, bookid.NormalizeEvidence(claim.Evidence), source); err != nil {
				return fmt.Errorf("storing work id %s for %s: %w", key, contentID, err)
			}
		}
		for _, key := range bookid.WorkKeys() {
			if answer.Keeps(key) {
				keep = append(keep, key)
			}
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM media_item_work_ids
			WHERE content_id = $1 AND source = $2 AND provider <> ALL($3::text[])
		`, contentID, source, keep); err != nil {
			return fmt.Errorf("clearing work ids %s no longer claims for %s: %w", source, contentID, err)
		}
	}
	return nil
}

// DeleteWorkClaimsTx removes every work id claim of an item, for a reset of
// its match.
func DeleteWorkClaimsTx(ctx context.Context, tx pgx.Tx, contentID string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM media_item_work_ids WHERE content_id = $1`, contentID); err != nil {
		return fmt.Errorf("clearing work ids for %s: %w", contentID, err)
	}
	return nil
}

// ReplaceByContentIDWithWorkClaims replaces the item's durable provider IDs, as
// ReplaceByContentID does, and applies its work claims (ApplyWorkClaimsTx) in
// the same transaction. An empty providerIDs leaves the stored provider IDs as
// they are, so an answer that holds only a work id cannot clear them.
func (r *ProviderIDRepository) ReplaceByContentIDWithWorkClaims(
	ctx context.Context,
	contentID string,
	providerIDs map[string]string,
	claims []bookid.WorkClaims,
) error {
	if strings.TrimSpace(contentID) == "" {
		return errors.New("content_id is required")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin provider id replace transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op once committed

	// Lock the item before its rows, as ReplaceByContentID does.
	var itemType string
	if err := tx.QueryRow(ctx, `SELECT type FROM media_items WHERE content_id = $1 FOR NO KEY UPDATE`, contentID).Scan(&itemType); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("loading item type for %s: content not found", contentID)
		}
		return fmt.Errorf("loading item type for %s: %w", contentID, err)
	}
	if len(providerIDs) > 0 {
		if err := r.ReplaceByContentIDTx(ctx, tx, contentID, itemType, providerIDs); err != nil {
			return err
		}
	}
	if err := ApplyWorkClaimsTx(ctx, tx, contentID, itemType, claims); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit provider id replace transaction: %w", err)
	}
	return nil
}
