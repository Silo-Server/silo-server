package metadata

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
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
	sort.SliceStable(regular, func(a, b int) bool {
		x, y := out[regular[a]], out[regular[b]]
		if x.SeasonNumber != y.SeasonNumber {
			return x.SeasonNumber < y.SeasonNumber
		}
		return x.EpisodeNumber < y.EpisodeNumber
	})
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
			ep.FileSeasonNumber, ep.FileEpisodeNumber, ep.FileNumbered = ep.SeasonNumber, ep.EpisodeNumber, true
			if ep.DefaultSeasonNumber != nil && ep.DefaultEpisodeNumber != nil {
				ep.FileSeasonNumber, ep.FileEpisodeNumber = *ep.DefaultSeasonNumber, *ep.DefaultEpisodeNumber
			}
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

// episodeOrderSettings is how a series' episodes are ordered: the library's
// provider unless the series overrides it (series_episode_orders).
type episodeOrderSettings struct {
	// Provider is the preferred structure provider (tvdb, tmdb); empty
	// follows the episode chain.
	Provider string
	// OrderID is one of Provider's episode orders; empty is its default.
	OrderID string
	// FileOrder is the provider whose default order the file names follow;
	// empty means the structure provider.
	FileOrder string
	Layout    string
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
	settings := episodeOrderSettings{Layout: episodeLayoutSeasons}
	provider, err := s.seriesEpisodeOrderProvider(ctx, contentID)
	if err != nil {
		return settings, err
	}
	settings.Provider = provider
	if s == nil || s.dbPool == nil || contentID == "" {
		return settings, nil
	}
	var override episodeOrderSettings
	err = s.dbPool.QueryRow(ctx, `
		SELECT provider, order_id, file_order, layout
		FROM series_episode_orders WHERE series_id = $1`, contentID).
		Scan(&override.Provider, &override.OrderID, &override.FileOrder, &override.Layout)
	if errors.Is(err, pgx.ErrNoRows) {
		return settings, nil
	}
	if err != nil {
		return settings, fmt.Errorf("loading the series episode order: %w", err)
	}
	if override.Provider != "" {
		settings.Provider, settings.OrderID = override.Provider, override.OrderID
	}
	settings.FileOrder = override.FileOrder
	if override.Layout != "" {
		settings.Layout = override.Layout
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

// writeEpisodeFileNumbers stores each target's file numbers on the row at its
// numbers inside tx: NULL when they equal the row's own numbers or the file
// order lacks the episode.
func writeEpisodeFileNumbers(ctx context.Context, tx pgx.Tx, seriesID string, targets []EpisodeResult) error {
	seasons := make([]int32, 0, len(targets))
	numbers := make([]int32, 0, len(targets))
	fileSeasons := make([]*int32, 0, len(targets))
	fileNumbers := make([]*int32, 0, len(targets))
	for _, ep := range targets {
		if !catalog.FitsPostgresInteger(ep.SeasonNumber) || !catalog.FitsPostgresInteger(ep.EpisodeNumber) {
			continue
		}
		seasons = append(seasons, int32(ep.SeasonNumber))
		numbers = append(numbers, int32(ep.EpisodeNumber))
		if !ep.FileNumbered || (ep.FileSeasonNumber == ep.SeasonNumber && ep.FileEpisodeNumber == ep.EpisodeNumber) ||
			!catalog.FitsPostgresInteger(ep.FileSeasonNumber) || !catalog.FitsPostgresInteger(ep.FileEpisodeNumber) {
			fileSeasons = append(fileSeasons, nil)
			fileNumbers = append(fileNumbers, nil)
			continue
		}
		fs, fe := int32(ep.FileSeasonNumber), int32(ep.FileEpisodeNumber)
		fileSeasons = append(fileSeasons, &fs)
		fileNumbers = append(fileNumbers, &fe)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE episodes e
		SET file_season_number = v.fs, file_episode_number = v.fe
		FROM unnest($2::int[], $3::int[], $4::int[], $5::int[]) AS v(s, n, fs, fe)
		WHERE e.series_id = $1 AND e.season_number = v.s AND e.episode_number = v.n
		  AND (e.file_season_number IS DISTINCT FROM v.fs OR e.file_episode_number IS DISTINCT FROM v.fe)`,
		seriesID, seasons, numbers, fileSeasons, fileNumbers); err != nil {
		return fmt.Errorf("writing episode file numbers of %s: %w", seriesID, err)
	}
	return nil
}

// storeEpisodeFileNumbers is writeEpisodeFileNumbers in a transaction of its
// own.
func (s *MetadataService) storeEpisodeFileNumbers(ctx context.Context, seriesID string, targets []EpisodeResult) error {
	if s.dbPool == nil {
		return nil
	}
	return pgx.BeginFunc(ctx, s.dbPool, func(tx pgx.Tx) error {
		return writeEpisodeFileNumbers(ctx, tx, seriesID, targets)
	})
}
