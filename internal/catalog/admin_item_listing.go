package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/naming"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// Item types the administrator catalog listing returns. Books, podcasts and
// their episodes are left out.
const (
	AdminCatalogTypeMovie   = "movie"
	AdminCatalogTypeSeries  = "series"
	AdminCatalogTypeEpisode = "episode"
)

// AdminCatalogItemFilter narrows the administrator catalog listing. Zero
// values do not filter.
type AdminCatalogItemFilter struct {
	// LibraryID keeps items present in that library and makes AddedAt (and
	// AddedAfter) that library's first-seen time.
	LibraryID  int        `json:",omitempty"`
	Type       string     `json:",omitempty"`
	AddedAfter *time.Time `json:",omitempty"`
	ContentIDs []string   `json:",omitempty"`
}

// AdminCatalogItem is one movie, series or episode present in an enabled
// library.
type AdminCatalogItem struct {
	ContentID     string
	Type          string
	Title         string
	Year          int
	Status        string
	ImdbID        string
	TmdbID        string
	TvdbID        string
	SeriesID      string
	SeriesTitle   string
	SeasonNumber  *int
	EpisodeNumber *int
	LibraryIDs    []int
	AddedAt       time.Time
	Versions      []AdminCatalogVersion
}

// AdminCatalogVersion is one present file of an item. Video facts come from
// the primary video track and audio facts from the track playback picks by
// default; file-level columns fill what the track lacks.
type AdminCatalogVersion struct {
	FileID        int
	LibraryID     int
	Resolution    string
	CodecVideo    string
	DynamicRange  string
	CodecAudio    string
	AudioChannels int
	Atmos         bool
	Container     string
	BitrateKbps   int
	FileSize      int64
	Edition       string
}

// AdminCatalogItemPage is one content_id-ordered page.
type AdminCatalogItemPage struct {
	Items   []AdminCatalogItem
	HasMore bool
}

// ListAdminCatalogItems reads the items after the given content_id and their
// present files in one snapshot: one statement for the page, one for its files.
func (r *ItemRepository) ListAdminCatalogItems(ctx context.Context, filter AdminCatalogItemFilter, after string, limit int) (AdminCatalogItemPage, error) {
	page := AdminCatalogItemPage{Items: []AdminCatalogItem{}}
	if r == nil || r.pool == nil {
		return page, errors.New("listing admin catalog items: item repository has no database pool")
	}
	if limit <= 0 {
		return page, errors.New("listing admin catalog items: limit must be positive")
	}
	switch filter.Type {
	case "", AdminCatalogTypeMovie, AdminCatalogTypeSeries, AdminCatalogTypeEpisode:
	default:
		return page, fmt.Errorf("listing admin catalog items: unknown type %q", filter.Type)
	}
	query, args := adminCatalogPageQuery(filter, after, limit)
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return page, fmt.Errorf("listing admin catalog items: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit

	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return page, fmt.Errorf("listing admin catalog items: %w", err)
	}
	for rows.Next() {
		item := AdminCatalogItem{Versions: []AdminCatalogVersion{}}
		if err := rows.Scan(&item.ContentID, &item.Type, &item.Title, &item.Year, &item.Status,
			&item.ImdbID, &item.TmdbID, &item.TvdbID, &item.SeriesID, &item.SeriesTitle,
			&item.SeasonNumber, &item.EpisodeNumber, &item.AddedAt, &item.LibraryIDs); err != nil {
			rows.Close()
			return page, fmt.Errorf("scanning admin catalog item: %w", err)
		}
		page.Items = append(page.Items, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return page, fmt.Errorf("listing admin catalog items: %w", err)
	}
	if len(page.Items) > limit {
		page.Items, page.HasMore = page.Items[:limit], true
	}
	if err := attachAdminCatalogVersions(ctx, tx, page.Items); err != nil {
		return page, err
	}
	return page, tx.Commit(ctx)
}

// adminCatalogPageQuery merges two content_id-ordered branches, items and
// episodes. Each walks a primary key in order, or the library's membership
// index when library_id is set, and reads the row's memberships through one
// index probe, so a page stops after limit+1 present rows.
func adminCatalogPageQuery(filter AdminCatalogItemFilter, after string, limit int) (string, []any) {
	args := []any{after, limit + 1}
	bind := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	addedAt := "min(l.first_seen_at)"
	var libraryParam string
	if filter.LibraryID > 0 {
		libraryParam = bind(filter.LibraryID)
		addedAt += " FILTER (WHERE l.media_folder_id = " + libraryParam + ")"
	}
	var addedParam, idsParam string
	if filter.AddedAfter != nil {
		addedParam = bind(*filter.AddedAfter)
	}
	if len(filter.ContentIDs) > 0 {
		idsParam = bind(filter.ContentIDs)
	}
	// branch fills in the walk for one table pair: the parent row (alias p),
	// its membership table and that table's id column.
	branch := func(columns, parent, membership, idColumn string, conditions ...string) string {
		from, key := parent+" p", "p.content_id"
		if libraryParam != "" {
			from = membership + " sel JOIN " + parent + " p ON p.content_id = sel." + idColumn
			key = "sel." + idColumn
			// Repeating the bound on p lets a merge join start mid-table.
			conditions = append(conditions, "sel.media_folder_id = "+libraryParam, "p.content_id > $1")
		}
		conditions = append(conditions, key+" > $1", "m.added_at IS NOT NULL")
		switch {
		case addedParam != "" && libraryParam != "":
			// The library's (media_folder_id, first_seen_at) index finds the
			// rows when the window is narrow.
			conditions = append(conditions, "sel.first_seen_at >= "+addedParam)
		case addedParam != "":
			// An item first seen at or after the instant has every membership
			// at or after it; the candidate set comes from a skip scan of that
			// index instead of a walk of the whole catalog.
			conditions = append(conditions, "m.added_at >= "+addedParam,
				"p.content_id IN (SELECT a."+idColumn+" FROM "+membership+" a WHERE a.first_seen_at >= "+addedParam+")")
		}
		if idsParam != "" {
			conditions = append(conditions, key+" = ANY("+idsParam+"::text[])")
		}
		return fmt.Sprintf(`(SELECT %[1]s, m.added_at, m.library_ids
			FROM %[2]s
			CROSS JOIN LATERAL (
				SELECT %[3]s AS added_at, array_agg(l.media_folder_id ORDER BY l.media_folder_id) AS library_ids
				FROM %[4]s l JOIN media_folders f ON f.id = l.media_folder_id AND f.enabled
				WHERE l.%[5]s = p.content_id
			) m
			WHERE %[6]s
			ORDER BY %[7]s
			LIMIT $2)`, columns, from, addedAt, membership, idColumn, strings.Join(conditions, " AND "), key)
	}

	var branches []string
	if filter.Type != AdminCatalogTypeEpisode {
		itemTypes := []string{AdminCatalogTypeMovie, AdminCatalogTypeSeries}
		if filter.Type != "" {
			itemTypes = []string{filter.Type}
		}
		branches = append(branches, branch(`p.content_id, p.type, p.title, p.year, p.status,
				p.imdb_id, p.tmdb_id, p.tvdb_id, ''::text AS series_id, ''::text AS series_title,
				NULL::int AS season_number, NULL::int AS episode_number`,
			"media_items", "media_item_libraries", "content_id", "p.type = ANY("+bind(itemTypes)+"::text[])"))
	}
	if filter.Type == "" || filter.Type == AdminCatalogTypeEpisode {
		branches = append(branches, branch(`p.content_id, 'episode'::text, coalesce(p.title, ''),
				coalesce(extract(year FROM p.air_date)::int, 0), ''::text,
				p.imdb_id, p.tmdb_id, p.tvdb_id, p.series_id::text,
				(SELECT s.title FROM media_items s WHERE s.content_id = p.series_id),
				p.season_number, p.episode_number`,
			"episodes", "episode_libraries", "episode_id",
			"EXISTS (SELECT 1 FROM media_items s WHERE s.content_id = p.series_id AND s.type = 'series')"))
	}
	return `SELECT * FROM (` + strings.Join(branches, "\nUNION ALL\n") + `) page
		ORDER BY content_id COLLATE "C"
		LIMIT $2`, args
}

// attachAdminCatalogVersions loads every present, non-extra file in an enabled
// library for the page's movies and episodes. A multi-episode file belongs to
// its first episode; a series has no files of its own.
func attachAdminCatalogVersions(ctx context.Context, tx pgx.Tx, items []AdminCatalogItem) error {
	var movieIDs, episodeIDs []string
	byID := make(map[string]*AdminCatalogItem, len(items))
	for i := range items {
		switch items[i].Type {
		case AdminCatalogTypeMovie:
			movieIDs = append(movieIDs, items[i].ContentID)
		case AdminCatalogTypeEpisode:
			episodeIDs = append(episodeIDs, items[i].ContentID)
		default:
			continue
		}
		byID[items[i].ContentID] = &items[i]
	}
	if len(byID) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `
		SELECT mf.id, coalesce(mf.episode_id, mf.content_id), mf.media_folder_id,
			coalesce(mf.resolution, ''), coalesce(mf.codec_video, ''), coalesce(mf.codec_audio, ''),
			coalesce(mf.audio_channels, 0), coalesce(mf.hdr, false), coalesce(mf.container, ''),
			coalesce(mf.bitrate, 0), coalesce(mf.file_size, 0), mf.edition_key, mf.video_tracks, mf.audio_tracks
		FROM media_files mf
		JOIN media_folders f ON f.id = mf.media_folder_id AND f.enabled
		WHERE mf.missing_since IS NULL AND mf.extra_id IS NULL
			AND (mf.episode_id = ANY($1::text[]) OR (mf.content_id = ANY($2::text[]) AND mf.episode_id IS NULL))
		ORDER BY mf.id`, episodeIDs, movieIDs)
	if err != nil {
		return fmt.Errorf("listing admin catalog versions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var owner string
		var videoJSON, audioJSON []byte
		var file models.MediaFile
		if err := rows.Scan(&file.ID, &owner, &file.MediaFolderID, &file.Resolution, &file.CodecVideo, &file.CodecAudio,
			&file.AudioChannels, &file.HDR, &file.Container, &file.Bitrate, &file.FileSize, &file.EditionKey, &videoJSON, &audioJSON); err != nil {
			return fmt.Errorf("scanning admin catalog version: %w", err)
		}
		if len(videoJSON) > 0 {
			if err := json.Unmarshal(videoJSON, &file.VideoTracks); err != nil {
				return fmt.Errorf("decoding video tracks of file %d: %w", file.ID, err)
			}
		}
		if len(audioJSON) > 0 {
			if err := json.Unmarshal(audioJSON, &file.AudioTracks); err != nil {
				return fmt.Errorf("decoding audio tracks of file %d: %w", file.ID, err)
			}
		}
		if item := byID[owner]; item != nil {
			item.Versions = append(item.Versions, adminCatalogVersion(&file))
		}
	}
	return rows.Err()
}

func adminCatalogVersion(file *models.MediaFile) AdminCatalogVersion {
	version := AdminCatalogVersion{
		FileID: file.ID, LibraryID: file.MediaFolderID, Resolution: file.Resolution,
		CodecVideo: file.CodecVideo, CodecAudio: file.CodecAudio, AudioChannels: file.AudioChannels,
		Container: file.Container, BitrateKbps: file.Bitrate, FileSize: file.FileSize,
		Edition: naming.EditionDisplayLabel(file.EditionKey),
	}
	if len(file.VideoTracks) > 0 {
		primary := file.VideoTracks[0]
		if primary.Codec != "" {
			version.CodecVideo = primary.Codec
		}
		version.DynamicRange = tonemap.DynamicRangeForVideoTrack(primary)
	}
	if version.DynamicRange == "" && file.HDR {
		version.DynamicRange = tonemap.DynamicRangeHDRUnknown
	}
	if len(file.AudioTracks) > 0 {
		track := file.AudioTracks[playback.SelectAudioTrack(file.AudioTracks, "", nil)]
		if track.Codec != "" {
			version.CodecAudio = track.Codec
		}
		if track.Channels > 0 {
			version.AudioChannels = track.Channels
		}
		version.Atmos = playback.AtmosAudioTrack(track)
	}
	return version
}
