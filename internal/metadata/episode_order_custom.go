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
		SELECT number, title, overview, poster_path, poster_source, poster_thumbhash FROM series_custom_seasons
		WHERE series_id = $1 ORDER BY number`, seriesID)
	if err != nil {
		return layout, false, fmt.Errorf("loading custom seasons: %w", err)
	}
	for rows.Next() {
		var season customSeason
		if err := rows.Scan(&season.Number, &season.Title, &season.Overview,
			&season.Poster.Path, &season.Poster.Source, &season.Poster.Thumbhash); err != nil {
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
	// StillPath is the provider's still, a provider image path.
	StillPath string

	position int
}

// CustomSeasonsSeason is one season in the arranger; Number 0 is Specials.
type CustomSeasonsSeason struct {
	Number int
	// Title and Overview are the admin's; empty takes the provider's.
	Title, Overview                 string
	ProviderTitle, ProviderOverview string
	// Poster is the admin's uploaded poster; ProviderPosterPath the poster
	// of the base order's season with this number (a provider image path).
	Poster             CustomSeasonPoster
	ProviderPosterPath string
	Episodes           []CustomSeasonsEpisode
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
	// A stored arrangement opens on the order it was made from, whatever the
	// series shows now, so switching to another layout and back keeps it; a
	// base starts over.
	switch {
	case base != nil:
		result.Settings = *base
	default:
		if stored, ok, err := s.customLayoutBase(ctx, seriesID); err != nil {
			return result, err
		} else if ok {
			result.Settings = stored
		}
	}
	result.Settings.Layout = episodeLayoutCustom
	if err := result.Settings.Validate(); err != nil {
		return result, err
	}

	ctx, ids, language, err := s.seriesForEpisodeOrder(ctx, seriesID)
	if err != nil {
		return result, err
	}
	order := episodeOrderSettings{EpisodeOrderSettings: result.Settings}
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
	if structureSlug != order.Provider {
		// The chosen provider has no episodes for the series, so the other
		// one numbered it in its default order: the arrangement is made from
		// that, and a save names it.
		result.Settings.Provider, result.Settings.OrderID = structureSlug, ""
		if result.Settings.FileOrder == structureSlug {
			result.Settings.FileOrder = ""
		}
	}
	assignFileNumbers(episodes, structureSlug, order.FileOrder, structures.list)

	var layout customLayout
	if base == nil && s.dbPool != nil {
		if layout, result.Saved, err = loadCustomLayout(ctx, s.dbPool, seriesID); err != nil {
			return result, err
		}
	}
	if !result.Saved {
		layout = startingLayout(seasons, episodes, structureSlug)
	}
	arrangedSeasons, arranged := applyCustomLayout(seasons, episodes, structureSlug, layout)

	files, err := s.seriesFileNames(ctx, seriesID)
	if err != nil {
		return result, err
	}
	result.Seasons = arrangerSeasons(seasons, arrangedSeasons, arranged, episodes, layout, files, structureSlug, result.Saved)
	return result, nil
}

// seriesFileNames maps the file numbers of the series' numbered files to
// their file names, the first file at each numbers winning.
func (s *MetadataService) seriesFileNames(ctx context.Context, seriesID string) (map[episodeSlot]string, error) {
	files := map[episodeSlot]string{}
	if s.dbPool == nil {
		return files, nil
	}
	links, err := s.loadSeriesFileLinks(ctx, s.dbPool, seriesID)
	if err != nil {
		return nil, err
	}
	for _, link := range links {
		if !link.Numbered {
			continue
		}
		if _, ok := files[episodeSlot{link.Season, link.Episode}]; !ok {
			files[episodeSlot{link.Season, link.Episode}] = path.Base(link.Path)
		}
	}
	return files, nil
}

// arrangerSeasons lists the arranged seasons with their episodes for the
// arranger, specials last. episodes are the base order's episodes in the
// same order as arranged; files names the file at each file numbers. When
// saved, an episode the layout does not place is marked as placed
// automatically.
func arrangerSeasons(seasons, arrangedSeasons []SeasonResult, arranged, inOrder []EpisodeResult, layout customLayout, files map[episodeSlot]string, structureSlug string, saved bool) []CustomSeasonsSeason {
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
			Number:             season.SeasonNumber,
			Title:              custom[season.SeasonNumber].Title,
			Overview:           custom[season.SeasonNumber].Overview,
			ProviderTitle:      strings.TrimSpace(provider[season.SeasonNumber].Title),
			ProviderOverview:   provider[season.SeasonNumber].Overview,
			Poster:             custom[season.SeasonNumber].Poster,
			ProviderPosterPath: provider[season.SeasonNumber].PosterPath,
		}
		if entry.ProviderTitle == "" {
			entry.ProviderTitle = fallbackSeasonTitle(season.SeasonNumber)
		}
		byNumber[season.SeasonNumber] = entry
	}
	for i, ep := range arranged {
		key := customEpisodeKey(ep, structureSlug)
		fileSlot := episodeSlot{-1, -1}
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
			File: files[fileSlot], PlacedAutomatically: saved && !inLayout,
			StillPath: ep.StillPath,
			position:  ep.EpisodeNumber,
		})
	}
	for _, season := range byNumber {
		sort.SliceStable(season.Episodes, func(i, j int) bool { return season.Episodes[i].position < season.Episodes[j].position })
	}
	out := make([]CustomSeasonsSeason, 0, len(byNumber))
	for _, season := range byNumber {
		out = append(out, *season)
	}
	sort.Slice(out, func(i, j int) bool { return seasonBefore(out[i].Number, out[j].Number) })
	return out
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
	// Episode keys name the provider that numbered the arrangement. The
	// series follows that provider from now on, so a later library change
	// cannot leave the keys matching nothing.
	keyProvider := ""
	for key := range seenKey {
		provider, _, _ := strings.Cut(key, ":")
		if keyProvider != "" && provider != keyProvider {
			return 0, fmt.Errorf("%w: episodes come from one provider", ErrEpisodeOrderInvalid)
		}
		keyProvider = provider
	}
	if keyProvider != "" {
		if save.Settings.Provider != "" && save.Settings.Provider != keyProvider {
			return 0, fmt.Errorf("%w: the episodes are %s's, not %s's", ErrEpisodeOrderInvalid, keyProvider, save.Settings.Provider)
		}
		save.Settings.Provider = keyProvider
		if err := save.Settings.Validate(); err != nil {
			return 0, err
		}
	}
	for n := 1; n <= regular; n++ {
		if !seenSeason[n] {
			return 0, fmt.Errorf("%w: seasons are numbered 1 to %d without gaps", ErrEpisodeOrderInvalid, regular)
		}
	}
	if _, err := s.requireSeries(ctx, seriesID); err != nil {
		return 0, err
	}
	if err := s.resolveCustomSeasonPosters(ctx, seriesID, save.Seasons); err != nil {
		return 0, err
	}
	orderName := s.episodeOrderName(ctx, seriesID, save.Settings)
	var saved int64
	err := pgx.BeginFunc(ctx, s.dbPool, func(tx pgx.Tx) error {
		var err error
		if saved, err = storeSeriesOrder(ctx, tx, seriesID, save.Settings, orderName, revision, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE series_episode_orders
			SET custom_provider = $2, custom_order_id = $3, custom_file_order = $4
			WHERE series_id = $1`, seriesID, save.Settings.Provider, save.Settings.OrderID, save.Settings.FileOrder); err != nil {
			return err
		}
		return replaceCustomLayout(ctx, tx, seriesID, save.Seasons)
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
	var titles, overviews, posterPaths, posterSources, posterThumbhashes []string
	var keys []string
	var slotSeasons, positions []int32
	for _, season := range seasons {
		if season.Number > 0 {
			numbers = append(numbers, int32(season.Number)) //nolint:gosec // SaveCustomSeasons numbers seasons 0…len(seasons)
			titles = append(titles, strings.TrimSpace(season.Title))
			overviews = append(overviews, strings.TrimSpace(season.Overview))
			posterPaths = append(posterPaths, season.Poster.Path)
			posterSources = append(posterSources, season.Poster.Source)
			posterThumbhashes = append(posterThumbhashes, season.Poster.Thumbhash)
		}
		for i, ep := range season.Episodes {
			keys = append(keys, ep.Key)
			slotSeasons = append(slotSeasons, int32(season.Number)) //nolint:gosec // SaveCustomSeasons numbers seasons 0…len(seasons)
			positions = append(positions, int32(i+1))
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO series_custom_seasons (series_id, number, title, overview, poster_path, poster_source, poster_thumbhash)
		SELECT $1, n, t, o, pp, ps, pt
		FROM unnest($2::int[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[]) AS v(n, t, o, pp, ps, pt)`,
		seriesID, numbers, titles, overviews, posterPaths, posterSources, posterThumbhashes); err != nil {
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

// UploadCustomSeasonPoster stores an uploaded season poster in artwork
// storage for the series' arranged seasons, without publishing it: the
// arrangement that names it puts it on a season when it is saved and the
// series refreshes. A poster belongs to its arranged season, whatever number
// the season takes.
func (s *MetadataService) UploadCustomSeasonPoster(ctx context.Context, seriesID string, data []byte) (CustomSeasonPoster, error) {
	if _, err := s.requireSeries(ctx, seriesID); err != nil {
		return CustomSeasonPoster{}, err
	}
	uploaded, err := s.ApplyUploadedItemImage(ctx, ApplyUploadedItemImageRequest{
		ContentID: seriesID, ContentType: anchoredItemTypeSeries, ImageType: ImagePoster, Data: data,
	})
	if err != nil {
		return CustomSeasonPoster{}, err
	}
	poster := CustomSeasonPoster{Path: uploaded.StoredPath, Source: uploaded.SourcePath, Thumbhash: uploaded.Thumbhash}
	// A save names the poster by its source; the server keeps where it is
	// stored, so a save cannot point a season at any other image.
	if _, err := s.dbPool.Exec(ctx, `
		INSERT INTO series_custom_season_uploads (series_id, source, path, thumbhash)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (series_id, source) DO UPDATE SET path = EXCLUDED.path, thumbhash = EXCLUDED.thumbhash`,
		seriesID, poster.Source, poster.Path, poster.Thumbhash); err != nil {
		return CustomSeasonPoster{}, fmt.Errorf("recording the uploaded season poster: %w", err)
	}
	return poster, nil
}

// resolveCustomSeasonPosters fills each season's poster from the series'
// uploads by its source; a source the series has no upload for is refused.
func (s *MetadataService) resolveCustomSeasonPosters(ctx context.Context, seriesID string, seasons []CustomSeasonsSeason) error {
	uploads, err := s.customSeasonUploads(ctx, seriesID)
	if err != nil {
		return err
	}
	for i := range seasons {
		source := seasons[i].Poster.Source
		if source == "" {
			seasons[i].Poster = CustomSeasonPoster{}
			continue
		}
		upload, ok := uploads[source]
		if !ok {
			return fmt.Errorf("%w: season %d's poster was not uploaded for this series", ErrEpisodeOrderInvalid, seasons[i].Number)
		}
		seasons[i].Poster = upload
	}
	return nil
}

// customSeasonUploads lists the posters uploaded for the series' arranged
// seasons, by source.
func (s *MetadataService) customSeasonUploads(ctx context.Context, seriesID string) (map[string]CustomSeasonPoster, error) {
	uploads := map[string]CustomSeasonPoster{}
	if s == nil || s.dbPool == nil {
		return uploads, nil
	}
	rows, err := s.dbPool.Query(ctx, `
		SELECT source, path, thumbhash FROM series_custom_season_uploads WHERE series_id = $1`, seriesID)
	if err != nil {
		return nil, fmt.Errorf("loading uploaded season posters: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var poster CustomSeasonPoster
		if err := rows.Scan(&poster.Source, &poster.Path, &poster.Thumbhash); err != nil {
			return nil, fmt.Errorf("loading uploaded season posters: %w", err)
		}
		uploads[poster.Source] = poster
	}
	return uploads, rows.Err()
}

// customLayoutBase is the order the series' stored arrangement was made from;
// ok is false when it has none.
func (s *MetadataService) customLayoutBase(ctx context.Context, seriesID string) (EpisodeOrderSettings, bool, error) {
	base := EpisodeOrderSettings{Layout: episodeLayoutCustom}
	if s == nil || s.dbPool == nil {
		return base, false, nil
	}
	err := s.dbPool.QueryRow(ctx, `
		SELECT o.custom_provider, o.custom_order_id, o.custom_file_order
		FROM series_episode_orders o
		WHERE o.series_id = $1 AND o.custom_provider <> ''
		  AND EXISTS (SELECT 1 FROM series_custom_seasons c WHERE c.series_id = o.series_id)`, seriesID).
		Scan(&base.Provider, &base.OrderID, &base.FileOrder)
	if errors.Is(err, pgx.ErrNoRows) {
		return base, false, nil
	}
	if err != nil {
		return base, false, fmt.Errorf("loading the arrangement's order: %w", err)
	}
	return base, true, nil
}
