package discoverrows

import (
	"context"
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
)

// TMDB is the slice of the TMDB client live rows read.
type TMDB interface {
	ChartPage(ctx context.Context, params tmdb.ChartParams, page int) (*tmdb.MediaPage, error)
	DiscoverPage(ctx context.Context, mediaType string, params tmdb.DiscoverParams, page int) (*tmdb.MediaPage, error)
}

// SnapshotReader reads the stored copy of a list.
type SnapshotReader interface {
	Get(ctx context.Context, key string) (Snapshot, bool, error)
}

// Env is what a row's source reads from.
type Env struct {
	TMDB TMDB
	// Snapshots holds list rows' stored copies; nil leaves list rows empty.
	Snapshots SnapshotReader
}

// Source is where one row's titles come from.
type Source interface {
	// Page returns one page of the row's titles in the source's own order
	// and page numbering, before any viewer filtering. Totals are the
	// source's.
	Page(ctx context.Context, in PageInput) (*tmdb.MediaPage, error)
}

// PageInput asks a source for one page.
type PageInput struct {
	Page int
	// ViewerCertification returns the viewer's rating ceiling as the
	// certification.lte TMDB takes for its media type ("movie" or "tv"), ""
	// for none. A discover row sends the stricter of it and the row's own.
	// It only saves fetching what the viewer's own filter drops anyway.
	ViewerCertification func(tmdbMediaType string) string
}

type chartSource struct {
	env  Env
	spec ChartSpec
}

func (s chartSource) Page(ctx context.Context, in PageInput) (*tmdb.MediaPage, error) {
	return s.env.TMDB.ChartPage(ctx, tmdb.ChartParams{
		Preset: s.spec.Preset, MediaType: tmdbMediaType(s.spec.MediaType), TimeWindow: s.spec.TimeWindow,
	}, in.Page)
}

type discoverSource struct {
	env  Env
	spec DiscoverSpec
}

// voteAverageFloor keeps a score-sorted row from opening on titles a handful
// of votes put at 10, as the brand browse pages do.
const voteAverageFloor = 100

func (s discoverSource) Page(ctx context.Context, in PageInput) (*tmdb.MediaPage, error) {
	mediaType := tmdbMediaType(s.spec.MediaType)
	return s.env.TMDB.DiscoverPage(ctx, mediaType, s.params(mediaType, in), in.Page)
}

func (s discoverSource) params(mediaType string, in PageInput) tmdb.DiscoverParams {
	d := s.spec
	params := tmdb.DiscoverParams{
		WithGenres: d.GenreIDs, WithoutGenres: d.ExcludeGenreIDs, WithCompanies: d.CompanyIDs, WithNetworks: d.NetworkIDs,
		SortBy: d.SortBy, VoteCountGte: d.VoteCountGte, VoteAverageGte: d.VoteAverageGte,
		CertificationLte: d.CertificationLte, WithRuntimeGte: d.RuntimeGte, WithRuntimeLte: d.RuntimeLte,
		OriginalLanguage: d.OriginalLanguage, WithKeywords: d.KeywordIDs, WithOriginCountry: d.OriginCountries,
		WithWatchProviders: d.WatchProviderIDs, WatchRegion: d.WatchRegion,
	}
	params.ReleaseDateGte, params.ReleaseDateLte = releaseDateRange(d)
	if in.ViewerCertification != nil {
		params.CertificationLte = stricterCertification(mediaType, in.ViewerCertification(mediaType), params.CertificationLte)
	}
	if strings.HasPrefix(d.SortBy, "vote_average.") && params.VoteCountGte == 0 {
		params.VoteCountGte = voteAverageFloor
	}
	return params
}

// listSource pages a stored list by PageSize. A list not stored yet reads as
// empty until the refresher fetches it.
type listSource struct {
	env Env
	key string
}

func (s listSource) Page(ctx context.Context, in PageInput) (*tmdb.MediaPage, error) {
	page := max(in.Page, 1)
	out := &tmdb.MediaPage{Page: page}
	if s.env.Snapshots == nil || s.key == "" {
		return out, nil
	}
	snapshot, found, err := s.env.Snapshots.Get(ctx, s.key)
	if err != nil || !found {
		return out, err
	}
	return CardsSource(snapshot.Cards).Page(ctx, PageInput{Page: page})
}

// CardsSource pages cards already fetched by PageSize: a stored list, or a
// list fetched live for a preview.
type CardsSource []Card

func (cards CardsSource) Page(_ context.Context, in PageInput) (*tmdb.MediaPage, error) {
	page := max(in.Page, 1)
	out := &tmdb.MediaPage{
		Page:         page,
		TotalPages:   (len(cards) + PageSize - 1) / PageSize,
		TotalResults: len(cards),
	}
	start := (page - 1) * PageSize
	if start >= len(cards) {
		return out, nil
	}
	for _, c := range cards[start:min(start+PageSize, len(cards))] {
		out.Results = append(out.Results, c.mediaResult())
	}
	return out, nil
}

// stricterCertification returns the lower of two certification.lte values on
// one ladder; an empty or unknown value does not narrow the other.
func stricterCertification(tmdbMediaType, a, b string) string {
	ladder := CertificationLadder(tmdbMediaType)
	ia, ib := slices.Index(ladder, a), slices.Index(ladder, b)
	switch {
	case ia < 0:
		return b
	case ib < 0:
		return a
	case ib < ia:
		return b
	default:
		return a
	}
}
