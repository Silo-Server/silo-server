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
// deleted unless the answer's work_lookup_failed names the key's catalog. An
// answer that holds a work id or names a failed catalog comes from a source
// that looks works up, so it also replaces the claims that have no source
// (rows the migration moved from media_item_provider_ids) under the same rule;
// an answer that says nothing about works leaves those alone. Rows from
// another named source stay. Other item types are left alone.
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
		keep = append(keep, answer.KeptKeys()...)
		sources := []string{source}
		if len(keep) > 0 {
			sources = append(sources, "")
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM media_item_work_ids
			WHERE content_id = $1 AND source = ANY($2::text[]) AND provider <> ALL($3::text[])
		`, contentID, sources, keep); err != nil {
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
// the same transaction. When providerIDs holds no durable edition identifier
// once a book's work keys and the keys Replace never stores (such as
// _filepath) are taken out, the stored provider IDs stay as they are, so an
// answer that holds only a work id cannot clear them.
func (r *ProviderIDRepository) ReplaceByContentIDWithWorkClaims(
	ctx context.Context,
	contentID string,
	providerIDs map[string]string,
	claims []bookid.WorkClaims,
) error {
	return r.inItemLockedTx(ctx, contentID, func(tx pgx.Tx, itemType string) error {
		if entries := durableEditionIDs(itemType, providerIDs); len(entries) > 0 {
			if err := replaceDurableProviderIDsTx(ctx, tx, contentID, itemType, entries); err != nil {
				return err
			}
		}
		return ApplyWorkClaimsTx(ctx, tx, contentID, itemType, claims)
	})
}
