package metadata

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

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
// promotion (canonicalizeLocalContentID) and the corrected-identity re-anchor
// (an Identify that replaces a wrong match, or a manual refresh after a fixed
// <uniqueid> in an NFO, whose item was anchored to the wrong provider id). Both
// must hold the provider-dedup lock so the target id cannot be claimed
// underneath us. When ids do not derive a provider-anchored id, or it equals
// `from`, this is a no-op.
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

	// Target free: pure value-move. A provider-anchored series takes the
	// season and episode ids composed from its old anchor along with it, so
	// the show that anchor names can claim them when it is scanned in.
	if err := s.renameContentID(ctx, from, target, normalizeItemTypeForContentID(itemType) == matchContentTypeSeries); err != nil {
		return "", err
	}
	return target, nil
}

// renameContentID moves a content_id value (a media item, season or episode)
// to a new, currently-free id. With withSeriesChildren it also moves the
// series' season and episode ids that were composed from `from` to the same
// composition under `to` (see seriesChildRenames). FK children follow via ON
// UPDATE CASCADE; silo_rename_content_ids also sweeps the unconstrained soft
// references. Everything moves in one transaction.
func (s *MetadataService) renameContentID(ctx context.Context, from, to string, withSeriesChildren bool) error {
	if s.dbPool == nil {
		return fmt.Errorf("rename content id requires database pool")
	}
	tx, err := s.dbPool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin content_id rename transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	sources, targets := []string{from}, []string{to}
	if withSeriesChildren {
		// Lock the series row before listing its children. A scan inserting a
		// season or episode under it holds a key-share lock on this row, so an
		// insert already in flight commits before the listing, and a later one
		// waits and then fails its FK check against the moved id instead of
		// landing under the new series with an old-anchor id.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM media_items WHERE content_id = $1 FOR UPDATE`, from); err != nil {
			return fmt.Errorf("lock series %s for rename: %w", from, err)
		}
		childSources, childTargets, err := seriesChildRenames(ctx, tx, from, to)
		if err != nil {
			return err
		}
		sources = append(sources, childSources...)
		targets = append(targets, childTargets...)
	}
	if _, err := tx.Exec(ctx, `SELECT silo_rename_content_ids($1, $2)`, sources, targets); err != nil {
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

// seriesChildRenames lists the season and episode ids of series `from` that
// were composed from its anchor, paired with the same composition under `to`.
// Children on any other id (Sonyflake ids from before the series matched) keep
// it. A child whose new id is already taken, which only earlier history can
// cause, moves to a fresh Sonyflake id instead and is logged: it must leave the
// old anchor's namespace either way, or the show that anchor names could not
// create that episode when it is scanned in.
func seriesChildRenames(ctx context.Context, tx pgx.Tx, from, to string) ([]string, []string, error) {
	rows, err := tx.Query(ctx, `
		SELECT content_id, season_number, NULL::int FROM seasons WHERE series_id = $1
		UNION ALL
		SELECT content_id, season_number, episode_number FROM episodes WHERE series_id = $1
	`, from)
	if err != nil {
		return nil, nil, fmt.Errorf("list children of series %s: %w", from, err)
	}
	var sources, targets []string
	for rows.Next() {
		var childID string
		var seasonNumber int
		var episodeNumber *int
		if err := rows.Scan(&childID, &seasonNumber, &episodeNumber); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("scan child of series %s: %w", from, err)
		}
		var oldID, newID string
		var okOld, okNew bool
		if episodeNumber == nil {
			oldID, okOld = contentid.ForSeason(from, seasonNumber)
			newID, okNew = contentid.ForSeason(to, seasonNumber)
		} else {
			oldID, okOld = contentid.ForEpisode(from, seasonNumber, *episodeNumber)
			newID, okNew = contentid.ForEpisode(to, seasonNumber, *episodeNumber)
		}
		if okOld && okNew && childID == oldID {
			sources = append(sources, childID)
			targets = append(targets, newID)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("list children of series %s: %w", from, err)
	}
	if len(targets) == 0 {
		return nil, nil, nil
	}

	taken := make(map[string]struct{})
	takenRows, err := tx.Query(ctx, `
		SELECT content_id FROM seasons WHERE content_id = ANY($1)
		UNION ALL
		SELECT content_id FROM episodes WHERE content_id = ANY($1)
	`, targets)
	if err != nil {
		return nil, nil, fmt.Errorf("check child targets of series %s: %w", to, err)
	}
	for takenRows.Next() {
		var id string
		if err := takenRows.Scan(&id); err != nil {
			takenRows.Close()
			return nil, nil, fmt.Errorf("scan child target of series %s: %w", to, err)
		}
		taken[id] = struct{}{}
	}
	takenRows.Close()
	if err := takenRows.Err(); err != nil {
		return nil, nil, fmt.Errorf("check child targets of series %s: %w", to, err)
	}
	for i, target := range targets {
		if _, ok := taken[target]; !ok {
			continue
		}
		fresh, err := generateContentID()
		if err != nil {
			return nil, nil, fmt.Errorf("mint id for child %s of series %s: %w", sources[i], from, err)
		}
		slog.WarnContext(ctx, "metadata: series child takes a fresh id; the re-anchored id is taken",
			"component", "metadata", "series_from", from, "series_to", to,
			"child_id", sources[i], "target_id", target, "fresh_id", fresh)
		targets[i] = fresh
	}
	return sources, targets, nil
}
