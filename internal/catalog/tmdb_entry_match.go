package catalog

import (
	"context"
	"strconv"
)

// TMDBExternalIDEnricher fills in, in place, the IMDb and TVDB IDs of TMDB
// entries. A TMDB fetcher implements it when it can look them up.
type TMDBExternalIDEnricher interface {
	EnrichExternalIDs(ctx context.Context, entries []TMDBCollectionEntry) error
}

// TMDBIDFinder looks up the TMDB IDs of catalog titles known only by IMDb or
// TVDB ID. A TMDB fetcher implements it when it can; each title is a TMDB
// request. The result holds one ID per title, 0 when TMDB has none.
type TMDBIDFinder interface {
	FindTMDBIDs(ctx context.Context, itemType string, titles []TitleWithoutTMDBID) ([]int, error)
}

// ExternalIDResolver finds catalog titles by external ID in one batch per
// title type. *ItemRepository satisfies it.
type ExternalIDResolver interface {
	GetByExternalIDs(ctx context.Context, batch ExternalIDBatch, itemType string) (*ExternalIDLookup, error)
	TitlesWithoutTMDBID(ctx context.Context, itemType string, limit int) ([]TitleWithoutTMDBID, error)
}

// MatchTMDBEntries returns, for each entry in order, the content_id of the
// catalog title it names, or "" when there is none. Entries other than movies
// and TV shows never match.
//
// Entries match on the IDs they carry first, which for TMDB sources is the
// TMDB ID alone. An entry left unmatched can still name a catalog title only
// when that title has no usable TMDB ID of its own (see TitlesWithoutTMDBID),
// so further TMDB requests are made only when the catalog holds such titles,
// and then for whichever side is smaller: those titles (through fetcher's TMDBIDFinder) or the unmatched
// entries (through its TMDBExternalIDEnricher, which writes the IMDb and TVDB
// IDs it finds back into entries). Titles the catalog does not hold cost no
// TMDB request.
func MatchTMDBEntries(ctx context.Context, items ExternalIDResolver, fetcher any, entries []TMDBCollectionEntry) ([]string, error) {
	contentIDs := make([]string, len(entries))
	candidates := make([]int, 0, len(entries))
	for i, entry := range entries {
		if tmdbEntryItemType(entry) != "" {
			candidates = append(candidates, i)
		}
	}
	if err := matchTMDBEntryIndexes(ctx, items, entries, candidates, contentIDs); err != nil {
		return nil, err
	}
	for _, itemType := range []string{playableTypeMovie, playableTypeSeries} {
		var unmatched []int
		for _, i := range candidates {
			if contentIDs[i] == "" && entries[i].ID > 0 && tmdbEntryItemType(entries[i]) == itemType {
				unmatched = append(unmatched, i)
			}
		}
		if len(unmatched) == 0 {
			continue
		}
		if err := matchUnmatchedTMDBEntries(ctx, items, fetcher, itemType, entries, unmatched, contentIDs); err != nil {
			return nil, err
		}
	}
	return contentIDs, nil
}

// matchUnmatchedTMDBEntries matches the listed entries of itemType, which
// their TMDB IDs did not match, against the catalog's titles without a TMDB
// ID.
func matchUnmatchedTMDBEntries(ctx context.Context, items ExternalIDResolver, fetcher any, itemType string, entries []TMDBCollectionEntry, unmatched []int, contentIDs []string) error {
	// One past the entries is enough to tell which side is smaller.
	titles, err := items.TitlesWithoutTMDBID(ctx, itemType, len(unmatched)+1)
	if err != nil || len(titles) == 0 {
		return err
	}
	if finder, ok := fetcher.(TMDBIDFinder); ok && len(titles) <= len(unmatched) {
		tmdbIDs, err := finder.FindTMDBIDs(ctx, itemType, titles)
		if err != nil {
			return err
		}
		byTMDB := make(map[int]string, len(titles))
		for j, id := range tmdbIDs {
			if _, taken := byTMDB[id]; id > 0 && !taken {
				byTMDB[id] = titles[j].ContentID
			}
		}
		for _, i := range unmatched {
			contentIDs[i] = byTMDB[entries[i].ID]
		}
		return nil
	}
	enricher, ok := fetcher.(TMDBExternalIDEnricher)
	if !ok {
		return nil
	}
	subset := make([]TMDBCollectionEntry, len(unmatched))
	for j, i := range unmatched {
		subset[j] = entries[i]
	}
	if err := enricher.EnrichExternalIDs(ctx, subset); err != nil {
		return err
	}
	for j, i := range unmatched {
		entries[i].IMDbID, entries[i].TVDBID = subset[j].IMDbID, subset[j].TVDBID
	}
	return matchTMDBEntryIndexes(ctx, items, entries, unmatched, contentIDs)
}

// matchTMDBEntryIndexes resolves entries[i] for each listed index into
// contentIDs[i], with one catalog lookup per title type.
func matchTMDBEntryIndexes(ctx context.Context, items ExternalIDResolver, entries []TMDBCollectionEntry, indexes []int, contentIDs []string) error {
	var movies, series ExternalIDBatch
	for _, i := range indexes {
		entry := entries[i]
		batch := &movies
		if tmdbEntryItemType(entry) == playableTypeSeries {
			batch = &series
		}
		if entry.ID > 0 {
			batch.TMDBIDs = append(batch.TMDBIDs, strconv.Itoa(entry.ID))
		}
		if entry.IMDbID != "" {
			batch.IMDbIDs = append(batch.IMDbIDs, entry.IMDbID)
		}
		if entry.TVDBID > 0 {
			batch.TVDBIDs = append(batch.TVDBIDs, strconv.Itoa(entry.TVDBID))
		}
	}
	movieLookup, err := items.GetByExternalIDs(ctx, movies, playableTypeMovie)
	if err != nil {
		return err
	}
	seriesLookup, err := items.GetByExternalIDs(ctx, series, playableTypeSeries)
	if err != nil {
		return err
	}
	for _, i := range indexes {
		entry := entries[i]
		itemType := tmdbEntryItemType(entry)
		lookup := movieLookup
		if itemType == playableTypeSeries {
			lookup = seriesLookup
		}
		var tmdbID, tvdbID string
		if entry.ID > 0 {
			tmdbID = strconv.Itoa(entry.ID)
		}
		if entry.TVDBID > 0 {
			tvdbID = strconv.Itoa(entry.TVDBID)
		}
		contentIDs[i] = lookup.Resolve(itemType, tvdbID, tmdbID, entry.IMDbID)
	}
	return nil
}

// tmdbEntryItemType is the catalog title type a TMDB entry names: "movie" or
// "series", or "" for anything else (TMDB's trending feed also lists people).
// Franchise parts carry no media type and are movies.
func tmdbEntryItemType(entry TMDBCollectionEntry) string {
	switch entry.MediaType {
	case playableTypeMovie, "":
		return playableTypeMovie
	case "tv":
		return playableTypeSeries
	default:
		return ""
	}
}
