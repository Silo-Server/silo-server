package simkl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

// Simkl rates on the integer 1 to 10 scale watchsync uses, so ratings pass
// through unchanged. Only movie and show ratings exist on Simkl; Silo rates
// movies and series, so nothing else is read or written.
//
// Simkl keeps anime, anime movies included, in a separate anime domain with its
// own rated_at activity. Anime movies are movies to Silo and every other anime
// entry is a series (see animeRatingIdentity), so the anime ratings belong to
// both Silo kinds.

const (
	simklCursorRatingsMovies = "simkl.ratings.movies"
	simklCursorRatingsShows  = "simkl.ratings.shows"
	simklCursorRatingsAnime  = "simkl.ratings.anime"

	// Simkl's media type path segments.
	simklTypeMovies = "movies"
	simklTypeShows  = "shows"
	simklTypeAnime  = "anime"

	// The anime_type values that name a Silo kind outright.
	simklAnimeTypeMovie = "movie"
	simklAnimeTypeTV    = "tv"

	// simklEveryRating is the rating filter of a ratings read. Listing every
	// value returns only rated items; without a filter Simkl returns the
	// whole library, unrated items included.
	simklEveryRating = "1,2,3,4,5,6,7,8,9,10"
)

// simklRatingsList is the reply to GET /sync/ratings/{type}/{rating}. Each
// read fills only the key of its type, and an account with no ratings of that
// type gets {}. Anime entries wrap their title in "show".
type simklRatingsList struct {
	Movies []simklRatedItem `json:"movies"`
	Shows  []simklRatedItem `json:"shows"`
	Anime  []simklRatedItem `json:"anime"`
}

type simklRatedItem struct {
	UserRating  *float64   `json:"user_rating"`
	UserRatedAt string     `json:"user_rated_at"`
	AnimeType   string     `json:"anime_type"`
	Movie       simklMovie `json:"movie"`
	Show        simklShow  `json:"show"`
}

// simklRatingItem is one entry of a ratings write or removal. A removal sends
// ids only.
type simklRatingItem struct {
	Rating  int      `json:"rating,omitempty"`
	RatedAt string   `json:"rated_at,omitempty"`
	IDs     simklIDs `json:"ids"`
}

// simklRatingsPayload sends Silo series as shows, anime included: Simkl
// resolves an anime title under shows as well as under anime.
type simklRatingsPayload struct {
	Movies []simklRatingItem `json:"movies,omitempty"`
	Shows  []simklRatingItem `json:"shows,omitempty"`
}

// simklRatingsWriteResponse is the reply to POST /sync/ratings and
// /sync/ratings/remove. The not_found lists echo the items Simkl could not
// match as they were sent, with anime folded into shows.
type simklRatingsWriteResponse struct {
	NotFound struct {
		Movies []simklRatingItem `json:"movies"`
		Shows  []simklRatingItem `json:"shows"`
	} `json:"not_found"`
}

// FetchRatings reads the ratings whose Simkl rated_at activity moved since the
// last read. A kind that is read at all is read in full, never with date_from:
// a delta cannot show a removed rating, and only a full read is a complete
// snapshot. Because anime ratings belong to both kinds, a movie or series
// snapshot always includes the anime read, and an anime change re-reads both
// kinds. A kind whose ratings did not move is left out of SnapshotKinds, so
// its absent items stay unknown.
func (p *Provider) FetchRatings(
	ctx context.Context,
	cfg watchsync.ServerConfig,
	conn watchsync.Connection,
) (watchsync.RatingImportBatch, error) {
	activities, err := p.fetchActivities(ctx, cfg, conn)
	if err != nil {
		return watchsync.RatingImportBatch{}, err
	}
	moviesChanged := simklRatingsChanged(conn, simklCursorRatingsMovies, activities.Movies.RatedAt)
	showsChanged := simklRatingsChanged(conn, simklCursorRatingsShows, activities.TVShows.RatedAt)
	animeChanged := simklRatingsChanged(conn, simklCursorRatingsAnime, activities.Anime.RatedAt)
	readMovies := moviesChanged || animeChanged
	readShows := showsChanged || animeChanged

	batch := watchsync.RatingImportBatch{UpdatedCursors: make(map[string]string)}
	anyUntyped := false
	skippedKinds := make(map[string]bool)
	for _, bucket := range []struct {
		read      bool
		listType  string
		cursorKey string
		activity  string
	}{
		{readMovies, simklTypeMovies, simklCursorRatingsMovies, activities.Movies.RatedAt},
		{readShows, simklTypeShows, simklCursorRatingsShows, activities.TVShows.RatedAt},
		{readMovies || readShows, simklTypeAnime, simklCursorRatingsAnime, activities.Anime.RatedAt},
	} {
		if !bucket.read {
			continue
		}
		var list simklRatingsList
		path := "/sync/ratings/" + bucket.listType + "/" + simklEveryRating
		if err := p.do(ctx, http.MethodGet, path, cfg, conn.AccessToken, nil, &list); err != nil {
			return watchsync.RatingImportBatch{}, err
		}
		rows, untyped, skipped, warnings := ratingRowsFromList(list, bucket.listType, p.Key())
		batch.Rows = append(batch.Rows, rows...)
		batch.Warnings = append(batch.Warnings, warnings...)
		anyUntyped = anyUntyped || untyped
		for kind := range skipped {
			skippedKinds[kind] = true
		}
		if bucket.activity != "" {
			batch.UpdatedCursors[bucket.cursorKey] = bucket.activity
		}
	}
	// An anime entry without a movie or tv type is read as a series, but it
	// may be an anime movie, which the movie read never returns. The movie read
	// is then not provably complete, so movie removals wait for a read without
	// such entries rather than risk reading a rated anime movie as removed.
	// A rated entry skipped for lack of an id is a title the read did not
	// return, so its kind is not a complete snapshot either.
	if readMovies && !anyUntyped && !skippedKinds[historyimport.KindMovie] {
		batch.SnapshotKinds = append(batch.SnapshotKinds, historyimport.KindMovie)
	}
	if readMovies && anyUntyped {
		batch.Warnings = append(batch.Warnings, "simkl returned rated anime without a movie or tv type; skipped movie rating removals")
	}
	if readShows && !skippedKinds[historyimport.KindSeries] {
		batch.SnapshotKinds = append(batch.SnapshotKinds, historyimport.KindSeries)
	}
	return batch, nil
}

// simklRatingsChanged reports whether a rated_at activity moved since the
// cursor. A null activity means the account never rated that type, which is
// no change: shouldSkipSimklBucket alone would re-read such a bucket, and the
// kinds that share it, on every run.
func simklRatingsChanged(conn watchsync.Connection, cursorKey, activity string) bool {
	if strings.TrimSpace(activity) == "" {
		return false
	}
	return !shouldSkipSimklBucket(conn.SyncCursors[cursorKey], activity)
}

// ratingRowsFromList maps one ratings read. listType is the type the read
// asked for; only that key of the reply is used. untyped reports a rated anime
// entry whose anime_type names neither a movie nor a series. skippedKinds holds
// the kind of each rated entry skipped for lack of a usable id; the read is
// not a complete snapshot of those kinds.
func ratingRowsFromList(
	list simklRatingsList,
	listType, provider string,
) (rows []watchsync.RemoteRating, untyped bool, skippedKinds map[string]bool, warnings []string) {
	var items []simklRatedItem
	switch listType {
	case simklTypeMovies:
		items = list.Movies
	case simklTypeShows:
		items = list.Shows
	case simklTypeAnime:
		items = list.Anime
	}
	rows = make([]watchsync.RemoteRating, 0, len(items))
	skippedKinds = make(map[string]bool)
	for _, item := range items {
		if item.UserRating == nil {
			continue
		}
		var (
			kind  string
			title string
			year  int
			ids   simklIDs
		)
		switch listType {
		case simklTypeMovies:
			kind, title, year, ids = historyimport.KindMovie, item.Movie.Title, item.Movie.Year, item.Movie.IDs
		case simklTypeAnime:
			kind, ids = animeRatingIdentity(item.AnimeType, item.Show.IDs)
			untyped = untyped || !typedAnime(item.AnimeType)
			title, year = item.Show.Title, item.Show.Year
		default:
			kind, title, year, ids = historyimport.KindSeries, item.Show.Title, item.Show.Year, item.Show.IDs
		}
		key := showKey(ids)
		if kind == historyimport.KindMovie {
			key = movieKey(ids)
		}
		if key == "" {
			skippedKinds[kind] = true
			warnings = append(warnings, "simkl "+kind+" rating skipped because it has no usable id; skipped "+kind+" rating removals")
			continue
		}
		rows = append(rows, watchsync.RemoteRating{
			RemoteFavorite: watchsync.RemoteFavorite{
				Provider:        provider,
				ProviderItemKey: key,
				Kind:            kind,
				Title:           title,
				Year:            year,
				IMDbID:          ids.IMDb,
				TMDBID:          intString(ids.TMDB),
				TVDBID:          intString(ids.TVDB),
			},
			Rating:  int(math.Round(*item.UserRating)),
			RatedAt: parseSimklTime(item.UserRatedAt),
		})
	}
	return rows, untyped, skippedKinds, warnings
}

// typedAnime reports whether an anime_type names a Silo kind outright.
func typedAnime(animeType string) bool {
	switch strings.ToLower(strings.TrimSpace(animeType)) {
	case simklAnimeTypeMovie, simklAnimeTypeTV:
		return true
	default:
		return false
	}
}

// animeRatingIdentity returns the Silo kind and the ids of a rated anime entry
// from its anime_type. A "movie" is a movie and a "tv" entry a series, each
// with every id. Any other type (ova, ona, special, music video) or a missing
// one is a series without its TMDB id: such an entry can carry a TMDB movie
// id, and TMDB numbers movies and series separately, so that id could match an
// unrelated series. The IMDb and TVDB ids are kept.
//
// Simkl's OpenAPI spec does not list anime_type on GET /sync/ratings reads;
// the documented anime entry has only the rating fields, status, and show.
// GET /sync/all-items documents it on every anime entry as nullable (tv,
// movie, ova, ona, special, music video). A ratings read without it is
// therefore expected and takes the missing-type path.
func animeRatingIdentity(animeType string, ids simklIDs) (string, simklIDs) {
	switch strings.ToLower(strings.TrimSpace(animeType)) {
	case simklAnimeTypeMovie:
		return historyimport.KindMovie, ids
	case simklAnimeTypeTV:
		return historyimport.KindSeries, ids
	default:
		ids.TMDB = 0
		return historyimport.KindSeries, ids
	}
}

// parseSimklTime parses a Simkl timestamp. A missing or malformed one is the
// zero time, which the rating merge treats as unknown.
func parseSimklTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// ExportRatings sets movie and series ratings in one batch. Simkl overwrites
// an existing rating, so resending one is harmless.
func (p *Provider) ExportRatings(
	ctx context.Context,
	cfg watchsync.ServerConfig,
	conn watchsync.Connection,
	items []watchsync.LocalRating,
) (watchsync.ExportResult, error) {
	var payload simklRatingsPayload
	sent := make([]watchsync.LocalFavorite, 0, len(items))
	for _, item := range items {
		if item.Rating < 1 || item.Rating > 10 {
			continue
		}
		entry := simklRatingItem{Rating: item.Rating, IDs: localItemIDs(item.LocalFavorite)}
		if !item.RatedAt.IsZero() {
			entry.RatedAt = item.RatedAt.UTC().Format(time.RFC3339)
		}
		if payload.add(item.Kind, entry) {
			sent = append(sent, item.LocalFavorite)
		}
	}
	return p.sendRatings(ctx, "/sync/ratings", cfg, conn, payload, sent)
}

// RemoveRatings clears movie and series ratings in one batch. The title stays
// on the user's Simkl list. Simkl reports a title it cannot match in
// not_found, which the caller treats as already cleared.
func (p *Provider) RemoveRatings(
	ctx context.Context,
	cfg watchsync.ServerConfig,
	conn watchsync.Connection,
	items []watchsync.LocalFavorite,
) (watchsync.ExportResult, error) {
	var payload simklRatingsPayload
	sent := make([]watchsync.LocalFavorite, 0, len(items))
	for _, item := range items {
		if payload.add(item.Kind, simklRatingItem{IDs: localItemIDs(item)}) {
			sent = append(sent, item)
		}
	}
	return p.sendRatings(ctx, "/sync/ratings/remove", cfg, conn, payload, sent)
}

// RatingExportRequiresWatched holds back movie ratings. Rating a released
// movie that is not on the user's Simkl list files it as completed, which
// records it as watched, so Silo sends a movie rating only after the profile
// has watched the movie. Series ratings are sent right away. Rating an
// unlisted show files it as watching with no episodes marked, except that a
// single-episode show is filed as completed; series are not held back for
// that case.
func (p *Provider) RatingExportRequiresWatched(kind string) bool {
	return kind == historyimport.KindMovie
}

// add appends entry under its kind and reports whether it was added. An entry
// with no usable id or of another kind is left out.
func (payload *simklRatingsPayload) add(kind string, entry simklRatingItem) bool {
	if entry.IDs == (simklIDs{}) {
		return false
	}
	switch kind {
	case historyimport.KindMovie:
		payload.Movies = append(payload.Movies, entry)
	case historyimport.KindSeries:
		payload.Shows = append(payload.Shows, entry)
	default:
		return false
	}
	return true
}

// sendRatings posts a ratings payload and maps the reply back to items, the
// entries payload holds. An item goes to NotFound when a not_found echo of the
// same kind shares any id with it, otherwise to Sent. Items are reported by
// media item id only: a provider item key such as tmdb:550 can name both a
// movie and a series, so reporting it could settle the wrong item.
func (p *Provider) sendRatings(
	ctx context.Context,
	path string,
	cfg watchsync.ServerConfig,
	conn watchsync.Connection,
	payload simklRatingsPayload,
	items []watchsync.LocalFavorite,
) (watchsync.ExportResult, error) {
	if len(items) == 0 {
		return watchsync.ExportResult{}, nil
	}
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return watchsync.ExportResult{}, fmt.Errorf("encode simkl ratings payload: %w", err)
	}
	var response simklRatingsWriteResponse
	if err := p.do(ctx, http.MethodPost, path, cfg, conn.AccessToken, &body, &response); err != nil {
		return watchsync.ExportResult{}, err
	}
	missing := simklIDIndex{}
	for _, movie := range response.NotFound.Movies {
		missing.add(historyimport.KindMovie, movie.IDs)
	}
	for _, show := range response.NotFound.Shows {
		missing.add(historyimport.KindSeries, show.IDs)
	}
	result := watchsync.ExportResult{Sent: make([]string, 0, len(items))}
	for _, item := range items {
		if item.MediaItemID == "" {
			continue
		}
		if missing.matches(item.Kind, localItemIDs(item)) {
			result.NotFound = append(result.NotFound, item.MediaItemID)
			continue
		}
		result.Sent = append(result.Sent, item.MediaItemID)
	}
	return result, nil
}

// simklIDIndex matches the items Simkl echoes in a not_found list to the
// request items that produced them. An echo matches an item of the same kind
// when the two share ANY id, so an echo that carries a different id subset
// than the item's preferred key still matches. Ids are qualified by Silo kind
// because TMDB and TVDB number movies and series separately. Create one with
// simklIDIndex{}.
type simklIDIndex map[string]struct{}

// add records every id in ids under kind.
func (idx simklIDIndex) add(kind string, ids simklIDs) {
	for _, key := range historyIDMatchKeys(ids) {
		idx[kind+":"+key] = struct{}{}
	}
}

// matches reports whether any id in ids was added under kind.
func (idx simklIDIndex) matches(kind string, ids simklIDs) bool {
	for _, key := range historyIDMatchKeys(ids) {
		if _, ok := idx[kind+":"+key]; ok {
			return true
		}
	}
	return false
}
