package mdblist

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

// MDBList rates on the integer 1 to 10 scale watchsync uses, so ratings pass
// through unchanged. Only movie and show ratings are read and written: Silo
// rates movies and series, not seasons or episodes.

// maxRatingWriteEntries caps the titles in one rating write. MDBList rejects a
// write that lists more than 200 shows with a 400; capping movies and shows
// together keeps every request under that limit.
const maxRatingWriteEntries = 200

type mdblistRatedMovie struct {
	RatedAt time.Time `json:"rated_at"`
	// Rating is a float so that 8.0 decodes; null leaves it 0, which is unrated.
	Rating float64      `json:"rating"`
	Movie  mdblistMovie `json:"movie"`
}

type mdblistRatedShow struct {
	RatedAt time.Time   `json:"rated_at"`
	Rating  float64     `json:"rating"`
	Show    mdblistShow `json:"show"`
}

// ratedEntry is one rated title as decoded, kept with its raw JSON. The raw
// JSON identifies an entry that maps to no rating row when a read checks its
// pages for a repeated entry.
type ratedEntry[T any] struct {
	value T
	raw   json.RawMessage
}

func (e *ratedEntry[T]) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, &e.value); err != nil {
		return err
	}
	e.raw = append(json.RawMessage(nil), data...)
	return nil
}

// mdblistRatingsResponse is one page of GET /sync/ratings. Shows stays raw so
// the read can tell a missing shows list from an empty one: MDBList's
// documented sample has no shows key, and a list that is not there cannot say
// a show is unrated. Seasons and episodes are only counted, because the
// pagination counts them too.
type mdblistRatingsResponse struct {
	Movies     []ratedEntry[mdblistRatedMovie] `json:"movies"`
	Shows      json.RawMessage                 `json:"shows"`
	Seasons    []json.RawMessage               `json:"seasons"`
	Episodes   []json.RawMessage               `json:"episodes"`
	Pagination *mdblistRatingsPagination       `json:"pagination"`
}

// mdblistRatingsPagination is the pagination of a ratings page. The schema
// documents {total, limit, offset, next_cursor}, where total counts the
// entries of the whole read and offset skips entries, both across movies,
// shows, seasons, and episodes together. The older sample reports per-type
// totals with has_more instead.
type mdblistRatingsPagination struct {
	mdblistPagination
	Total         *int `json:"total"`
	TotalMovies   *int `json:"total_movies"`
	TotalShows    *int `json:"total_shows"`
	TotalSeasons  *int `json:"total_seasons"`
	TotalEpisodes *int `json:"total_episodes"`
}

// entryTotal returns the number of entries in the whole read: total, or else
// the sum of the per-type totals. It reports false when the page has neither.
func (p *mdblistRatingsPagination) entryTotal() (int, bool) {
	if p == nil {
		return 0, false
	}
	if p.Total != nil {
		return *p.Total, true
	}
	sum, found := 0, false
	for _, n := range []*int{p.TotalMovies, p.TotalShows, p.TotalSeasons, p.TotalEpisodes} {
		if n != nil {
			sum += *n
			found = true
		}
	}
	return sum, found
}

// base returns the pagination fields every MDBList read shares, or nil.
func (p *mdblistRatingsPagination) base() *mdblistPagination {
	if p == nil {
		return nil
	}
	return &p.mdblistPagination
}

// mdblistSyncWriteResponse is the part of a POST /sync/ratings or
// /sync/ratings/remove response that decides the outcome. MDBList documents
// the counts as updated, deleted, or removed, with or without not_found and
// errors; only not_found and errors matter, and both are optional.
type mdblistSyncWriteResponse struct {
	NotFound json.RawMessage `json:"not_found"`
	Errors   json.RawMessage `json:"errors"`
}

type mdblistRatingEntry struct {
	IDs     mdblistIDs `json:"ids"`
	Rating  int        `json:"rating,omitempty"`
	RatedAt string     `json:"rated_at,omitempty"`
}

type mdblistRatingsPayload struct {
	Movies []mdblistRatingEntry `json:"movies,omitempty"`
	Shows  []mdblistRatingEntry `json:"shows,omitempty"`
}

// ratingWrite is one title of a rating write, kept with the media item it
// reports on.
type ratingWrite struct {
	mediaItemID string
	kind        string
	entry       mdblistRatingEntry
}

// FetchRatings reads every movie and show rating. Once every page is read the
// movie list is a complete snapshot. The show list is one only when MDBList
// returned a shows list at all. A kind with a rated title Silo cannot identify
// is left out of the snapshot, because that title could be any local item.
// When the pagination reports how many entries the read holds, a read that
// ends with fewer is not a snapshot of either kind. Neither is a read that
// returns an entry twice: a rating added or changed between page requests
// shifts the pages, so a later page repeats an entry and skips another, and
// the repeat hides the skip from the count. The skipped entry's kind is
// unknown, so both kinds are left out.
func (p *Provider) FetchRatings(
	ctx context.Context,
	_ watchsync.ServerConfig,
	conn watchsync.Connection,
) (watchsync.RatingImportBatch, error) {
	var rows []watchsync.RemoteRating
	hasShows := false
	unidentified := make(map[string]int)
	page := mdblistPageState{}
	// read counts entries of every type; total is the largest entry total a
	// page reported, or -1.
	read, total := 0, -1
	// seen keys every entry read so far: a rating row by kind and provider
	// item key, any other entry by its raw JSON.
	seen := make(map[string]struct{})
	repeated := false
	// offsetPaged records that a later page was requested by offset. Offsets
	// shift when ratings change mid-read, and two changes can skip an entry
	// without repeating one or changing the count, so such a read is never a
	// complete snapshot. Cursor pages do not shift.
	offsetPaged := false
	see := func(key string) {
		if _, dup := seen[key]; dup {
			repeated = true
		}
		seen[key] = struct{}{}
	}
	for {
		var payload mdblistRatingsResponse
		if err := p.do(ctx, http.MethodGet, page.path("/sync/ratings"), conn.AccessToken, nil, &payload); err != nil {
			return watchsync.RatingImportBatch{}, err
		}
		shows, present, err := ratedShows(payload.Shows)
		if err != nil {
			return watchsync.RatingImportBatch{}, fmt.Errorf("decode mdblist rated shows: %w", err)
		}
		// A shows list on any page means MDBList reports show ratings; pages
		// may leave out a list they have nothing for.
		hasShows = hasShows || present
		for _, entry := range payload.Movies {
			item := entry.value
			row, ok := p.ratingRow(historyimport.KindMovie, item.Rating, item.RatedAt, item.Movie.Title, item.Movie.Year, item.Movie.IDs)
			if ok {
				rows = append(rows, row)
				see(row.Kind + " " + row.ProviderItemKey)
				continue
			}
			if providerRating(item.Rating) != 0 {
				unidentified[historyimport.KindMovie]++
			}
			see("movie entry " + string(entry.raw))
		}
		for _, entry := range shows {
			item := entry.value
			row, ok := p.ratingRow(historyimport.KindSeries, item.Rating, item.RatedAt, item.Show.Title, item.Show.Year, item.Show.IDs)
			if ok {
				rows = append(rows, row)
				see(row.Kind + " " + row.ProviderItemKey)
				continue
			}
			if providerRating(item.Rating) != 0 {
				unidentified[historyimport.KindSeries]++
			}
			see("show entry " + string(entry.raw))
		}
		// Seasons and episodes count toward total too, so a repeated one
		// hides a skipped entry just the same.
		for _, raw := range payload.Seasons {
			see("season entry " + string(raw))
		}
		for _, raw := range payload.Episodes {
			see("episode entry " + string(raw))
		}
		fetched := len(payload.Movies) + len(shows) + len(payload.Seasons) + len(payload.Episodes)
		read += fetched
		if n, ok := payload.Pagination.entryTotal(); ok {
			total = max(total, n)
		}
		done, err := page.advanceTo(payload.Pagination.base(), fetched, read, total)
		if err != nil {
			return watchsync.RatingImportBatch{}, fmt.Errorf("mdblist ratings pagination: %w", err)
		}
		if done {
			break
		}
		offsetPaged = offsetPaged || page.legacyOffset
	}

	batch := watchsync.RatingImportBatch{Rows: rows}
	if total >= 0 && read < total {
		batch.Warnings = append(batch.Warnings, fmt.Sprintf(
			"mdblist ratings read ended after %d of %d entries; skipped rating removals", read, total))
		return batch, nil
	}
	if repeated {
		batch.Warnings = append(batch.Warnings,
			"mdblist ratings pages repeated an entry, so ratings changed during the read; skipped rating removals")
		return batch, nil
	}
	if offsetPaged {
		batch.Warnings = append(batch.Warnings,
			"mdblist ratings were read by offset, which can skip entries that change during the read; skipped rating removals")
		return batch, nil
	}
	for _, kind := range []string{historyimport.KindMovie, historyimport.KindSeries} {
		if kind == historyimport.KindSeries && !hasShows {
			continue
		}
		if n := unidentified[kind]; n > 0 {
			batch.Warnings = append(batch.Warnings, fmt.Sprintf(
				"mdblist returned %d %s ratings without an IMDb, TMDB, or TVDB id; skipped %s rating removals", n, kind, kind))
			continue
		}
		batch.SnapshotKinds = append(batch.SnapshotKinds, kind)
	}
	return batch, nil
}

// ratingRow maps one rated title. It reports false for an unrated title (a
// null or zero rating) and for one without an IMDb, TMDB, or TVDB id: Silo
// matches the catalog by those, so a title with only MDBList's own id could
// be any local item.
func (p *Provider) ratingRow(kind string, rating float64, ratedAt time.Time, title string, year int, ids mdblistIDs) (watchsync.RemoteRating, bool) {
	value := providerRating(rating)
	if value == 0 {
		return watchsync.RemoteRating{}, false
	}
	if ids.IMDb == "" && ids.TMDB <= 0 && ids.TVDB <= 0 {
		return watchsync.RemoteRating{}, false
	}
	key := movieKey(ids)
	if kind == historyimport.KindSeries {
		key = showKey(ids)
	}
	return watchsync.RemoteRating{
		RemoteFavorite: watchsync.RemoteFavorite{
			Provider:        p.Key(),
			ProviderItemKey: key,
			Kind:            kind,
			Title:           title,
			Year:            year,
			IMDbID:          ids.IMDb,
			TMDBID:          intString(ids.TMDB),
			TVDBID:          intString(ids.TVDB),
		},
		Rating:  value,
		RatedAt: ratedAt,
	}, true
}

// providerRating rounds a rating to the integer scale; 0 means unrated. Values
// outside 1 to 10 pass through for watchsync to reject.
func providerRating(rating float64) int {
	return int(math.Round(rating))
}

// advanceTo is advance for a read that knows its entry total, or -1. While
// fewer than total entries are read, a page without next_cursor does not end
// the read: it goes on by offset until a page comes back empty, and the
// caller checks whether the read reached total. The offset is always the
// count of entries read so far.
func (s *mdblistPageState) advanceTo(pagination *mdblistPagination, fetched, read, total int) (bool, error) {
	hasCursor := pagination != nil && strings.TrimSpace(pagination.NextCursor) != ""
	if total < 0 || read >= total || hasCursor {
		done, err := s.advance(pagination, fetched)
		if s.legacyOffset {
			s.offset = read
		}
		return done, err
	}
	if fetched == 0 {
		return true, nil
	}
	s.cursor = ""
	s.legacyOffset = true
	s.offset = read
	return false, nil
}

// ratedShows decodes a shows list and reports whether the response carried
// one. A null list counts as missing.
func ratedShows(raw json.RawMessage) ([]ratedEntry[mdblistRatedShow], bool, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, false, nil
	}
	var shows []ratedEntry[mdblistRatedShow]
	if err := json.Unmarshal(raw, &shows); err != nil {
		return nil, false, err
	}
	return shows, true, nil
}

// ExportRatings sets movie and show ratings. MDBList replaces an existing
// rating, so resending one is harmless.
func (p *Provider) ExportRatings(
	ctx context.Context,
	_ watchsync.ServerConfig,
	conn watchsync.Connection,
	items []watchsync.LocalRating,
) (watchsync.ExportResult, error) {
	result := newSyncWriteResult("ratings", false, len(items))
	writes := make([]ratingWrite, 0, len(items))
	for _, item := range items {
		ids, ok := listItemIDs(item.LocalFavorite)
		if !ok {
			result.skip(item.MediaItemID, "MDBList rating sync requires a movie or series with an external ID")
			continue
		}
		if item.Rating < 1 || item.Rating > 10 {
			result.skip(item.MediaItemID, "MDBList ratings must be from 1 to 10")
			continue
		}
		entry := mdblistRatingEntry{IDs: ids, Rating: item.Rating}
		if !item.RatedAt.IsZero() {
			entry.RatedAt = item.RatedAt.UTC().Format(time.RFC3339)
		}
		writes = append(writes, ratingWrite{mediaItemID: item.MediaItemID, kind: item.Kind, entry: entry})
	}
	if err := p.writeRatings(ctx, conn, "/sync/ratings", writes, result); err != nil {
		return watchsync.ExportResult{}, err
	}
	return result.exportResult(), nil
}

// RemoveRatings clears movie and show ratings. A title MDBList does not know
// has no rating to clear, so a not_found answer reconciles the batch.
func (p *Provider) RemoveRatings(
	ctx context.Context,
	_ watchsync.ServerConfig,
	conn watchsync.Connection,
	items []watchsync.LocalFavorite,
) (watchsync.ExportResult, error) {
	result := newSyncWriteResult("rating removals", true, len(items))
	writes := make([]ratingWrite, 0, len(items))
	for _, item := range items {
		ids, ok := listItemIDs(item)
		if !ok {
			result.skip(item.MediaItemID, "MDBList rating sync requires a movie or series with an external ID")
			continue
		}
		writes = append(writes, ratingWrite{mediaItemID: item.MediaItemID, kind: item.Kind, entry: mdblistRatingEntry{IDs: ids}})
	}
	if err := p.writeRatings(ctx, conn, "/sync/ratings/remove", writes, result); err != nil {
		return watchsync.ExportResult{}, err
	}
	return result.exportResult(), nil
}

// writeRatings posts the writes in requests of at most maxRatingWriteEntries
// titles and records each request's outcome.
func (p *Provider) writeRatings(ctx context.Context, conn watchsync.Connection, path string, writes []ratingWrite, result *syncWriteResult) error {
	for start := 0; start < len(writes); start += maxRatingWriteEntries {
		chunk := writes[start:min(start+maxRatingWriteEntries, len(writes))]
		var payload mdblistRatingsPayload
		requested := make([]string, 0, len(chunk))
		for _, write := range chunk {
			if write.kind == historyimport.KindMovie {
				payload.Movies = append(payload.Movies, write.entry)
			} else {
				payload.Shows = append(payload.Shows, write.entry)
			}
			requested = append(requested, write.mediaItemID)
		}
		var body bytes.Buffer
		if err := json.NewEncoder(&body).Encode(payload); err != nil {
			return fmt.Errorf("encode mdblist ratings payload: %w", err)
		}
		var response mdblistSyncWriteResponse
		if err := p.do(ctx, http.MethodPost, path, conn.AccessToken, &body, &response); err != nil {
			return err
		}
		result.request(requested, !emptyJSONValue(response.NotFound), !emptyJSONValue(response.Errors))
	}
	return nil
}
