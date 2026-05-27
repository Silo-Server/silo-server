package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
)

// ABSPlaylistStore implements abs.PlaylistStore against the canonical
// user_personal_collections + user_personal_collection_items tables
// (migration 156). ABS playlists live in user_personal_collections with
// collection_type = 'playlist'; their entries are rows in
// user_personal_collection_items where sub_item_id is the ABS
// episode_id (empty string for whole-book entries, non-empty for
// podcast-episode entries).
//
// abs.Playlist.IsPublic maps to user_personal_collections.is_shared.
// profile_id is a text column (NOT NULL DEFAULT '') in the canonical
// schema, so the empty string stands in for "primary profile".
//
// abs.Playlist.CoverItem has no canonical column (deferred per
// spec §6); reads always return the zero value and writes ignore it.
type ABSPlaylistStore struct {
	Pool *pgxpool.Pool
}

var _ abs.PlaylistStore = (*ABSPlaylistStore)(nil)

// absCollectionTypePlaylist is the discriminator value for ABS
// playlists in the canonical user_personal_collections table.
const absCollectionTypePlaylist = "playlist"

// NOTE: PK-collision caveat. user_personal_collection_items has
// PRIMARY KEY (user_id, collection_id, media_item_id) — sub_item_id is
// NOT part of the PK. The old abs_playlist_items PK included
// episode_id, so a playlist that referenced two episodes of the same
// library item would have had two distinct rows. Under the canonical
// schema the second insert collides on the PK and the ON CONFLICT
// clause silently drops it.
//
// Prod baseline at migration time: the single existing playlist has
// zero items, so no live data is affected. Multi-episode-of-same-book
// inside one playlist remains a potential future limitation and would
// require a schema change (extending the PK to include sub_item_id, or
// adding a unique index over the four columns) — out of scope here.

func (s *ABSPlaylistStore) ListUserPlaylists(ctx context.Context, userID, profileID string) ([]abs.Playlist, error) {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return nil, fmt.Errorf("abs_playlist_store: invalid user id %q: %w", userID, err)
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, user_id, profile_id, name, description, is_shared, created_at, updated_at
		FROM user_personal_collections
		WHERE collection_type = $3
		  AND user_id = $1
		  AND profile_id = $2
		ORDER BY created_at DESC`,
		uid, profileID, absCollectionTypePlaylist,
	)
	if err != nil {
		return nil, fmt.Errorf("abs_playlist_store: list: %w", err)
	}
	defer rows.Close()
	out := make([]abs.Playlist, 0)
	for rows.Next() {
		var p abs.Playlist
		var uidScan int
		if err := rows.Scan(&p.ID, &uidScan, &p.ProfileID, &p.Name, &p.Description, &p.IsPublic, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("abs_playlist_store: list scan: %w", err)
		}
		p.UserID = strconv.Itoa(uidScan)
		// CoverItem has no canonical column — always zero.
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("abs_playlist_store: list rows: %w", err)
	}
	return out, nil
}

func (s *ABSPlaylistStore) GetPlaylist(ctx context.Context, id string) (abs.Playlist, error) {
	var p abs.Playlist
	var uidScan int
	row := s.Pool.QueryRow(ctx, `
		SELECT id, user_id, profile_id, name, description, is_shared, created_at, updated_at
		FROM user_personal_collections
		WHERE id = $1 AND collection_type = $2`,
		id, absCollectionTypePlaylist,
	)
	if err := row.Scan(&p.ID, &uidScan, &p.ProfileID, &p.Name, &p.Description, &p.IsPublic, &p.CreatedAt, &p.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return abs.Playlist{}, abs.ErrNotFound
		}
		return abs.Playlist{}, fmt.Errorf("abs_playlist_store: get: %w", err)
	}
	p.UserID = strconv.Itoa(uidScan)
	// CoverItem has no canonical column — always zero.
	return p, nil
}

func (s *ABSPlaylistStore) CreatePlaylist(ctx context.Context, p abs.Playlist) error {
	uid, err := strconv.Atoi(p.UserID)
	if err != nil {
		return fmt.Errorf("abs_playlist_store: invalid user id %q: %w", p.UserID, err)
	}
	// CoverItem is intentionally not persisted (no canonical column).
	if _, err := s.Pool.Exec(ctx, `
		INSERT INTO user_personal_collections
		    (id, user_id, profile_id, creator_profile_id, name, description,
		     collection_type, is_shared, query_definition, created_at, updated_at)
		VALUES ($1, $2, $3, $3, $4, $5, $6, $7, '{}'::jsonb, now(), now())`,
		p.ID, uid, p.ProfileID, p.Name, p.Description, absCollectionTypePlaylist, p.IsPublic,
	); err != nil {
		return fmt.Errorf("abs_playlist_store: create: %w", err)
	}
	return nil
}

func (s *ABSPlaylistStore) UpdatePlaylist(ctx context.Context, p abs.Playlist) error {
	// CoverItem is intentionally not persisted (no canonical column).
	if _, err := s.Pool.Exec(ctx, `
		UPDATE user_personal_collections
		   SET name = $2, description = $3, is_shared = $4, updated_at = now()
		 WHERE id = $1 AND collection_type = $5`,
		p.ID, p.Name, p.Description, p.IsPublic, absCollectionTypePlaylist,
	); err != nil {
		return fmt.Errorf("abs_playlist_store: update: %w", err)
	}
	return nil
}

func (s *ABSPlaylistStore) DeletePlaylist(ctx context.Context, id string) error {
	// user_personal_collection_items has no FK to user_personal_collections,
	// so cascade is not automatic — drop items first, then the parent.
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("abs_playlist_store: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx,
		`DELETE FROM user_personal_collection_items WHERE collection_id = $1`,
		id,
	); err != nil {
		return fmt.Errorf("abs_playlist_store: delete-items: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM user_personal_collections WHERE id = $1 AND collection_type = $2`,
		id, absCollectionTypePlaylist,
	); err != nil {
		return fmt.Errorf("abs_playlist_store: delete: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("abs_playlist_store: commit: %w", err)
	}
	return nil
}

func (s *ABSPlaylistStore) ListPlaylistItems(ctx context.Context, playlistID string) ([]abs.PlaylistItem, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT media_item_id, COALESCE(sub_item_id, '') AS episode_id, position, added_at
		FROM user_personal_collection_items
		WHERE collection_id = $1
		ORDER BY position ASC, added_at ASC`,
		playlistID,
	)
	if err != nil {
		return nil, fmt.Errorf("abs_playlist_store: list-items: %w", err)
	}
	defer rows.Close()
	out := make([]abs.PlaylistItem, 0)
	for rows.Next() {
		var it abs.PlaylistItem
		if err := rows.Scan(&it.LibraryItemID, &it.EpisodeID, &it.Position, &it.AddedAt); err != nil {
			return nil, fmt.Errorf("abs_playlist_store: list-items scan: %w", err)
		}
		it.PlaylistID = playlistID
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("abs_playlist_store: list-items rows: %w", err)
	}
	return out, nil
}

func (s *ABSPlaylistStore) AddPlaylistItem(ctx context.Context, playlistID, libraryItemID, episodeID string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("abs_playlist_store: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// user_personal_collection_items.user_id is NOT NULL and has no
	// default; copy it from the parent playlist row. INSERT ... SELECT
	// preserves the silent-no-op semantics of the ABS interface when
	// the parent is missing (zero rows selected → zero rows inserted)
	// and the PK ON CONFLICT keeps re-adds idempotent.
	//
	// See the top-of-file note on the PK-collision caveat: episode_id
	// (sub_item_id) is NOT in the PK, so a second add of the same
	// (collection_id, media_item_id) with a different episode_id will
	// be silently dropped by ON CONFLICT.
	if _, err := tx.Exec(ctx, `
		INSERT INTO user_personal_collection_items
		    (user_id, collection_id, media_item_id, sub_item_id, position, added_at)
		SELECT c.user_id, c.id, $2, $3,
		       COALESCE((
		           SELECT MAX(i.position) + 1
		           FROM user_personal_collection_items i
		           WHERE i.collection_id = c.id
		       ), 0),
		       now()
		FROM user_personal_collections c
		WHERE c.id = $1 AND c.collection_type = $4
		ON CONFLICT (user_id, collection_id, media_item_id) DO NOTHING`,
		playlistID, libraryItemID, episodeID, absCollectionTypePlaylist,
	); err != nil {
		return fmt.Errorf("abs_playlist_store: add-item: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE user_personal_collections SET updated_at = now() WHERE id = $1 AND collection_type = $2`,
		playlistID, absCollectionTypePlaylist,
	); err != nil {
		return fmt.Errorf("abs_playlist_store: bump-parent: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("abs_playlist_store: commit: %w", err)
	}
	return nil
}

func (s *ABSPlaylistStore) RemovePlaylistItem(ctx context.Context, playlistID, libraryItemID, episodeID string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("abs_playlist_store: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx,
		`DELETE FROM user_personal_collection_items
		 WHERE collection_id = $1 AND media_item_id = $2 AND sub_item_id = $3`,
		playlistID, libraryItemID, episodeID,
	); err != nil {
		return fmt.Errorf("abs_playlist_store: remove-item: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE user_personal_collections SET updated_at = now() WHERE id = $1 AND collection_type = $2`,
		playlistID, absCollectionTypePlaylist,
	); err != nil {
		return fmt.Errorf("abs_playlist_store: bump-parent: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("abs_playlist_store: commit: %w", err)
	}
	return nil
}
