package translation

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// LocalizationStore reads existing localizations (for skip-if-filled) and
// writes AI translations with provenance enforced by the underlying repos.
type LocalizationStore interface {
	ItemLocalization(ctx context.Context, contentID, language string) (*models.MediaItemLocalization, error)
	SeasonLocalizations(ctx context.Context, ids []string, language string) (map[string]*models.SeasonLocalization, error)
	EpisodeLocalizations(ctx context.Context, ids []string, language string) (map[string]*models.EpisodeLocalization, error)
	// The AI writes take the base text each translation was made from, and
	// write nothing once it has changed; and the job's force flag: a plain job
	// fills only empty fields, a forced one also replaces earlier AI text.
	UpsertItemAI(ctx context.Context, contentID, language string, overview, tagline *string, fromOverview, fromTagline string, force bool) error
	UpsertSeasonAI(ctx context.Context, contentID, language, overview, from string, force bool) error
	UpsertEpisodeAI(ctx context.Context, contentID, language, overview, from string, force bool) error
}

// CatalogLocalizationStore adapts the catalog localization repositories to
// LocalizationStore.
type CatalogLocalizationStore struct {
	Items    *catalog.MediaItemLocalizationRepository
	Seasons  *catalog.SeasonLocalizationRepository
	Episodes *catalog.EpisodeLocalizationRepository
}

func (s *CatalogLocalizationStore) ItemLocalization(ctx context.Context, contentID, language string) (*models.MediaItemLocalization, error) {
	return s.Items.Get(ctx, contentID, language)
}

func (s *CatalogLocalizationStore) SeasonLocalizations(ctx context.Context, ids []string, language string) (map[string]*models.SeasonLocalization, error) {
	return s.Seasons.GetBySeasonIDs(ctx, ids, language)
}

func (s *CatalogLocalizationStore) EpisodeLocalizations(ctx context.Context, ids []string, language string) (map[string]*models.EpisodeLocalization, error) {
	return s.Episodes.GetByEpisodeIDs(ctx, ids, language)
}

func (s *CatalogLocalizationStore) UpsertItemAI(ctx context.Context, contentID, language string, overview, tagline *string, fromOverview, fromTagline string, force bool) error {
	return s.Items.UpsertAITranslation(ctx, contentID, language, overview, tagline, fromOverview, fromTagline, force)
}

func (s *CatalogLocalizationStore) UpsertSeasonAI(ctx context.Context, contentID, language, overview, from string, force bool) error {
	return s.Seasons.UpsertAIOverview(ctx, contentID, language, overview, from, force)
}

func (s *CatalogLocalizationStore) UpsertEpisodeAI(ctx context.Context, contentID, language, overview, from string, force bool) error {
	return s.Episodes.UpsertAIOverview(ctx, contentID, language, overview, from, force)
}
