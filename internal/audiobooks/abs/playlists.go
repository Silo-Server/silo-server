package abs

import (
	"context"
	"time"
)

// PlaylistStore is the narrow slice of user_personal_collections
// (collection_type='playlist') and user_personal_collection_items the
// playlists handlers need. Implemented by ABSPlaylistStore in
// internal/audiobooks/abs_playlist_store.go; post-migration-156 it reads
// the unified canonical tables.
type PlaylistStore interface {
	ListUserPlaylists(ctx context.Context, userID, profileID string) ([]Playlist, error)
	GetPlaylist(ctx context.Context, id string) (Playlist, error)
	CreatePlaylist(ctx context.Context, p Playlist) error
	UpdatePlaylist(ctx context.Context, p Playlist) error
	DeletePlaylist(ctx context.Context, id string) error
	ListPlaylistItems(ctx context.Context, playlistID string) ([]PlaylistItem, error)
	AddPlaylistItem(ctx context.Context, playlistID, libraryItemID, episodeID string) error
	RemovePlaylistItem(ctx context.Context, playlistID, libraryItemID, episodeID string) error
}

// Playlist is the in-memory representation of a user_personal_collections
// row with collection_type='playlist'.
type Playlist struct {
	ID          string
	UserID      string
	ProfileID   string
	Name        string
	Description string
	CoverItem   string // empty when unset
	IsPublic    bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// PlaylistItem is the in-memory representation of a
// user_personal_collection_items row scoped to a playlist (sub_item_id
// may be non-empty for podcast-episode entries).
type PlaylistItem struct {
	PlaylistID    string
	LibraryItemID string
	EpisodeID     string // empty for audiobook items
	Position      int
	AddedAt       time.Time
}

// playlistToABS shapes a Playlist in the ABS wire format. When items
// is nil the list-shape is emitted (no "items" key); when items is
// non-nil (possibly empty) the full-shape is emitted.
//
// coverPath is omitted when CoverItem is empty (matches continuum).
// Description is always present (round-tripped from storage).
func playlistToABS(p Playlist, items []map[string]any) map[string]any {
	out := map[string]any{
		"id":          p.ID,
		"userId":      p.UserID,
		"name":        p.Name,
		"description": p.Description,
		"isPublic":    p.IsPublic,
		"createdAt":   p.CreatedAt.UnixMilli(),
		"lastUpdate":  p.UpdatedAt.UnixMilli(),
	}
	if p.CoverItem != "" {
		out["coverPath"] = p.CoverItem
	}
	if items != nil {
		out["items"] = items
	}
	return out
}
