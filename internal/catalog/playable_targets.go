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
	allCandidateIDs := make([]string, 0, len(ids))
	seenCandidateIDs := make(map[string]struct{}, len(ids))
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
		if _, ok := seenCandidateIDs[playContentID]; !ok {
			seenCandidateIDs[playContentID] = struct{}{}
			allCandidateIDs = append(allCandidateIDs, playContentID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating playable poster targets: %w", err)
	}
	progress := map[string]userstore.WatchProgress{}
	if q.ProgressStore != nil && len(allCandidateIDs) > 0 {
		progress, err = listPlayableTargetProgress(ctx, q.ProgressStore, q.ProfileID, allCandidateIDs)
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
