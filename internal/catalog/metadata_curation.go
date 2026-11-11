package catalog

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/contentid"
)

// MetadataCurationTxOptions pins the fresh-snapshot isolation required by the
// curation guard, without changing automatic processing's transaction defaults.
func MetadataCurationTxOptions(ctx context.Context) pgx.TxOptions {
	if access.HasMetadataCurationAuthorizer(ctx) {
		return pgx.TxOptions{IsoLevel: pgx.ReadCommitted}
	}
	return pgx.TxOptions{}
}

// LockMetadataCurationItems locks existing parents in content-id order, then
// authorizes each item's full library set in a separate READ COMMITTED statement.
// FOR UPDATE fences FK membership inserts/rebinds while a fresh statement sees
// memberships committed by a writer we waited for. Repository membership writers
// do not update media_folder_id in place. Missing parents are absent from the
// result; callers must distinguish an allowed creation from a missing source.
func LockMetadataCurationItems(ctx context.Context, tx pgx.Tx, ids ...string) (map[string]string, error) {
	rows, err := tx.Query(ctx, `SELECT content_id, type FROM media_items
		WHERE content_id = ANY($1::text[]) ORDER BY content_id FOR UPDATE`, ids)
	if err != nil {
		return nil, fmt.Errorf("lock curation items: %w", err)
	}
	types := make(map[string]string, len(ids))
	for rows.Next() {
		var id, itemType string
		if err := rows.Scan(&id, &itemType); err != nil {
			rows.Close()
			return nil, err
		}
		types[id] = itemType
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Keep a stable authorization order too, including empty sets.
	rows, err = tx.Query(ctx, `SELECT mi.content_id, mil.media_folder_id
		FROM media_items mi LEFT JOIN media_item_libraries mil USING (content_id)
		WHERE mi.content_id = ANY($1::text[])
		ORDER BY mi.content_id, mil.media_folder_id`, ids)
	if err != nil {
		return nil, fmt.Errorf("read curation libraries: %w", err)
	}
	libraries := make(map[string][]int, len(types))
	var ordered []string
	for rows.Next() {
		var id string
		var library *int
		if err := rows.Scan(&id, &library); err != nil {
			rows.Close()
			return nil, err
		}
		if _, ok := libraries[id]; !ok {
			ordered = append(ordered, id)
			libraries[id] = nil
		}
		if library != nil {
			libraries[id] = append(libraries[id], *library)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ordered {
		if err := access.AuthorizeMetadataCurationLibraries(ctx, libraries[id]); err != nil {
			return nil, err
		}
	}
	return types, nil
}

// LockMetadataCurationFiles locks the files an explicit curation is about to
// move or rename, before GuardMetadataCurationItems locks their parents. Split
// and scanner writers lock files before parent items; taking the parents first
// deadlocks with them. Automatic processing is unchanged.
func LockMetadataCurationFiles(ctx context.Context, tx pgx.Tx, ids ...string) error {
	if !access.HasMetadataCurationAuthorizer(ctx) {
		return nil
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM media_files
		WHERE content_id = ANY($1::text[]) ORDER BY id FOR UPDATE`, ids); err != nil {
		return fmt.Errorf("lock curation files: %w", err)
	}
	return nil
}

// GuardMetadataCurationItems leaves automatic metadata processing unchanged.
// Explicit curation must find every expected existing item under these locks.
func GuardMetadataCurationItems(ctx context.Context, tx pgx.Tx, ids ...string) error {
	if !access.HasMetadataCurationAuthorizer(ctx) {
		return nil
	}
	types, err := LockMetadataCurationItems(ctx, tx, ids...)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, ok := types[id]; !ok {
			return ErrItemNotFound
		}
		if types[id] != types[ids[0]] {
			return errors.New("curation item types differ")
		}
	}
	return nil
}

// LockMetadataCurationIdentity holds provider IDs that explicit curation is
// about to store to the curator's scope. Stored IDs outlive the request: a
// later refresh, refresh-debt retry or scan acts on them with no curator
// present and merges the item onto the item already at the anchor they derive
// or already holding one of them. So every such item (other than holders) must
// be one the curator may curate, as match/apply requires of its destination.
//
// anchor is the content id the stored identity derives ("" for none) and ids
// the provider IDs being chosen. It locks holders and those items together in
// content-id order and authorizes each one's libraries. Automatic processing,
// which installs no authorizer, is unchanged.
func LockMetadataCurationIdentity(ctx context.Context, tx pgx.Tx, itemType, anchor string, ids contentid.ProviderIDs, holders ...string) error {
	if !access.HasMetadataCurationAuthorizer(ctx) {
		return nil
	}
	owners, err := metadataCurationIdentityOwners(ctx, tx, itemType, anchor, ids, holders)
	if err != nil {
		return err
	}
	if len(owners) == 0 {
		return nil
	}
	if _, err := LockMetadataCurationItems(ctx, tx, append(slices.Clone(holders), owners...)...); err != nil {
		return err
	}
	// An owner committed between the read and the locks is held to scope too.
	late, err := metadataCurationIdentityOwners(ctx, tx, itemType, anchor, ids, append(slices.Clone(holders), owners...))
	if err != nil || len(late) == 0 {
		return err
	}
	_, err = LockMetadataCurationItems(ctx, tx, late...)
	return err
}

func metadataCurationIdentityOwners(ctx context.Context, tx pgx.Tx, itemType, anchor string, ids contentid.ProviderIDs, exclude []string) ([]string, error) {
	tmdb := strings.TrimSpace(ids.Tmdb)
	imdb := strings.ToLower(strings.TrimSpace(ids.Imdb))
	tvdb := strings.TrimSpace(ids.Tvdb)
	if anchor == "" && tmdb == "" && imdb == "" && tvdb == "" {
		return nil, nil
	}
	if exclude == nil {
		exclude = []string{}
	}
	// One branch per lookup so each uses its index: the primary key, the
	// partial provider-column indexes (WHERE <column> <> ''), and the
	// media_item_provider_ids (provider, provider_id, item_type) key. Exact
	// matching finds every owner a merge can: the merge lookups
	// (GetByExternalID, FindContentIDByProviderIDs) compare stored IDs exactly
	// too, against IDs the refresh has sanitized.
	rows, err := tx.Query(ctx, `
		SELECT content_id FROM (
			SELECT content_id, type FROM media_items WHERE $2 <> '' AND content_id = $2
			UNION SELECT content_id, type FROM media_items WHERE tmdb_id <> '' AND tmdb_id = $3
			UNION SELECT content_id, type FROM media_items WHERE imdb_id <> '' AND imdb_id = $4
			UNION SELECT content_id, type FROM media_items WHERE tvdb_id <> '' AND tvdb_id = $5
			UNION SELECT content_id, item_type FROM media_item_provider_ids
				WHERE item_type = $1 AND provider_id <> '' AND (provider, provider_id) IN (('tmdb', $3), ('imdb', $4), ('tvdb', $5))
		) owner(content_id, type)
		WHERE type = $1 AND NOT (content_id = ANY($6::text[]))
		ORDER BY content_id`, itemType, anchor, tmdb, imdb, tvdb, exclude)
	if err != nil {
		return nil, fmt.Errorf("find curation identity owners: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// The media_items provider ID columns a metadata edit can set or clear.
const (
	tmdbIDColumn = "tmdb_id"
	imdbIDColumn = "imdb_id"
	tvdbIDColumn = "tvdb_id"
)

// lockCuratedProviderIDs applies LockMetadataCurationIdentity to an edit that
// changes a media item's provider IDs: the anchor the item's resulting
// identity derives, and any item already holding a newly chosen ID.
func lockCuratedProviderIDs(ctx context.Context, tx pgx.Tx, contentID string, upd *MetadataUpdate) error {
	if upd == nil || !access.HasMetadataCurationAuthorizer(ctx) {
		return nil
	}
	edits := map[string]*string{tmdbIDColumn: upd.TmdbID, imdbIDColumn: upd.ImdbID, tvdbIDColumn: upd.TvdbID}
	empty := ""
	for _, field := range upd.Clear {
		if _, ok := edits[field]; ok {
			edits[field] = &empty
		}
	}
	if edits[tmdbIDColumn] == nil && edits[imdbIDColumn] == nil && edits[tvdbIDColumn] == nil {
		return nil
	}
	var itemType string
	var stored contentid.ProviderIDs
	err := tx.QueryRow(ctx, `SELECT type, COALESCE(tmdb_id, ''), COALESCE(imdb_id, ''), COALESCE(tvdb_id, '')
		FROM media_items WHERE content_id = $1`, contentID).Scan(&itemType, &stored.Tmdb, &stored.Imdb, &stored.Tvdb)
	if errors.Is(err, pgx.ErrNoRows) {
		// Not a media item; seasons and episodes don't anchor merges.
		return nil
	}
	if err != nil {
		return fmt.Errorf("read curated item identity: %w", err)
	}
	resulting, chosen := stored, contentid.ProviderIDs{}
	changed := false
	for field, dst := range map[string][2]*string{
		tmdbIDColumn: {&resulting.Tmdb, &chosen.Tmdb},
		imdbIDColumn: {&resulting.Imdb, &chosen.Imdb},
		tvdbIDColumn: {&resulting.Tvdb, &chosen.Tvdb},
	} {
		edit := edits[field]
		if edit == nil {
			continue
		}
		value := strings.TrimSpace(*edit)
		// An edit form resends unchanged IDs; only a changed one is a choice.
		if strings.EqualFold(value, strings.TrimSpace(*dst[0])) {
			continue
		}
		changed = true
		*dst[0], *dst[1] = value, value
	}
	if !changed {
		return nil
	}
	var anchor string
	switch strings.ToLower(itemType) {
	case AdminCatalogTypeMovie:
		anchor, _ = contentid.ForMovie(resulting)
	case AdminCatalogTypeSeries:
		anchor, _ = contentid.ForSeries(resulting)
	}
	return LockMetadataCurationIdentity(ctx, tx, itemType, anchor, chosen, contentID)
}
