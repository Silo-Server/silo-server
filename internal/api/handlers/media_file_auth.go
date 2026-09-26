package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// MediaFileAuthorizer validates that the authenticated user can access a media file.
type MediaFileAuthorizer struct {
	FileResolver  FilePathResolver
	ItemAccess    PlaybackItemAccessChecker
	EpisodeLookup PlaybackEpisodeLookup
	ExtraLookup   PlaybackExtraLookup
}

// Authorize returns the media file when the caller may access it, or catalog.ErrItemNotFound.
func (a *MediaFileAuthorizer) Authorize(r *http.Request, fileID int) (*models.MediaFile, error) {
	return a.AuthorizeContext(r.Context(), fileID, requestAccessFilter(r))
}

// AuthorizeContext applies the same file and parent access policy without HTTP transport.
func (a *MediaFileAuthorizer) AuthorizeContext(ctx context.Context, fileID int, filter catalog.AccessFilter) (*models.MediaFile, error) {
	if a == nil || a.FileResolver == nil || a.ItemAccess == nil {
		return nil, fmt.Errorf("media file authorization dependencies not configured")
	}

	file, err := a.FileResolver.GetByID(ctx, fileID)
	if err != nil {
		return nil, mapMediaFileLookupError(err)
	}
	if file == nil || file.MissingSince != nil {
		return nil, catalog.ErrItemNotFound
	}

	switch {
	case file.EpisodeID != "":
		if a.EpisodeLookup == nil {
			return nil, fmt.Errorf("episode lookup not configured")
		}
		episode, err := a.EpisodeLookup.GetByID(ctx, file.EpisodeID)
		if err != nil {
			return nil, err
		}
		if episode == nil {
			return nil, catalog.ErrEpisodeNotFound
		}
		if err := a.ItemAccess.EnsureAccessible(ctx, episode.SeriesID, filter); err != nil {
			return nil, err
		}
	case file.ContentID != "":
		if err := a.ItemAccess.EnsureAccessible(ctx, file.ContentID, filter); err != nil {
			return nil, err
		}
	case file.ExtraID != "":
		// Local extras authorize through their parent item, like episodes
		// authorize through their series.
		if a.ExtraLookup == nil {
			return nil, fmt.Errorf("extra lookup not configured")
		}
		extra, err := a.ExtraLookup.GetByID(ctx, file.ExtraID)
		if err != nil {
			if errors.Is(err, catalog.ErrExtraNotFound) {
				return nil, catalog.ErrItemNotFound
			}
			return nil, err
		}
		if extra == nil {
			return nil, catalog.ErrItemNotFound
		}
		if err := a.ItemAccess.EnsureAccessible(ctx, extra.ParentID, filter); err != nil {
			return nil, err
		}
	default:
		return nil, catalog.ErrItemNotFound
	}

	if !catalog.FileAllowedByAccess(file, filter) {
		return nil, catalog.ErrItemNotFound
	}

	return file, nil
}

func mapMediaFileLookupError(err error) error {
	if errors.Is(err, scanner.ErrFileNotFound) {
		return catalog.ErrItemNotFound
	}
	return err
}
