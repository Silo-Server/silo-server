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

// ABSPlaylistStore implements abs.PlaylistStore against the abs_playlists
// + abs_playlist_items tables (migrations 151 + 152).
type ABSPlaylistStore struct {
	Pool *pgxpool.Pool
}

var _ abs.PlaylistStore = (*ABSPlaylistStore)(nil)

func (s *ABSPlaylistStore) ListUserPlaylists(ctx context.Context, userID, profileID string) ([]abs.Playlist, error) {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return nil, fmt.Errorf("abs_playlist_store: invalid user id %q: %w", userID, err)
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, user_id, profile_id, name, description, cover_item, is_public, created_at, updated_at
		FROM abs_playlists
		WHERE user_id = $1
		  AND COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid)
		      = COALESCE($2::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
		ORDER BY created_at DESC`,
		uid, profileArg(profileID),
	)
	if err != nil {
		return nil, fmt.Errorf("abs_playlist_store: list: %w", err)
	}
	defer rows.Close()
	out := make([]abs.Playlist, 0)
	for rows.Next() {
		var p abs.Playlist
		var uidScan int
		var profileScan, coverScan *string
		if err := rows.Scan(&p.ID, &uidScan, &profileScan, &p.Name, &p.Description, &coverScan, &p.IsPublic, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("abs_playlist_store: list scan: %w", err)
		}
		p.UserID = strconv.Itoa(uidScan)
		if profileScan != nil {
			p.ProfileID = *profileScan
		}
		if coverScan != nil {
			p.CoverItem = *coverScan
		}
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
	var profileScan, coverScan *string
	row := s.Pool.QueryRow(ctx, `
		SELECT id, user_id, profile_id, name, description, cover_item, is_public, created_at, updated_at
		FROM abs_playlists WHERE id = $1`, id)
	if err := row.Scan(&p.ID, &uidScan, &profileScan, &p.Name, &p.Description, &coverScan, &p.IsPublic, &p.CreatedAt, &p.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return abs.Playlist{}, abs.ErrNotFound
		}
		return abs.Playlist{}, fmt.Errorf("abs_playlist_store: get: %w", err)
	}
	p.UserID = strconv.Itoa(uidScan)
	if profileScan != nil {
		p.ProfileID = *profileScan
	}
	if coverScan != nil {
		p.CoverItem = *coverScan
	}
	return p, nil
}

// coverArg returns the value to bind for cover_item; empty string maps
// to NULL so the FK doesn't reject empty.
func coverArg(cover string) any {
	if cover == "" {
		return nil
	}
	return cover
}

func (s *ABSPlaylistStore) CreatePlaylist(ctx context.Context, p abs.Playlist) error {
	uid, err := strconv.Atoi(p.UserID)
	if err != nil {
		return fmt.Errorf("abs_playlist_store: invalid user id %q: %w", p.UserID, err)
	}
	if _, err := s.Pool.Exec(ctx, `
		INSERT INTO abs_playlists (id, user_id, profile_id, name, description, cover_item, is_public)
		VALUES ($1, $2, $3::uuid, $4, $5, $6, $7)`,
		p.ID, uid, profileArg(p.ProfileID), p.Name, p.Description, coverArg(p.CoverItem), p.IsPublic,
	); err != nil {
		return fmt.Errorf("abs_playlist_store: create: %w", err)
	}
	return nil
}

func (s *ABSPlaylistStore) UpdatePlaylist(ctx context.Context, p abs.Playlist) error {
	if _, err := s.Pool.Exec(ctx, `
		UPDATE abs_playlists
		   SET name = $2, description = $3, cover_item = $4, is_public = $5, updated_at = now()
		 WHERE id = $1`,
		p.ID, p.Name, p.Description, coverArg(p.CoverItem), p.IsPublic,
	); err != nil {
		return fmt.Errorf("abs_playlist_store: update: %w", err)
	}
	return nil
}

func (s *ABSPlaylistStore) DeletePlaylist(ctx context.Context, id string) error {
	if _, err := s.Pool.Exec(ctx, `DELETE FROM abs_playlists WHERE id = $1`, id); err != nil {
		return fmt.Errorf("abs_playlist_store: delete: %w", err)
	}
	return nil
}

func (s *ABSPlaylistStore) ListPlaylistItems(ctx context.Context, playlistID string) ([]abs.PlaylistItem, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT playlist_id, library_item_id, episode_id, position, added_at
		FROM abs_playlist_items
		WHERE playlist_id = $1
		ORDER BY position ASC`, playlistID)
	if err != nil {
		return nil, fmt.Errorf("abs_playlist_store: list-items: %w", err)
	}
	defer rows.Close()
	out := make([]abs.PlaylistItem, 0)
	for rows.Next() {
		var it abs.PlaylistItem
		if err := rows.Scan(&it.PlaylistID, &it.LibraryItemID, &it.EpisodeID, &it.Position, &it.AddedAt); err != nil {
			return nil, fmt.Errorf("abs_playlist_store: list-items scan: %w", err)
		}
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
	defer tx.Rollback(ctx)
	// Position assignment: MAX(position)+1 inside the INSERT, one round-trip,
	// no read-before-write race.
	if _, err := tx.Exec(ctx, `
		INSERT INTO abs_playlist_items (playlist_id, library_item_id, episode_id, position)
		SELECT $1, $2, $3, COALESCE(MAX(position), 0) + 1
		  FROM abs_playlist_items WHERE playlist_id = $1
		ON CONFLICT (playlist_id, library_item_id, episode_id) DO NOTHING`,
		playlistID, libraryItemID, episodeID,
	); err != nil {
		return fmt.Errorf("abs_playlist_store: add-item: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE abs_playlists SET updated_at = now() WHERE id = $1`, playlistID); err != nil {
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
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		DELETE FROM abs_playlist_items
		WHERE playlist_id = $1 AND library_item_id = $2 AND episode_id = $3`,
		playlistID, libraryItemID, episodeID,
	); err != nil {
		return fmt.Errorf("abs_playlist_store: remove-item: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE abs_playlists SET updated_at = now() WHERE id = $1`, playlistID); err != nil {
		return fmt.Errorf("abs_playlist_store: bump-parent: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("abs_playlist_store: commit: %w", err)
	}
	return nil
}
