package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// MediaFileAuthorizer validates that the authenticated user can access a media file.
type MediaFileAuthorizer struct {
	FileResolver          FilePathResolver
	ItemAccess            PlaybackItemAccessChecker
	EpisodeLookup         PlaybackEpisodeLookup
	ItemLookup            MediaItemLookup
	ConsumptionAuthorizer auth.Authorizer
}

type MediaItemLookup interface {
	GetByID(ctx context.Context, contentID string) (*models.MediaItem, error)
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
	resourceID := file.ContentID
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
		resourceID = episode.SeriesID
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
	contentRating, err := lookupMediaContentRating(r.Context(), a.ItemLookup, resourceID)
	if err != nil {
		return nil, err
	}
	if err := authorizeMediaPlaybackAction(r, a.ConsumptionAuthorizer, file, resourceID, auth.ActionPlaybackPlay, "", contentRating); err != nil {
		return nil, err
	}

	return file, nil
}

var errMediaConsumptionForbidden = errors.New("media consumption forbidden")

func authorizeMediaConsumption(r *http.Request, authorizer auth.Authorizer, file *models.MediaFile, resourceID string) error {
	return authorizeMediaPlaybackAction(r, authorizer, file, resourceID, auth.ActionPlaybackPlay, "", "")
}

func authorizeMediaPlaybackAction(
	r *http.Request,
	authorizer auth.Authorizer,
	file *models.MediaFile,
	resourceID string,
	action auth.ACLAction,
	playbackQuality string,
	contentRating string,
) error {
	if authorizer == nil {
		return nil
	}
	if file == nil {
		return catalog.ErrItemNotFound
	}
	if resourceID == "" {
		return catalog.ErrItemNotFound
	}
	libraryIDs := []int(nil)
	if file.MediaFolderID > 0 {
		libraryIDs = []int{file.MediaFolderID}
	}
	err := authorizeACLAction(r.Context(), authorizer, auth.AccessRequest{
		UserID:          apimw.GetUserID(r.Context()),
		ProfileID:       apimw.GetProfileID(r.Context()),
		Action:          action,
		ResourceType:    auth.ResourceMediaItem,
		ResourceID:      resourceID,
		LibraryIDs:      libraryIDs,
		MediaType:       file.BaseType,
		PlaybackQuality: mediaPlaybackQuality(file, playbackQuality),
		ContentRating:   strings.TrimSpace(contentRating),
	})
	if errors.Is(err, errACLActionDenied) {
		return errMediaConsumptionForbidden
	}
	return err
}

func mediaPlaybackQuality(file *models.MediaFile, requestedQuality string) string {
	if quality := strings.TrimSpace(requestedQuality); quality != "" {
		return quality
	}
	if file == nil {
		return ""
	}
	return strings.TrimSpace(file.Resolution)
}

func lookupMediaContentRating(ctx context.Context, lookup MediaItemLookup, resourceID string) (string, error) {
	if lookup == nil || strings.TrimSpace(resourceID) == "" {
		return "", nil
	}
	item, err := lookup.GetByID(ctx, resourceID)
	if err != nil {
		return "", err
	}
	if item == nil {
		return "", nil
	}
	return strings.TrimSpace(item.ContentRating), nil
}

func mapMediaFileLookupError(err error) error {
	if errors.Is(err, scanner.ErrFileNotFound) {
		return catalog.ErrItemNotFound
	}
	return err
}
