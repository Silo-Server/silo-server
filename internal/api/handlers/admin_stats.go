package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/cache"
)

const (
	adminStatsCacheKey = "global"
	adminStatsCacheTTL = 15 * time.Second
)

// AdminStats represents system statistics for the admin dashboard.
type AdminStats struct {
	TotalItems            int                   `json:"total_items"`
	TotalFiles            int                   `json:"total_files"`
	TotalUsers            int                   `json:"total_users"`
	TotalMovies           int                   `json:"total_movies"`
	TotalMovieFiles       int                   `json:"total_movie_files"`
	TotalShows            int                   `json:"total_shows"`
	TotalShowFiles        int                   `json:"total_show_files"`
	ActiveStreams         int                   `json:"active_streams"`
	TotalStorageBytes     int64                 `json:"total_storage_bytes"`
	WatchProviderActivity WatchProviderActivity `json:"watch_provider_activity"`
}

type WatchProviderActivity struct {
	TraktConnectedProfiles int64      `json:"trakt_connected_profiles"`
	TraktEnabledProfiles   int64      `json:"trakt_enabled_profiles"`
	TraktExportEnabled     int64      `json:"trakt_export_enabled"`
	TraktScrobbleEnabled   int64      `json:"trakt_scrobble_enabled"`
	LastSyncCompletedAt    *time.Time `json:"last_sync_completed_at,omitempty"`
	SyncRuns24h            int64      `json:"sync_runs_24h"`
	SyncErrors24h          int64      `json:"sync_errors_24h"`
	ImportedWatched24h     int64      `json:"imported_watched_24h"`
	ImportedProgress24h    int64      `json:"imported_progress_24h"`
	ExportedWatched24h     int64      `json:"exported_watched_24h"`
	PendingExports         int64      `json:"pending_exports"`
	FailedExports          int64      `json:"failed_exports"`
	OpenScrobbles          int64      `json:"open_scrobbles"`
	Scrobbles24h           int64      `json:"scrobbles_24h"`
}

// AdminStatsSource returns cached or freshly queried admin stats.
type AdminStatsSource interface {
	Get(ctx context.Context) (AdminStats, error)
	Invalidate()
}

// AdminStatsProvider serves exact admin stats with a short in-process TTL and
// optional cross-node invalidation via the shared event bus.
type AdminStatsProvider struct {
	pool  *pgxpool.Pool
	cache *cache.TTLCache[AdminStats]
	ttl   time.Duration
}

var _ AdminStatsSource = (*AdminStatsProvider)(nil)

// NewAdminStatsProvider creates a cached provider and subscribes it to the
// shared invalidation channels when an event bus is configured.
func NewAdminStatsProvider(ctx context.Context, pool *pgxpool.Pool, bus cache.EventBus) (*AdminStatsProvider, error) {
	provider := &AdminStatsProvider{
		pool:  pool,
		cache: cache.NewTTLCache[AdminStats](),
		ttl:   adminStatsCacheTTL,
	}

	if bus == nil || ctx == nil {
		return provider, nil
	}

	handler := func(cache.Event) {
		provider.Invalidate()
	}
	for _, channel := range []string{cache.ChannelCatalog, cache.ChannelAdmin, cache.ChannelPlayback} {
		if err := bus.Subscribe(ctx, channel, handler); err != nil {
			provider.Close()
			return nil, fmt.Errorf("subscribing admin stats provider to %s: %w", channel, err)
		}
	}

	return provider, nil
}

// Get returns cached stats when available, otherwise it queries Postgres and
// stores the exact result for a short period.
func (p *AdminStatsProvider) Get(ctx context.Context) (AdminStats, error) {
	if p == nil || p.pool == nil {
		return AdminStats{}, fmt.Errorf("admin stats provider is not configured")
	}
	if stats, ok := p.cache.Get(adminStatsCacheKey); ok {
		return stats, nil
	}

	stats, err := queryAdminStats(ctx, p.pool)
	if err != nil {
		return AdminStats{}, err
	}
	p.cache.Set(adminStatsCacheKey, stats, p.ttl)
	return stats, nil
}

// Invalidate drops the current cached stats snapshot.
func (p *AdminStatsProvider) Invalidate() {
	if p == nil || p.cache == nil {
		return
	}
	p.cache.Invalidate(adminStatsCacheKey)
}

// Close stops the background TTL sweeper.
func (p *AdminStatsProvider) Close() {
	if p == nil || p.cache == nil {
		return
	}
	p.cache.Close()
}

func queryAdminStats(ctx context.Context, pool *pgxpool.Pool) (AdminStats, error) {
	if pool == nil {
		return AdminStats{}, fmt.Errorf("database not configured")
	}

	var (
		totalUsers      int64
		totalItems      int64
		totalFiles      int64
		totalMovies     int64
		totalMovieFiles int64
		totalShows      int64
		totalShowFiles  int64
		activeStreams   int64
		totalStorage    int64
	)

	row := pool.QueryRow(ctx, `
		WITH user_stats AS (
			SELECT COUNT(*)::bigint AS total_users
			FROM users
		),
		item_stats AS (
			SELECT
				COUNT(*)::bigint AS total_items,
				COUNT(*) FILTER (WHERE type = 'movie')::bigint AS total_movies,
				COUNT(*) FILTER (WHERE type = 'series')::bigint AS total_shows
			FROM media_items
		),
		file_stats AS (
			SELECT
				COUNT(*)::bigint AS total_files,
				COUNT(*) FILTER (WHERE file_kind = 'movie')::bigint AS total_movie_files,
				COUNT(*) FILTER (WHERE file_kind = 'series')::bigint AS total_show_files,
				COALESCE(SUM(file_size), 0)::bigint AS total_storage_bytes
			FROM (
				SELECT
					media_files.file_size,
					CASE
						WHEN lower(trim(COALESCE(NULLIF(media_items.type, ''), ''))) = 'movie'
							THEN 'movie'
						WHEN lower(trim(COALESCE(NULLIF(media_items.type, ''), ''))) = 'series'
							THEN 'series'
						WHEN episodes.content_id IS NOT NULL
							THEN 'series'
						WHEN lower(trim(COALESCE(NULLIF(media_files.base_type, ''), ''))) IN ('movie', 'movies')
							THEN 'movie'
						WHEN lower(trim(COALESCE(NULLIF(media_files.base_type, ''), ''))) IN ('series', 'tv', 'show', 'shows', 'tvshows')
							THEN 'series'
						WHEN lower(trim(media_folders.type)) IN ('movie', 'movies')
							THEN 'movie'
						WHEN lower(trim(media_folders.type)) IN ('series', 'tv', 'show', 'shows', 'tvshows')
							THEN 'series'
						ELSE ''
					END AS file_kind
				FROM media_files
				JOIN media_folders ON media_folders.id = media_files.media_folder_id
				LEFT JOIN media_items ON media_items.content_id = media_files.content_id
				LEFT JOIN episodes ON episodes.content_id = media_files.episode_id
			) classified_files
		),
		session_stats AS (
			SELECT COUNT(*)::bigint AS active_streams
			FROM playback_sessions_sync
		)
		SELECT
			user_stats.total_users,
			item_stats.total_items,
			file_stats.total_files,
			item_stats.total_movies,
			file_stats.total_movie_files,
			item_stats.total_shows,
			file_stats.total_show_files,
			session_stats.active_streams,
			file_stats.total_storage_bytes
		FROM user_stats
		CROSS JOIN item_stats
		CROSS JOIN file_stats
		CROSS JOIN session_stats
	`)
	if err := row.Scan(
		&totalUsers,
		&totalItems,
		&totalFiles,
		&totalMovies,
		&totalMovieFiles,
		&totalShows,
		&totalShowFiles,
		&activeStreams,
		&totalStorage,
	); err != nil {
		return AdminStats{}, fmt.Errorf("querying admin stats: %w", err)
	}

	activity, err := queryWatchProviderActivity(ctx, pool)
	if err != nil {
		slog.Warn("failed to query watch provider admin stats", "error", err)
		activity = WatchProviderActivity{}
	}

	return AdminStats{
		TotalUsers:            int(totalUsers),
		TotalItems:            int(totalItems),
		TotalFiles:            int(totalFiles),
		TotalMovies:           int(totalMovies),
		TotalMovieFiles:       int(totalMovieFiles),
		TotalShows:            int(totalShows),
		TotalShowFiles:        int(totalShowFiles),
		ActiveStreams:         int(activeStreams),
		TotalStorageBytes:     totalStorage,
		WatchProviderActivity: activity,
	}, nil
}

func queryWatchProviderActivity(ctx context.Context, pool *pgxpool.Pool) (WatchProviderActivity, error) {
	ready, err := watchProviderStatsTablesReady(ctx, pool)
	if err != nil {
		return WatchProviderActivity{}, err
	}
	if !ready {
		return WatchProviderActivity{}, nil
	}

	var activity WatchProviderActivity
	row := pool.QueryRow(ctx, `
		WITH watch_provider_connection_stats AS (
			SELECT
				COUNT(*) FILTER (WHERE provider = 'trakt')::bigint AS trakt_connected_profiles,
				COUNT(*) FILTER (
					WHERE provider = 'trakt'
					  AND (
						import_watched_enabled
						OR import_progress_enabled
						OR export_watched_enabled
						OR scrobble_enabled
					  )
				)::bigint AS trakt_enabled_profiles,
				COUNT(*) FILTER (WHERE provider = 'trakt' AND export_watched_enabled)::bigint AS trakt_export_enabled,
				COUNT(*) FILTER (WHERE provider = 'trakt' AND scrobble_enabled)::bigint AS trakt_scrobble_enabled
			FROM watch_provider_connections
		),
		watch_provider_sync_stats AS (
			SELECT
				MAX(completed_at) FILTER (WHERE provider = 'trakt') AS last_sync_completed_at,
				COUNT(*) FILTER (
					WHERE provider = 'trakt'
					  AND started_at >= now() - interval '24 hours'
				)::bigint AS sync_runs_24h,
				COUNT(*) FILTER (
					WHERE provider = 'trakt'
					  AND status = 'failed'
					  AND started_at >= now() - interval '24 hours'
				)::bigint AS sync_errors_24h,
				COALESCE(SUM(inbound_watched_imported) FILTER (
					WHERE provider = 'trakt'
					  AND started_at >= now() - interval '24 hours'
				), 0)::bigint AS imported_watched_24h,
				COALESCE(SUM(inbound_progress_imported) FILTER (
					WHERE provider = 'trakt'
					  AND started_at >= now() - interval '24 hours'
				), 0)::bigint AS imported_progress_24h,
				COALESCE(SUM(outbound_sent) FILTER (
					WHERE provider = 'trakt'
					  AND started_at >= now() - interval '24 hours'
				), 0)::bigint AS exported_watched_24h
			FROM watch_provider_sync_runs
		),
		watch_provider_export_stats AS (
			SELECT
				COUNT(*) FILTER (
					WHERE c.provider = 'trakt'
					  AND e.status = 'pending'
				)::bigint AS pending_exports,
				COUNT(*) FILTER (
					WHERE c.provider = 'trakt'
					  AND e.status = 'failed'
				)::bigint AS failed_exports
			FROM watch_provider_history_exports e
			JOIN watch_provider_connections c ON c.id = e.connection_id
		),
		watch_provider_scrobble_stats AS (
			SELECT
				COUNT(*) FILTER (
					WHERE c.provider = 'trakt'
					  AND s.stop_sent_at IS NULL
				)::bigint AS open_scrobbles,
				COUNT(*) FILTER (
					WHERE c.provider = 'trakt'
					  AND s.updated_at >= now() - interval '24 hours'
				)::bigint AS scrobbles_24h
			FROM watch_provider_scrobble_sessions s
			JOIN watch_provider_connections c ON c.id = s.connection_id
		)
		SELECT
			watch_provider_connection_stats.trakt_connected_profiles,
			watch_provider_connection_stats.trakt_enabled_profiles,
			watch_provider_connection_stats.trakt_export_enabled,
			watch_provider_connection_stats.trakt_scrobble_enabled,
			watch_provider_sync_stats.last_sync_completed_at,
			watch_provider_sync_stats.sync_runs_24h,
			watch_provider_sync_stats.sync_errors_24h,
			watch_provider_sync_stats.imported_watched_24h,
			watch_provider_sync_stats.imported_progress_24h,
			watch_provider_sync_stats.exported_watched_24h,
			watch_provider_export_stats.pending_exports,
			watch_provider_export_stats.failed_exports,
			watch_provider_scrobble_stats.open_scrobbles,
			watch_provider_scrobble_stats.scrobbles_24h
		FROM watch_provider_connection_stats
		CROSS JOIN watch_provider_sync_stats
		CROSS JOIN watch_provider_export_stats
		CROSS JOIN watch_provider_scrobble_stats
	`)
	if err := row.Scan(
		&activity.TraktConnectedProfiles,
		&activity.TraktEnabledProfiles,
		&activity.TraktExportEnabled,
		&activity.TraktScrobbleEnabled,
		&activity.LastSyncCompletedAt,
		&activity.SyncRuns24h,
		&activity.SyncErrors24h,
		&activity.ImportedWatched24h,
		&activity.ImportedProgress24h,
		&activity.ExportedWatched24h,
		&activity.PendingExports,
		&activity.FailedExports,
		&activity.OpenScrobbles,
		&activity.Scrobbles24h,
	); err != nil {
		return WatchProviderActivity{}, fmt.Errorf("querying watch provider activity stats: %w", err)
	}

	return activity, nil
}

func watchProviderStatsTablesReady(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	var ready bool
	err := pool.QueryRow(ctx, `
		SELECT bool_and(to_regclass(table_name) IS NOT NULL)
		FROM unnest($1::text[]) AS table_name
	`, []string{
		"public.watch_provider_connections",
		"public.watch_provider_sync_runs",
		"public.watch_provider_history_exports",
		"public.watch_provider_scrobble_sessions",
	}).Scan(&ready)
	if err != nil {
		return false, fmt.Errorf("checking watch provider stats tables: %w", err)
	}
	return ready, nil
}
