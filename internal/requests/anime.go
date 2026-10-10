package requests

import (
	"context"
	"log/slog"
	"slices"

	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
)

// animeKeywordID is TMDB's "anime" keyword id, the one Seerr checks
// (server/api/themoviedb/constants.ts).
const animeKeywordID = 210024

// animationGenreID is TMDB's Animation genre.
const animationGenreID = 16

// AnimeIndex answers whether an AniDB-based anime list names a title, by the
// TVDB and IMDb IDs TMDB reports. *animeids.Store implements it.
type AnimeIndex interface {
	Listed(ctx context.Context, movie bool, tvdbID int, imdbID string) (bool, error)
}

// SetAnimeIndex lets anime detection consult the anime list.
func (s *Service) SetAnimeIndex(index AnimeIndex) { s.animeIndex = index }

// detectAnime decides whether a title is anime. Anime here means Japanese
// animation. TMDB's anime keyword alone misses about one anime series in
// eight and one film in three, so a title also counts when TMDB files it as
// Animation from Japan or in Japanese, or when the AniDB-based list names it.
// AniDB also catalogs Chinese and Korean animation, which only counts when
// TMDB itself tags it anime or it is a Japanese co-production; an admin
// routes it with a genre and language rule instead.
func detectAnime(detail *tmdb.MediaDetail, listed bool) bool {
	if detail == nil {
		return false
	}
	if slices.Contains(detail.KeywordIDs, animeKeywordID) {
		return true
	}
	japanese := detail.OriginalLanguage == "ja" || slices.Contains(detail.OriginCountries, "JP")
	if japanese && slices.Contains(detail.GenreIDs, animationGenreID) {
		return true
	}
	if !listed {
		return false
	}
	// A Japanese co-production still counts; Chinese or Korean animation by
	// language or origin country does not on the list's word alone.
	if !japanese && chineseOrKorean(detail) {
		return false
	}
	// The list also names Western series AniDB catalogs (The Boondocks). A
	// series' anime flag can set Sonarr's series type, and with it episode
	// numbering, so a listed series also needs a Japanese signal; a film is
	// only routed, so the list alone will do.
	return detail.MediaType != string(MediaTypeSeries) || japanese
}

// chineseOrKorean reports whether TMDB files a title in Chinese or Korean or
// as made in China or Korea.
func chineseOrKorean(detail *tmdb.MediaDetail) bool {
	switch detail.OriginalLanguage {
	case "zh", "cn", "ko":
		return true
	}
	return slices.Contains(detail.OriginCountries, "CN") || slices.Contains(detail.OriginCountries, "KR")
}

// animeListed asks the anime list about a title. The list only adds to TMDB's
// own signals, so a failed lookup counts as not listed rather than holding the
// request up.
func (s *Service) animeListed(ctx context.Context, detail *tmdb.MediaDetail) bool {
	if s.animeIndex == nil || detail == nil {
		return false
	}
	listed, err := s.animeIndex.Listed(ctx, detail.MediaType == string(MediaTypeMovie), detail.TVDBID, detail.IMDbID)
	if err != nil {
		slog.WarnContext(ctx, "requests: anime list lookup failed", "component", "requests",
			"tmdb_id", detail.ID, "err", err)
		return false
	}
	return listed
}

// routingFacts reads a request's routing facts off a TMDB detail, asking the
// anime list first.
func (s *Service) routingFacts(ctx context.Context, detail *tmdb.MediaDetail) RoutingFacts {
	return routingFactsFrom(detail, s.animeListed(ctx, detail), s.now())
}
