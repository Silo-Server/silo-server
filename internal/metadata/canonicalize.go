package metadata

import (
	"context"
	"errors"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/contentid"
)

// providerIDsStruct adapts the denormalized provider-id map carried on a
// MetadataResult to the contentid.ProviderIDs shape used for id derivation.
func providerIDsStruct(m map[string]string) contentid.ProviderIDs {
	return contentid.ProviderIDs{
		Tmdb: m["tmdb"],
		Imdb: m["imdb"],
		Tvdb: m["tvdb"],
	}
}

// canonicalizeLocalContentID promotes a local skeleton to its deterministic,
// provider-anchored content_id once a confirmed match has supplied provider IDs
// (the untagged-then-matched re-ID). It returns the canonical id, which equals
// from when there is nothing to do.
//
// Tagged content and every refresh hit only the IsLocal guard, so the common
// path is free. When there is work to do, the promotion is one of:
//
//   - target already taken: the two are the same logical item, so merge this
//     skeleton onto the existing row using the shared rebind machinery; or
//   - target free: rename in place. FK children move via ON UPDATE CASCADE (see
//     migration 20260614120000_content_id_online_reid), so for a fresh skeleton
//     this is a handful of rows, not the full-table remap the bulk migration does.
//
// It must run with the provider-dedup lock held (mergeAndPersist holds it), so a
// concurrent match of the same title cannot claim the target underneath us. The
// rename is self-healing: if it loses a rare race with skeleton creation and the
// unique constraint fires, the match returns an error, retries, and takes the
// merge branch on the next pass.
//
// Note: a series that already had season/episode rows before it matched keeps
// those children on their Sonyflake ids (ForSeason/ForEpisode need a series
// anchor). At first match the children usually do not exist yet, so this is
// rare; re-deriving them is a deferred follow-up (recomposeSeriesChildIDs).
func (s *MetadataService) canonicalizeLocalContentID(
	ctx context.Context,
	from string,
	ids contentid.ProviderIDs,
	itemType string,
) (string, error) {
	if s == nil || !contentid.IsLocal(from) {
		return from, nil
	}
	return s.reanchorContentID(ctx, from, ids, itemType)
}

// reanchorContentID moves `from` to the provider-anchored content_id derived
// from `ids` when the two differ, merging onto an existing target or renaming a
// free one. It is the shared core behind two callers: the local-skeleton
// promotion (canonicalizeLocalContentID) and the manual-refresh corrected-
// identity re-anchor (a fixed <uniqueid> in an NFO whose item was already
// anchored to the wrong provider id). Both must hold the provider-dedup lock so
// the target id cannot be claimed underneath us. When ids do not derive a
// provider-anchored id, or it equals `from`, this is a no-op.
func (s *MetadataService) reanchorContentID(
	ctx context.Context,
	from string,
	ids contentid.ProviderIDs,
	itemType string,
) (string, error) {
	if s == nil {
		return from, nil
	}

	// Derive with no path fallback: we only want a provider-anchored id here, not
	// another local value.
	target, err := deriveLogicalContentID(itemType, ids, "")
	if err != nil {
		return "", err
	}
	if !contentid.IsProviderAnchored(target) || target == from {
		return from, nil
	}

	// Look up the target, distinguishing "free" (not-found) from a transient
	// failure. Treating a real error as "target free" would fall through to the
	// rename path and surface as a misleading unique-constraint conflict instead
	// of a retryable error.
	existing, err := s.itemRepo.GetByID(ctx, target)
	switch {
	case err == nil && existing != nil:
		// A row already at the target id is the same logical item; merge onto it.
		// allowMatchedSource: this also runs when refreshing an already-matched
		// local item, so the source row may be 'matched'; we still consolidate it
		// onto the canonical target rather than orphaning a duplicate. Safe under
		// the provider-dedup lock the caller holds.
		if err := s.rebindItemToExistingItem(ctx, from, target, true); err != nil {
			return "", fmt.Errorf("merging local item %s into %s: %w", from, target, err)
		}
		return target, nil
	case err != nil && !errors.Is(err, catalog.ErrItemNotFound):
		return "", fmt.Errorf("looking up canonical target %s: %w", target, err)
	}

	// Target free: pure value-move.
	if err := s.renameContentID(ctx, from, target); err != nil {
		return "", err
	}
	return target, nil
}

// renameContentID moves a single content_id value (a media item, season or
// episode) to a new, currently-free id. FK children follow via ON UPDATE
// CASCADE; the silo_rename_content_id function also sweeps the unconstrained
// soft references. The function body runs as one statement, so the move is
// atomic.
func (s *MetadataService) renameContentID(ctx context.Context, from, to string) error {
	if s.dbPool == nil {
		return fmt.Errorf("rename content id requires database pool")
	}
	tx, err := s.dbPool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin content_id rename transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT silo_rename_content_id($1, $2)`, from, to); err != nil {
		return fmt.Errorf("rename content_id %s -> %s: %w", from, to, err)
	}
	if err := catalog.EnqueueSearchIndexRename(ctx, tx, from, to); err != nil {
		return fmt.Errorf("enqueue catalog search rename %s -> %s: %w", from, to, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit content_id rename transaction: %w", err)
	}
	return nil
}
