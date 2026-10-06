package shuffle

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/librarykind"
)

// pool describes the movies and episodes one scope plays. Exactly the fields
// its scope sets are non-zero.
type pool struct {
	movies   bool
	episodes bool
	// libraryID limits both kinds to files stored in one library.
	libraryID int
	// seriesID, with seasonNumber when hasSeason, limits episodes to one
	// series or one of its seasons.
	seriesID     string
	seasonNumber int
	hasSeason    bool
	// Collection members: movies play as themselves, a series plays its
	// episodes, an episode plays itself.
	movieIDs   []string
	seriesIDs  []string
	episodeIDs []string
}

// scopeInfo is what resolving a scope yields: its pool and display titles.
type scopeInfo struct {
	pool        pool
	title       string
	parentTitle string
}

// querier is the slice of pgx a pick needs, so it runs in or out of a
// transaction.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// resolveScope checks the viewer may see the scope and returns what it plays.
func (s *Service) resolveScope(ctx context.Context, q querier, scope Scope, access catalog.AccessFilter) (scopeInfo, error) {
	id := strings.TrimSpace(scope.ID)
	if id == "" {
		return scopeInfo{}, ErrScopeNotFound
	}
	switch scope.Kind {
	case ScopeLibrary:
		return resolveLibrary(ctx, q, id, access)
	case ScopeSeries:
		title, err := visibleSeriesTitle(ctx, q, id, access)
		if err != nil {
			return scopeInfo{}, err
		}
		return scopeInfo{pool: pool{episodes: true, seriesID: id}, title: title}, nil
	case ScopeSeason:
		return resolveSeason(ctx, q, id, access)
	case ScopeLibraryCollection, ScopeUserCollection:
		return s.resolveCollection(ctx, scope, access)
	default:
		return scopeInfo{}, ErrScopeNotFound
	}
}

func resolveLibrary(ctx context.Context, q querier, id string, access catalog.AccessFilter) (scopeInfo, error) {
	libraryID, err := strconv.Atoi(id)
	if err != nil || libraryID <= 0 || !catalog.FitsPostgresInteger(libraryID) {
		return scopeInfo{}, ErrScopeNotFound
	}
	if access.AllowedLibraryIDs != nil && !slices.Contains(access.AllowedLibraryIDs, libraryID) {
		return scopeInfo{}, ErrScopeNotFound
	}
	if slices.Contains(access.DisabledLibraryIDs, libraryID) {
		return scopeInfo{}, ErrScopeNotFound
	}
	var name, kind string
	err = q.QueryRow(ctx, `SELECT name, type FROM media_folders WHERE id = $1 AND enabled = TRUE`, libraryID).Scan(&name, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return scopeInfo{}, ErrScopeNotFound
	}
	if err != nil {
		return scopeInfo{}, fmt.Errorf("loading library %d: %w", libraryID, err)
	}
	kinds := librarykind.Of(kind)
	p := pool{libraryID: libraryID, movies: kinds.Movie || kinds.Mixed, episodes: kinds.TV || kinds.Mixed}
	if !p.movies && !p.episodes {
		return scopeInfo{}, ErrUnsupportedScope
	}
	return scopeInfo{pool: p, title: name}, nil
}

// visibleSeriesTitle returns a series' title when the viewer may see it.
func visibleSeriesTitle(ctx context.Context, q querier, seriesID string, access catalog.AccessFilter) (string, error) {
	args := []any{seriesID}
	conditions := []string{"mi.content_id = $1", "mi.type = 'series'"}
	conditions = appendItemAccess("mi", "mi.content_id", access, conditions, &args)
	var title string
	err := q.QueryRow(ctx, `SELECT mi.title FROM media_items mi WHERE `+strings.Join(conditions, " AND "), args...).Scan(&title)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrScopeNotFound
	}
	if err != nil {
		return "", fmt.Errorf("loading series %s: %w", seriesID, err)
	}
	return title, nil
}

func resolveSeason(ctx context.Context, q querier, id string, access catalog.AccessFilter) (scopeInfo, error) {
	var seriesID, title string
	var number int
	err := q.QueryRow(ctx, `SELECT series_id, season_number, COALESCE(title, '') FROM seasons WHERE content_id = $1`, id).
		Scan(&seriesID, &number, &title)
	if errors.Is(err, pgx.ErrNoRows) {
		// A series with no stored season metadata has seasons named by its
		// episodes, "<series>-S<number>".
		var ok bool
		seriesID, number, ok = catalog.ParseSyntheticSeasonID(id)
		if !ok {
			return scopeInfo{}, ErrScopeNotFound
		}
		title = ""
	} else if err != nil {
		return scopeInfo{}, fmt.Errorf("loading season %s: %w", id, err)
	}
	seriesTitle, err := visibleSeriesTitle(ctx, q, seriesID, access)
	if err != nil {
		return scopeInfo{}, err
	}
	if strings.TrimSpace(title) == "" {
		title = "Season " + strconv.Itoa(number)
		if number == 0 {
			title = "Specials"
		}
	}
	return scopeInfo{
		pool:        pool{episodes: true, seriesID: seriesID, seasonNumber: number, hasSeason: true},
		title:       title,
		parentTitle: seriesTitle,
	}, nil
}

func (s *Service) resolveCollection(ctx context.Context, scope Scope, access catalog.AccessFilter) (scopeInfo, error) {
	if s.collections == nil {
		return scopeInfo{}, ErrScopeNotFound
	}
	source := catalog.CatalogSourceLibraryCollection
	if scope.Kind == ScopeUserCollection {
		source = catalog.CatalogSourceUserCollection
	}
	title, items, err := s.collections.CollectionMembers(ctx, source, scope.ID, access)
	if errors.Is(err, catalog.ErrCatalogSourceNotFound) {
		return scopeInfo{}, ErrScopeNotFound
	}
	if err != nil {
		return scopeInfo{}, fmt.Errorf("loading collection %s: %w", scope.ID, err)
	}
	p := pool{}
	for _, item := range items {
		if item == nil {
			continue
		}
		switch item.Type {
		case itemTypeMovie:
			p.movieIDs = append(p.movieIDs, item.ContentID)
		case itemTypeSeries:
			p.seriesIDs = append(p.seriesIDs, item.ContentID)
		case itemTypeEpisode:
			p.episodeIDs = append(p.episodeIDs, item.ContentID)
		}
	}
	p.movies = len(p.movieIDs) > 0
	p.episodes = len(p.seriesIDs) > 0 || len(p.episodeIDs) > 0
	return scopeInfo{pool: p, title: title}, nil
}

// pick returns a random playable item from the pool that the shuffle has not
// handed out this cycle and that is not in exclude, or "" when none is left.
// shuffleID is empty before the shuffle row exists.
func pick(ctx context.Context, q querier, p pool, access catalog.AccessFilter, shuffleID string, exclude []string) (string, error) {
	args := []any{}
	source := candidates(p, access, &args)
	if source == "" {
		return "", nil
	}
	var filters []string
	if shuffleID != "" {
		filters = append(filters, fmt.Sprintf(
			"NOT EXISTS (SELECT 1 FROM playback_shuffle_items played WHERE played.shuffle_id = %s AND played.content_id = candidate.content_id)",
			bind(&args, shuffleID)))
	}
	if len(exclude) > 0 {
		filters = append(filters, fmt.Sprintf("NOT (candidate.content_id = ANY(%s))", bind(&args, exclude)))
	}
	sql := `SELECT candidate.content_id FROM (` + source + `) candidate`
	if len(filters) > 0 {
		sql += ` WHERE ` + strings.Join(filters, " AND ")
	}
	sql += ` ORDER BY random() LIMIT 1`
	var contentID string
	err := q.QueryRow(ctx, sql, args...).Scan(&contentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("picking a shuffle item: %w", err)
	}
	return contentID, nil
}

// playable reports whether contentID is still in the pool for this viewer:
// present, reachable, and within the viewer's limits.
func playable(ctx context.Context, q querier, p pool, access catalog.AccessFilter, contentID string) (bool, error) {
	args := []any{}
	source := candidates(p, access, &args)
	if source == "" {
		return false, nil
	}
	sql := `SELECT EXISTS (SELECT 1 FROM (` + source + `) candidate WHERE candidate.content_id = ` + bind(&args, contentID) + `)`
	var ok bool
	if err := q.QueryRow(ctx, sql, args...).Scan(&ok); err != nil {
		return false, fmt.Errorf("checking a shuffle item: %w", err)
	}
	return ok, nil
}

// candidates is the pool's playable items as one SQL relation with a
// content_id column, or "" when the pool holds no kind of media.
func candidates(p pool, access catalog.AccessFilter, args *[]any) string {
	branches := make([]string, 0, 2)
	if p.movies {
		branches = append(branches, movieCandidates(p, access, args))
	}
	if p.episodes {
		branches = append(branches, episodeCandidates(p, access, args))
	}
	return strings.Join(branches, " UNION ALL ")
}

// movieCandidates selects the pool's movies the viewer may see and play.
func movieCandidates(p pool, access catalog.AccessFilter, args *[]any) string {
	conditions := []string{"mi.type = 'movie'"}
	if len(p.movieIDs) > 0 {
		conditions = append(conditions, fmt.Sprintf("mi.content_id = ANY(%s)", bind(args, p.movieIDs)))
	}
	conditions = appendItemAccess("mi", "mi.content_id", access, conditions, args)
	conditions = append(conditions, playableFileExists("mf.content_id = mi.content_id", p.libraryID, access, args))
	return `SELECT mi.content_id FROM media_items mi WHERE ` + strings.Join(conditions, " AND ")
}

// episodeCandidates selects the pool's episodes the viewer may see and play.
// Maturity and library access come from the parent series, as everywhere else
// in the catalog. Specials play like any other episode.
func episodeCandidates(p pool, access catalog.AccessFilter, args *[]any) string {
	conditions := []string{}
	switch {
	case p.hasSeason:
		conditions = append(conditions,
			fmt.Sprintf("e.series_id = %s", bind(args, p.seriesID)),
			fmt.Sprintf("e.season_number = %s", bind(args, p.seasonNumber)))
	case p.seriesID != "":
		conditions = append(conditions, fmt.Sprintf("e.series_id = %s", bind(args, p.seriesID)))
	case len(p.seriesIDs) > 0 || len(p.episodeIDs) > 0:
		var members []string
		if len(p.seriesIDs) > 0 {
			members = append(members, fmt.Sprintf("e.series_id = ANY(%s)", bind(args, p.seriesIDs)))
		}
		if len(p.episodeIDs) > 0 {
			members = append(members, fmt.Sprintf("e.content_id = ANY(%s)", bind(args, p.episodeIDs)))
		}
		conditions = append(conditions, "("+strings.Join(members, " OR ")+")")
	}
	conditions = appendItemAccess("si", "e.series_id", access, conditions, args)
	conditions = append(conditions, playableFileExists("mf.episode_id = e.content_id", p.libraryID, access, args))
	return `SELECT e.content_id FROM episodes e JOIN media_items si ON si.content_id = e.series_id AND si.type = 'series' WHERE ` +
		strings.Join(conditions, " AND ")
}

// Collection member types, as stored in media_items.type.
const (
	itemTypeMovie   = "movie"
	itemTypeSeries  = "series"
	itemTypeEpisode = "episode"
)

// appendItemAccess adds the viewer's maturity limits, excluded media types,
// and library membership rules for an item alias and its library key.
func appendItemAccess(alias, libraryKey string, access catalog.AccessFilter, conditions []string, args *[]any) []string {
	argIdx := len(*args) + 1
	catalog.ApplySectionAccessFilter(alias, access, &conditions, args, &argIdx)
	catalog.ApplyLibraryAccessFilter(libraryKey, access, &conditions, args, &argIdx)
	return conditions
}

// playableFileExists requires a present file in an enabled library that the
// viewer may play, and in libraryID when it is set.
func playableFileExists(join string, libraryID int, access catalog.AccessFilter, args *[]any) string {
	conditions := []string{join, "mf.missing_since IS NULL", "pf.enabled = TRUE"}
	if libraryID > 0 {
		conditions = append(conditions, fmt.Sprintf("mf.media_folder_id = %s", bind(args, libraryID)))
	}
	var fileAccess []string
	fileAccess, *args = catalog.MediaFileAccessSQL("mf", access, *args)
	conditions = append(conditions, fileAccess...)
	return `EXISTS (SELECT 1 FROM media_files mf JOIN media_folders pf ON pf.id = mf.media_folder_id WHERE ` +
		strings.Join(conditions, " AND ") + `)`
}

// bind appends value to args and returns its placeholder.
func bind(args *[]any, value any) string {
	*args = append(*args, value)
	return "$" + strconv.Itoa(len(*args))
}
