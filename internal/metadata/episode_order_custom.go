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

// loadCustomLayout reads a series' arranged seasons and episode slots.
func loadCustomLayout(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}, seriesID string) (customLayout, bool, error) {
	layout := customLayout{Slots: map[string]episodeSlot{}}
	rows, err := q.Query(ctx, `
		SELECT number, title, overview FROM series_custom_seasons
		WHERE series_id = $1 ORDER BY number`, seriesID)
	if err != nil {
		return layout, false, fmt.Errorf("loading custom seasons: %w", err)
	}
	for rows.Next() {
		var season customSeason
		if err := rows.Scan(&season.Number, &season.Title, &season.Overview); err != nil {
			rows.Close()
			return layout, false, fmt.Errorf("loading custom seasons: %w", err)
		}
		layout.Seasons = append(layout.Seasons, season)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return layout, false, fmt.Errorf("loading custom seasons: %w", err)
	}
	rows, err = q.Query(ctx, `
		SELECT episode_key, season, position FROM series_custom_episode_slots
		WHERE series_id = $1`, seriesID)
	if err != nil {
		return layout, false, fmt.Errorf("loading custom episode slots: %w", err)
	}
	for rows.Next() {
		var key string
		var slot episodeSlot
		if err := rows.Scan(&key, &slot.season, &slot.episode); err != nil {
			rows.Close()
			return layout, false, fmt.Errorf("loading custom episode slots: %w", err)
		}
		layout.Slots[key] = slot
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return layout, false, fmt.Errorf("loading custom episode slots: %w", err)
	}
	return layout, len(layout.Seasons) > 0, nil
}

// CustomSeasonsEpisode is one episode in the arranger.
type CustomSeasonsEpisode struct {
	// Key identifies the episode across refreshes (customEpisodeKey).
	Key   string
	Title string
	// ProviderSeason and ProviderEpisode place it in the base order.
	ProviderSeason, ProviderEpisode int
	AirDate                         string
	// File names the file the episode links to; empty when none.
	File string
	// PlacedAutomatically marks an episode a saved layout did not place.
	PlacedAutomatically bool

	position int
}

// CustomSeasonsSeason is one season in the arranger; Number 0 is Specials.
type CustomSeasonsSeason struct {
	Number int
	// Title and Overview are the admin's; empty takes the provider's.
	Title, Overview                 string
	ProviderTitle, ProviderOverview string
	Episodes                        []CustomSeasonsEpisode
}

// CustomSeasons is a series' arranged seasons, or a starting point taken from
// its base order when it has none.
type CustomSeasons struct {
	Saved    bool
	Revision int64
	// Settings is the base order the arrangement starts from.
	Settings          EpisodeOrderSettings
	StructureProvider string
	Seasons           []CustomSeasonsSeason
}

// CustomSeasonsSave is an arrangement to store: every season in order with its
// episodes' keys in order; Number 0 holds the specials.
type CustomSeasonsSave struct {
	Settings EpisodeOrderSettings
	Seasons  []CustomSeasonsSeason
}

// CustomSeasons returns the series' arrangement. A base other than the
// stored one (base non-nil) starts over from that order's seasons.
func (s *MetadataService) CustomSeasons(ctx context.Context, seriesID string, base *EpisodeOrderSettings) (CustomSeasons, error) {
	var result CustomSeasons
	state, err := s.EpisodeOrderState(ctx, seriesID, false)
	if err != nil {
		return result, err
	}
	result.Revision = state.Revision
	result.Settings = state.Settings
	stored := state.Override && state.Settings.Layout == episodeLayoutCustom
	if base != nil {
		result.Settings = *base
		stored = false
	}
	result.Settings.Layout = episodeLayoutCustom
	if err := result.Settings.Validate(); err != nil {
		return result, err
	}

	ctx, ids, language, err := s.seriesForEpisodeOrder(ctx, seriesID)
	if err != nil {
		return result, err
	}
	order := episodeOrderSettings{Provider: result.Settings.Provider, OrderID: result.Settings.OrderID, FileOrder: result.Settings.FileOrder}
	if order.Provider == "" {
		order.Provider = state.LibraryProvider
	}
	structures, err := s.fetchRemoteStructures(ctx, ids, language, order)
	if err != nil {
		return result, err
	}
	seasons, episodes, structureSlug := mergeSeriesStructure(structures.list, order.Provider, language)
	result.StructureProvider = structureSlug
	if structureSlug == "" {
		return result, nil
	}
	assignFileNumbers(episodes, structureSlug, order.FileOrder, structures.list)
	inOrder := append([]EpisodeResult(nil), episodes...)

	var layout customLayout
	if stored && s.dbPool != nil {
		if layout, result.Saved, err = loadCustomLayout(ctx, s.dbPool, seriesID); err != nil {
			return result, err
		}
	}
	if !result.Saved {
		layout = startingLayout(seasons, episodes, structureSlug)
	}
	arrangedSeasons, arranged := applyCustomLayout(seasons, episodes, structureSlug, layout)

	files := map[episodeSlot]string{}
	if s.dbPool != nil {
		links, err := s.loadSeriesFileLinks(ctx, s.dbPool, seriesID)
		if err != nil {
			return result, err
		}
		for _, link := range links {
			if link.Numbered {
				if _, ok := files[episodeSlot{link.Season, link.Episode}]; !ok {
					files[episodeSlot{link.Season, link.Episode}] = path.Base(link.Path)
				}
			}
		}
	}
	provider := map[int]SeasonResult{}
	for _, season := range seasons {
		provider[season.SeasonNumber] = season
	}
	custom := map[int]customSeason{}
	for _, season := range layout.Seasons {
		custom[season.Number] = season
	}
	byNumber := map[int]*CustomSeasonsSeason{}
	for _, season := range arrangedSeasons {
		entry := &CustomSeasonsSeason{
			Number:           season.SeasonNumber,
			Title:            custom[season.SeasonNumber].Title,
			Overview:         custom[season.SeasonNumber].Overview,
			ProviderTitle:    strings.TrimSpace(provider[season.SeasonNumber].Title),
			ProviderOverview: provider[season.SeasonNumber].Overview,
		}
		if entry.ProviderTitle == "" {
			entry.ProviderTitle = fallbackSeasonTitle(season.SeasonNumber)
		}
		byNumber[season.SeasonNumber] = entry
	}
	for i, ep := range arranged {
		key := customEpisodeKey(ep, structureSlug)
		fileSlot := episodeSlot{inOrder[i].SeasonNumber, inOrder[i].EpisodeNumber}
		if inOrder[i].FileNumbered {
			fileSlot = episodeSlot{inOrder[i].FileSeasonNumber, inOrder[i].FileEpisodeNumber}
		}
		_, inLayout := layout.Slots[key]
		season := byNumber[ep.SeasonNumber]
		if season == nil {
			season = &CustomSeasonsSeason{Number: ep.SeasonNumber, ProviderTitle: fallbackSeasonTitle(ep.SeasonNumber)}
			byNumber[ep.SeasonNumber] = season
		}
		season.Episodes = append(season.Episodes, CustomSeasonsEpisode{
			Key: key, Title: ep.Title, AirDate: ep.AirDate,
			ProviderSeason: inOrder[i].SeasonNumber, ProviderEpisode: inOrder[i].EpisodeNumber,
			File: files[fileSlot], PlacedAutomatically: result.Saved && !inLayout,
			position: ep.EpisodeNumber,
		})
	}
	for _, season := range byNumber {
		sort.SliceStable(season.Episodes, func(i, j int) bool { return season.Episodes[i].position < season.Episodes[j].position })
	}
	for _, season := range byNumber {
		result.Seasons = append(result.Seasons, *season)
	}
	sort.Slice(result.Seasons, func(i, j int) bool {
		a, b := result.Seasons[i].Number, result.Seasons[j].Number
		if (a == 0) != (b == 0) {
			return b == 0
		}
		return a < b
	})
	return result, nil
}

// startingLayout arranges the episodes as the order numbers them.
func startingLayout(seasons []SeasonResult, episodes []EpisodeResult, structureSlug string) customLayout {
	layout := customLayout{Slots: map[string]episodeSlot{}}
	seen := map[int]bool{}
	for _, season := range seasons {
		if season.SeasonNumber > 0 && !seen[season.SeasonNumber] {
			seen[season.SeasonNumber] = true
			layout.Seasons = append(layout.Seasons, customSeason{Number: season.SeasonNumber})
		}
	}
	for _, ep := range episodes {
		if key := customEpisodeKey(ep, structureSlug); key != "" {
			layout.Slots[key] = episodeSlot{ep.SeasonNumber, ep.EpisodeNumber}
		}
		if ep.SeasonNumber > 0 && !seen[ep.SeasonNumber] {
			seen[ep.SeasonNumber] = true
			layout.Seasons = append(layout.Seasons, customSeason{Number: ep.SeasonNumber})
		}
	}
	sort.Slice(layout.Seasons, func(i, j int) bool { return layout.Seasons[i].Number < layout.Seasons[j].Number })
	return layout
}

// SaveCustomSeasons stores an arrangement and switches the series to it.
// Seasons are numbered as given: regular seasons must be 1…N without gaps,
// and every episode key may appear once.
func (s *MetadataService) SaveCustomSeasons(ctx context.Context, seriesID string, save CustomSeasonsSave, revision *int64, userID int) (int64, error) {
	save.Settings.Layout = episodeLayoutCustom
	if err := save.Settings.Validate(); err != nil {
		return 0, err
	}
	regular := 0
	seenSeason := map[int]bool{}
	seenKey := map[string]bool{}
	for _, season := range save.Seasons {
		if season.Number < 0 || seenSeason[season.Number] {
			return 0, fmt.Errorf("%w: season numbers must be unique", ErrEpisodeOrderInvalid)
		}
		seenSeason[season.Number] = true
		if season.Number > 0 {
			regular++
		}
		if len(season.Title) > 500 || len(season.Overview) > 20000 {
			return 0, fmt.Errorf("%w: season %d's title or description is too long", ErrEpisodeOrderInvalid, season.Number)
		}
		for _, ep := range season.Episodes {
			if ep.Key == "" || len(ep.Key) > 200 || seenKey[ep.Key] {
				return 0, fmt.Errorf("%w: every episode appears once", ErrEpisodeOrderInvalid)
			}
			seenKey[ep.Key] = true
		}
	}
	if regular == 0 {
		return 0, fmt.Errorf("%w: keep at least one season", ErrEpisodeOrderInvalid)
	}
	for n := 1; n <= regular; n++ {
		if !seenSeason[n] {
			return 0, fmt.Errorf("%w: seasons are numbered 1 to %d without gaps", ErrEpisodeOrderInvalid, regular)
		}
	}
	item, err := s.itemRepo.GetByID(ctx, seriesID)
	if err != nil {
		return 0, err
	}
	if item.Type != "series" {
		return 0, ErrEpisodeOrderNotSeries
	}
	orderName := s.episodeOrderName(ctx, seriesID, save.Settings)
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
		var updatedBy any
		if userID > 0 {
			updatedBy = userID
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO series_episode_orders (series_id, provider, order_id, order_name, file_order, layout, updated_by)
			VALUES ($1, $2, $3, $4, $5, 'custom', $6)
			ON CONFLICT (series_id) DO UPDATE SET
				provider = EXCLUDED.provider, order_id = EXCLUDED.order_id, order_name = EXCLUDED.order_name,
				file_order = EXCLUDED.file_order, layout = 'custom',
				updated_by = EXCLUDED.updated_by, updated_at = now(),
				revision = series_episode_orders.revision + 1
			RETURNING revision`,
			seriesID, save.Settings.Provider, save.Settings.OrderID, orderName, save.Settings.FileOrder, updatedBy).Scan(&saved); err != nil {
			return err
		}
		if err := replaceCustomLayout(ctx, tx, seriesID, save.Seasons); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrEpisodeOrderConflict) {
			return 0, err
		}
		return 0, fmt.Errorf("saving custom seasons: %w", err)
	}
	return saved, nil
}

// replaceCustomLayout stores seasons as the series' whole arrangement inside
// tx: each season's episodes take positions 1…n in the order given.
func replaceCustomLayout(ctx context.Context, tx pgx.Tx, seriesID string, seasons []CustomSeasonsSeason) error {
	if _, err := tx.Exec(ctx, `DELETE FROM series_custom_episode_slots WHERE series_id = $1`, seriesID); err != nil {
		return fmt.Errorf("replacing custom episode slots: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM series_custom_seasons WHERE series_id = $1`, seriesID); err != nil {
		return fmt.Errorf("replacing custom seasons: %w", err)
	}
	var numbers []int32
	var titles, overviews []string
	var keys []string
	var slotSeasons, positions []int32
	for _, season := range seasons {
		if season.Number > 0 {
			numbers = append(numbers, int32(season.Number))
			titles = append(titles, strings.TrimSpace(season.Title))
			overviews = append(overviews, strings.TrimSpace(season.Overview))
		}
		for i, ep := range season.Episodes {
			keys = append(keys, ep.Key)
			slotSeasons = append(slotSeasons, int32(season.Number))
			positions = append(positions, int32(i+1))
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO series_custom_seasons (series_id, number, title, overview)
		SELECT $1, n, t, o FROM unnest($2::int[], $3::text[], $4::text[]) AS v(n, t, o)`,
		seriesID, numbers, titles, overviews); err != nil {
		return fmt.Errorf("storing custom seasons: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO series_custom_episode_slots (series_id, episode_key, season, position)
		SELECT $1, k, s, p FROM unnest($2::text[], $3::int[], $4::int[]) AS v(k, s, p)`,
		seriesID, keys, slotSeasons, positions); err != nil {
		return fmt.Errorf("storing custom episode slots: %w", err)
	}
	return nil
}
