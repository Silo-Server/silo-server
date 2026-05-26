package handlers

import (
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
}

// Authorize returns the media file when the caller may access it, or catalog.ErrItemNotFound.
func (a *MediaFileAuthorizer) Authorize(r *http.Request, fileID int) (*models.MediaFile, error) {
	if a == nil || a.FileResolver == nil || a.ItemAccess == nil {
		return nil, fmt.Errorf("media file authorization dependencies not configured")
	}

	file, err := a.FileResolver.GetByID(r.Context(), fileID)
	if err != nil {
		return nil, mapMediaFileLookupError(err)
	}
	if file == nil || file.MissingSince != nil {
		return nil, catalog.ErrItemNotFound
	}

	filter := requestAccessFilter(r)
	switch {
	case file.EpisodeID != "":
		if a.EpisodeLookup == nil {
			return nil, fmt.Errorf("episode lookup not configured")
		}
		episode, err := a.EpisodeLookup.GetByID(r.Context(), file.EpisodeID)
		if err != nil {
			return nil, err
		}
		if episode == nil {
			return nil, catalog.ErrEpisodeNotFound
		}
		if err := a.ItemAccess.EnsureAccessible(r.Context(), episode.SeriesID, filter); err != nil {
			return nil, err
		}
	case file.ContentID != "":
		if err := a.ItemAccess.EnsureAccessible(r.Context(), file.ContentID, filter); err != nil {
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
