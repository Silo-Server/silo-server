package abs

import (
	"context"
	"time"
)

// PlaylistStore is the narrow slice of abs_playlists + abs_playlist_items
// the playlists handlers need. Implemented by ABSPlaylistStore in
// internal/audiobooks/abs_playlist_store.go.
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

// Playlist is the in-memory representation of an abs_playlists row.
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

// PlaylistItem is the in-memory representation of an abs_playlist_items row.
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
