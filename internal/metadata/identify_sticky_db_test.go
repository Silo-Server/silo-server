package metadata

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// Merging an item an admin identified onto its canonical row keeps the
// Identify choice on the survivor (the canonicalization step that merges the
// item row's own columns).
func TestCanonicalizeKeepsIdentityChoicePostgres(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	suffix := uuid.NewString()
	source, canonical := "identity-choice-source-"+suffix, "identity-choice-canonical-"+suffix
	if _, err := tx.Exec(ctx, `INSERT INTO media_items (content_id, type, title, status, identity_chosen_at)
		VALUES ($1, 'movie', 'Source', 'matched', NOW()), ($2, 'movie', 'Canonical', 'matched', NULL)`, source, canonical); err != nil {
		t.Fatalf("seed items: %v", err)
	}
	ran := false
	for _, step := range mediaItemMergeSteps {
		if step.name != "merge legacy provider columns" {
			continue
		}
		if _, err := tx.Exec(ctx, step.sql, mergeStepArgs(step.sql, source, canonical)...); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		ran = true
	}
	if !ran {
		t.Fatal(`merge step "merge legacy provider columns" not found`)
	}
	var chosen bool
	if err := tx.QueryRow(ctx, `SELECT identity_chosen_at IS NOT NULL FROM media_items WHERE content_id = $1`, canonical).Scan(&chosen); err != nil {
		t.Fatalf("read canonical: %v", err)
	}
	if !chosen {
		t.Error("canonical item lost the Identify choice of the item merged onto it")
	}
}
