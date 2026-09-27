package themesongs

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	ownerSeries = "series"
	ownerSeason = "season"
)

type Repository struct{ pool *pgxpool.Pool }

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// Find resolves a compatibility audio ID without a process-local ID map.
func (r *Repository) Find(ctx context.Context, id string, filter catalog.AccessFilter) (File, error) {
	n, ok := NumericID(id)
	if !ok {
		return File{}, ErrNotFound
	}
	rows, err := r.pool.Query(ctx, `SELECT DISTINCT mf.content_id, COALESCE(e.season_id,''), e.season_number FROM item_theme_songs t
		JOIN media_files mf ON mf.media_folder_id=t.media_folder_id
		LEFT JOIN episodes e ON e.content_id=mf.episode_id
		WHERE t.id=$1 AND mf.content_id IS NOT NULL AND mf.missing_since IS NULL AND mf.extra_id IS NULL
		AND mf.file_path ~>=~ (t.owner_path||'/') AND mf.file_path ~<~ (t.owner_path||'0')`, n)
	if err != nil {
		return File{}, err
	}
	items, seasons := map[string]bool{}, map[string]bool{}
	for rows.Next() {
		var item, season string
		var number *int
		if err := rows.Scan(&item, &season, &number); err != nil {
			rows.Close()
			return File{}, err
		}
		items[item] = true
		if season == "" && number != nil {
			season = fmt.Sprintf("%s-S%02d", item, *number)
		}
		seasons[season] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return File{}, err
	}
	// Multiple items make this directory ambiguous for every owner. Likewise,
	// a directory spanning seasons cannot belong to any one season.
	if len(items) != 1 {
		return File{}, ErrNotFound
	}
	owners := make([]string, 0, 2)
	for item := range items {
		owners = append(owners, item)
	}
	if len(seasons) == 1 {
		for season := range seasons {
			if season != "" {
				owners = append(owners, season)
			}
		}
	}
	for _, owner := range owners {
		_, files, err := r.Resolve(ctx, owner, false, filter)
		if errors.Is(err, catalog.ErrItemNotFound) || errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return File{}, err
		}
		for _, f := range files {
			if f.ID == id {
				return f, nil
			}
		}
	}
	return File{}, ErrNotFound
}

type owner struct {
	id, kind, series string
	seasonNumber     int
}

func (r *Repository) Resolve(ctx context.Context, id string, inherit bool, filter catalog.AccessFilter) (string, []File, error) {
	var kind, series, season string
	var number int
	err := r.pool.QueryRow(ctx, `SELECT type, content_id, '', 0 FROM media_items WHERE content_id=$1
		UNION ALL SELECT 'season', series_id, content_id, season_number FROM seasons WHERE content_id=$1
		UNION ALL SELECT 'episode', series_id, COALESCE(season_id,''), season_number FROM episodes WHERE content_id=$1`, id).Scan(&kind, &series, &season, &number)
	if errors.Is(err, pgx.ErrNoRows) {
		if seriesID, seasonNumber, ok := catalog.ParseSyntheticSeasonID(id); ok {
			err = r.pool.QueryRow(ctx, `SELECT 'season', series_id, COALESCE(season_id,''), season_number FROM episodes
				WHERE series_id=$1 AND season_number=$2 LIMIT 1`, seriesID, seasonNumber).Scan(&kind, &series, &season, &number)
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	if err != nil {
		return "", nil, err
	}
	if err := catalog.NewItemRepository(r.pool).EnsureAccessible(ctx, series, filter); err != nil {
		return "", nil, err
	}
	owners := []owner{}
	switch kind {
	case "movie", ownerSeries:
		owners = append(owners, owner{id, kind, series, number})
	case ownerSeason:
		owners = append(owners, owner{id, kind, series, number})
		if inherit {
			owners = append(owners, owner{series, ownerSeries, series, 0})
		}
	case "episode":
		if inherit {
			if season == "" {
				season = fmt.Sprintf("%s-S%02d", series, number)
			}
			owners = append(owners, owner{season, ownerSeason, series, number})
			owners = append(owners, owner{series, ownerSeries, series, 0})
		}
	}
	for _, candidate := range owners {
		files, err := r.files(ctx, candidate, filter)
		if err != nil {
			return "", nil, err
		}
		if len(files) > 0 {
			for i := range files {
				files[i].OwnerType = candidate.kind
			}
			return candidate.id, files, nil
		}
	}
	return id, []File{}, nil
}

func (r *Repository) files(ctx context.Context, target owner, filter catalog.AccessFilter) ([]File, error) {
	conditions, args := catalog.MediaFileAccessSQL("mf", filter, []any{target.series, target.kind, target.seasonNumber})
	conditions = append(conditions, `mf.content_id=$1`, `mf.missing_since IS NULL`, `mf.extra_id IS NULL`)
	// Collapse episodes and versions to owner directories before checking ambiguity.
	// Flat episode roots name the video file; themes belong to its directory,
	// subject to the same access and ambiguity checks as directory-valued roots.
	// The bytewise prefix bounds use media_files' existing text_pattern_ops index;
	// '/' followed by any suffix sorts before the next byte, '0'.
	rows, err := r.pool.Query(ctx, `WITH candidates AS MATERIALIZED (
   SELECT DISTINCT mf.media_folder_id,
     CASE WHEN mf.canonical_root_path=mf.file_path
       THEN regexp_replace(mf.file_path, '/[^/]+$', '')
       ELSE mf.canonical_root_path END AS canonical_root_path,
     regexp_replace(mf.file_path, '/[^/]+$', '') AS directory, e.series_id, e.season_number
   FROM media_files mf LEFT JOIN episodes e ON e.content_id=mf.episode_id
   WHERE `+strings.Join(conditions, " AND ")+`)
 SELECT DISTINCT t.id, t.media_folder_id, t.owner_path, t.file_path, t.title, t.duration_seconds, t.container, t.file_size, t.file_modified_at, t.audio_codec, t.audio_channels, t.bitrate_kbps, t.sample_rate
 FROM item_theme_songs t JOIN candidates c ON c.media_folder_id=t.media_folder_id
 JOIN media_folders folder ON folder.id=t.media_folder_id
 WHERE folder.enabled AND (
   ($2='series' AND t.owner_path=c.canonical_root_path) OR
   ($2='movie' AND (t.owner_path=c.directory OR t.owner_path=c.canonical_root_path)) OR
   ($2='season' AND c.series_id=$1 AND c.season_number=$3
     AND (c.directory=t.owner_path OR starts_with(c.directory,t.owner_path||'/'))
     AND starts_with(t.owner_path,c.canonical_root_path||'/') AND c.canonical_root_path<>''))
 AND NOT EXISTS (
   SELECT 1 FROM media_files other LEFT JOIN episodes oe ON oe.content_id=other.episode_id
   WHERE other.media_folder_id=t.media_folder_id AND other.missing_since IS NULL AND other.extra_id IS NULL
   AND other.file_path ~>=~ (t.owner_path||'/') AND other.file_path ~<~ (t.owner_path||'0')
   AND (($2='season' AND (oe.series_id IS DISTINCT FROM $1 OR oe.season_number IS DISTINCT FROM $3))
     OR ($2<>'season' AND other.content_id IS DISTINCT FROM $1)))
 ORDER BY t.file_path, t.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	files := []File{}
	for rows.Next() {
		var f File
		var id int64
		if err := rows.Scan(&id, &f.FolderID, &f.OwnerPath, &f.Path, &f.Title, &f.DurationSeconds, &f.Container, &f.Size, &f.Modified, &f.AudioCodec, &f.AudioChannels, &f.BitrateKbps, &f.SampleRate); err != nil {
			return nil, err
		}
		f.ID = strconv.FormatInt(id, 10)
		files = append(files, f)
	}
	return files, rows.Err()
}

// Directories includes unmatched video files: ownership is resolved at read time
// so metadata matching and rematching cannot strand a theme on an old item.
func (r *Repository) Directories(ctx context.Context, folderID int, scope string) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT DISTINCT regexp_replace(file_path, '/[^/]+$', ''), canonical_root_path FROM media_files
        WHERE media_folder_id=$1 AND missing_since IS NULL AND extra_id IS NULL
        AND ($2='' OR starts_with(file_path,$2||'/') OR (canonical_root_path<>'' AND starts_with($2,canonical_root_path||'/')))`, folderID, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var path, root string
		if err := rows.Scan(&path, &root); err != nil {
			return nil, err
		}
		// Ownership queries use stored paths verbatim. Do not create a cleaned
		// alias that those queries cannot resolve or retain during pruning.
		if path != filepath.Clean(path) {
			continue
		}
		seen[path] = true
		if root != "" {
			root = filepath.Clean(root)
		}
		if rel, err := filepath.Rel(root, path); root != "" && err == nil && (rel == "." || filepath.IsLocal(rel)) {
			for dir := path; ; dir = filepath.Dir(dir) {
				seen[dir] = true
				if dir == root || filepath.Dir(dir) == dir {
					break
				}
			}
		}
	}
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, rows.Err()
}

// Replace updates only a completely observed directory. Unreadable directories
// are never passed here, so an offline mount cannot erase its theme records.
func (r *Repository) Replace(ctx context.Context, folderID int, directory string, files []File) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Serialize rescans of a directory, including an initially empty directory.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, $2))`, directory, int64(folderID)); err != nil {
		return err
	}
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
		_, err := tx.Exec(ctx, `INSERT INTO item_theme_songs(media_folder_id,owner_path,file_path,title,duration_seconds,container,file_size,file_modified_at,audio_codec,audio_channels,bitrate_kbps,sample_rate)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT(media_folder_id,file_path) DO UPDATE SET
			owner_path=EXCLUDED.owner_path,title=EXCLUDED.title,duration_seconds=EXCLUDED.duration_seconds,
			container=EXCLUDED.container,file_size=EXCLUDED.file_size,file_modified_at=EXCLUDED.file_modified_at,
 audio_codec=EXCLUDED.audio_codec,audio_channels=EXCLUDED.audio_channels,bitrate_kbps=EXCLUDED.bitrate_kbps,sample_rate=EXCLUDED.sample_rate`,
			folderID, directory, file.Path, file.Title, file.DurationSeconds, file.Container, file.Size, file.Modified, file.AudioCodec, file.AudioChannels, file.BitrateKbps, file.SampleRate)
		if err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM item_theme_songs WHERE media_folder_id=$1 AND owner_path=$2 AND NOT(file_path=ANY($3))`, folderID, directory, paths); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ScanFiles loads cached probes once per scan, including ancestor owners for a
// subtree. Directories with no themes require no per-directory database calls.
func (r *Repository) ScanFiles(ctx context.Context, folderID int, scope string, exact bool) (map[string]map[string]File, error) {
	scopeSQL := `($2='' OR owner_path=$2 OR starts_with(owner_path,$2||'/') OR starts_with($2,owner_path||'/'))`
	if exact {
		scopeSQL = `owner_path=$2`
	}
	rows, err := r.pool.Query(ctx, `SELECT owner_path,file_path,title,duration_seconds,container,file_size,file_modified_at,audio_codec,audio_channels,bitrate_kbps,sample_rate FROM item_theme_songs WHERE media_folder_id=$1
 AND `+scopeSQL, folderID, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]map[string]File{}
	for rows.Next() {
		f := File{FolderID: folderID}
		if err := rows.Scan(&f.OwnerPath, &f.Path, &f.Title, &f.DurationSeconds, &f.Container, &f.Size, &f.Modified, &f.AudioCodec, &f.AudioChannels, &f.BitrateKbps, &f.SampleRate); err != nil {
			return nil, err
		}
		if result[f.OwnerPath] == nil {
			result[f.OwnerPath] = make(map[string]File)
		}
		result[f.OwnerPath][f.Path] = f
	}
	return result, rows.Err()
}

// PruneOrphans follows media_files retention. Missing videos keep their theme
// records until normal scanner cleanup removes the video rows; an offline mount
// therefore cannot retire cached themes.
func (r *Repository) PruneOrphans(ctx context.Context, folderID int, scope string, exact bool) error {
	scopeSQL := `($2='' OR owner_path=$2 OR starts_with(owner_path,$2||'/') OR starts_with($2,owner_path||'/'))`
	if exact {
		scopeSQL = `owner_path=$2`
	}
	_, err := r.pool.Exec(ctx, `DELETE FROM item_theme_songs t WHERE media_folder_id=$1
 AND `+scopeSQL+`
 AND NOT EXISTS (SELECT 1 FROM media_files mf WHERE mf.media_folder_id=t.media_folder_id
 AND mf.extra_id IS NULL AND mf.file_path ~>=~ (t.owner_path||'/') AND mf.file_path ~<~ (t.owner_path||'0'))`, folderID, scope)
	return err
}

// IsActiveTheme reports whether id is a discovered theme whose file is still
// path in an enabled library. Transcode nodes use it to approve a theme as an
// FFmpeg input: the lookup is by primary key, and the path must match the row
// exactly, so a signed token cannot name any other file.
func (r *Repository) IsActiveTheme(ctx context.Context, id int64, path string) (bool, error) {
	if r == nil || r.pool == nil || id <= 0 || path == "" {
		return false, nil
	}
	var active bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM item_theme_songs t
		JOIN media_folders f ON f.id = t.media_folder_id
		WHERE t.id = $1 AND t.file_path = $2 AND f.enabled
	)`, id, path).Scan(&active)
	if err != nil {
		return false, fmt.Errorf("checking active theme %d: %w", id, err)
	}
	return active, nil
}
