package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/models"
)

// HandleGetAudiobook serves GET /api/v1/audiobooks/{id}. Returns the
// audiobook media_item, its media_files (with embedded chapters),
// author/narrator from item_people (kinds 7/8), and the caller's
// per-profile listening progress.
func (h *AudiobookHandler) HandleGetAudiobook(w http.ResponseWriter, r *http.Request) {
	contentID := chi.URLParam(r, "id")
	if contentID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "id is required")
		return
	}

	items, err := h.Items.GetByIDsWithAccess(r.Context(), []string{contentID}, requestAccessFilter(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "load audiobook failed")
		return
	}
	if len(items) == 0 {
		writeError(w, http.StatusNotFound, "not_found", "audiobook not found")
		return
	}
	item := items[0]
	if item == nil || item.Type != "audiobook" {
		writeError(w, http.StatusNotFound, "not_found", "audiobook not found")
		return
	}

	files, err := h.Files.GetByContentID(r.Context(), contentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "load files failed")
		return
	}

	author, narrator, err := h.fetchAuthorNarrator(r, contentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "load people failed")
		return
	}

	progress := h.fetchProgress(r, contentID)

	alsoByAuthor := h.fetchAlsoByAuthor(r.Context(), contentID)
	inSeries := h.fetchInSeries(r.Context(), contentID)
	similar := h.fetchSimilar(r.Context(), contentID)
	otherNarrations := h.fetchOtherNarrations(r.Context(), contentID)

	resp := audiobookDetailResponse{
		Audiobook: audiobookDetailItem{
			ContentID: item.ContentID,
			Title:     item.Title,
			Year:      item.Year,
			Overview:  item.Overview,
			PosterURL: h.presignAudiobookPoster(r.Context(), item.PosterPath),
			Publisher: pickFirstString(item.Studios),
			Genres:    append([]string(nil), item.Genres...),
		},
		Author:            author,
		Narrator:          narrator,
		Files:             audiobookDetailFiles(files),
		Progress:          progress,
		AlsoByAuthor:      alsoByAuthor,
		InSeries:          inSeries,
		SimilarAudiobooks: similar,
		OtherNarrations:   otherNarrations,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// fetchAuthorNarrator retrieves the first author (kind=7) and narrator (kind=8)
// name from item_people for the given content ID.
func (h *AudiobookHandler) fetchAuthorNarrator(r *http.Request, contentID string) (author, narrator string, err error) {
	people, err := h.Items.GetPeople(r.Context(), contentID)
	if err != nil {
		return "", "", err
	}
	for _, p := range people {
		switch p.Kind {
		case models.PersonKindAuthor:
			if author == "" {
				author = p.Name
			}
		case models.PersonKindNarrator:
			if narrator == "" {
				narrator = p.Name
			}
		}
		if author != "" && narrator != "" {
			break
		}
	}
	return author, narrator, nil
}

// fetchAlsoByAuthor returns other audiobooks sharing any author (item_people
// kind=PersonKindAuthor) with the given book. Capped at 12 to keep the rail
// snappy on the client. Returns an empty slice when there is no author or
// no other matching titles.
func (h *AudiobookHandler) fetchAlsoByAuthor(ctx context.Context, contentID string) []audiobookRelatedItem {
	if h.Files == nil {
		return nil
	}
	const q = `
		SELECT m2.content_id, m2.title, COALESCE(m2.year, 0), COALESCE(m2.poster_path, '')
		FROM item_people ip1
		JOIN item_people ip2
		  ON ip2.person_id = ip1.person_id
		 AND ip2.kind = ip1.kind
		 AND ip2.content_id <> ip1.content_id
		JOIN media_items m2
		  ON m2.content_id = ip2.content_id
		 AND m2.type = 'audiobook'
		WHERE ip1.content_id = $1
		  AND ip1.kind = $2
		ORDER BY m2.year DESC NULLS LAST, LOWER(m2.sort_title)
		LIMIT 12
	`
	rows, err := h.Files.Pool().Query(ctx, q, contentID, models.PersonKindAuthor)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]audiobookRelatedItem, 0, 12)
	seen := make(map[string]struct{})
	for rows.Next() {
		var it audiobookRelatedItem
		var poster string
		if err := rows.Scan(&it.ContentID, &it.Title, &it.Year, &poster); err != nil {
			return nil
		}
		if _, dup := seen[it.ContentID]; dup {
			continue
		}
		seen[it.ContentID] = struct{}{}
		it.PosterURL = h.presignPoster(ctx, poster)
		out = append(out, it)
	}
	return out
}

// fetchSimilar returns the "You might also like" rail. Prefers embedding-based
// nearest-neighbor search when the source book has an embedding; falls back to
// shared-genre matching otherwise (book is brand new, embedding job hasn't run,
// or the embedding lookup errored). The genre fallback excludes same-author
// books so the rail complements rather than duplicates also_by_author.
func (h *AudiobookHandler) fetchSimilar(ctx context.Context, contentID string) []audiobookRelatedItem {
	if h.Recs != nil {
		if items := h.fetchSimilarByEmbedding(ctx, contentID); items != nil {
			return items
		}
	}
	return h.fetchSimilarByGenres(ctx, contentID)
}

// fetchSimilarByEmbedding returns nearest-neighbor audiobooks by cosine
// similarity on the gemini embedding vector. Returns nil (not an empty slice)
// when the source book has no embedding yet, so the caller can fall back to
// the genre-based recommender.
func (h *AudiobookHandler) fetchSimilarByEmbedding(ctx context.Context, contentID string) []audiobookRelatedItem {
	if h.Recs == nil || h.Files == nil {
		return nil
	}
	emb, err := h.Recs.GetEmbedding(ctx, contentID)
	if err != nil || emb == nil {
		return nil
	}
	// Pull extra candidates so we can drop the source book's own author from
	// the rail (they belong in also_by_author).
	scored, err := h.Recs.FindSimilar(ctx, emb, []string{contentID}, "audiobook", 36)
	if err != nil || len(scored) == 0 {
		return nil
	}
	ids := make([]string, 0, len(scored))
	for _, s := range scored {
		ids = append(ids, s.MediaItemID)
	}
	const q = `
		WITH this_author AS (
			SELECT person_id FROM item_people WHERE content_id = $1 AND kind = $2
		)
		SELECT m.content_id, m.title, COALESCE(m.year, 0), COALESCE(m.poster_path, '')
		FROM media_items m
		WHERE m.content_id = ANY($3)
		  AND m.type = 'audiobook'
		  AND NOT EXISTS (
			SELECT 1 FROM item_people ip
			WHERE ip.content_id = m.content_id
			  AND ip.kind = $2
			  AND ip.person_id IN (SELECT person_id FROM this_author)
		  )
	`
	rows, err := h.Files.Pool().Query(ctx, q, contentID, models.PersonKindAuthor, ids)
	if err != nil {
		return nil
	}
	defer rows.Close()
	byID := make(map[string]audiobookRelatedItem, len(scored))
	for rows.Next() {
		var it audiobookRelatedItem
		var poster string
		if err := rows.Scan(&it.ContentID, &it.Title, &it.Year, &poster); err != nil {
			return nil
		}
		it.PosterURL = h.presignPoster(ctx, poster)
		byID[it.ContentID] = it
	}
	out := make([]audiobookRelatedItem, 0, 12)
	for _, s := range scored {
		if it, ok := byID[s.MediaItemID]; ok {
			out = append(out, it)
			if len(out) >= 12 {
				break
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// fetchOtherNarrations returns sibling audiobook items that share the same
// "core" title (after stripping the common "read by X" / "(Read by X)" /
// "(UK Version: Read by X)" suffixes) and the same author. Powers the
// narrator picker dropdown on the detail page when a single book has
// multiple narration editions stored as separate items in the catalog.
func (h *AudiobookHandler) fetchOtherNarrations(ctx context.Context, contentID string) []audiobookNarration {
	if h.Files == nil {
		return nil
	}
	// The regex strips a trailing block of the form "[ separator] [( ]read by ...[) ]".
	// Tested against patterns in the corpus: "Foo Read By X",
	// "Foo - read by X", "Foo (Read by X)", "Foo (UK Version: Read by X)".
	const stripRE = `\s*\(?\s*[-:,]?\s*(UK Version:?|US Version:?)?\s*[Rr]ead [Bb]y [^()]+\)?\s*$`
	const q = `
		WITH this_book AS (
			SELECT trim(regexp_replace(title, $1, '', 'g')) AS norm_title
			FROM media_items WHERE content_id = $2
		),
		this_author AS (
			SELECT person_id FROM item_people WHERE content_id = $2 AND kind = $3
		)
		SELECT
			m.content_id,
			m.title,
			COALESCE(MAX(p.name), '') AS narrator,
			COALESCE(m.year, 0)
		FROM media_items m
		LEFT JOIN item_people ipn ON ipn.content_id = m.content_id AND ipn.kind = $4
		LEFT JOIN people p ON p.id = ipn.person_id
		WHERE m.type = 'audiobook'
		  AND m.content_id <> $2
		  AND trim(regexp_replace(m.title, $1, '', 'g')) = (SELECT norm_title FROM this_book)
		  AND EXISTS (
			SELECT 1 FROM item_people ip
			WHERE ip.content_id = m.content_id
			  AND ip.kind = $3
			  AND ip.person_id IN (SELECT person_id FROM this_author)
		  )
		GROUP BY m.content_id, m.title, m.year
		ORDER BY m.year DESC NULLS LAST, m.title
		LIMIT 8
	`
	rows, err := h.Files.Pool().Query(ctx, q,
		stripRE, contentID, models.PersonKindAuthor, models.PersonKindNarrator)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]audiobookNarration, 0, 4)
	for rows.Next() {
		var n audiobookNarration
		if err := rows.Scan(&n.ContentID, &n.Title, &n.Narrator, &n.Year); err != nil {
			return nil
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// fetchSimilarByGenres returns up to 12 audiobooks sharing genres with the
// given book, excluding books by the same author so the rail complements
// rather than duplicates also_by_author. Used as a fallback when no
// embedding exists for the source yet.
func (h *AudiobookHandler) fetchSimilarByGenres(ctx context.Context, contentID string) []audiobookRelatedItem {
	if h.Files == nil {
		return nil
	}
	const q = `
		WITH this_genres AS (
			SELECT unnest(genres) AS g FROM media_items WHERE content_id = $1
		),
		this_author AS (
			SELECT person_id FROM item_people WHERE content_id = $1 AND kind = $2
		)
		SELECT m.content_id, m.title, COALESCE(m.year, 0), COALESCE(m.poster_path, '')
		FROM media_items m
		WHERE m.type = 'audiobook'
		  AND m.content_id <> $1
		  AND m.genres && (SELECT array_agg(g) FROM this_genres)
		  AND NOT EXISTS (
			SELECT 1 FROM item_people ip
			WHERE ip.content_id = m.content_id
			  AND ip.kind = $2
			  AND ip.person_id IN (SELECT person_id FROM this_author)
		  )
		ORDER BY
			cardinality(ARRAY(SELECT unnest(m.genres) INTERSECT SELECT g FROM this_genres)) DESC,
			COALESCE(m.year, 0) DESC,
			LOWER(m.sort_title)
		LIMIT 12
	`
	rows, err := h.Files.Pool().Query(ctx, q, contentID, models.PersonKindAuthor)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]audiobookRelatedItem, 0, 12)
	for rows.Next() {
		var it audiobookRelatedItem
		var poster string
		if err := rows.Scan(&it.ContentID, &it.Title, &it.Year, &poster); err != nil {
			return nil
		}
		it.PosterURL = h.presignPoster(ctx, poster)
		out = append(out, it)
	}
	return out
}

// fetchInSeries returns the series the audiobook belongs to (if any) along
// with the ordered list of sibling entries. Driven by the audiobook_series
// table populated by either the scanner (tag-derived) or migration 145's
// best-effort title-pattern backfill.
func (h *AudiobookHandler) fetchInSeries(ctx context.Context, contentID string) *audiobookSeriesGroup {
	if h.Files == nil {
		return nil
	}
	const q = `
		SELECT
			m.content_id,
			m.title,
			COALESCE(m.year, 0),
			COALESCE(m.poster_path, ''),
			s.series_name,
			s.series_index
		FROM audiobook_series root
		JOIN audiobook_series s ON LOWER(s.series_name) = LOWER(root.series_name)
		JOIN media_items m ON m.content_id = s.content_id AND m.type = 'audiobook'
		WHERE root.content_id = $1
		ORDER BY s.series_index NULLS LAST, LOWER(m.sort_title)
		LIMIT 30
	`
	rows, err := h.Files.Pool().Query(ctx, q, contentID)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var seriesName string
	entries := make([]audiobookRelatedItem, 0, 16)
	for rows.Next() {
		var (
			it       audiobookRelatedItem
			poster   string
			thisName string
			idxNum   *float64
		)
		if err := rows.Scan(&it.ContentID, &it.Title, &it.Year, &poster, &thisName, &idxNum); err != nil {
			return nil
		}
		if seriesName == "" {
			seriesName = thisName
		}
		if idxNum != nil {
			n := int(*idxNum)
			if float64(n) == *idxNum {
				it.SeriesIndex = &n
			}
		}
		it.PosterURL = h.presignPoster(ctx, poster)
		entries = append(entries, it)
	}
	// A series with only this book is not useful as a rail.
	if len(entries) < 2 {
		return nil
	}
	return &audiobookSeriesGroup{Name: seriesName, Entries: entries}
}

// fetchProgress returns the caller's listening progress for this audiobook, or
// nil when no progress exists or the caller is not authenticated with a profile.
func (h *AudiobookHandler) fetchProgress(r *http.Request, contentID string) *audiobookDetailProgress {
	if h.StoreProvider == nil {
		return nil
	}
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		return nil
	}
	profileID := apimw.GetProfileID(r.Context())
	if profileID == "" {
		profileID = r.Header.Get("X-Profile-Id")
	}
	if profileID == "" {
		return nil
	}
	store, err := h.StoreProvider.ForUser(r.Context(), userID)
	if err != nil || store == nil {
		return nil
	}
	wp, err := store.GetProgress(r.Context(), profileID, contentID)
	if err != nil || wp == nil {
		return nil
	}
	return &audiobookDetailProgress{
		PositionSeconds: wp.PositionSeconds,
		Completed:       wp.Completed,
		UpdatedAt:       wp.UpdatedAt,
	}
}

type audiobookDetailResponse struct {
	Audiobook         audiobookDetailItem      `json:"audiobook"`
	Author            string                   `json:"author,omitempty"`
	Narrator          string                   `json:"narrator,omitempty"`
	Files             []audiobookDetailFile    `json:"files"`
	Progress          *audiobookDetailProgress `json:"progress,omitempty"`
	AlsoByAuthor      []audiobookRelatedItem   `json:"also_by_author,omitempty"`
	InSeries          *audiobookSeriesGroup    `json:"in_series,omitempty"`
	SimilarAudiobooks []audiobookRelatedItem   `json:"similar_audiobooks,omitempty"`
	OtherNarrations   []audiobookNarration     `json:"other_narrations,omitempty"`
}

// audiobookNarration is a sibling narration (same book, different
// narrator/edition). Returned alongside the primary item so the UI can
// surface a narrator picker.
type audiobookNarration struct {
	ContentID string `json:"content_id"`
	Title     string `json:"title"`
	Narrator  string `json:"narrator,omitempty"`
	Year      int    `json:"year,omitempty"`
}

type audiobookRelatedItem struct {
	ContentID   string `json:"content_id"`
	Title       string `json:"title"`
	Year        int    `json:"year,omitempty"`
	PosterURL   string `json:"poster_url,omitempty"`
	SeriesIndex *int   `json:"series_index,omitempty"`
}

type audiobookSeriesGroup struct {
	Name    string                 `json:"name,omitempty"`
	Entries []audiobookRelatedItem `json:"entries"`
}

type audiobookDetailItem struct {
	ContentID string   `json:"content_id"`
	Title     string   `json:"title"`
	Year      int      `json:"year"`
	Overview  string   `json:"overview,omitempty"`
	PosterURL string   `json:"poster_url,omitempty"`
	Publisher string   `json:"publisher,omitempty"`
	Genres    []string `json:"genres,omitempty"`
}

func (h *AudiobookHandler) presignPoster(ctx context.Context, path string) string {
	if path == "" || h.Detail == nil {
		return ""
	}
	return h.Detail.PresignURL(ctx, path, "featured")
}

func pickFirstString(values []string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

type audiobookDetailFile struct {
	ID              int                   `json:"id"`
	Path            string                `json:"path"`
	DurationSeconds int                   `json:"duration_seconds"`
	Container       string                `json:"container,omitempty"`
	CodecAudio      string                `json:"codec_audio,omitempty"`
	Bitrate         int                   `json:"bitrate,omitempty"`
	AudioChannels   int                   `json:"audio_channels,omitempty"`
	Chapters        []models.MediaChapter `json:"chapters,omitempty"`
}

type audiobookDetailProgress struct {
	PositionSeconds float64 `json:"position_seconds"`
	Completed       bool    `json:"completed"`
	UpdatedAt       string  `json:"updated_at"`
}

func audiobookDetailFiles(files []*models.MediaFile) []audiobookDetailFile {
	out := make([]audiobookDetailFile, 0, len(files))
	for _, f := range files {
		out = append(out, audiobookDetailFile{
			ID:              f.ID,
			Path:            f.FilePath,
			DurationSeconds: f.Duration,
			Container:       f.Container,
			CodecAudio:      f.CodecAudio,
			Bitrate:         f.Bitrate,
			AudioChannels:   f.AudioChannels,
			Chapters:        f.Chapters,
		})
	}
	return out
}
