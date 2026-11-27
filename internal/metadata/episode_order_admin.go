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
	provider := func(v string) bool { return v == "" || v == providerSlugTVDB || v == providerSlugTMDB }
	switch {
	case !provider(o.Provider):
		return fmt.Errorf("%w: provider must be tvdb, tmdb or empty", ErrEpisodeOrderInvalid)
	case !provider(o.FileOrder):
		return fmt.Errorf("%w: file_order must be tvdb, tmdb or empty", ErrEpisodeOrderInvalid)
	case o.Layout != episodeLayoutSeasons && o.Layout != episodeLayoutAbsolute && o.Layout != episodeLayoutCustom:
		return fmt.Errorf("%w: layout must be seasons, absolute or custom", ErrEpisodeOrderInvalid)
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
	// OrderName names Settings.OrderID as the provider did when it was saved.
	OrderName string
	Revision  int64
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
	Episodes     []EpisodeOrderPreviewEpisode
}

// EpisodeOrderPreviewEpisode is one episode of a previewed season.
type EpisodeOrderPreviewEpisode struct {
	Number int
	Title  string
	// File names the file the episode would link to; empty when none.
	File string
	// FileSeason and FileEpisode are the numbers a file name gives the
	// episode in the file order; zero when the file order lacks it.
	FileSeason, FileEpisode int
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
	// FileOrderFits counts the files each file order would place, for the
	// file orders that read differently: empty (the show's provider), tvdb
	// and tmdb.
	FileOrderFits []EpisodeOrderFileFit
}

// EpisodeOrderFileFit is how many files one file order places.
type EpisodeOrderFileFit struct {
	FileOrder    string
	MatchedFiles int
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
	if item.Type != anchoredItemTypeSeries {
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

// EpisodeOrderState returns the series' episode order settings and, with
// withOrders, every remote episode provider's orders for it.
func (s *MetadataService) EpisodeOrderState(ctx context.Context, seriesID string, withOrders bool) (EpisodeOrderState, error) {
	state := EpisodeOrderState{Settings: EpisodeOrderSettings{Layout: episodeLayoutSeasons}}
	ctx, ids, language, err := s.seriesForEpisodeOrder(ctx, seriesID)
	if err != nil {
		return state, err
	}
	if s.dbPool != nil {
		err := s.dbPool.QueryRow(ctx, `
			SELECT provider, order_id, order_name, file_order, layout, revision
			FROM series_episode_orders WHERE series_id = $1`, seriesID).
			Scan(&state.Settings.Provider, &state.Settings.OrderID, &state.OrderName, &state.Settings.FileOrder, &state.Settings.Layout, &state.Revision)
		switch {
		case err == nil:
			state.Override = !state.Settings.isDefault()
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
		if slug != providerSlugTVDB && slug != providerSlugTMDB {
			continue
		}
		if state.LibraryProvider == "" {
			state.LibraryProvider = slug
		}
		entry := EpisodeOrderProviderOrders{Provider: slug}
		if lister, ok := p.(episodeOrderListerProvider); ok && withOrders {
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
// Default settings make the series follow its library again. A non-nil revision must match the stored one (zero when the series
// has none), or the save fails with ErrEpisodeOrderConflict.
func (s *MetadataService) SaveEpisodeOrder(ctx context.Context, seriesID string, settings EpisodeOrderSettings, revision *int64, userID int) (int64, error) {
	if err := settings.Validate(); err != nil {
		return 0, err
	}
	item, err := s.itemRepo.GetByID(ctx, seriesID)
	if err != nil {
		return 0, err
	}
	if item.Type != anchoredItemTypeSeries {
		return 0, ErrEpisodeOrderNotSeries
	}
	if settings.Layout == episodeLayoutCustom {
		// Seasons are arranged in the arranger (SaveCustomSeasons); this
		// switches back to an arrangement already stored.
		if _, ok, err := loadCustomLayout(ctx, s.dbPool, seriesID); err != nil {
			return 0, err
		} else if !ok {
			return 0, fmt.Errorf("%w: arrange the seasons first", ErrEpisodeOrderInvalid)
		}
	}
	orderName := s.episodeOrderName(ctx, seriesID, settings)
	var saved int64
	err = pgx.BeginFunc(ctx, s.dbPool, func(tx pgx.Tx) error {
		var err error
		saved, err = storeSeriesOrder(ctx, tx, seriesID, settings, orderName, revision, userID)
		return err
	})
	if err != nil {
		if errors.Is(err, ErrEpisodeOrderConflict) {
			return 0, err
		}
		return 0, fmt.Errorf("saving the series episode order: %w", err)
	}
	return saved, nil
}

// storeSeriesOrder writes the series' order row inside tx and returns its new
// revision. The series row lock serializes saves, including the first one,
// so two admins saving at once cannot both pass the revision check. A
// non-nil revision must equal the stored one (0 before the first save). The
// row is kept when the settings return to the defaults, so its revision
// never starts over and the custom arrangement stays stored.
func storeSeriesOrder(ctx context.Context, tx pgx.Tx, seriesID string, settings EpisodeOrderSettings, orderName string, revision *int64, userID int) (int64, error) {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM media_items WHERE content_id = $1 FOR UPDATE`, seriesID); err != nil {
		return 0, err
	}
	var stored int64
	if err := tx.QueryRow(ctx, `SELECT revision FROM series_episode_orders WHERE series_id = $1`, seriesID).
		Scan(&stored); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	if revision != nil && *revision != stored {
		return 0, ErrEpisodeOrderConflict
	}
	var updatedBy any
	if userID > 0 {
		updatedBy = userID
	}
	var saved int64
	err := tx.QueryRow(ctx, `
		INSERT INTO series_episode_orders (series_id, provider, order_id, order_name, file_order, layout, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (series_id) DO UPDATE SET
			provider = EXCLUDED.provider, order_id = EXCLUDED.order_id, order_name = EXCLUDED.order_name,
			file_order = EXCLUDED.file_order, layout = EXCLUDED.layout,
			updated_by = EXCLUDED.updated_by, updated_at = now(),
			revision = series_episode_orders.revision + 1
		RETURNING revision`,
		seriesID, settings.Provider, settings.OrderID, orderName, settings.FileOrder, settings.Layout, updatedBy).Scan(&saved)
	return saved, err
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
	var files []seriesFileLink
	if s.dbPool != nil {
		if files, err = s.loadSeriesFileLinks(ctx, s.dbPool, seriesID); err != nil {
			return preview, err
		}
	}
	if structureSlug != "" {
		preview.FileOrderFits = fileOrderFits(files, episodes, structureSlug, structures.list)
		assignFileNumbers(episodes, structureSlug, order.FileOrder, structures.list)
		if seasons, episodes, err = s.applyPreviewLayout(ctx, seriesID, order.Layout, seasons, episodes, structureSlug); err != nil {
			return preview, err
		}
	}
	fillPreviewSeasons(&preview, files, seasons, episodes)
	return preview, nil
}

// fileOrderFits reports how many of files each file order other than the
// structure provider's own would place. File numbers do not depend on the
// layout, so each file order is tried on the order's own numbering.
func fileOrderFits(files []seriesFileLink, episodes []EpisodeResult, structureSlug string, providers []providerStructure) []EpisodeOrderFileFit {
	var fits []EpisodeOrderFileFit
	for _, fileOrder := range []string{"", providerSlugTVDB, providerSlugTMDB} {
		if fileOrder == structureSlug {
			continue
		}
		trial := append([]EpisodeResult(nil), episodes...)
		if !assignFileNumbers(trial, structureSlug, fileOrder, providers) {
			continue
		}
		fits = append(fits, EpisodeOrderFileFit{
			FileOrder: fileOrder, MatchedFiles: countPlacedFiles(files, trial),
		})
	}
	return fits
}

// applyPreviewLayout arranges the order's seasons and episodes in layout. A
// custom layout uses the series' stored arrangement, or the starting one when
// none is stored.
func (s *MetadataService) applyPreviewLayout(ctx context.Context, seriesID, layout string, seasons []SeasonResult, episodes []EpisodeResult, structureSlug string) ([]SeasonResult, []EpisodeResult, error) {
	switch layout {
	case episodeLayoutAbsolute:
		seasons, episodes = applyAbsoluteLayout(seasons, episodes)
	case episodeLayoutCustom:
		custom, ok := customLayout{}, false
		if s.dbPool != nil {
			var err error
			if custom, ok, err = loadCustomLayout(ctx, s.dbPool, seriesID); err != nil {
				return nil, nil, err
			}
		}
		if !ok {
			custom = startingLayout(seasons, episodes, structureSlug)
		}
		seasons, episodes = applyCustomLayout(seasons, episodes, structureSlug, custom)
	}
	return seasons, episodes, nil
}

// fillPreviewSeasons fills preview's file counts and seasons: which of files
// land on which episode, and each season's episodes in number order with
// specials last.
func fillPreviewSeasons(preview *EpisodeOrderPreview, files []seriesFileLink, seasons []SeasonResult, episodes []EpisodeResult) {
	fileSlots := make(map[episodeSlot]int, len(episodes))
	for i, ep := range episodes {
		if ep.FileNumbered {
			fileSlots[episodeSlot{ep.FileSeasonNumber, ep.FileEpisodeNumber}] = i
		}
	}
	filesBySeason := make(map[int]int)
	fileOf := make(map[int]string)
	for _, file := range files {
		if !file.Numbered {
			continue
		}
		preview.FileCount++
		if i, ok := fileSlots[episodeSlot{file.Season, file.Episode}]; ok {
			preview.MatchedFiles++
			filesBySeason[episodes[i].SeasonNumber]++
			if _, seen := fileOf[i]; !seen {
				fileOf[i] = path.Base(file.Path)
			}
			continue
		}
		if len(preview.UnmatchedFiles) < 50 {
			preview.UnmatchedFiles = append(preview.UnmatchedFiles, path.Base(file.Path))
		}
	}

	titles := make(map[int]string, len(seasons))
	for _, season := range seasons {
		titles[season.SeasonNumber] = season.Title
	}
	bySeason := make(map[int][]EpisodeOrderPreviewEpisode)
	for i, ep := range episodes {
		entry := EpisodeOrderPreviewEpisode{Number: ep.EpisodeNumber, Title: ep.Title, File: fileOf[i]}
		if ep.FileNumbered {
			entry.FileSeason, entry.FileEpisode = ep.FileSeasonNumber, ep.FileEpisodeNumber
		}
		bySeason[ep.SeasonNumber] = append(bySeason[ep.SeasonNumber], entry)
	}
	for number, seasonEpisodes := range bySeason {
		title := strings.TrimSpace(titles[number])
		if title == "" {
			title = fallbackSeasonTitle(number)
		}
		sort.Slice(seasonEpisodes, func(i, j int) bool { return seasonEpisodes[i].Number < seasonEpisodes[j].Number })
		preview.Seasons = append(preview.Seasons, EpisodeOrderPreviewSeason{
			Number: number, Title: title, EpisodeCount: len(seasonEpisodes), FileCount: filesBySeason[number],
			Episodes: seasonEpisodes,
		})
		preview.EpisodeCount += len(seasonEpisodes)
	}
	sort.Slice(preview.Seasons, func(i, j int) bool {
		// Specials last, as series pages list them.
		a, b := preview.Seasons[i].Number, preview.Seasons[j].Number
		if (a == 0) != (b == 0) {
			return b == 0
		}
		return a < b
	})
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
			ProviderIDs: ids, ContentType: anchoredItemTypeSeries, Language: language, Ordering: order.orderingFor(p.Slug()),
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

// countPlacedFiles counts the number-named files whose numbers name one of
// episodes in its file order.
func countPlacedFiles(files []seriesFileLink, episodes []EpisodeResult) int {
	slots := make(map[episodeSlot]bool, len(episodes))
	for _, ep := range episodes {
		if ep.FileNumbered {
			slots[episodeSlot{ep.FileSeasonNumber, ep.FileEpisodeNumber}] = true
		}
	}
	placed := 0
	for _, file := range files {
		if file.Numbered && slots[episodeSlot{file.Season, file.Episode}] {
			placed++
		}
	}
	return placed
}

// episodeOrderName asks the order's provider for its name; empty for the
// default order, and the ID when the provider cannot say.
func (s *MetadataService) episodeOrderName(ctx context.Context, seriesID string, settings EpisodeOrderSettings) string {
	if settings.OrderID == "" {
		return ""
	}
	state, err := s.EpisodeOrderState(ctx, seriesID, true)
	if err != nil {
		return settings.OrderID
	}
	for _, p := range state.Providers {
		if p.Provider != settings.Provider {
			continue
		}
		for _, order := range p.Orders {
			if order.ID == settings.OrderID {
				return order.Name
			}
		}
	}
	return settings.OrderID
}
