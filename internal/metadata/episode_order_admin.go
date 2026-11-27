package metadata

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/contentid"
	"github.com/Silo-Server/silo-server/internal/models"
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
	// ErrResetReorderedChild refuses Reset to Provider for one season or
	// episode of a series shown in another order than its provider's
	// default; resetting the whole series works.
	ErrResetReorderedChild = errors.New("reset to provider: this series uses its own episode order, so reset the whole series instead of one season or episode")
)

// Validate checks the settings' vocabulary.
func (o EpisodeOrderSettings) Validate() error {
	switch {
	case !models.ValidEpisodeOrderProvider(o.Provider):
		return fmt.Errorf("%w: provider must be tvdb, tmdb or empty", ErrEpisodeOrderInvalid)
	case !models.ValidEpisodeOrderProvider(o.FileOrder):
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

// episodeOrderListerProvider is a provider that lists a series' episode orders.
type episodeOrderListerProvider interface {
	Provider
	ListEpisodeOrders(ctx context.Context, providerIDs map[string]string, language string) ([]EpisodeOrder, error)
}

// requireSeries loads the item an episode order operation names, which must
// be a series.
func (s *MetadataService) requireSeries(ctx context.Context, seriesID string) (*models.MediaItem, error) {
	item, err := s.itemRepo.GetByID(ctx, seriesID)
	if err != nil {
		return nil, err
	}
	if item.Type != anchoredItemTypeSeries {
		return nil, ErrEpisodeOrderNotSeries
	}
	return item, nil
}

// seriesForEpisodeOrder loads a series with its identity IDs and scopes ctx to
// its libraries' provider chains.
func (s *MetadataService) seriesForEpisodeOrder(ctx context.Context, seriesID string) (context.Context, map[string]string, string, error) {
	item, err := s.requireSeries(ctx, seriesID)
	if err != nil {
		return ctx, nil, "", err
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
		if slug != contentid.ProviderTVDB && slug != contentid.ProviderTMDB {
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
	if _, err := s.requireSeries(ctx, seriesID); err != nil {
		return 0, err
	}
	if settings.Layout == episodeLayoutCustom {
		// Seasons are arranged in the arranger (SaveCustomSeasons); this
		// switches back to an arrangement already stored, on the provider its
		// episode keys belong to (any of that provider's orders shares them).
		base, arranged, err := s.customLayoutBase(ctx, seriesID)
		switch {
		case err != nil:
			return 0, err
		case !arranged:
			return 0, fmt.Errorf("%w: arrange the seasons first", ErrEpisodeOrderInvalid)
		case settings.Provider == "":
			settings.Provider = base.Provider
		case settings.Provider != base.Provider:
			return 0, fmt.Errorf("%w: the seasons were arranged from %s episodes", ErrEpisodeOrderInvalid, base.Provider)
		}
	}
	orderName := s.episodeOrderName(ctx, seriesID, settings)
	var saved int64
	err := pgx.BeginFunc(ctx, s.dbPool, func(tx pgx.Tx) error {
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
	order := episodeOrderSettings{EpisodeOrderSettings: settings}
	if order.Provider == "" {
		if order.Provider, err = s.seriesEpisodeOrderProvider(ctx, seriesID); err != nil {
			return preview, err
		}
	}
	structures, err := s.fetchRemoteStructures(ctx, ids, language, order)
	if err != nil {
		return preview, err
	}
	seasons, episodes, structureSlug := mergeSeriesStructure(structures.list, structures.episodeChain, order.Provider, language)
	preview.StructureProvider = structureSlug
	var files []seriesFileLink
	if s.dbPool != nil {
		if files, err = s.loadSeriesFileLinks(ctx, s.dbPool, seriesID); err != nil {
			return preview, err
		}
	}
	if structureSlug != "" {
		preview.FileOrderFits = fileOrderFits(files, episodes, structureSlug, structures.list)
		if order.Layout == episodeLayoutCustom {
			if order.Custom, err = s.previewCustomLayout(ctx, seriesID, seasons, episodes, structureSlug); err != nil {
				return preview, err
			}
		}
		seasons, episodes, _ = applyEpisodeOrder(order, seasons, episodes, structureSlug, structures.list)
	}
	fillPreviewSeasons(&preview, files, seasons, episodes)
	return preview, nil
}

// fileOrderFits reports how many of files each file order other than the
// structure provider's own would place. File numbers do not depend on the
// layout, so each file order is tried on the order's own numbering.
func fileOrderFits(files []seriesFileLink, episodes []EpisodeResult, structureSlug string, providers []providerStructure) []EpisodeOrderFileFit {
	var fits []EpisodeOrderFileFit
	for _, fileOrder := range []string{"", contentid.ProviderTVDB, contentid.ProviderTMDB} {
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

// previewCustomLayout is the arrangement a custom layout preview shows: the
// series' stored one, or the order's own seasons when none is stored.
func (s *MetadataService) previewCustomLayout(ctx context.Context, seriesID string, seasons []SeasonResult, episodes []EpisodeResult, structureSlug string) (*customLayout, error) {
	if s.dbPool != nil {
		custom, ok, err := loadCustomLayout(ctx, s.dbPool, seriesID)
		if err != nil || ok {
			return &custom, err
		}
	}
	custom := startingLayout(seasons, episodes, structureSlug)
	return &custom, nil
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
		return seasonBefore(preview.Seasons[i].Number, preview.Seasons[j].Number)
	})
}

// fetchRemoteStructures asks every remote season and episode provider of the
// scoped chain for the series' seasons and episodes, the order's provider in
// the chosen order. A provider that does not know the series is left out; one
// that fails is marked failed (mergeSeriesStructure).
func (s *MetadataService) fetchRemoteStructures(ctx context.Context, ids map[string]string, language string, order episodeOrderSettings) (*providerStructures, error) {
	remote := func(chain []Provider) []EpisodeProvider {
		var out []EpisodeProvider
		for _, p := range chain {
			ep, ok := p.(EpisodeProvider)
			if _, local := p.(IdentityHintProvider); ok && !local {
				out = append(out, ep)
			}
		}
		return out
	}
	seasonChain, err := s.resolveChainCached(ctx, 0, "season")
	if err != nil {
		return nil, err
	}
	episodeChain, err := s.resolveChainCached(ctx, 0, "episode")
	if err != nil {
		return nil, err
	}

	// Every provider call is independent, so they run at once (a few at a
	// time); results land in their own slots and merge in chain and season
	// order afterwards, as a sequential fetch would have merged them.
	type answer struct {
		seasons  []SeasonResult
		episodes []EpisodeResult
		err      error
	}
	run := func(calls []func() answer) []answer {
		out := make([]answer, len(calls))
		sem := make(chan struct{}, 6)
		var wg sync.WaitGroup
		for i, call := range calls {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer func() { <-sem; wg.Done() }()
				out[i] = call()
			}()
		}
		wg.Wait()
		return out
	}
	record := func(structures *providerStructures, slug string, a answer) {
		structure := structures.of(slug, false)
		if a.err != nil {
			structure.failed = structure.failed || !isProvider404(a.err)
			return
		}
		structure.seasons = append(structure.seasons, a.seasons...)
		structure.episodes = append(structure.episodes, a.episodes...)
	}

	structures := newProviderStructures()
	seasonProviders := remote(seasonChain)
	seasonCalls := make([]func() answer, len(seasonProviders))
	for i, ep := range seasonProviders {
		seasonCalls[i] = func() answer {
			seasons, err := ep.GetSeasons(ctx, SeasonsRequest{
				ProviderIDs: ids, ContentType: anchoredItemTypeSeries, Language: language, Ordering: order.orderingFor(ep.Slug()),
			})
			return answer{seasons: seasons, err: err}
		}
	}
	for i, a := range run(seasonCalls) {
		record(structures, seasonProviders[i].Slug(), a)
	}

	episodeProviders := remote(episodeChain)
	for _, ep := range episodeProviders {
		structures.episodeChain = append(structures.episodeChain, ep.Slug())
	}
	numbers := structures.seasonNumbers()
	episodeCalls := make([]func() answer, 0, len(numbers)*len(episodeProviders))
	for _, seasonNumber := range numbers {
		for _, ep := range episodeProviders {
			episodeCalls = append(episodeCalls, func() answer {
				episodes, err := ep.GetEpisodes(ctx, EpisodesRequest{
					ProviderIDs: ids, SeasonNumber: seasonNumber, Language: language, Ordering: order.orderingFor(ep.Slug()),
				})
				return answer{episodes: episodes, err: err}
			})
		}
	}
	for i, a := range run(episodeCalls) {
		record(structures, episodeProviders[i%len(episodeProviders)].Slug(), a)
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
	ctx, ids, language, err := s.seriesForEpisodeOrder(ctx, seriesID)
	if err != nil {
		return settings.OrderID
	}
	chain, err := s.resolveChainCached(ctx, 0, "episode")
	if err != nil {
		return settings.OrderID
	}
	for _, p := range chain {
		lister, ok := p.(episodeOrderListerProvider)
		if !ok || p.Slug() != settings.Provider {
			continue
		}
		orders, err := lister.ListEpisodeOrders(ctx, ids, language)
		if err != nil {
			break
		}
		for _, order := range orders {
			if order.ID == settings.OrderID {
				return order.Name
			}
		}
	}
	return settings.OrderID
}
