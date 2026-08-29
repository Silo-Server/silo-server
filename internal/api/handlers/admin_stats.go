package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

const (
	adminStatsCacheKey = "global"
	adminStatsCacheTTL = 15 * time.Second
)

// AdminStats represents system statistics for the admin dashboard.
type AdminStats struct {
	TotalItems        int                  `json:"total_items"`
	TotalFiles        int                  `json:"total_files"`
	TotalUsers        int                  `json:"total_users"`
	TotalMovies       int                  `json:"total_movies"`
	TotalMovieFiles   int                  `json:"total_movie_files"`
	TotalShows        int                  `json:"total_shows"`
	TotalShowFiles    int                  `json:"total_show_files"`
	ActiveStreams     int                  `json:"active_streams"`
	TotalStorageBytes int64                `json:"total_storage_bytes"`
	WatchProviders    []WatchProviderStats `json:"watch_providers"`
}

// WatchProviderStats is one registered watch provider's connection and 24-hour
// sync activity. The dashboard renders one row per entry, so every provider the
// watchsync registry knows about appears — including providers contributed by a
// plugin at runtime — with zeros when it has never synced.
//
// An entry whose provider is not (or is no longer) registered still appears
// when the watch-provider tables hold rows for it: uninstalling a plugin must
// not silently drop the history an admin is looking at. Those entries carry
// Registered=false and fall back to the provider key as the display name.
type WatchProviderStats struct {
	Provider    string `json:"provider"`
	DisplayName string `json:"display_name"`
	// Registered is false for a provider that only exists in stored rows —
	// its plugin is uninstalled or disabled.
	Registered bool `json:"registered"`
	// Scrobbling and Exporting mirror the provider's declared capabilities so
	// the widget can hide counters a provider can never produce.
	Scrobbling              bool       `json:"scrobbling"`
	Exporting               bool       `json:"exporting"`
	ConnectedProfiles       int64      `json:"connected_profiles"`
	EnabledProfiles         int64      `json:"enabled_profiles"`
	ExportEnabledProfiles   int64      `json:"export_enabled_profiles"`
	ScrobbleEnabledProfiles int64      `json:"scrobble_enabled_profiles"`
	LastSyncCompletedAt     *time.Time `json:"last_sync_completed_at,omitempty"`
	SyncRuns24h             int64      `json:"sync_runs_24h"`
	SyncErrors24h           int64      `json:"sync_errors_24h"`
	ImportedWatched24h      int64      `json:"imported_watched_24h"`
	ImportedProgress24h     int64      `json:"imported_progress_24h"`
	ExportedWatched24h      int64      `json:"exported_watched_24h"`
	PendingExports          int64      `json:"pending_exports"`
	FailedExports           int64      `json:"failed_exports"`
	OpenScrobbles           int64      `json:"open_scrobbles"`
	Scrobbles24h            int64      `json:"scrobbles_24h"`
}

// WatchProviderLister is the narrow view of the watchsync registry the admin
// stats need: which providers exist and what they can do. A nil lister (no
// database, so no registry) degrades to "activity rows only".
type WatchProviderLister interface {
	List() []watchsync.ProviderSummary
}

// AdminStatsSource returns cached or freshly queried admin stats.
type AdminStatsSource interface {
	Get(ctx context.Context) (AdminStats, error)
	Invalidate()
}

// AdminStatsProvider serves exact admin stats with a short in-process TTL and
// optional cross-node invalidation via the shared event bus.
//
// The cached payload is a pointer because cache.TTLCache requires a comparable
// value and AdminStats now carries the per-provider slice. Callers only read
// and serialize the snapshot, as with the other admin aggregates.
type AdminStatsProvider struct {
	pool      *pgxpool.Pool
	providers WatchProviderLister
	cache     *cache.TTLCache[*AdminStats]
	ttl       time.Duration
}

var _ AdminStatsSource = (*AdminStatsProvider)(nil)

// NewAdminStatsProvider creates a cached provider and subscribes it to the
// shared invalidation channels when an event bus is configured. providers is
// the watchsync registry (or any narrow view of it) and may be nil.
func NewAdminStatsProvider(ctx context.Context, pool *pgxpool.Pool, bus cache.EventBus, providers WatchProviderLister) (*AdminStatsProvider, error) {
	provider := &AdminStatsProvider{
		pool:      pool,
		providers: providers,
		cache:     cache.NewTTLCache[*AdminStats](),
		ttl:       adminStatsCacheTTL,
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
	if stats, ok := p.cache.Get(adminStatsCacheKey); ok && stats != nil {
		return *stats, nil
	}

	stats, err := queryAdminStats(ctx, p.pool, p.providers)
	if err != nil {
		return AdminStats{}, err
	}
	p.cache.Set(adminStatsCacheKey, &stats, p.ttl)
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

func queryAdminStats(ctx context.Context, pool *pgxpool.Pool, providers WatchProviderLister) (AdminStats, error) {
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
		slog.WarnContext(ctx, "failed to query watch provider admin stats", "component", "api", "error", err)
		activity = nil
	}

	return AdminStats{
		TotalUsers:        int(totalUsers),
		TotalItems:        int(totalItems),
		TotalFiles:        int(totalFiles),
		TotalMovies:       int(totalMovies),
		TotalMovieFiles:   int(totalMovieFiles),
		TotalShows:        int(totalShows),
		TotalShowFiles:    int(totalShowFiles),
		ActiveStreams:     int(activeStreams),
		TotalStorageBytes: totalStorage,
		WatchProviders:    mergeWatchProviderStats(listWatchProviders(providers), activity),
	}, nil
}

// listWatchProviders tolerates both a nil interface and a typed-nil registry.
func listWatchProviders(providers WatchProviderLister) []watchsync.ProviderSummary {
	if providers == nil {
		return nil
	}
	return providers.List()
}

// mergeWatchProviderStats overlays the per-provider activity rows onto the set
// of registered providers. Registered providers always appear (with zeros when
// they have no rows); a row for an unregistered provider is kept so history
// from an uninstalled plugin stays visible. Ordering is by provider key so the
// dashboard rows do not reshuffle between polls.
func mergeWatchProviderStats(summaries []watchsync.ProviderSummary, activity []WatchProviderStats) []WatchProviderStats {
	byProvider := make(map[string]WatchProviderStats, len(summaries)+len(activity))
	for _, row := range activity {
		if row.Provider == "" {
			continue
		}
		row.DisplayName = row.Provider
		byProvider[row.Provider] = row
	}
	for _, summary := range summaries {
		if summary.Key == "" {
			continue
		}
		stats := byProvider[summary.Key]
		stats.Provider = summary.Key
		stats.DisplayName = summary.DisplayName
		if stats.DisplayName == "" {
			stats.DisplayName = summary.Key
		}
		stats.Registered = true
		stats.Scrobbling = summary.Capabilities.ScrobblePlayback
		stats.Exporting = summary.Capabilities.ExportWatched
		byProvider[summary.Key] = stats
	}

	merged := make([]WatchProviderStats, 0, len(byProvider))
	for _, stats := range byProvider {
		merged = append(merged, stats)
	}
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Provider < merged[j].Provider
	})
	return merged
}

func queryWatchProviderActivity(ctx context.Context, pool *pgxpool.Pool) ([]WatchProviderStats, error) {
	ready, err := watchProviderStatsTablesReady(ctx, pool)
	if err != nil {
		return nil, err
	}
	if !ready {
		return nil, nil
	}

	// One pass per table, grouped by provider, then joined onto the union of
	// every provider key those tables mention. Grouping keeps this the same
	// four scans the single-provider version cost, whatever the provider count.
	rows, err := pool.Query(ctx, `
		WITH watch_provider_connection_stats AS (
			SELECT
				provider,
				COUNT(*)::bigint AS connected_profiles,
				COUNT(*) FILTER (
					WHERE import_watched_enabled
					   OR import_progress_enabled
					   OR export_watched_enabled
					   OR scrobble_enabled
				)::bigint AS enabled_profiles,
				COUNT(*) FILTER (WHERE export_watched_enabled)::bigint AS export_enabled_profiles,
				COUNT(*) FILTER (WHERE scrobble_enabled)::bigint AS scrobble_enabled_profiles
			FROM watch_provider_connections
			GROUP BY provider
		),
		watch_provider_sync_stats AS (
			SELECT
				provider,
				MAX(completed_at) AS last_sync_completed_at,
				COUNT(*) FILTER (WHERE started_at >= now() - interval '24 hours')::bigint AS sync_runs_24h,
				COUNT(*) FILTER (
					WHERE status = 'failed'
					  AND started_at >= now() - interval '24 hours'
				)::bigint AS sync_errors_24h,
				COALESCE(SUM(inbound_watched_imported) FILTER (
					WHERE started_at >= now() - interval '24 hours'
				), 0)::bigint AS imported_watched_24h,
				COALESCE(SUM(inbound_progress_imported) FILTER (
					WHERE started_at >= now() - interval '24 hours'
				), 0)::bigint AS imported_progress_24h,
				COALESCE(SUM(outbound_sent) FILTER (
					WHERE started_at >= now() - interval '24 hours'
				), 0)::bigint AS exported_watched_24h
			FROM watch_provider_sync_runs
			GROUP BY provider
		),
		watch_provider_export_stats AS (
			SELECT
				c.provider,
				COUNT(*) FILTER (WHERE e.status = 'pending')::bigint AS pending_exports,
				COUNT(*) FILTER (WHERE e.status = 'failed')::bigint AS failed_exports
			FROM watch_provider_history_exports e
			JOIN watch_provider_connections c ON c.id = e.connection_id
			GROUP BY c.provider
		),
		watch_provider_scrobble_stats AS (
			SELECT
				c.provider,
				COUNT(*) FILTER (WHERE s.stop_sent_at IS NULL)::bigint AS open_scrobbles,
				COUNT(*) FILTER (WHERE s.updated_at >= now() - interval '24 hours')::bigint AS scrobbles_24h
			FROM watch_provider_scrobble_sessions s
			JOIN watch_provider_connections c ON c.id = s.connection_id
			GROUP BY c.provider
		),
		watch_provider_keys AS (
			SELECT provider FROM watch_provider_connection_stats
			UNION
			SELECT provider FROM watch_provider_sync_stats
			UNION
			SELECT provider FROM watch_provider_export_stats
			UNION
			SELECT provider FROM watch_provider_scrobble_stats
		)
		SELECT
			k.provider,
			COALESCE(c.connected_profiles, 0),
			COALESCE(c.enabled_profiles, 0),
			COALESCE(c.export_enabled_profiles, 0),
			COALESCE(c.scrobble_enabled_profiles, 0),
			s.last_sync_completed_at,
			COALESCE(s.sync_runs_24h, 0),
			COALESCE(s.sync_errors_24h, 0),
			COALESCE(s.imported_watched_24h, 0),
			COALESCE(s.imported_progress_24h, 0),
			COALESCE(s.exported_watched_24h, 0),
			COALESCE(e.pending_exports, 0),
			COALESCE(e.failed_exports, 0),
			COALESCE(sc.open_scrobbles, 0),
			COALESCE(sc.scrobbles_24h, 0)
		FROM watch_provider_keys k
		LEFT JOIN watch_provider_connection_stats c ON c.provider = k.provider
		LEFT JOIN watch_provider_sync_stats s ON s.provider = k.provider
		LEFT JOIN watch_provider_export_stats e ON e.provider = k.provider
		LEFT JOIN watch_provider_scrobble_stats sc ON sc.provider = k.provider
		ORDER BY k.provider
	`)
	if err != nil {
		return nil, fmt.Errorf("querying watch provider activity stats: %w", err)
	}
	defer rows.Close()

	var activity []WatchProviderStats
	for rows.Next() {
		var stats WatchProviderStats
		if err := rows.Scan(
			&stats.Provider,
			&stats.ConnectedProfiles,
			&stats.EnabledProfiles,
			&stats.ExportEnabledProfiles,
			&stats.ScrobbleEnabledProfiles,
			&stats.LastSyncCompletedAt,
			&stats.SyncRuns24h,
			&stats.SyncErrors24h,
			&stats.ImportedWatched24h,
			&stats.ImportedProgress24h,
			&stats.ExportedWatched24h,
			&stats.PendingExports,
			&stats.FailedExports,
			&stats.OpenScrobbles,
			&stats.Scrobbles24h,
		); err != nil {
			return nil, fmt.Errorf("scanning watch provider activity stats: %w", err)
		}
		activity = append(activity, stats)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("querying watch provider activity stats: %w", err)
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
