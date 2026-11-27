package metadata

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/contentid"
	"github.com/Silo-Server/silo-server/internal/models"
)

// Episode order layouts (series_episode_orders.layout).
const (
	// episodeLayoutSeasons keeps the seasons of the show's order.
	episodeLayoutSeasons = "seasons"
	// episodeLayoutAbsolute numbers every regular episode in one season, in
	// the order's sequence; specials keep season 0.
	episodeLayoutAbsolute = "absolute"
)

// applyAbsoluteLayout renumbers the regular episodes 1…N in one season 1, in
// the order's season and episode sequence. Specials keep their numbers.
// Season 1 keeps the first regular season's artwork and air date under a
// plain "Season 1" title, since its provider title names only part of the run.
func applyAbsoluteLayout(seasons []SeasonResult, episodes []EpisodeResult) ([]SeasonResult, []EpisodeResult) {
	out := make([]EpisodeResult, len(episodes))
	copy(out, episodes)
	regular := make([]int, 0, len(out))
	for i := range out {
		if out[i].SeasonNumber > 0 {
			regular = append(regular, i)
		}
	}
	sort.SliceStable(regular, func(a, b int) bool { return episodeBefore(&out[regular[a]], &out[regular[b]]) })
	for n, i := range regular {
		out[i].SeasonNumber = 1
		out[i].EpisodeNumber = n + 1
	}

	var first *SeasonResult
	keptSeasons := make([]SeasonResult, 0, 2)
	for i := range seasons {
		season := seasons[i]
		if season.SeasonNumber == 0 {
			keptSeasons = append(keptSeasons, season)
			continue
		}
		if first == nil || season.SeasonNumber < first.SeasonNumber {
			first = &seasons[i]
		}
	}
	if len(regular) > 0 {
		one := SeasonResult{SeasonNumber: 1, Title: fallbackSeasonTitle(1)}
		if first != nil {
			one.ContentID = first.ContentID
			one.AirDate = first.AirDate
			one.PosterPath = first.PosterPath
			one.BackdropPath = first.BackdropPath
		}
		keptSeasons = append(keptSeasons, one)
	}
	return keptSeasons, out
}

// assignFileNumbers gives each structure episode the numbers its file name
// carries. Files are named in one provider's default order: fileOrder, or the
// structure provider's when fileOrder is empty. For the structure provider
// that is the episode's default-order place (its own numbers unless it was
// fetched in another order). For the other provider, the episode bridges to
// that provider's episode (bridgeEpisodes) and takes its numbers; an episode
// with no counterpart there has no file numbers.
//
// It runs before a layout renumbers the episodes, and reports false when the
// file order's provider returned nothing to bridge to: stored file numbers
// then stay as they are.
func assignFileNumbers(episodes []EpisodeResult, structureSlug, fileOrder string, providers []providerStructure) bool {
	if fileOrder == "" || fileOrder == structureSlug {
		for i := range episodes {
			ep := &episodes[i]
			ep.FileSeasonNumber, ep.FileEpisodeNumber = defaultPlace(*ep)
			ep.FileNumbered = true
		}
		return true
	}
	for i := range episodes {
		episodes[i].FileSeasonNumber, episodes[i].FileEpisodeNumber, episodes[i].FileNumbered = 0, 0, false
	}
	for _, p := range providers {
		if p.slug != fileOrder || p.local || p.failed || len(p.episodes) == 0 {
			continue
		}
		pointers := make([]*EpisodeResult, len(episodes))
		for i := range episodes {
			pointers[i] = &episodes[i]
		}
		for j, target := range bridgeEpisodes(pointers, p.episodes) {
			target.FileSeasonNumber, target.FileEpisodeNumber, target.FileNumbered =
				p.episodes[j].SeasonNumber, p.episodes[j].EpisodeNumber, true
		}
		return true
	}
	return false
}

// defaultPlace is the episode's place in its provider's default order: the
// default numbers the provider gave when it was fetched in another order,
// else its own numbers.
func defaultPlace(ep EpisodeResult) (int, int) {
	if ep.DefaultSeasonNumber != nil && ep.DefaultEpisodeNumber != nil {
		return *ep.DefaultSeasonNumber, *ep.DefaultEpisodeNumber
	}
	return ep.SeasonNumber, ep.EpisodeNumber
}

// seasonBefore orders season numbers as series pages list them: regular
// seasons in order, then Specials.
func seasonBefore(a, b int) bool {
	if (a == 0) != (b == 0) {
		return b == 0
	}
	return a < b
}

// episodeOrderSettings is how a series' episodes are ordered, resolved for a
// refresh: the series' settings with its library's provider filled in, and
// the arrangement of a custom layout.
type episodeOrderSettings struct {
	EpisodeOrderSettings
	// Custom is the arrangement of a custom layout.
	Custom *customLayout
	// Override is true when an admin chose the series' order.
	Override bool
}

// applyEpisodeOrder numbers the merged structure as the order lays it out:
// provider and file numbers first, from the order's own numbering, then the
// layout. It reports whether the file numbers are known (assignFileNumbers).
// Refresh and preview share it, so a preview shows what a save applies.
func applyEpisodeOrder(order episodeOrderSettings, seasons []SeasonResult, episodes []EpisodeResult, structureSlug string, providers []providerStructure) ([]SeasonResult, []EpisodeResult, bool) {
	assignProviderNumbers(episodes)
	known := assignFileNumbers(episodes, structureSlug, order.FileOrder, providers)
	switch {
	case order.Layout == episodeLayoutAbsolute:
		seasons, episodes = applyAbsoluteLayout(seasons, episodes)
	case order.Layout == episodeLayoutCustom && order.Custom != nil:
		seasons, episodes = applyCustomLayout(seasons, episodes, structureSlug, *order.Custom)
	}
	return seasons, episodes, known
}

// orderingFor is the ordering to request from the provider slug.
func (o episodeOrderSettings) orderingFor(slug string) string {
	if o.OrderID != "" && slug == o.Provider {
		return o.OrderID
	}
	return ""
}

// seriesEpisodeOrder resolves the series' episode order: its own override
// when an admin set one, else its library's provider with the default order
// and layout.
func (s *MetadataService) seriesEpisodeOrder(ctx context.Context, contentID string) (episodeOrderSettings, error) {
	settings := episodeOrderSettings{EpisodeOrderSettings: EpisodeOrderSettings{Layout: episodeLayoutSeasons}}
	if s != nil && s.dbPool != nil && contentID != "" {
		var stored EpisodeOrderSettings
		err := s.dbPool.QueryRow(ctx, `
			SELECT provider, order_id, file_order, layout
			FROM series_episode_orders WHERE series_id = $1`, contentID).
			Scan(&stored.Provider, &stored.OrderID, &stored.FileOrder, &stored.Layout)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return settings, fmt.Errorf("loading the series episode order: %w", err)
		default:
			if stored.Layout == "" {
				stored.Layout = episodeLayoutSeasons
			}
			settings.EpisodeOrderSettings, settings.Override = stored, !stored.isDefault()
		}
	}
	// Only a series without its own provider follows its library's.
	if settings.Provider == "" {
		provider, err := s.seriesEpisodeOrderProvider(ctx, contentID)
		if err != nil {
			return settings, err
		}
		settings.Provider = provider
	}
	if settings.Layout == episodeLayoutCustom {
		layout, ok, err := loadCustomLayout(ctx, s.dbPool, contentID)
		if err != nil {
			return settings, err
		}
		if ok {
			settings.Custom = &layout
		} else {
			settings.Layout = episodeLayoutSeasons
		}
	}
	return settings, nil
}

type episodeFileNumbersKey struct{}

// withEpisodeFileNumbers marks a refresh whose structure episodes carry
// their file numbers (assignFileNumbers), so persisting writes them.
func withEpisodeFileNumbers(ctx context.Context) context.Context {
	return context.WithValue(ctx, episodeFileNumbersKey{}, true)
}

func episodeFileNumbersOf(ctx context.Context) bool {
	known, _ := ctx.Value(episodeFileNumbersKey{}).(bool)
	return known
}

// noFileNumber is the file number of an episode the file order lacks.
var noFileNumber int32 = -1

// writeEpisodeFileNumbers stores each target's file numbers and provider
// numbers on the row at its numbers inside tx: NULL when they equal the row's
// own numbers, and -1 file numbers when the file order lacks the episode.
// structureSlug names the provider the provider numbers belong to.
func writeEpisodeFileNumbers(ctx context.Context, tx pgx.Tx, seriesID, structureSlug string, targets []EpisodeResult) error {
	n := len(targets)
	seasons, numbers := make([]int32, 0, n), make([]int32, 0, n)
	fileSeasons, fileNumbers := make([]*int32, 0, n), make([]*int32, 0, n)
	providerSeasons, providerNumbers := make([]*int32, 0, n), make([]*int32, 0, n)
	// pair is nil when (s, e) equals the row's own numbers or does not fit.
	pair := func(ok bool, s, e int, own EpisodeResult) (*int32, *int32) {
		if !ok || (s == own.SeasonNumber && e == own.EpisodeNumber) ||
			!catalog.FitsPostgresInteger(s) || !catalog.FitsPostgresInteger(e) {
			return nil, nil
		}
		a, b := int32(s), int32(e) //nolint:gosec // FitsPostgresInteger checked the range
		return &a, &b
	}
	for _, ep := range targets {
		if !catalog.FitsPostgresInteger(ep.SeasonNumber) || !catalog.FitsPostgresInteger(ep.EpisodeNumber) {
			continue
		}
		seasons = append(seasons, int32(ep.SeasonNumber))  //nolint:gosec // FitsPostgresInteger checked the range
		numbers = append(numbers, int32(ep.EpisodeNumber)) //nolint:gosec // FitsPostgresInteger checked the range
		fs, fe := pair(ep.FileNumbered, ep.FileSeasonNumber, ep.FileEpisodeNumber, ep)
		if !ep.FileNumbered {
			// The file order lacks the episode: no file name names it, and
			// -1 matches no parsed number where NULL would mean its own.
			fs, fe = &noFileNumber, &noFileNumber
		}
		fileSeasons, fileNumbers = append(fileSeasons, fs), append(fileNumbers, fe)
		ps, pe := pair(ep.ProviderEpisodeNumber > 0 || ep.ProviderSeasonNumber > 0, ep.ProviderSeasonNumber, ep.ProviderEpisodeNumber, ep)
		providerSeasons, providerNumbers = append(providerSeasons, ps), append(providerNumbers, pe)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE episodes e
		SET file_season_number = v.fs, file_episode_number = v.fe,
		    provider_season_number = v.ps, provider_episode_number = v.pe,
		    provider_numbering = NULLIF($8, '')
		FROM unnest($2::int[], $3::int[], $4::int[], $5::int[], $6::int[], $7::int[]) AS v(s, n, fs, fe, ps, pe)
		WHERE e.series_id = $1 AND e.season_number = v.s AND e.episode_number = v.n
		  AND (e.file_season_number IS DISTINCT FROM v.fs OR e.file_episode_number IS DISTINCT FROM v.fe
		    OR e.provider_season_number IS DISTINCT FROM v.ps OR e.provider_episode_number IS DISTINCT FROM v.pe
		    OR e.provider_numbering IS DISTINCT FROM NULLIF($8, ''))`,
		seriesID, seasons, numbers, fileSeasons, fileNumbers, providerSeasons, providerNumbers, structureSlug); err != nil {
		return fmt.Errorf("writing episode file numbers of %s: %w", seriesID, err)
	}
	return nil
}

// markListedEpisodes records inside tx which rows the series' order lists
// (episodesOfCurrentNumbering). In each season targets has episodes in, the
// rows at the targets' numbers are listed and the rest, which the order
// lacks (a renumber parks them after the season's last episode), are not.
// A season targets has none of keeps its marks: a refresh that could not
// load a season says nothing about its episodes. It returns how many rows
// changed.
func markListedEpisodes(ctx context.Context, tx pgx.Tx, seriesID string, targets []EpisodeResult) (int64, error) {
	seasons, numbers := make([]int32, 0, len(targets)), make([]int32, 0, len(targets))
	for _, target := range targets {
		if catalog.FitsPostgresInteger(target.SeasonNumber) && catalog.FitsPostgresInteger(target.EpisodeNumber) {
			seasons = append(seasons, int32(target.SeasonNumber))  //nolint:gosec // FitsPostgresInteger checked the range
			numbers = append(numbers, int32(target.EpisodeNumber)) //nolint:gosec // FitsPostgresInteger checked the range
		}
	}
	tag, err := tx.Exec(ctx, `
		WITH listed AS (SELECT s, n FROM unnest($2::int[], $3::int[]) AS v(s, n))
		UPDATE episodes e
		SET unlisted = NOT EXISTS (SELECT 1 FROM listed WHERE s = e.season_number AND n = e.episode_number)
		WHERE e.series_id = $1
		  AND e.season_number IN (SELECT s FROM listed)
		  AND e.unlisted IS DISTINCT FROM NOT EXISTS (SELECT 1 FROM listed WHERE s = e.season_number AND n = e.episode_number)`,
		seriesID, seasons, numbers)
	if err != nil {
		return 0, fmt.Errorf("marking the listed episodes of %s: %w", seriesID, err)
	}
	return tag.RowsAffected(), nil
}

// episodesOfCurrentNumbering drops the episodes the series' order does not
// list (episodes.unlisted, markListedEpisodes): rows a renumber parked
// because the chosen order lacks them, which would otherwise shift every
// later bare episode number.
func (s *MetadataService) episodesOfCurrentNumbering(ctx context.Context, seriesID string, episodes []*models.Episode) ([]*models.Episode, error) {
	rows, err := s.dbPool.Query(ctx, `SELECT content_id FROM episodes WHERE series_id = $1 AND unlisted`, seriesID)
	if err != nil {
		return nil, fmt.Errorf("loading the unlisted episodes of %s: %w", seriesID, err)
	}
	unlisted, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("loading the unlisted episodes of %s: %w", seriesID, err)
	}
	if len(unlisted) == 0 {
		return episodes, nil
	}
	kept := make([]*models.Episode, 0, len(episodes))
	for _, episode := range episodes {
		if episode != nil && !slices.Contains(unlisted, episode.ContentID) {
			kept = append(kept, episode)
		}
	}
	return kept, nil
}

// storeEpisodeFileNumbers is writeEpisodeFileNumbers in a transaction of its
// own.
func (s *MetadataService) storeEpisodeFileNumbers(ctx context.Context, seriesID string, targets []EpisodeResult) error {
	if s.dbPool == nil {
		return nil
	}
	return pgx.BeginFunc(ctx, s.dbPool, func(tx pgx.Tx) error {
		return writeEpisodeFileNumbers(ctx, tx, seriesID, episodeStructureOf(ctx), targets)
	})
}

// episodeLayoutCustom places episodes in admin-arranged seasons
// (series_custom_seasons, series_custom_episode_slots).
const episodeLayoutCustom = "custom"

// customSeason is one admin-arranged season. Empty Title and Overview take
// the provider season with the same number's.
type customSeason struct {
	Number   int
	Title    string
	Overview string
	Poster   CustomSeasonPoster
}

// CustomSeasonPoster is a poster an admin uploaded for an arranged season:
// the artwork storage path, its upload:// source and thumbhash.
type CustomSeasonPoster struct {
	Path, Source, Thumbhash string
}

// customLayout is a series' arranged seasons and where each episode sits,
// keyed by "<provider>:<provider episode id>" of the structure provider.
type customLayout struct {
	Seasons []customSeason
	Slots   map[string]episodeSlot
}

// customEpisodeKey identifies an episode across refreshes: its structure
// provider's episode ID.
func customEpisodeKey(ep EpisodeResult, structureSlug string) string {
	if id := ep.ProviderIDs[structureSlug]; id != "" {
		return structureSlug + ":" + id
	}
	return ""
}

// applyCustomLayout numbers the episodes as the layout places them. An
// episode the layout does not place (new since it was saved) goes after the
// last episode of the last custom season, or of Specials for a special, in
// the order's sequence. Each custom season takes the admin's title and
// overview, else those of the order's season with the same number, and that
// season's artwork and air date.
func applyCustomLayout(seasons []SeasonResult, episodes []EpisodeResult, structureSlug string, layout customLayout) ([]SeasonResult, []EpisodeResult) {
	// An arrangement of another provider's episodes (the series fell back
	// to the other provider) places none of these; the order's own seasons
	// stand rather than every episode piling into the last season.
	placesAny := false
	for _, ep := range episodes {
		if _, ok := layout.Slots[customEpisodeKey(ep, structureSlug)]; ok {
			placesAny = true
			break
		}
	}
	if !placesAny {
		return seasons, episodes
	}
	out := make([]EpisodeResult, len(episodes))
	copy(out, episodes)
	lastSeason := 1
	for _, season := range layout.Seasons {
		lastSeason = max(lastSeason, season.Number)
	}
	next := map[int]int{}
	for _, slot := range layout.Slots {
		next[slot.season] = max(next[slot.season], slot.episode)
	}
	order := make([]int, len(out))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		x, y := out[order[a]], out[order[b]]
		if x.SeasonNumber != y.SeasonNumber {
			return seasonBefore(x.SeasonNumber, y.SeasonNumber)
		}
		return x.EpisodeNumber < y.EpisodeNumber
	})
	for _, i := range order {
		ep := &out[i]
		if slot, ok := layout.Slots[customEpisodeKey(*ep, structureSlug)]; ok {
			ep.SeasonNumber, ep.EpisodeNumber = slot.season, slot.episode
			continue
		}
		season := lastSeason
		if ep.SeasonNumber == 0 {
			season = 0
		}
		next[season]++
		ep.SeasonNumber, ep.EpisodeNumber = season, next[season]
	}

	provider := make(map[int]SeasonResult, len(seasons))
	for _, season := range seasons {
		provider[season.SeasonNumber] = season
	}
	arranged := make([]SeasonResult, 0, len(layout.Seasons)+1)
	if specials, ok := provider[0]; ok {
		arranged = append(arranged, specials)
	}
	numbers := map[int]customSeason{}
	for _, season := range layout.Seasons {
		numbers[season.Number] = season
	}
	if len(numbers) == 0 {
		numbers[1] = customSeason{Number: 1}
	}
	for _, custom := range numbers {
		season := provider[custom.Number]
		season.SeasonNumber = custom.Number
		if custom.Title != "" {
			season.Title = custom.Title
		}
		if strings.TrimSpace(season.Title) == "" {
			season.Title = fallbackSeasonTitle(custom.Number)
		}
		if custom.Overview != "" {
			season.Overview = custom.Overview
		}
		if custom.Poster.Path != "" {
			poster := custom.Poster
			season.customPoster = &poster
		}
		arranged = append(arranged, season)
	}
	sort.Slice(arranged, func(i, j int) bool { return arranged[i].SeasonNumber < arranged[j].SeasonNumber })
	return arranged, out
}

// assignProviderNumbers records each structure episode's place in its
// provider's default order: the default numbers the provider gave when it was
// fetched in another order, else its own numbers. It runs before a layout
// renumbers the episodes.
func assignProviderNumbers(episodes []EpisodeResult) {
	for i := range episodes {
		episodes[i].ProviderSeasonNumber, episodes[i].ProviderEpisodeNumber = defaultPlace(episodes[i])
	}
}

// seriesShowsOtherNumbering reports whether the series' shown numbers may
// differ from those its episode providers number it by in their default
// order: an admin chose its order, its episodes record other provider
// numbers, or its library follows a provider other than the first remote
// one of its episode chain.
func (s *MetadataService) seriesShowsOtherNumbering(ctx context.Context, seriesID string, folderID int) (bool, error) {
	order, err := s.seriesEpisodeOrder(ctx, seriesID)
	if err != nil || order.Override {
		return order.Override, err
	}
	if s.dbPool != nil {
		var renumbered bool
		if err := s.dbPool.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM episodes WHERE series_id = $1
			  AND (provider_season_number IS NOT NULL OR file_season_number IS NOT NULL))`, seriesID).
			Scan(&renumbered); err != nil {
			return false, fmt.Errorf("checking the series' episode numbering: %w", err)
		}
		if renumbered {
			return true, nil
		}
	}
	if order.Provider == "" {
		return false, nil
	}
	chain, err := s.resolveChainCached(ctx, folderID, "episode")
	if err != nil {
		return false, err
	}
	for _, p := range chain {
		if slug := p.Slug(); slug == contentid.ProviderTVDB || slug == contentid.ProviderTMDB {
			return slug != order.Provider, nil
		}
	}
	return false, nil
}
