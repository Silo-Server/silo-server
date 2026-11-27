package metadata

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// EpisodeOrderSettings is a series' episode order as an admin sets it
// (series_episode_orders). Empty Provider follows the library; empty OrderID
// is the provider's default order; empty FileOrder reads file names in the
// show's provider's default order.
type EpisodeOrderSettings struct {
	Provider  string
	OrderID   string
	FileOrder string
	Layout    string
}

// Episode order errors the admin API answers with a client error.
var (
	ErrEpisodeOrderNotSeries = errors.New("episode order: the item is not a series")
	ErrEpisodeOrderInvalid   = errors.New("episode order: invalid settings")
	ErrEpisodeOrderConflict  = errors.New("episode order: changed since it was read")
)

// Validate checks the settings' vocabulary.
func (o EpisodeOrderSettings) Validate() error {
	provider := func(v string) bool { return v == "" || v == "tvdb" || v == "tmdb" }
	switch {
	case !provider(o.Provider):
		return fmt.Errorf("%w: provider must be tvdb, tmdb or empty", ErrEpisodeOrderInvalid)
	case !provider(o.FileOrder):
		return fmt.Errorf("%w: file_order must be tvdb, tmdb or empty", ErrEpisodeOrderInvalid)
	case o.Layout != episodeLayoutSeasons && o.Layout != episodeLayoutAbsolute:
		return fmt.Errorf("%w: layout must be seasons or absolute", ErrEpisodeOrderInvalid)
	case o.OrderID != "" && o.Provider == "":
		return fmt.Errorf("%w: order_id needs a provider", ErrEpisodeOrderInvalid)
	case len(o.OrderID) > 200:
		return fmt.Errorf("%w: order_id is too long", ErrEpisodeOrderInvalid)
	}
	return nil
}

func (o EpisodeOrderSettings) isDefault() bool {
	return o.Provider == "" && o.OrderID == "" && o.FileOrder == "" && o.Layout == episodeLayoutSeasons
}

// EpisodeOrderProviderOrders lists one provider's episode orders for a series.
type EpisodeOrderProviderOrders struct {
	Provider string
	// Supported is false for a provider that serves only its default order.
	Supported bool
	Orders    []EpisodeOrder
	// Failed is set when listing the orders failed.
	Failed bool
}

// EpisodeOrderState is a series' episode order and the choices it has.
type EpisodeOrderState struct {
	// Override is true when the series has its own settings.
	Override bool
	Settings EpisodeOrderSettings
	Revision int64
	// LibraryProvider is the provider the series follows without an
	// override: its library's setting, else the first remote provider of the
	// episode chain.
	LibraryProvider string
	Providers       []EpisodeOrderProviderOrders
}

// EpisodeOrderPreviewSeason is one season of a previewed order.
type EpisodeOrderPreviewSeason struct {
	Number       int
	Title        string
	EpisodeCount int
	FileCount    int
}

// EpisodeOrderPreview is what a series would look like under settings.
type EpisodeOrderPreview struct {
	// StructureProvider numbered the preview: the chosen provider, or the
	// fallback when it has no episodes for the series; empty when no remote
	// provider has any.
	StructureProvider string
	Seasons           []EpisodeOrderPreviewSeason
	EpisodeCount      int
	// FileCount counts the series' files named by season and episode;
	// MatchedFiles those the order places. UnmatchedFiles names the rest (at
	// most 50).
	FileCount      int
	MatchedFiles   int
	UnmatchedFiles []string
}

// episodeOrderLister is a provider that lists a series' episode orders.
type episodeOrderListerProvider interface {
	Provider
	ListEpisodeOrders(ctx context.Context, providerIDs map[string]string, language string) ([]EpisodeOrder, error)
}

// seriesForEpisodeOrder loads a series with its identity IDs and scopes ctx to
// its libraries' provider chains.
func (s *MetadataService) seriesForEpisodeOrder(ctx context.Context, seriesID string) (context.Context, map[string]string, string, error) {
	item, err := s.itemRepo.GetByID(ctx, seriesID)
	if err != nil {
		return ctx, nil, "", err
	}
	if item.Type != "series" {
		return ctx, nil, "", ErrEpisodeOrderNotSeries
	}
	ctx, err = s.withItemChainScope(ctx, seriesID, 0)
	if err != nil {
		return ctx, nil, "", err
	}
	language := item.DefaultMetadataLanguage
	if language == "" {
		language = "en"
	}
	return ctx, storedIdentityProviderIDs(item), language, nil
}

// EpisodeOrderState returns the series' episode order settings and every
// remote episode provider's orders for it.
func (s *MetadataService) EpisodeOrderState(ctx context.Context, seriesID string) (EpisodeOrderState, error) {
	state := EpisodeOrderState{Settings: EpisodeOrderSettings{Layout: episodeLayoutSeasons}}
	ctx, ids, language, err := s.seriesForEpisodeOrder(ctx, seriesID)
	if err != nil {
		return state, err
	}
	if s.dbPool != nil {
		err := s.dbPool.QueryRow(ctx, `
			SELECT provider, order_id, file_order, layout, revision
			FROM series_episode_orders WHERE series_id = $1`, seriesID).
			Scan(&state.Settings.Provider, &state.Settings.OrderID, &state.Settings.FileOrder, &state.Settings.Layout, &state.Revision)
		switch {
		case err == nil:
			state.Override = true
		case !errors.Is(err, pgx.ErrNoRows):
			return state, fmt.Errorf("loading the series episode order: %w", err)
		}
	}
	chain, err := s.resolveChainCached(ctx, 0, "episode")
	if err != nil {
		return state, err
	}
	state.LibraryProvider, err = s.seriesEpisodeOrderProvider(ctx, seriesID)
	if err != nil {
		return state, err
	}
	for _, p := range chain {
		slug := p.Slug()
		if slug != "tvdb" && slug != "tmdb" {
			continue
		}
		if state.LibraryProvider == "" {
			state.LibraryProvider = slug
		}
		entry := EpisodeOrderProviderOrders{Provider: slug}
		if lister, ok := p.(episodeOrderListerProvider); ok {
			orders, err := lister.ListEpisodeOrders(ctx, ids, language)
			switch {
			case errors.Is(err, ErrEpisodeOrdersUnsupported):
			case err != nil:
				entry.Supported, entry.Failed = true, true
			default:
				entry.Supported, entry.Orders = true, orders
			}
		}
		state.Providers = append(state.Providers, entry)
	}
	return state, nil
}

// SaveEpisodeOrder stores the series' settings and returns the new revision.
// Default settings remove the override, so the series follows its library
// again. A non-nil revision must match the stored one (zero when the series
// has none), or the save fails with ErrEpisodeOrderConflict.
func (s *MetadataService) SaveEpisodeOrder(ctx context.Context, seriesID string, settings EpisodeOrderSettings, revision *int64, userID int) (int64, error) {
	if err := settings.Validate(); err != nil {
		return 0, err
	}
	item, err := s.itemRepo.GetByID(ctx, seriesID)
	if err != nil {
		return 0, err
	}
	if item.Type != "series" {
		return 0, ErrEpisodeOrderNotSeries
	}
	var saved int64
	err = pgx.BeginFunc(ctx, s.dbPool, func(tx pgx.Tx) error {
		var stored int64
		if err := tx.QueryRow(ctx, `SELECT revision FROM series_episode_orders WHERE series_id = $1 FOR UPDATE`, seriesID).
			Scan(&stored); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if revision != nil && *revision != stored {
			return ErrEpisodeOrderConflict
		}
		if settings.isDefault() {
			_, err := tx.Exec(ctx, `DELETE FROM series_episode_orders WHERE series_id = $1`, seriesID)
			return err
		}
		var updatedBy any
		if userID > 0 {
			updatedBy = userID
		}
		return tx.QueryRow(ctx, `
			INSERT INTO series_episode_orders (series_id, provider, order_id, file_order, layout, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (series_id) DO UPDATE SET
				provider = EXCLUDED.provider, order_id = EXCLUDED.order_id,
				file_order = EXCLUDED.file_order, layout = EXCLUDED.layout,
				updated_by = EXCLUDED.updated_by, updated_at = now(),
				revision = series_episode_orders.revision + 1
			RETURNING revision`,
			seriesID, settings.Provider, settings.OrderID, settings.FileOrder, settings.Layout, updatedBy).Scan(&saved)
	})
	if err != nil {
		if errors.Is(err, ErrEpisodeOrderConflict) {
			return 0, err
		}
		return 0, fmt.Errorf("saving the series episode order: %w", err)
	}
	return saved, nil
}

// PreviewEpisodeOrder fetches the series' seasons and episodes from its
// remote providers as settings would order them, without storing anything,
// and reports how the series' files would fit.
func (s *MetadataService) PreviewEpisodeOrder(ctx context.Context, seriesID string, settings EpisodeOrderSettings) (EpisodeOrderPreview, error) {
	var preview EpisodeOrderPreview
	if err := settings.Validate(); err != nil {
		return preview, err
	}
	ctx, ids, language, err := s.seriesForEpisodeOrder(ctx, seriesID)
	if err != nil {
		return preview, err
	}
	order := episodeOrderSettings{Provider: settings.Provider, OrderID: settings.OrderID, FileOrder: settings.FileOrder, Layout: settings.Layout}
	if order.Provider == "" {
		if order.Provider, err = s.seriesEpisodeOrderProvider(ctx, seriesID); err != nil {
			return preview, err
		}
	}
	structures, err := s.fetchRemoteStructures(ctx, ids, language, order)
	if err != nil {
		return preview, err
	}
	seasons, episodes, structureSlug := mergeSeriesStructure(structures.list, order.Provider, language)
	preview.StructureProvider = structureSlug
	if structureSlug != "" {
		assignFileNumbers(episodes, structureSlug, order.FileOrder, structures.list)
		if order.Layout == episodeLayoutAbsolute {
			seasons, episodes = applyAbsoluteLayout(seasons, episodes)
		}
	}

	fileSlots := make(map[episodeSlot]int, len(episodes))
	for _, ep := range episodes {
		slot := episodeSlot{ep.SeasonNumber, ep.EpisodeNumber}
		if ep.FileNumbered {
			slot = episodeSlot{ep.FileSeasonNumber, ep.FileEpisodeNumber}
		}
		fileSlots[slot] = ep.SeasonNumber
	}
	filesBySeason := make(map[int]int)
	if s.dbPool != nil {
		files, err := s.loadSeriesFileLinks(ctx, s.dbPool, seriesID)
		if err != nil {
			return preview, err
		}
		for _, file := range files {
			if !file.Numbered {
				continue
			}
			preview.FileCount++
			if season, ok := fileSlots[episodeSlot{file.Season, file.Episode}]; ok {
				preview.MatchedFiles++
				filesBySeason[season]++
				continue
			}
			if len(preview.UnmatchedFiles) < 50 {
				preview.UnmatchedFiles = append(preview.UnmatchedFiles, path.Base(file.Path))
			}
		}
	}

	titles := make(map[int]string, len(seasons))
	for _, season := range seasons {
		titles[season.SeasonNumber] = season.Title
	}
	counts := make(map[int]int)
	for _, ep := range episodes {
		counts[ep.SeasonNumber]++
	}
	for number, count := range counts {
		title := strings.TrimSpace(titles[number])
		if title == "" {
			title = fallbackSeasonTitle(number)
		}
		preview.Seasons = append(preview.Seasons, EpisodeOrderPreviewSeason{
			Number: number, Title: title, EpisodeCount: count, FileCount: filesBySeason[number],
		})
		preview.EpisodeCount += count
	}
	sort.Slice(preview.Seasons, func(i, j int) bool {
		// Specials last, as series pages list them.
		a, b := preview.Seasons[i].Number, preview.Seasons[j].Number
		if (a == 0) != (b == 0) {
			return b == 0
		}
		return a < b
	})
	return preview, nil
}

// fetchRemoteStructures asks every remote season and episode provider of the
// scoped chain for the series' seasons and episodes, the order's provider in
// the chosen order. A provider that does not know the series is left out; one
// that fails is marked failed (mergeSeriesStructure).
func (s *MetadataService) fetchRemoteStructures(ctx context.Context, ids map[string]string, language string, order episodeOrderSettings) (*providerStructures, error) {
	structures := newProviderStructures()
	seasonChain, err := s.resolveChainCached(ctx, 0, "season")
	if err != nil {
		return nil, err
	}
	for _, p := range seasonChain {
		ep, ok := p.(EpisodeProvider)
		if _, local := p.(IdentityHintProvider); !ok || local {
			continue
		}
		seasons, err := ep.GetSeasons(ctx, SeasonsRequest{
			ProviderIDs: ids, ContentType: "series", Language: language, Ordering: order.orderingFor(p.Slug()),
		})
		if err != nil {
			if !isProvider404(err) {
				structures.of(p.Slug(), false).failed = true
			}
			continue
		}
		structures.of(p.Slug(), false).seasons = seasons
	}
	episodeChain, err := s.resolveChainCached(ctx, 0, "episode")
	if err != nil {
		return nil, err
	}
	for _, seasonNumber := range structures.seasonNumbers() {
		for _, p := range episodeChain {
			ep, ok := p.(EpisodeProvider)
			if _, local := p.(IdentityHintProvider); !ok || local {
				continue
			}
			episodes, err := ep.GetEpisodes(ctx, EpisodesRequest{
				ProviderIDs: ids, SeasonNumber: seasonNumber, Language: language, Ordering: order.orderingFor(p.Slug()),
			})
			if err != nil {
				if !isProvider404(err) {
					structures.of(p.Slug(), false).failed = true
				}
				continue
			}
			structure := structures.of(p.Slug(), false)
			structure.episodes = append(structure.episodes, episodes...)
		}
	}
	return structures, nil
}
