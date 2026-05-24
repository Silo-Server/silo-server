package pgstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/collectionutil"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

const collectionSelectColumns = `id, profile_id, creator_profile_id, name, description, collection_type, is_shared,
	query_definition, sort_config, source_url, source_config, sync_schedule, next_sync_at,
	last_sync_at, last_sync_status, last_sync_message, item_count, include_in_server_collections,
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
		&lastSyncAt, &c.LastSyncStatus, &c.LastSyncMessage, &c.ItemCount, &c.IncludeInServerCollections,
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
	allowedProfiles := normalizeCollectionProfiles(input.CreatorProfileID, input.AllowedProfileIDs, input.IsShared)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning collection create: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var sortOrder int
	err = tx.QueryRow(ctx,
		`INSERT INTO user_personal_collections (
			id, user_id, profile_id, creator_profile_id, name, description, collection_type, is_shared,
			query_definition, sort_config, source_url, source_config, sync_schedule, next_sync_at,
			sort_order, include_in_server_collections, poster_url, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14,
			COALESCE((
				SELECT MAX(sort_order) + 1
				FROM user_personal_collections
				WHERE user_id = $2 AND group_id IS NULL
			), 0), $15, $16, $17, $18)
		RETURNING sort_order`,
		id, s.userID, input.CreatorProfileID, input.CreatorProfileID, input.Name, input.Description,
		input.CollectionType, input.IsShared, input.QueryDefinition, input.SortConfig,
		input.SourceURL, input.SourceConfig, input.SyncSchedule, input.NextSyncAt,
		input.IncludeInServerCollections, input.PosterURL, now, now,
	).Scan(&sortOrder)
	if err != nil {
		return nil, fmt.Errorf("creating collection: %w", err)
	}
	for _, profileID := range allowedProfiles {
		if _, err := tx.Exec(ctx,
			`INSERT INTO user_personal_collection_profiles (user_id, collection_id, profile_id)
			 VALUES ($1, $2, $3)`,
			s.userID, id, profileID,
		); err != nil {
			return nil, fmt.Errorf("creating collection profile visibility: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing collection create: %w", err)
	}
	return &userstore.Collection{
		ID:                         id,
		ProfileID:                  input.CreatorProfileID,
		CreatorProfileID:           input.CreatorProfileID,
		Name:                       input.Name,
		Description:                input.Description,
		CollectionType:             input.CollectionType,
		IsShared:                   input.IsShared,
		AllowedProfileIDs:          allowedProfiles,
		QueryDefinition:            input.QueryDefinition,
		SortConfig:                 input.SortConfig,
		SourceURL:                  input.SourceURL,
		SourceConfig:               input.SourceConfig,
		SyncSchedule:               input.SyncSchedule,
		NextSyncAt:                 input.NextSyncAt,
		IncludeInServerCollections: input.IncludeInServerCollections,
		PosterURL:                  input.PosterURL,
		SortOrder:                  sortOrder,
		CreatedAt:                  now,
		UpdatedAt:                  now,
	}, nil
}

func (s *PostgresUserStore) GetCollection(ctx context.Context, id string) (*userstore.Collection, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+collectionSelectColumns+`
		 FROM user_personal_collections WHERE user_id = $1 AND id = $2`,
		s.userID, id,
	)
	c, err := scanCollection(row)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("collection %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("getting collection: %w", err)
	}
	c.AllowedProfileIDs, err = s.listCollectionProfiles(ctx, id)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// ListCollections fetches collections plus their allowed profile lists in one
// query using ARRAY_AGG, avoiding the N+1 round trip a naive per-row profile
// lookup would create.
func (s *PostgresUserStore) ListCollections(ctx context.Context, profileID string) ([]userstore.Collection, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+collectionSelectColumns+`,
		        COALESCE(
		          (SELECT array_agg(p.profile_id ORDER BY p.profile_id)
		           FROM user_personal_collection_profiles p
		           WHERE p.user_id = upc.user_id AND p.collection_id = upc.id),
		          ARRAY[]::TEXT[]
		        ) AS allowed_profile_ids
		 FROM user_personal_collections upc
		 WHERE user_id = $1
		   AND EXISTS (
		     SELECT 1 FROM user_personal_collection_profiles vp
		     WHERE vp.user_id = upc.user_id
		       AND vp.collection_id = upc.id
		       AND vp.profile_id = $2
		   )
		 ORDER BY sort_order ASC, created_at ASC, id ASC`,
		s.userID, profileID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing collections: %w", err)
	}
	defer rows.Close()

	var collections []userstore.Collection
	for rows.Next() {
		var c userstore.Collection
		var (
			createdAt, updatedAt   time.Time
			nextSyncAt, lastSyncAt *time.Time
			syncSchedule           *string
			allowed                []string
		)
		if err := rows.Scan(
			&c.ID, &c.ProfileID, &c.CreatorProfileID, &c.Name, &c.Description, &c.CollectionType, &c.IsShared,
			&c.QueryDefinition, &c.SortConfig, &c.SourceURL, &c.SourceConfig, &syncSchedule, &nextSyncAt,
			&lastSyncAt, &c.LastSyncStatus, &c.LastSyncMessage, &c.ItemCount, &c.IncludeInServerCollections,
			&c.PosterURL, &c.PosterThumbhash, &c.SortOrder, &c.GroupID, &createdAt, &updatedAt, &allowed,
		); err != nil {
			return nil, fmt.Errorf("scanning collection row: %w", err)
		}
		c.SyncSchedule = syncSchedule
		c.NextSyncAt = nextSyncAt
		c.LastSyncAt = lastSyncAt
		c.CreatedAt = timeToString(createdAt)
		c.UpdatedAt = timeToString(updatedAt)
		c.AllowedProfileIDs = allowed
		collections = append(collections, c)
	}
	return collections, rows.Err()
}

// UpdateCollection composes one UPDATE statement covering every field the
// caller asked to change. Avoids the previous N-statements-per-edit pattern
// where each conditional re-touched updated_at on its own.
func (s *PostgresUserStore) UpdateCollection(ctx context.Context, input userstore.UpdateCollectionInput) error {
	var creatorProfileID string
	if err := s.pool.QueryRow(ctx,
		`SELECT creator_profile_id FROM user_personal_collections WHERE user_id = $1 AND id = $2`,
		s.userID, input.ID,
	).Scan(&creatorProfileID); err != nil {
		return fmt.Errorf("loading collection creator: %w", err)
	}
	if creatorProfileID != input.RequestProfileID {
		return fmt.Errorf("only the creator can update this collection")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning collection update: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

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
	if input.SourceConfig != nil {
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

	if input.AllowedProfileIDs != nil || input.IsShared != nil {
		isShared := false
		if input.IsShared != nil {
			isShared = *input.IsShared
		} else {
			if err := tx.QueryRow(ctx,
				`SELECT is_shared FROM user_personal_collections WHERE user_id = $1 AND id = $2`,
				s.userID, input.ID,
			).Scan(&isShared); err != nil {
				return err
			}
		}
		allowed := []string{}
		if input.AllowedProfileIDs != nil {
			allowed = *input.AllowedProfileIDs
		} else {
			allowed, err = s.listCollectionProfilesTx(ctx, tx, input.ID)
			if err != nil {
				return err
			}
		}
		allowed = normalizeCollectionProfiles(creatorProfileID, allowed, isShared)
		if _, err := tx.Exec(ctx,
			`DELETE FROM user_personal_collection_profiles WHERE user_id = $1 AND collection_id = $2`,
			s.userID, input.ID,
		); err != nil {
			return err
		}
		for _, profileID := range allowed {
			if _, err := tx.Exec(ctx,
				`INSERT INTO user_personal_collection_profiles (user_id, collection_id, profile_id)
				 VALUES ($1, $2, $3)`,
				s.userID, input.ID, profileID,
			); err != nil {
				return err
			}
		}
	}

	return tx.Commit(ctx)
}

func (s *PostgresUserStore) DeleteCollection(ctx context.Context, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction for collection delete: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `DELETE FROM user_personal_collection_items WHERE user_id = $1 AND collection_id = $2`, s.userID, id); err != nil {
		return fmt.Errorf("deleting collection items: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM user_personal_collections WHERE user_id = $1 AND id = $2`, s.userID, id); err != nil {
		return fmt.Errorf("deleting collection: %w", err)
	}

	return tx.Commit(ctx)
}

func (s *PostgresUserStore) AddCollectionItem(ctx context.Context, collectionID, mediaItemID string, position int) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO user_personal_collection_items (user_id, collection_id, media_item_id, position, added_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (user_id, collection_id, media_item_id)
		   DO UPDATE SET position = EXCLUDED.position`,
		s.userID, collectionID, mediaItemID, position, nowUTC(),
	)
	return err
}

// ReorderCollectionItems sets each item's position to its index in the
// supplied list. The list must be a permutation of the existing membership;
// concurrent edits that would silently drop or duplicate items are rejected.
func (s *PostgresUserStore) ReorderCollectionItems(ctx context.Context, collectionID string, orderedMediaItemIDs []string) error {
	if collectionutil.HasDuplicateOrderedIDs(orderedMediaItemIDs) {
		return fmt.Errorf("ordered_ids contains duplicates")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning collection item reorder: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var updated, total int
	if err := tx.QueryRow(ctx, `
		WITH supplied AS (
		  SELECT id, pos FROM unnest($1::text[]) WITH ORDINALITY AS u(id, pos)
		),
		upd AS (
		  UPDATE user_personal_collection_items t
		  SET position = supplied.pos - 1
		  FROM supplied
		  WHERE t.user_id = $2 AND t.collection_id = $3 AND t.media_item_id = supplied.id
		  RETURNING 1
		)
		SELECT (SELECT count(*) FROM upd),
		       (SELECT count(*) FROM user_personal_collection_items
		         WHERE user_id = $2 AND collection_id = $3)
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

	return tx.Commit(ctx)
}

// ReorderCollections sets each collection's sort_order to its index in the
// supplied list. The list must be a permutation of the user's collections in
// the supplied group. A nil groupID targets the implicit Ungrouped bucket.
func (s *PostgresUserStore) ReorderCollections(ctx context.Context, profileID string, groupID *string, orderedIDs []string) error {
	if collectionutil.HasDuplicateOrderedIDs(orderedIDs) {
		return fmt.Errorf("ordered_ids contains duplicates")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning collection reorder: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

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
		    AND t.group_id IS NOT DISTINCT FROM $4
		    AND EXISTS (
		      SELECT 1
		      FROM user_personal_collection_profiles p
		      WHERE p.user_id = t.user_id
		        AND p.collection_id = t.id
		        AND p.profile_id = $5
		    )
		  RETURNING 1
		)
		SELECT (SELECT count(*) FROM upd),
		       (SELECT count(*) FROM user_personal_collections
		         WHERE user_id = $2
		           AND group_id IS NOT DISTINCT FROM $4
		           AND EXISTS (
		             SELECT 1
		             FROM user_personal_collection_profiles p
		             WHERE p.user_id = user_personal_collections.user_id
		               AND p.collection_id = user_personal_collections.id
		               AND p.profile_id = $5
		           ))
	`, orderedIDs, s.userID, nowUTC(), groupID, profileID).Scan(&updated, &total); err != nil {
		return fmt.Errorf("reordering collections: %w", err)
	}
	if updated != len(orderedIDs) || updated != total {
		return collectionutil.ErrOrderedIDsMismatch
	}

	return tx.Commit(ctx)
}

// ListCollectionGroups returns the user's explicit group rows. Collections
// with nil group_id fall into an implicit Ungrouped bucket and are not
// represented in this table.
func (s *PostgresUserStore) ListCollectionGroups(ctx context.Context) ([]userstore.CollectionGroup, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, slug, default_sort_mode, sort_order, created_at, updated_at
		FROM user_collection_groups
		WHERE user_id = $1
		ORDER BY sort_order ASC, name ASC, id ASC
	`, s.userID)
	if err != nil {
		return nil, fmt.Errorf("listing user collection groups: %w", err)
	}
	defer rows.Close()

	var groups []userstore.CollectionGroup
	for rows.Next() {
		var g userstore.CollectionGroup
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&g.ID, &g.Name, &g.Slug, &g.DefaultSortMode, &g.SortOrder, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scanning user collection group: %w", err)
		}
		g.CreatedAt = timeToString(createdAt)
		g.UpdatedAt = timeToString(updatedAt)
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

func (s *PostgresUserStore) getCollectionGroup(ctx context.Context, id string) (*userstore.CollectionGroup, error) {
	var g userstore.CollectionGroup
	var createdAt, updatedAt time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT id, name, slug, default_sort_mode, sort_order, created_at, updated_at
		FROM user_collection_groups
		WHERE user_id = $1 AND id = $2
	`, s.userID, id).Scan(&g.ID, &g.Name, &g.Slug, &g.DefaultSortMode, &g.SortOrder, &createdAt, &updatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("user collection group not found")
		}
		return nil, fmt.Errorf("getting user collection group: %w", err)
	}
	g.CreatedAt = timeToString(createdAt)
	g.UpdatedAt = timeToString(updatedAt)
	return &g, nil
}

func (s *PostgresUserStore) EnsureCollectionGroup(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM user_collection_groups WHERE user_id = $1 AND id = $2
		)
	`, s.userID, id).Scan(&exists); err != nil {
		return fmt.Errorf("checking user collection group: %w", err)
	}
	if !exists {
		return userstore.ErrCollectionGroupNotFound
	}
	return nil
}

func (s *PostgresUserStore) CreateCollectionGroup(ctx context.Context, name, slug string, defaultSortMode userstore.GroupSortMode) (*userstore.CollectionGroup, error) {
	if name == "" {
		return nil, fmt.Errorf("group name cannot be empty")
	}
	if slug == "" {
		slug = collectionutil.SlugifyGroupSlug(name)
	}
	if defaultSortMode == "" {
		defaultSortMode = userstore.GroupSortManual
	}
	id := "ucg_" + generateUUID()
	var g userstore.CollectionGroup
	var createdAt, updatedAt time.Time
	err := s.pool.QueryRow(ctx, `
		INSERT INTO user_collection_groups (
			user_id, label, title, id, name, slug, default_sort_mode, sort_order
		)
		SELECT $1, $2, $3, $4, $3, $2, $5,
		       COALESCE((SELECT MAX(sort_order) + 1 FROM user_collection_groups WHERE user_id = $1), 0)
		RETURNING id, name, slug, default_sort_mode, sort_order, created_at, updated_at
	`, s.userID, slug, name, id, defaultSortMode).Scan(&g.ID, &g.Name, &g.Slug, &g.DefaultSortMode, &g.SortOrder, &createdAt, &updatedAt)
	if err != nil {
		return nil, fmt.Errorf("creating user collection group: %w", err)
	}
	g.CreatedAt = timeToString(createdAt)
	g.UpdatedAt = timeToString(updatedAt)
	return &g, nil
}

func (s *PostgresUserStore) UpdateCollectionGroup(ctx context.Context, id string, name *string, slug *string, defaultSortMode *userstore.GroupSortMode) (*userstore.CollectionGroup, error) {
	sets := []string{}
	args := []any{s.userID, id}
	add := func(column string, value any) {
		args = append(args, value)
		sets = append(sets, fmt.Sprintf("%s = $%d", column, len(args)))
	}
	if name != nil {
		add("name", *name)
		add("title", *name)
	}
	if slug != nil {
		add("slug", *slug)
		add("label", *slug)
	}
	if defaultSortMode != nil {
		add("default_sort_mode", *defaultSortMode)
	}
	if len(sets) == 0 {
		return s.getCollectionGroup(ctx, id)
	}
	add("updated_at", nowUTC())
	query := fmt.Sprintf(`
		UPDATE user_collection_groups
		SET %s
		WHERE user_id = $1 AND id = $2
		RETURNING id, name, slug, default_sort_mode, sort_order, created_at, updated_at
	`, strings.Join(sets, ", "))
	var g userstore.CollectionGroup
	var createdAt, updatedAt time.Time
	err := s.pool.QueryRow(ctx, query, args...).Scan(&g.ID, &g.Name, &g.Slug, &g.DefaultSortMode, &g.SortOrder, &createdAt, &updatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("user collection group not found")
		}
		return nil, fmt.Errorf("updating user collection group: %w", err)
	}
	g.CreatedAt = timeToString(createdAt)
	g.UpdatedAt = timeToString(updatedAt)
	return &g, nil
}

func (s *PostgresUserStore) DeleteCollectionGroup(ctx context.Context, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning user collection group delete: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `
		UPDATE user_personal_collections
		SET group_id = NULL, updated_at = $3
		WHERE user_id = $1 AND group_id = $2
	`, s.userID, id, nowUTC()); err != nil {
		return fmt.Errorf("clearing group_id on collections: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		DELETE FROM user_collection_groups WHERE user_id = $1 AND id = $2
	`, s.userID, id)
	if err != nil {
		return fmt.Errorf("deleting user collection group: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("user collection group not found")
	}
	return tx.Commit(ctx)
}

func (s *PostgresUserStore) ReorderCollectionGroups(ctx context.Context, orderedIDs []string) error {
	if collectionutil.HasDuplicateOrderedIDs(orderedIDs) {
		return fmt.Errorf("ordered_ids contains duplicates")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning user collection groups reorder: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var updated, total int
	if err := tx.QueryRow(ctx, `
		WITH supplied AS (
		  SELECT id, pos FROM unnest($1::text[]) WITH ORDINALITY AS u(id, pos)
		),
		upd AS (
		  UPDATE user_collection_groups g
		  SET sort_order = supplied.pos - 1, updated_at = $3
		  FROM supplied
		  WHERE g.user_id = $2 AND g.id = supplied.id
		  RETURNING 1
		)
		SELECT (SELECT count(*) FROM upd),
		       (SELECT count(*) FROM user_collection_groups WHERE user_id = $2)
	`, orderedIDs, s.userID, nowUTC()).Scan(&updated, &total); err != nil {
		return fmt.Errorf("reordering user collection groups: %w", err)
	}
	if updated != len(orderedIDs) || updated != total {
		return collectionutil.ErrOrderedIDsMismatch
	}
	return tx.Commit(ctx)
}

func (s *PostgresUserStore) RemoveCollectionItem(ctx context.Context, collectionID, mediaItemID string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM user_personal_collection_items WHERE user_id = $1 AND collection_id = $2 AND media_item_id = $3`,
		s.userID, collectionID, mediaItemID,
	)
	return err
}

func (s *PostgresUserStore) ListCollectionItems(ctx context.Context, collectionID string) ([]userstore.CollectionItem, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT collection_id, media_item_id, position, added_at
		 FROM user_personal_collection_items
		 WHERE user_id = $1 AND collection_id = $2 ORDER BY position ASC`,
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
		`DELETE FROM user_personal_collection_items WHERE user_id = $1 AND collection_id = $2`,
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
		     item_count = $4, next_sync_at = $5, updated_at = $6
		 WHERE user_id = $7 AND id = $8`,
		input.LastSyncAt, input.Status, input.Message, input.ItemCount, input.NextSyncAt,
		nowUTC(), s.userID, input.ID,
	)
	if err != nil {
		return fmt.Errorf("updating collection sync state: %w", err)
	}
	return nil
}

func (s *PostgresUserStore) listCollectionProfiles(ctx context.Context, collectionID string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT profile_id
		 FROM user_personal_collection_profiles
		 WHERE user_id = $1 AND collection_id = $2
		 ORDER BY profile_id ASC`,
		s.userID, collectionID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing collection profiles: %w", err)
	}
	defer rows.Close()
	return scanCollectionProfiles(rows)
}

func (s *PostgresUserStore) listCollectionProfilesTx(ctx context.Context, tx pgx.Tx, collectionID string) ([]string, error) {
	rows, err := tx.Query(ctx,
		`SELECT profile_id
		 FROM user_personal_collection_profiles
		 WHERE user_id = $1 AND collection_id = $2
		 ORDER BY profile_id ASC`,
		s.userID, collectionID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing collection profiles in tx: %w", err)
	}
	defer rows.Close()
	return scanCollectionProfiles(rows)
}

func scanCollectionProfiles(rows pgx.Rows) ([]string, error) {
	var profiles []string
	for rows.Next() {
		var profileID string
		if err := rows.Scan(&profileID); err != nil {
			return nil, fmt.Errorf("scanning collection profile row: %w", err)
		}
		profiles = append(profiles, profileID)
	}
	return profiles, rows.Err()
}

func normalizeCollectionProfiles(creatorProfileID string, allowedProfiles []string, isShared bool) []string {
	if !isShared {
		return []string{creatorProfileID}
	}
	seen := map[string]struct{}{creatorProfileID: {}}
	normalized := []string{creatorProfileID}
	for _, profileID := range allowedProfiles {
		if profileID == "" {
			continue
		}
		if _, ok := seen[profileID]; ok {
			continue
		}
		seen[profileID] = struct{}{}
		normalized = append(normalized, profileID)
	}
	return normalized
}
