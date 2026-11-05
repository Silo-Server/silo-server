package handlers

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata/translation"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
)

type metadataAIItemAccess interface {
	GetByID(ctx context.Context, contentID string) (*models.MediaItem, error)
	EnsureAccessible(ctx context.Context, contentID string, filter catalog.AccessFilter) error
}

type metadataAISeasonLookup interface {
	GetByID(ctx context.Context, contentID string) (*models.Season, error)
}

type metadataAIEpisodeLookup interface {
	GetByID(ctx context.Context, contentID string) (*models.Episode, error)
}

// metadataAIPendingLanguage reports the language a viewer's detail page is
// missing for an item, season, or episode (the detail document's
// pending_translation_language); *catalog.DetailService implements it.
type metadataAIPendingLanguage interface {
	PendingTranslationLanguageChecked(ctx context.Context, item *models.MediaItem, filter catalog.AccessFilter) (string, error)
	PendingSeasonTranslationLanguageChecked(ctx context.Context, season *models.Season, filter catalog.AccessFilter) (string, error)
	PendingEpisodeTranslationLanguageChecked(ctx context.Context, episode *models.Episode, filter catalog.AccessFilter) (string, error)
}

type metadataAITarget struct {
	kind            translation.TargetKind
	accessContentID string
	item            *models.MediaItem
	season          *models.Season
	episode         *models.Episode
}

// metadataAIOnViewRate bounds viewer-triggered translation requests per
// account. Auto mode sends one per detail view of an untranslated item, so a
// person browsing stays well inside it; a script looping over the catalog
// does not.
var metadataAIOnViewRate = ratelimit.Rate{
	RequestsPerSecond: 5,
	RequestsPerMinute: 30,
	Burst:             10,
}

func metadataAIOnViewLimiterKey(userID int) string {
	return "metadata-translate-on-view:" + strconv.Itoa(userID)
}

// MetadataAIHandler exposes AI translation of catalog descriptions into the
// localization tables. The admin routes are mounted under the per-item
// metadata curation guard; the on-view route is viewer-facing and enforces
// item access itself.
type MetadataAIHandler struct {
	service *translation.Service
	// ItemAccess authorizes the viewer-facing on-view route; nil disables it.
	ItemAccess metadataAIItemAccess
	// SeasonLookup and EpisodeLookup let the viewer route resolve non-item
	// detail pages to the parent series for authorization.
	SeasonLookup  metadataAISeasonLookup
	EpisodeLookup metadataAIEpisodeLookup
	// Libraries resolves library prewarm targets; nil leaves them unavailable.
	Libraries metadataAILibraryLookup
	// Pending limits the on-view route to the language the viewer's own detail
	// page reports missing, so a caller cannot queue arbitrary languages.
	Pending metadataAIPendingLanguage
	// ItemLibraries lists the libraries of the requested item, or of the
	// requested episode itself. A detail page opened inside a library resolves the language
	// from that library when the profile sets none, so the check accepts the
	// language any of the viewer's libraries of the item reports.
	ItemLibraries metadataAIItemLibraries
	// Limiter bounds on-view requests per account; the router passes a
	// Redis limiter whenever Redis is configured, so every node shares one
	// budget.
	Limiter ratelimit.RateLimiter
}

// NewMetadataAIHandler creates a handler backed by the given service.
func NewMetadataAIHandler(service *translation.Service) *MetadataAIHandler {
	return &MetadataAIHandler{service: service}
}

type translateDescriptionRequest struct {
	// TargetLanguage echoes the detail response's pending_translation_language.
	TargetLanguage string `json:"target_language"`
}

type metadataAIItemLibraries interface {
	LibraryIDsForItem(ctx context.Context, contentID string) ([]int, error)
}

// viewerPendingLanguage is the language a detail page of target reports
// missing that equals requested: the profile-level answer, or else the answer
// of a library-scoped page in one of the viewer's libraries of the item. It
// returns the profile-level answer (possibly "") when none matches, and an
// error when a read fails.
func (h *MetadataAIHandler) viewerPendingLanguage(ctx context.Context, target metadataAITarget, filter catalog.AccessFilter, requested string) (string, error) {
	matches := func(language string) bool {
		return language != "" && strings.EqualFold(strings.TrimSpace(requested), language)
	}
	pending, err := h.pendingLanguage(ctx, target, filter)
	if err != nil {
		return "", err
	}
	if matches(pending) || h.ItemLibraries == nil {
		return pending, nil
	}
	// An episode page in a library is validated against the episode's own
	// memberships, which can differ from its series'; seasons and items use
	// the item the request authorizes.
	membershipID := target.accessContentID
	if target.episode != nil {
		membershipID = target.episode.ContentID
	}
	libraryIDs, err := h.ItemLibraries.LibraryIDsForItem(ctx, membershipID)
	if err != nil {
		return "", err
	}
	// No membership means no library-scoped page resolves the target (an
	// episode without files, say). LibraryScope would read an empty list as
	// the viewer's whole allowlist, so stop here.
	if len(libraryIDs) == 0 {
		return pending, nil
	}
	scope, none := filter.LibraryScope(libraryIDs)
	if none {
		return pending, nil
	}
	if scope == nil {
		scope = libraryIDs
	}
	for _, libraryID := range scope {
		if slices.Contains(filter.DisabledLibraryIDs, libraryID) {
			continue
		}
		scoped := filter
		scoped.PresentationLibraryID = &libraryID
		language, err := h.pendingLanguage(ctx, target, scoped)
		if err != nil {
			return "", err
		}
		if matches(language) {
			return language, nil
		}
	}
	return pending, nil
}

// pendingLanguage is the language the viewer's detail page for target reports
// missing, or "" when nothing is missing.
func (h *MetadataAIHandler) pendingLanguage(ctx context.Context, target metadataAITarget, filter catalog.AccessFilter) (string, error) {
	switch {
	case target.item != nil:
		return h.Pending.PendingTranslationLanguageChecked(ctx, target.item, filter)
	case target.season != nil:
		return h.Pending.PendingSeasonTranslationLanguageChecked(ctx, target.season, filter)
	case target.episode != nil:
		return h.Pending.PendingEpisodeTranslationLanguageChecked(ctx, target.episode, filter)
	}
	return "", nil
}

func (h *MetadataAIHandler) resolveTranslationTarget(ctx context.Context, contentID string) (metadataAITarget, error) {
	if h.ItemAccess == nil {
		return metadataAITarget{}, catalog.ErrItemNotFound
	}

	item, err := h.ItemAccess.GetByID(ctx, contentID)
	switch {
	case err == nil:
		if item == nil {
			return metadataAITarget{}, catalog.ErrItemNotFound
		}
		return metadataAITarget{kind: translation.TargetItem, accessContentID: contentID, item: item}, nil
	case !errors.Is(err, catalog.ErrItemNotFound):
		return metadataAITarget{}, err
	}

	if h.SeasonLookup != nil {
		season, err := h.SeasonLookup.GetByID(ctx, contentID)
		switch {
		case err == nil:
			if season == nil {
				return metadataAITarget{}, catalog.ErrItemNotFound
			}
			return metadataAITarget{kind: translation.TargetSeason, accessContentID: season.SeriesID, season: season}, nil
		case !errors.Is(err, catalog.ErrSeasonNotFound):
			return metadataAITarget{}, err
		}
	}

	if h.EpisodeLookup != nil {
		episode, err := h.EpisodeLookup.GetByID(ctx, contentID)
		switch {
		case err == nil:
			if episode == nil {
				return metadataAITarget{}, catalog.ErrItemNotFound
			}
			return metadataAITarget{kind: translation.TargetEpisode, accessContentID: episode.SeriesID, episode: episode}, nil
		case !errors.Is(err, catalog.ErrEpisodeNotFound):
			return metadataAITarget{}, err
		}
	}

	return metadataAITarget{}, catalog.ErrItemNotFound
}

type TranslateMetadataRequest struct {
	TargetLanguage  string `json:"target_language"`
	IncludeChildren *bool  `json:"include_children"` // default true
	Force           bool   `json:"force"`
}
