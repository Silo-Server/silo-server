package catalog

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	playableTypeMovie  = "movie"
	playableTypeSeries = "series"
	playableTypeSeason = "season"
	playableFileExists = "mf.missing_since IS NULL"

	// playableTargetProgressBatchSize keeps each progress lookup well below
	// PostgreSQL's 65535 bind-parameter limit. Candidates are deliberately NOT
	// capped per card: a profile deep into a long-running series has its
	// in-progress episode far down the season/episode ordering, and dropping it
	// would silently degrade that card to "play the first episode". Batching
	// here removes the parameter ceiling without that behavioral cost.
	playableTargetProgressBatchSize = 500
)

// PlayableTargetInput identifies a displayed card whose direct-play target
// should be resolved for the acting profile.
type PlayableTargetInput struct {
	ContentID    string
	Type         string
	SeriesID     string
	SeasonNumber *int
	// PreferredContentID is an optional, profile-independent anchor hint for
	// this card: the leaf (episode or movie) the surface would like to play,
	// such as RecentTVTarget.PlayContentID / models.MediaItem.PlayContentID.
	//
	// Callers that render such a hint MUST route it through here instead of
	// emitting it directly. Those hints are produced before profile-aware
	// filtering (and are cached across profiles), so only Resolve can check
	// them against this profile's library access and playback-quality ceiling.
	// Resolve returns the hint only when it is one of this card's own
	// available leaves and passes exactly the same file conditions as any
	// other candidate; otherwise it falls back to normal series/season/leaf
	// resolution for the card.
	PreferredContentID string
}

// Key identifies this card in the map Resolve returns. It includes the anchor
// hint because two cards can legitimately display the SAME item and still want
// different targets — recently-added TV keeps one card per scan-run event, so a
// series hit by two multi-episode runs appears twice with different anchors.
// Callers look a response row up with the key built from the same three fields.
func (in PlayableTargetInput) Key() string {
	return strings.ToLower(strings.TrimSpace(in.Type)) + "\x00" +
		strings.TrimSpace(in.ContentID) + "\x00" +
		strings.TrimSpace(in.PreferredContentID)
}

// PlayableTargetQuery scopes direct-play target resolution to the acting
// profile and, when supplied, the libraries represented by the surface.
type PlayableTargetQuery struct {
	UserID        int
	ProfileID     string
	LibraryIDs    []int
	Access        AccessFilter
	Items         []PlayableTargetInput
	ProgressStore PlayableTargetProgressStore
}

// PlayableTargetProgressStore is the backend-neutral progress capability used
// to rank series and season targets without coupling catalog reads to the
// PostgreSQL user tables.
type PlayableTargetProgressStore interface {
	ListProgressByMediaItems(ctx context.Context, profileID string, mediaItemIDs []string) (map[string]userstore.WatchProgress, error)
}

// PlayableTargetResolver resolves card-level playback targets in one query.
// It deliberately returns a map instead of mutating MediaItem models because
// section/catalog models may have come from a process-global shared cache.
type PlayableTargetResolver struct {
	pool *pgxpool.Pool
}

func NewPlayableTargetResolver(pool *pgxpool.Pool) *PlayableTargetResolver {
	return &PlayableTargetResolver{pool: pool}
}

// NewPlayableTargetResolverForItems builds a resolver from the repository
// already owned by ItemsHandler without exposing the repository's pool.
func NewPlayableTargetResolverForItems(repo *ItemRepository) *PlayableTargetResolver {
	if repo == nil {
		return &PlayableTargetResolver{}
	}
	return NewPlayableTargetResolver(repo.pool)
}

// Resolve returns one accessible, currently available target for each
// playable movie/TV card, keyed by PlayableTargetInput.Key so that two cards
// displaying the same item resolve independently. A card's PreferredContentID
// hint wins when it still satisfies this profile's file conditions; otherwise
// series and seasons prefer the newest in-progress episode, then the first
// unwatched episode, then the first available episode.
// MaxContentRating and AllowedContentIDs are intentionally not reapplied to
// candidate episodes: the displayed item has already passed those content
// access filters, and episodes inherit the parent series rating. File-library
// access is still enforced here before any target is returned.
func (r *PlayableTargetResolver) Resolve(ctx context.Context, q PlayableTargetQuery) (map[string]string, error) {
	result := make(map[string]string)
	if r == nil || r.pool == nil || q.UserID <= 0 || strings.TrimSpace(q.ProfileID) == "" {
		return result, nil
	}
	if q.Access.AllowedLibraryIDs != nil && len(q.Access.AllowedLibraryIDs) == 0 {
		return result, nil
	}

	ids := make([]string, 0, len(q.Items))
	types := make([]string, 0, len(q.Items))
	seriesIDs := make([]string, 0, len(q.Items))
	seasonNumbers := make([]int, 0, len(q.Items))
	preferredIDs := make([]string, 0, len(q.Items))
	// keysByOrd maps a request row's 1-based SQL ordinality back to its input
	// key, so two cards that share a content ID stay distinguishable.
	keysByOrd := make([]string, 0, len(q.Items))
	seen := make(map[string]struct{}, len(q.Items))
	for _, item := range q.Items {
		contentID := strings.TrimSpace(item.ContentID)
		mediaType := strings.ToLower(strings.TrimSpace(item.Type))
		if contentID == "" || (mediaType != playableTypeMovie && mediaType != recentTVTypeEpisode && mediaType != playableTypeSeries && mediaType != playableTypeSeason) {
			continue
		}
		key := item.Key()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keysByOrd = append(keysByOrd, key)
		ids = append(ids, contentID)
		types = append(types, mediaType)
		seriesIDs = append(seriesIDs, strings.TrimSpace(item.SeriesID))
		seasonNumber := -1
		if item.SeasonNumber != nil {
			seasonNumber = *item.SeasonNumber
		}
		seasonNumbers = append(seasonNumbers, seasonNumber)
		preferredIDs = append(preferredIDs, strings.TrimSpace(item.PreferredContentID))
	}
	if len(ids) == 0 {
		return result, nil
	}

	args := []any{ids, types, seriesIDs, seasonNumbers, preferredIDs}
	argIdx := 6
	fileConditions := []string{
		playableFileExists,
		"EXISTS (SELECT 1 FROM media_folders pf WHERE pf.id = mf.media_folder_id AND pf.enabled = TRUE)",
	}
	if maxQuality := access.NormalizePlaybackQuality(q.Access.MaxPlaybackQuality); maxQuality != "" {
		maxRank := 3
		if maxQuality == access.PlaybackQuality4K {
			maxRank = 4
		}
		fileConditions = append(fileConditions, fmt.Sprintf(`CASE UPPER(BTRIM(COALESCE(mf.resolution, '')))
			WHEN '480P' THEN 1 WHEN '720P' THEN 2 WHEN '1080P' THEN 3
			WHEN '2160P' THEN 4 WHEN '4320P' THEN 5 ELSE 0 END <= %d`, maxRank))
	}
	effectiveLibraries := uniquePositiveInts(q.LibraryIDs)
	if len(effectiveLibraries) > 0 {
		if q.Access.AllowedLibraryIDs != nil {
			effectiveLibraries = intersectOptionalInts(effectiveLibraries, q.Access.AllowedLibraryIDs)
		}
		effectiveLibraries = subtractInts(effectiveLibraries, q.Access.DisabledLibraryIDs)
		if len(effectiveLibraries) == 0 {
			return result, nil
		}
		fileConditions = append(fileConditions, fmt.Sprintf("mf.media_folder_id = ANY($%d)", argIdx))
		args = append(args, effectiveLibraries)
	} else {
		if q.Access.AllowedLibraryIDs != nil {
			fileConditions = append(fileConditions, fmt.Sprintf("mf.media_folder_id = ANY($%d)", argIdx))
			args = append(args, q.Access.AllowedLibraryIDs)
			argIdx++
		}
		if len(q.Access.DisabledLibraryIDs) > 0 {
			fileConditions = append(fileConditions, fmt.Sprintf("NOT (mf.media_folder_id = ANY($%d))", argIdx))
			args = append(args, q.Access.DisabledLibraryIDs)
		}
	}

	fileFilter := strings.Join(fileConditions, " AND ")
	if userID, ok := catalogProgressUserID(q); ok {
		return r.resolveRanked(ctx, keysByOrd, args, fileFilter, userID, q.ProfileID)
	}

	query := fmt.Sprintf(`
		WITH requested AS (
			SELECT content_id, media_type, series_id, season_number, preferred_content_id, ord
			FROM unnest($1::text[], $2::text[], $3::text[], $4::integer[], $5::text[]) WITH ORDINALITY
			  AS requested(content_id, media_type, series_id, season_number, preferred_content_id, ord)
		),
		leaf_targets AS (
			-- The movie and episode file checks are separate EXISTS branches
			-- rather than one CASE expression: a CASE over both columns keeps
			-- PostgreSQL from using either media_files index and forces a scan
			-- of the whole table per requested card.
			SELECT requested.ord, requested.content_id, requested.content_id AS play_content_id
			FROM requested
			WHERE (
				requested.media_type = 'movie'
				AND EXISTS (
					SELECT 1
					FROM media_files mf
					WHERE mf.content_id = requested.content_id
					  AND %s
				)
			  ) OR (
				requested.media_type = 'episode'
				AND EXISTS (
					SELECT 1
					FROM media_files mf
					WHERE mf.episode_id = requested.content_id
					  AND %s
				)
			  )
		),
		candidate_episodes AS (
			SELECT requested.ord, episode.content_id, episode.season_number, episode.episode_number
			FROM requested
			JOIN episodes episode
			  ON requested.media_type = 'series'
			 AND episode.series_id = requested.content_id
			UNION ALL
			SELECT requested.ord, episode.content_id, episode.season_number, episode.episode_number
			FROM requested
			JOIN episodes episode
			  ON requested.media_type = 'season'
			 AND requested.series_id <> ''
			 AND requested.season_number >= 0
			 AND episode.series_id = requested.series_id
			 AND episode.season_number = requested.season_number
			UNION ALL
			SELECT requested.ord, episode.content_id, episode.season_number, episode.episode_number
			FROM requested
			JOIN seasons season
			  ON requested.media_type = 'season'
			 AND season.content_id = requested.content_id
			 AND NOT (
				requested.series_id <> ''
				AND requested.season_number >= 0
				AND requested.series_id = season.series_id
				AND requested.season_number = season.season_number
			 )
			JOIN episodes episode
			  ON episode.series_id = season.series_id
			 AND episode.season_number = season.season_number
		),
		available_candidates AS (
			SELECT requested.ord,
			       requested.content_id,
			       candidate.content_id AS play_content_id,
			       candidate.season_number,
			       candidate.episode_number
			FROM requested
			JOIN candidate_episodes candidate ON candidate.ord = requested.ord
			WHERE EXISTS (
				SELECT 1
				FROM media_files mf
				WHERE mf.episode_id = candidate.content_id
				  AND %s
			  )
		),
		hint_targets AS (
			-- A card's anchor hint is honored only when it is one of that
			-- card's own available leaves, so it passes exactly the same file
			-- conditions (library, enabled folder, quality rank) as any other
			-- candidate and can never point outside the displayed item.
			SELECT candidate.ord, candidate.content_id, candidate.play_content_id
			FROM available_candidates candidate
			JOIN requested
			  ON requested.ord = candidate.ord
			 AND requested.preferred_content_id = candidate.play_content_id
			UNION ALL
			SELECT leaf.ord, leaf.content_id, leaf.play_content_id
			FROM leaf_targets leaf
			JOIN requested
			  ON requested.ord = leaf.ord
			 AND requested.preferred_content_id = leaf.play_content_id
		),
		resolved AS (
			SELECT ord, play_content_id, TRUE AS is_hint, -1 AS season_number, -1 AS episode_number FROM hint_targets
			UNION ALL
			SELECT ord, play_content_id, FALSE, -1, -1 FROM leaf_targets
			UNION ALL
			SELECT ord, play_content_id, FALSE, season_number, episode_number FROM available_candidates
		)
		SELECT ord, play_content_id, is_hint
		FROM resolved
		ORDER BY ord,
		         is_hint DESC,
		         CASE WHEN season_number = 0 THEN 1 ELSE 0 END,
		         season_number,
		         episode_number,
		         play_content_id
	`, strings.Join(fileConditions, " AND "), strings.Join(fileConditions, " AND "), strings.Join(fileConditions, " AND "))

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("resolving playable poster targets: %w", err)
	}
	defer rows.Close()
	candidates := make(map[string][]string, len(ids))
	hints := make(map[string]string, len(ids))
	for rows.Next() {
		var ord int64
		var playContentID string
		var isHint bool
		if err := rows.Scan(&ord, &playContentID, &isHint); err != nil {
			return nil, fmt.Errorf("scanning playable poster target: %w", err)
		}
		if ord < 1 || ord > int64(len(keysByOrd)) {
			return nil, fmt.Errorf("playable poster target ordinality %d is outside the requested set", ord)
		}
		key := keysByOrd[ord-1]
		if isHint {
			// Hints are ordered first within a card; the first one wins.
			if _, ok := hints[key]; !ok {
				hints[key] = playContentID
			}
			continue
		}
		candidates[key] = append(candidates[key], playContentID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating playable poster targets: %w", err)
	}
	progress := map[string]userstore.WatchProgress{}
	if q.ProgressStore != nil {
		progressIDs := playableTargetProgressIDs(candidates, hints)
		progress, err = listPlayableTargetProgress(ctx, q.ProgressStore, q.ProfileID, progressIDs)
		if err != nil {
			return nil, fmt.Errorf("listing progress for playable poster targets: %w", err)
		}
	}
	for key, targetCandidates := range candidates {
		if len(targetCandidates) > 0 {
			result[key] = preferredPlayableTarget(targetCandidates, progress)
		}
	}
	// A validated hint is the surface's own anchor (for example the episode a
	// recently-added event is about), so it outranks progress-based ranking.
	for key, hint := range hints {
		result[key] = hint
	}
	return result, nil
}

// catalogProgressUserID reports whether q's progress can be ranked inside the
// catalog query: the store must keep it in this database's user_watch_progress
// for the same user the query is scoped to.
func catalogProgressUserID(q PlayableTargetQuery) (int, bool) {
	store, ok := q.ProgressStore.(userstore.CatalogProgressStore)
	if !ok {
		return 0, false
	}
	userID, ok := store.CatalogProgressUserID()
	if !ok || userID != q.UserID {
		return 0, false
	}
	return userID, true
}

// resolveRanked answers Resolve with one row per card, ranking series and
// season candidates against the profile's progress in SQL. The general path
// returns every available episode of every series card and then reads their
// progress in serial batches: one card for a daily show is tens of thousands
// of rows and dozens of round trips. Here each card instead takes the best of
// at most a few picks, each found by an early-stopping index walk:
//
//   - a validated hint, which wins outright;
//   - the newest in-progress episode, driven from the profile's own progress;
//   - the first available episode not completed, walking the series in
//     season/episode order from its first available episode and stopping at
//     the first match;
//   - failing that, that first available episode.
//
// Specials (season 0) are walked only when no regular episode qualifies, which
// is the same place the general ordering puts them. Each walk is skipped when
// a better pick already exists for the card. The ranking must stay identical
// to preferredPlayableTarget; TestPlayableTargetsRankedMatchesGeneral pins it.
// Progress times compare at whole seconds because the store reports them in
// RFC 3339 without fractions, so the general path ties within a second too.
func (r *PlayableTargetResolver) resolveRanked(ctx context.Context, keysByOrd []string, args []any, fileFilter string, userID int, profileID string) (map[string]string, error) {
	args = append(args, userID, profileID)
	userArg, profileArg := len(args)-1, len(args)
	visibleProgress := fmt.Sprintf(`wp.user_id = $%[1]d AND wp.profile_id = $%[2]d
			AND NOT EXISTS (
				SELECT 1
				FROM user_history_hidden_items hhi
				WHERE hhi.user_id = wp.user_id
				  AND hhi.profile_id = wp.profile_id
				  AND hhi.media_item_id = wp.media_item_id
				  AND wp.updated_at <= hhi.hidden_before
			)`, userArg, profileArg)
	available := fmt.Sprintf(`EXISTS (
				SELECT 1
				FROM media_files mf
				WHERE mf.episode_id = e.content_id
				  AND %s
			)`, fileFilter)
	notCompleted := fmt.Sprintf(`NOT EXISTS (
				SELECT 1
				FROM user_watch_progress wp
				WHERE wp.media_item_id = e.content_id
				  AND wp.completed
				  AND %s
			)`, visibleProgress)
	// walk returns the first available episode of the scope, in season/episode
	// order, at or after the episode named by from (when set) and satisfying
	// extra. The (series_id, season_number, episode_number) unique index
	// supplies the order and the start position, so LIMIT 1 stops at the first
	// match.
	walk := func(specials bool, from, extra string) string {
		season := "e.season_number <> 0"
		if specials {
			season = "e.season_number = 0"
		}
		if from != "" {
			extra = fmt.Sprintf(" AND (e.season_number, e.episode_number) >= (%[1]s.season_number, %[1]s.episode_number)", from) + extra
		}
		// The season bound is a range, not "IS NULL OR =", so the planner can
		// use it as an index condition: with the OR a season card walks every
		// episode of every earlier season before reaching its own.
		return fmt.Sprintf(`SELECT e.content_id, e.season_number, e.episode_number
			FROM episodes e
			WHERE e.series_id = scope.series_id
			  AND e.season_number BETWEEN COALESCE(scope.season_number, -2147483648) AND COALESCE(scope.season_number, 2147483647)
			  AND %s
			  AND %s%s
			ORDER BY e.season_number, e.episode_number
			LIMIT 1`, season, available, extra)
	}

	query := fmt.Sprintf(`
		WITH requested AS (
			SELECT content_id, media_type, series_id, season_number, preferred_content_id, ord
			FROM unnest($1::text[], $2::text[], $3::text[], $4::integer[], $5::text[]) WITH ORDINALITY
			  AS requested(content_id, media_type, series_id, season_number, preferred_content_id, ord)
		),
		leaf_targets AS (
			-- Same shape as the general query: one EXISTS branch per column so
			-- each uses its own media_files index.
			SELECT requested.ord, requested.content_id AS play_content_id
			FROM requested
			WHERE (
				requested.media_type = 'movie'
				AND EXISTS (
					SELECT 1
					FROM media_files mf
					WHERE mf.content_id = requested.content_id
					  AND %[1]s
				)
			  ) OR (
				requested.media_type = 'episode'
				AND EXISTS (
					SELECT 1
					FROM media_files mf
					WHERE mf.episode_id = requested.content_id
					  AND %[1]s
				)
			  )
		),
		-- A scope is the episode range a series or season card draws from. A
		-- season card can carry two: its own series/season fields and the
		-- seasons row its content ID names, when those differ.
		scopes AS (
			SELECT requested.ord, requested.content_id AS series_id, NULL::integer AS season_number
			FROM requested
			WHERE requested.media_type = 'series'
			UNION ALL
			SELECT requested.ord, requested.series_id, requested.season_number
			FROM requested
			WHERE requested.media_type = 'season'
			  AND requested.series_id <> ''
			  AND requested.season_number >= 0
			UNION ALL
			SELECT requested.ord, season.series_id, season.season_number
			FROM requested
			JOIN seasons season
			  ON requested.media_type = 'season'
			 AND season.content_id = requested.content_id
			 AND NOT (
				requested.series_id <> ''
				AND requested.season_number >= 0
				AND requested.series_id = season.series_id
				AND requested.season_number = season.season_number
			 )
		),
		hint_targets AS (
			SELECT scope.ord, e.content_id AS play_content_id
			FROM scopes scope
			JOIN requested ON requested.ord = scope.ord
			JOIN episodes e
			  ON e.content_id = requested.preferred_content_id
			 AND e.series_id = scope.series_id
			 AND (scope.season_number IS NULL OR e.season_number = scope.season_number)
			WHERE %[2]s
			UNION ALL
			SELECT leaf.ord, leaf.play_content_id
			FROM leaf_targets leaf
			JOIN requested
			  ON requested.ord = leaf.ord
			 AND requested.preferred_content_id = leaf.play_content_id
		),
		in_progress AS MATERIALIZED (
			SELECT e.content_id, e.series_id, e.season_number, e.episode_number,
			       date_trunc('second', wp.updated_at) AS progress_second
			FROM user_watch_progress wp
			JOIN episodes e ON e.content_id = wp.media_item_id
			WHERE wp.position_seconds > 0
			  AND NOT wp.completed
			  AND %[3]s
			  AND e.series_id = ANY (ARRAY(SELECT series_id FROM scopes))
			  AND %[2]s
		),
		progress_picks AS (
			SELECT scope.ord, ip.content_id, ip.progress_second, ip.season_number, ip.episode_number
			FROM scopes scope
			JOIN in_progress ip
			  ON ip.series_id = scope.series_id
			 AND (scope.season_number IS NULL OR ip.season_number = scope.season_number)
		),
		walk_scopes AS (
			SELECT scope.*
			FROM scopes scope
			WHERE NOT EXISTS (SELECT 1 FROM hint_targets hint WHERE hint.ord = scope.ord)
			  AND NOT EXISTS (SELECT 1 FROM progress_picks pick WHERE pick.ord = scope.ord)
		),
		-- Per scope, find the first available regular episode, then walk on
		-- from it to the first one not completed; everything before it has
		-- no available file. Specials are walked only when no regular episode
		-- is unwatched, since they order after every regular episode.
		walk_picks AS (
			SELECT scope.ord,
			       COALESCE(unwatched.content_id, unwatched_special.content_id, first_available.content_id, first_special.content_id) AS content_id,
			       CASE WHEN COALESCE(unwatched.content_id, unwatched_special.content_id) IS NOT NULL THEN 1 ELSE 2 END AS progress_rank,
			       COALESCE(unwatched.season_number, unwatched_special.season_number, first_available.season_number, first_special.season_number) AS season_number,
			       COALESCE(unwatched.episode_number, unwatched_special.episode_number, first_available.episode_number, first_special.episode_number) AS episode_number
			FROM walk_scopes scope
			LEFT JOIN LATERAL (%[4]s) first_available ON TRUE
			LEFT JOIN LATERAL (%[5]s) unwatched ON first_available.content_id IS NOT NULL
			LEFT JOIN LATERAL (%[6]s) first_special ON unwatched.content_id IS NULL
			LEFT JOIN LATERAL (%[7]s) unwatched_special ON unwatched.content_id IS NULL AND first_special.content_id IS NOT NULL
		),
		picks AS (
			SELECT ord, play_content_id, -1 AS progress_rank, NULL::timestamptz AS progress_second, -1 AS season_number, -1 AS episode_number
			FROM hint_targets
			UNION ALL
			SELECT ord, play_content_id, 1, NULL, -1, -1
			FROM leaf_targets
			UNION ALL
			SELECT ord, content_id, 0, progress_second, season_number, episode_number
			FROM progress_picks
			UNION ALL
			SELECT ord, content_id, progress_rank, NULL, season_number, episode_number
			FROM walk_picks
			WHERE content_id IS NOT NULL
		)
		SELECT DISTINCT ON (ord) ord, play_content_id
		FROM picks
		ORDER BY ord,
		         progress_rank,
		         progress_second DESC NULLS LAST,
		         CASE WHEN season_number = 0 THEN 1 ELSE 0 END,
		         season_number,
		         episode_number,
		         play_content_id
	`, fileFilter, available, visibleProgress,
		walk(false, "", ""), walk(false, "first_available", " AND "+notCompleted),
		walk(true, "", ""), walk(true, "first_special", " AND "+notCompleted))

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("resolving ranked playable poster targets: %w", err)
	}
	defer rows.Close()
	result := make(map[string]string, len(keysByOrd))
	for rows.Next() {
		var ord int64
		var playContentID string
		if err := rows.Scan(&ord, &playContentID); err != nil {
			return nil, fmt.Errorf("scanning ranked playable poster target: %w", err)
		}
		if ord < 1 || ord > int64(len(keysByOrd)) {
			return nil, fmt.Errorf("playable poster target ordinality %d is outside the requested set", ord)
		}
		result[keysByOrd[ord-1]] = playContentID
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating ranked playable poster targets: %w", err)
	}
	return result, nil
}

// Progress can only affect a card with several candidates and no validated
// anchor. A leaf shared with such a card must still participate in its ranking.
func playableTargetProgressIDs(candidates map[string][]string, hints map[string]string) []string {
	var ids []string
	seen := make(map[string]struct{})
	for key, targets := range candidates {
		if _, hinted := hints[key]; hinted || len(targets) < 2 {
			continue
		}
		for _, id := range targets {
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// listPlayableTargetProgress fetches progress in batches: the PostgreSQL store
// binds one parameter per ID and PostgreSQL rejects more than 65535 bind
// parameters in a single statement, which one page of long series can exceed.
func listPlayableTargetProgress(
	ctx context.Context,
	store PlayableTargetProgressStore,
	profileID string,
	ids []string,
) (map[string]userstore.WatchProgress, error) {
	progress := make(map[string]userstore.WatchProgress, len(ids))
	for start := 0; start < len(ids); start += playableTargetProgressBatchSize {
		batch, err := store.ListProgressByMediaItems(ctx, profileID, ids[start:min(start+playableTargetProgressBatchSize, len(ids))])
		if err != nil {
			return nil, err
		}
		maps.Copy(progress, batch)
	}
	return progress, nil
}

func preferredPlayableTarget(candidates []string, progress map[string]userstore.WatchProgress) string {
	best := candidates[0]
	bestRank := playableProgressRank(progress, best)
	for _, candidate := range candidates[1:] {
		rank := playableProgressRank(progress, candidate)
		if rank < bestRank || (rank == 0 && bestRank == 0 && progressUpdatedAfter(progress[candidate].UpdatedAt, progress[best].UpdatedAt)) {
			best = candidate
			bestRank = rank
		}
	}
	return best
}

func playableProgressRank(progress map[string]userstore.WatchProgress, contentID string) int {
	entry, ok := progress[contentID]
	if ok && entry.PositionSeconds > 0 && !entry.Completed {
		return 0
	}
	if !ok || !entry.Completed {
		return 1
	}
	return 2
}

func progressUpdatedAfter(candidate, current string) bool {
	candidateTime, candidateErr := time.Parse(time.RFC3339Nano, candidate)
	currentTime, currentErr := time.Parse(time.RFC3339Nano, current)
	if candidateErr == nil && currentErr == nil {
		return candidateTime.After(currentTime)
	}
	return candidate > current
}
