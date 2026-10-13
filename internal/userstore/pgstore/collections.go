package pgstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/collectionutil"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

const collectionSelectColumns = `id, profile_id, creator_profile_id, name, description, collection_type, is_shared,
	query_definition, sort_config, source_url, source_config, sync_schedule, next_sync_at,
	last_sync_at, last_sync_status, last_sync_message, COALESCE(display_query_definition::text, '') AS display_query_definition, item_count, include_in_server_collections,
	poster_url, poster_thumbhash, sort_order, group_id, created_at, updated_at`

func scanCollection(scanner interface{ Scan(dest ...any) error }) (*userstore.Collection, error) {
	var c userstore.Collection
	var (
		createdAt, updatedAt   time.Time
		nextSyncAt, lastSyncAt *time.Time
		syncSchedule           *string
	)
	err := scanner.Scan(
		&c.ID, &c.ProfileID, &c.CreatorProfileID, &c.Name, &c.Description, &c.CollectionType, &c.IsShared,
		&c.QueryDefinition, &c.SortConfig, &c.SourceURL, &c.SourceConfig, &syncSchedule, &nextSyncAt,
		&lastSyncAt, &c.LastSyncStatus, &c.LastSyncMessage, &c.DisplayQueryDefinition, &c.ItemCount, &c.IncludeInServerCollections,
		&c.PosterURL, &c.PosterThumbhash, &c.SortOrder, &c.GroupID, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	c.SyncSchedule = syncSchedule
	c.NextSyncAt = nextSyncAt
	c.LastSyncAt = lastSyncAt
	c.CreatedAt = timeToString(createdAt)
	c.UpdatedAt = timeToString(updatedAt)
	return &c, nil
}

// displayQueryDefinitionArg returns the value bound to the nullable
// display_query_definition jsonb column: SQL NULL when no filter is configured,
// otherwise the canonical JSON fragment. An empty string must never be written
// to a jsonb column.
func displayQueryDefinitionArg(fragment string) any {
	fragment = storedDisplayQueryDefinition(fragment)
	if fragment == "" {
		return nil
	}
	return fragment
}

func storedDisplayQueryDefinition(fragment string) string {
	return strings.TrimSpace(fragment)
}

func (s *PostgresUserStore) CreateCollection(ctx context.Context, input userstore.CreateCollectionInput) (*userstore.Collection, error) {
	id := generateUUID()
	now := nowUTC()
	if input.CollectionType == "" {
		input.CollectionType = "manual"
	}
	if input.QueryDefinition == "" {
		input.QueryDefinition = "{}"
	}
	if input.SortConfig == "" {
		input.SortConfig = "{}"
	}
	if input.SourceConfig == "" {
		input.SourceConfig = "{}"
	}
	// The new collection goes to the end of its creator's own order.
	var sortOrder int
	err := s.pool.QueryRow(ctx,
		`INSERT INTO user_personal_collections (
			id, user_id, profile_id, creator_profile_id, name, description, collection_type, is_shared,
			query_definition, sort_config, source_url, source_config, sync_schedule, next_sync_at,
			sort_order, display_query_definition, include_in_server_collections, poster_url, created_at, updated_at, native
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14,
			COALESCE((
				SELECT MAX(sort_order) + 1
				FROM user_personal_collections
				WHERE user_id = $2 AND creator_profile_id = $4 AND native
			), 0), $15, $16, $17, $18, $19, TRUE)
		RETURNING sort_order`,
		id, s.userID, input.CreatorProfileID, input.CreatorProfileID, input.Name, input.Description,
		input.CollectionType, input.IsShared, input.QueryDefinition, input.SortConfig,
		input.SourceURL, input.SourceConfig, input.SyncSchedule, input.NextSyncAt,
		displayQueryDefinitionArg(input.DisplayQueryDefinition), input.IncludeInServerCollections, input.PosterURL, now, now,
	).Scan(&sortOrder)
	if err != nil {
		return nil, fmt.Errorf("creating collection: %w", err)
	}
	return &userstore.Collection{
		ID:                         id,
		ProfileID:                  input.CreatorProfileID,
		CreatorProfileID:           input.CreatorProfileID,
		Name:                       input.Name,
		Description:                input.Description,
		CollectionType:             input.CollectionType,
		IsShared:                   input.IsShared,
		QueryDefinition:            input.QueryDefinition,
		SortConfig:                 input.SortConfig,
		SourceURL:                  input.SourceURL,
		SourceConfig:               input.SourceConfig,
		SyncSchedule:               input.SyncSchedule,
		NextSyncAt:                 input.NextSyncAt,
		DisplayQueryDefinition:     storedDisplayQueryDefinition(input.DisplayQueryDefinition),
		IncludeInServerCollections: input.IncludeInServerCollections,
		PosterURL:                  input.PosterURL,
		SortOrder:                  sortOrder,
		CreatedAt:                  now,
		UpdatedAt:                  now,
	}, nil
}

// GetCollection reads a native collection of the login. Audiobookshelf rows,
// which share the table, are not found.
func (s *PostgresUserStore) GetCollection(ctx context.Context, id string) (*userstore.Collection, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+collectionSelectColumns+`
		 FROM user_personal_collections WHERE user_id = $1 AND id = $2 AND native`,
		s.userID, id,
	)
	c, err := scanCollection(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("collection %s: %w", id, userstore.ErrCollectionNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("getting collection: %w", err)
	}
	return c, nil
}

// ListCollections lists the collections profileID may see: its own in its
// order, then other profiles' shared collections, grouped by creator and each
// in its creator's order.
func (s *PostgresUserStore) ListCollections(ctx context.Context, profileID string) ([]userstore.Collection, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+collectionSelectColumns+`
		 FROM user_personal_collections
		 WHERE user_id = $1
		   AND native
		   AND (creator_profile_id = $2 OR is_shared)
		 ORDER BY (creator_profile_id = $2) DESC, creator_profile_id ASC, sort_order ASC, created_at ASC, id ASC`,
		s.userID, profileID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing collections: %w", err)
	}
	defer rows.Close()

	var collections []userstore.Collection
	for rows.Next() {
		c, err := scanCollection(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning collection row: %w", err)
		}
		collections = append(collections, *c)
	}
	return collections, rows.Err()
}

// UpdateCollection composes one UPDATE statement covering every field the
// caller asked to change. Avoids the previous N-statements-per-edit pattern
// where each conditional re-touched updated_at on its own.
func (s *PostgresUserStore) UpdateCollection(ctx context.Context, input userstore.UpdateCollectionInput) error {
	if input.ExpectedRevision == nil {
		return s.updateCollectionAttempt(ctx, input)
	}
	return s.runCollectionMutation(ctx, input.ID, *input.ExpectedRevision, func() error { return s.updateCollectionAttempt(ctx, input) })
}
func (s *PostgresUserStore) updateCollectionAttempt(ctx context.Context, input userstore.UpdateCollectionInput) error {

	tx, err := s.pool.BeginTx(ctx, collectionMutationTxOptions(input.ExpectedRevision))
	if err != nil {
		return fmt.Errorf("beginning collection update: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := s.checkCollectionRevision(ctx, tx, input.ID, input.ExpectedRevision); err != nil {
		return err
	}

	var creatorProfileID string
	if err := tx.QueryRow(ctx,
		`SELECT creator_profile_id FROM user_personal_collections WHERE user_id = $1 AND id = $2 AND native`,
		s.userID, input.ID,
	).Scan(&creatorProfileID); err != nil {
		return fmt.Errorf("loading collection creator: %w", err)
	}
	if creatorProfileID != input.RequestProfileID {
		return fmt.Errorf("only the creator can update this collection")
	}

	now := nowUTC()
	sets := []string{}
	args := []any{}
	add := func(col string, value any) {
		args = append(args, value)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if input.Name != nil {
		add("name", *input.Name)
	}
	if input.Description != nil {
		add("description", *input.Description)
	}
	if input.IsShared != nil {
		add("is_shared", *input.IsShared)
	}
	if input.QueryDefinition != nil {
		add("query_definition", *input.QueryDefinition)
	}
	if input.SortConfig != nil {
		add("sort_config", *input.SortConfig)
	}
	if input.SourceURL != nil {
		add("source_url", *input.SourceURL)
	}
	if input.SourceConfigPatch != nil {
		if input.SourceConfig != nil {
			return fmt.Errorf("source config replacement and patch are mutually exclusive")
		}
		args = append(args, *input.SourceConfigPatch)
		sets = append(sets, fmt.Sprintf("source_config = source_config || $%d::jsonb", len(args)))
	} else if input.SourceConfig != nil {
		add("source_config", *input.SourceConfig)
	}
	if input.ClearSyncSchedule {
		add("sync_schedule", nil)
	} else if input.SyncSchedule != nil {
		add("sync_schedule", input.SyncSchedule)
	}
	if input.ClearNextSyncAt {
		add("next_sync_at", nil)
	} else if input.NextSyncAt != nil {
		add("next_sync_at", input.NextSyncAt)
	}
	if input.DisplayQueryDefinition != nil {
		add("display_query_definition", displayQueryDefinitionArg(*input.DisplayQueryDefinition))
	}
	if input.IncludeInServerCollections != nil {
		add("include_in_server_collections", *input.IncludeInServerCollections)
	}
	if input.PosterURL != nil {
		add("poster_url", *input.PosterURL)
	}
	if input.PosterThumbhash != nil {
		add("poster_thumbhash", *input.PosterThumbhash)
	}
	if input.GroupID != nil {
		targetGroupID := *input.GroupID
		add("group_id", targetGroupID)
		args = append(args, targetGroupID, s.userID, input.ID)
		targetGroupArg, userIDArg, collectionIDArg := len(args)-2, len(args)-1, len(args)
		sets = append(sets, fmt.Sprintf(`sort_order = CASE
			WHEN group_id IS NOT DISTINCT FROM $%d THEN sort_order
			ELSE COALESCE((
				SELECT MAX(sort_order) + 1
				FROM user_personal_collections
				WHERE user_id = $%d
				  AND group_id IS NOT DISTINCT FROM $%d
				  AND id <> $%d
			), 0)
		END`, targetGroupArg, userIDArg, targetGroupArg, collectionIDArg))
	}

	if len(sets) > 0 {
		add("updated_at", now)
		args = append(args, s.userID, input.ID)
		query := fmt.Sprintf(
			`UPDATE user_personal_collections SET %s WHERE user_id = $%d AND id = $%d`,
			strings.Join(sets, ", "), len(args)-1, len(args),
		)
		if _, err := tx.Exec(ctx, query, args...); err != nil {
			return err
		}
	}

	if err := s.finishCollectionRevision(ctx, tx, input.ID, input.ExpectedRevision); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresUserStore) DeleteCollection(ctx context.Context, id string) error {
	return s.deleteCollection(ctx, id, nil)
}
func (s *PostgresUserStore) DeleteCollectionIfRevision(ctx context.Context, id string, expected int64) error {
	return s.runCollectionMutation(ctx, id, expected, func() error { return s.deleteCollection(ctx, id, &expected) })
}
func (s *PostgresUserStore) deleteCollection(ctx context.Context, id string, expected *int64) error {
	tx, err := s.pool.BeginTx(ctx, collectionMutationTxOptions(expected))
	if err != nil {
		return fmt.Errorf("beginning transaction for collection delete: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := s.checkCollectionRevision(ctx, tx, id, expected); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `DELETE FROM user_personal_collection_items WHERE user_id = $1 AND collection_id = $2`, s.userID, id); err != nil {
		return fmt.Errorf("deleting collection items: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM user_collection_sort_preferences
		WHERE user_id = $1 AND collection_kind = 'user' AND collection_id = $2`, s.userID, id); err != nil {
		return fmt.Errorf("deleting collection sort preferences: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM user_personal_collections WHERE user_id = $1 AND id = $2`, s.userID, id); err != nil {
		return fmt.Errorf("deleting collection: %w", err)
	}

	if err := s.finishCollectionRevision(ctx, tx, id, expected); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// AddCollectionItem preserves existing membership, including its position and
// added timestamp. Changing existing positions requires an explicit reorder.
func (s *PostgresUserStore) AddCollectionItem(ctx context.Context, collectionID, mediaItemID string, position int) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO user_personal_collection_items (user_id, collection_id, media_item_id, position, added_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (user_id, collection_id, media_item_id, sub_item_id)
		   DO NOTHING`,
		s.userID, collectionID, mediaItemID, position, nowUTC(),
	)
	return err
}

// ReorderCollectionItems sets each item's position to its index in the
// supplied list. The list must be a permutation of the existing membership;
// concurrent edits that would silently drop or duplicate items are rejected.
func (s *PostgresUserStore) ReorderCollectionItems(ctx context.Context, collectionID string, orderedMediaItemIDs []string) error {
	return s.reorderCollectionItems(ctx, collectionID, orderedMediaItemIDs, nil)
}
func (s *PostgresUserStore) ReorderCollectionItemsIfRevision(ctx context.Context, collectionID string, orderedMediaItemIDs []string, expected int64) error {
	return s.runCollectionMutation(ctx, collectionID, expected, func() error { return s.reorderCollectionItems(ctx, collectionID, orderedMediaItemIDs, &expected) })
}
func (s *PostgresUserStore) reorderCollectionItems(ctx context.Context, collectionID string, orderedMediaItemIDs []string, expected *int64) error {
	if collectionutil.HasDuplicateOrderedIDs(orderedMediaItemIDs) {
		return fmt.Errorf("ordered_ids contains duplicates")
	}

	tx, err := s.pool.BeginTx(ctx, collectionMutationTxOptions(expected))
	if err != nil {
		return fmt.Errorf("beginning collection item reorder: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := s.checkCollectionRevision(ctx, tx, collectionID, expected); err != nil {
		return err
	}

	var updated, total int
	if err := tx.QueryRow(ctx, `
		WITH supplied AS (
		  SELECT id, pos FROM unnest($1::text[]) WITH ORDINALITY AS u(id, pos)
		),
		upd AS (
		  UPDATE user_personal_collection_items t
		  SET position = supplied.pos - 1
		  FROM supplied
		  WHERE t.user_id = $2 AND t.collection_id = $3 AND t.media_item_id = supplied.id AND t.sub_item_id = ''
		  RETURNING 1
		)
		SELECT (SELECT count(*) FROM upd),
		       (SELECT count(*) FROM user_personal_collection_items
		         WHERE user_id = $2 AND collection_id = $3 AND sub_item_id = '')
	`, orderedMediaItemIDs, s.userID, collectionID).Scan(&updated, &total); err != nil {
		return fmt.Errorf("reordering collection items: %w", err)
	}
	if updated != len(orderedMediaItemIDs) || updated != total {
		return collectionutil.ErrOrderedIDsMismatch
	}

	if _, err := tx.Exec(ctx,
		`UPDATE user_personal_collections SET updated_at = $1
		 WHERE user_id = $2 AND id = $3`,
		nowUTC(), s.userID, collectionID,
	); err != nil {
		return fmt.Errorf("touching collection: %w", err)
	}

	if err := s.finishCollectionRevision(ctx, tx, collectionID, expected); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ReorderCollections sets each of profileID's own collections' sort_order to
// its index in the supplied list. The list must be a permutation of exactly
// those collections: another profile's collection, shared or not, is a
// mismatch, and nobody else's order changes.
func (s *PostgresUserStore) ReorderCollections(ctx context.Context, profileID string, orderedIDs []string) error {
	return s.reorderCollections(ctx, profileID, orderedIDs, nil)
}
func (s *PostgresUserStore) ReorderCollectionsIfRevision(ctx context.Context, profileID string, orderedIDs []string, expected int64) error {
	return s.runCollectionMutation(ctx, "", expected, func() error { return s.reorderCollections(ctx, profileID, orderedIDs, &expected) })
}
func (s *PostgresUserStore) reorderCollections(ctx context.Context, profileID string, orderedIDs []string, expected *int64) error {
	if collectionutil.HasDuplicateOrderedIDs(orderedIDs) {
		return fmt.Errorf("ordered_ids contains duplicates")
	}

	tx, err := s.pool.BeginTx(ctx, collectionMutationTxOptions(expected))
	if err != nil {
		return fmt.Errorf("beginning collection reorder: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := s.checkCollectionOrderRevision(ctx, tx, expected); err != nil {
		return err
	}

	var updated, total int
	if err := tx.QueryRow(ctx, `
		WITH supplied AS (
		  SELECT id, pos FROM unnest($1::text[]) WITH ORDINALITY AS u(id, pos)
		),
		upd AS (
		  UPDATE user_personal_collections t
		  SET sort_order = supplied.pos - 1, updated_at = $3
		  FROM supplied
		  WHERE t.user_id = $2
		    AND t.id = supplied.id
		    AND t.creator_profile_id = $4
		    AND t.native
		  RETURNING 1
		)
		SELECT (SELECT count(*) FROM upd),
		       (SELECT count(*) FROM user_personal_collections
		         WHERE user_id = $2 AND creator_profile_id = $4 AND native)
	`, orderedIDs, s.userID, nowUTC(), profileID).Scan(&updated, &total); err != nil {
		return fmt.Errorf("reordering collections: %w", err)
	}
	if updated != len(orderedIDs) || updated != total {
		return collectionutil.ErrOrderedIDsMismatch
	}

	if err := s.finishCollectionOrderRevision(ctx, tx, expected); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Personal collections are flat. Keep the UserStore methods for the frozen
// bridge contract; handlers reject group operations through CollectionFeatures.
var errPersonalCollectionGroupsUnsupported = errors.New("personal collection groups are not supported")

func (s *PostgresUserStore) ListCollectionGroups(context.Context) ([]userstore.CollectionGroup, error) {
	return nil, errPersonalCollectionGroupsUnsupported
}

func (s *PostgresUserStore) EnsureCollectionGroup(_ context.Context, id string) error {
	if id == "" {
		return nil
	}
	return errPersonalCollectionGroupsUnsupported
}

func (s *PostgresUserStore) CreateCollectionGroup(context.Context, string, string, userstore.GroupSortMode) (*userstore.CollectionGroup, error) {
	return nil, errPersonalCollectionGroupsUnsupported
}

func (s *PostgresUserStore) UpdateCollectionGroup(context.Context, string, *string, *string, *userstore.GroupSortMode) (*userstore.CollectionGroup, error) {
	return nil, errPersonalCollectionGroupsUnsupported
}

func (s *PostgresUserStore) UpdateCollectionGroupIfRevision(context.Context, string, *string, *string, *userstore.GroupSortMode, int64) (*userstore.CollectionGroup, error) {
	return nil, errPersonalCollectionGroupsUnsupported
}

func (s *PostgresUserStore) DeleteCollectionGroup(context.Context, string) error {
	return errPersonalCollectionGroupsUnsupported
}

func (s *PostgresUserStore) DeleteCollectionGroupIfRevision(context.Context, string, int64) error {
	return errPersonalCollectionGroupsUnsupported
}

func (s *PostgresUserStore) ReorderCollectionGroups(context.Context, []string) error {
	return errPersonalCollectionGroupsUnsupported
}

func (s *PostgresUserStore) ReorderCollectionGroupsIfRevision(context.Context, []string, int64) error {
	return errPersonalCollectionGroupsUnsupported
}

func (s *PostgresUserStore) RemoveCollectionItem(ctx context.Context, collectionID, mediaItemID string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM user_personal_collection_items WHERE user_id = $1 AND collection_id = $2 AND media_item_id = $3 AND sub_item_id = ''`,
		s.userID, collectionID, mediaItemID,
	)
	return err
}

func (s *PostgresUserStore) ListCollectionItems(ctx context.Context, collectionID string) ([]userstore.CollectionItem, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT collection_id, media_item_id, position, added_at
		 FROM user_personal_collection_items
		 WHERE user_id = $1 AND collection_id = $2 AND sub_item_id = '' ORDER BY position ASC, media_item_id ASC`,
		s.userID, collectionID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing collection items: %w", err)
	}
	defer rows.Close()

	var items []userstore.CollectionItem
	for rows.Next() {
		var ci userstore.CollectionItem
		var addedAt time.Time
		if err := rows.Scan(&ci.CollectionID, &ci.MediaItemID, &ci.Position, &addedAt); err != nil {
			return nil, fmt.Errorf("scanning collection item row: %w", err)
		}
		ci.AddedAt = timeToString(addedAt)
		items = append(items, ci)
	}
	return items, rows.Err()
}

func (s *PostgresUserStore) ReplaceCollectionItems(ctx context.Context, collectionID string, items []userstore.CollectionItemReplacement) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning collection items replace: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx,
		`DELETE FROM user_personal_collection_items WHERE user_id = $1 AND collection_id = $2 AND sub_item_id = ''`,
		s.userID, collectionID,
	); err != nil {
		return fmt.Errorf("clearing collection items: %w", err)
	}

	now := nowUTC()
	for _, item := range items {
		if _, err := tx.Exec(ctx,
			`INSERT INTO user_personal_collection_items (user_id, collection_id, media_item_id, position, added_at)
			 VALUES ($1, $2, $3, $4, $5)
			 ON CONFLICT DO NOTHING`,
			s.userID, collectionID, item.MediaItemID, item.Position, now,
		); err != nil {
			return fmt.Errorf("inserting collection item: %w", err)
		}
	}

	return tx.Commit(ctx)
}

func (s *PostgresUserStore) UpdateCollectionSyncState(ctx context.Context, input userstore.UpdateCollectionSyncStateInput) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE user_personal_collections
		 SET last_sync_at = $1, last_sync_status = $2, last_sync_message = $3,
		     item_count = $4, updated_at = $6,
		     next_sync_at = CASE
		         WHEN sync_schedule IS NOT DISTINCT FROM $9 AND next_sync_at IS NOT DISTINCT FROM $10 THEN $5
		         ELSE next_sync_at
		     END
		 WHERE user_id = $7 AND id = $8`,
		input.LastSyncAt, input.Status, input.Message, input.ItemCount, input.NextSyncAt,
		nowUTC(), s.userID, input.ID, input.ScheduleAtStart, input.NextSyncAtAtStart,
	)
	if err != nil {
		return fmt.Errorf("updating collection sync state: %w", err)
	}
	return nil
}
