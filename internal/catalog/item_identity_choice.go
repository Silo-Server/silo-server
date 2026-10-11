package catalog

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// MarkIdentityChosen records that an admin chose the item's identity with
// Identify. A refresh then keeps that identity when a local .nfo names a
// different one. A missing item is not an error: there is nothing to mark.
func (r *ItemRepository) MarkIdentityChosen(ctx context.Context, contentID string) error {
	return MarkIdentityChosenTx(ctx, r.pool, contentID)
}

// MarkIdentityChosenTx is MarkIdentityChosen inside the caller's transaction,
// so the mark commits only with the identity write or move it records.
func MarkIdentityChosenTx(ctx context.Context, execer itemExecer, contentID string) error {
	if _, err := execer.Exec(ctx,
		`UPDATE media_items SET identity_chosen_at = NOW() WHERE content_id = $1`,
		contentID,
	); err != nil {
		return fmt.Errorf("marking identity chosen for %s: %w", contentID, err)
	}
	return nil
}

// IdentityChosen reports whether an admin chose the item's identity with
// Identify. A missing item reports false.
func (r *ItemRepository) IdentityChosen(ctx context.Context, contentID string) (bool, error) {
	var chosen bool
	err := r.pool.QueryRow(ctx,
		`SELECT identity_chosen_at IS NOT NULL FROM media_items WHERE content_id = $1`,
		contentID,
	).Scan(&chosen)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading identity choice for %s: %w", contentID, err)
	}
	return chosen, nil
}
