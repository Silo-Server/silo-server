package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/cache"
)

const (
	// adminDownloadsStatsCacheTTL sits at the dashboard's 60s refresh cadence:
	// download state changes far slower than playback, and the widget's numbers
	// are read as "roughly now", not as a live counter.
	adminDownloadsStatsCacheTTL    = time.Minute
	adminDownloadsStatsCachePrefix = "downloads-limit="

	adminDownloadsStatsDefaultLimit = 10
	adminDownloadsStatsMinLimit     = 1
	adminDownloadsStatsMaxLimit     = 25
)

// downloadsActiveFilter defines "active": a managed device entry (the
// device-aware lifecycle in internal/downloads) that has not ended in
// failure, cancellation, or revocation. Ephemeral web rows (NULL device_id)
// are one-shot transfers that get pruned, not media somebody keeps on a
// device, so they stay out of the active numbers — they still count in the
// 24-hour started/completed totals below. prefix qualifies the columns for a
// query that aliases the downloads table.
func downloadsActiveFilter(prefix string) string {
	return prefix + `device_id IS NOT NULL AND ` + prefix + `status IN ('queued', 'preparing', 'ready', 'downloading', 'completed')`
}

// AdminDownloadsUser is one row of the per-user active-downloads list.
type AdminDownloadsUser struct {
	UserID   int    `json:"user_id"`
	Username string `json:"username"`
	// Downloads is this account's active managed device entries.
	Downloads int64 `json:"downloads"`
	// TotalBytes sums the file sizes of this account's completed device
	// entries — bytes actually sitting on devices, not bytes still queued.
	TotalBytes int64 `json:"total_bytes"`
}

// AdminDownloadsStats is the GET /admin/stats/downloads body. Every count is
// zero and TopUsers is an empty array on a deployment where nobody downloads
// (or where the downloads feature is disabled); the widget reads that as its
// empty state, never as an error.
type AdminDownloadsStats struct {
	// UsersWithDownloads is the distinct accounts (users rows, not household
	// profiles) with at least one active managed download.
	UsersWithDownloads int64 `json:"users_with_downloads"`
	// ActiveDownloads counts active managed device entries (items, where a
	// series batch contributes one entry per episode).
	ActiveDownloads int64 `json:"active_downloads"`
	// TotalBytes sums the file sizes of completed device entries: the bytes
	// currently sitting on devices, as far as the server can know without the
	// device reporting back.
	TotalBytes int64 `json:"total_bytes"`
	// DownloadsStarted24h counts rows created in the last 24 hours across both
	// lifecycles (managed device entries and one-shot web downloads).
	DownloadsStarted24h int64 `json:"downloads_started_24h"`
	// DownloadsCompleted24h counts rows that reached completed in the last 24
	// hours across both lifecycles.
	DownloadsCompleted24h int64 `json:"downloads_completed_24h"`
	// Limit is the clamped top-list size the response was built with.
	Limit int `json:"limit"`
	// TopUsers ranks accounts by active managed downloads.
	TopUsers []AdminDownloadsUser `json:"top_users"`
}

// AdminDownloadsStatsSource returns cached or freshly queried download stats.
type AdminDownloadsStatsSource interface {
	Get(ctx context.Context, limit int) (*AdminDownloadsStats, error)
	Invalidate()
}

// AdminDownloadsStatsProvider serves the downloads aggregate with a short
// in-process TTL and optional cross-node invalidation via the shared event
// bus, mirroring AdminStatsProvider. Downloads publish no bus events of their
// own today, so the TTL (and the widget's ?refresh=1) is what bounds
// staleness; the admin channel subscription exists so a future downloads
// event needs no provider change.
//
// The cached payload is a pointer because cache.TTLCache requires a comparable
// value type and this struct carries a slice.
type AdminDownloadsStatsProvider struct {
	pool  *pgxpool.Pool
	cache *cache.TTLCache[*AdminDownloadsStats]
	ttl   time.Duration
}

var _ AdminDownloadsStatsSource = (*AdminDownloadsStatsProvider)(nil)

// NewAdminDownloadsStatsProvider creates a cached provider and subscribes it
// to the admin invalidation channel when an event bus is configured.
func NewAdminDownloadsStatsProvider(ctx context.Context, pool *pgxpool.Pool, bus cache.EventBus) (*AdminDownloadsStatsProvider, error) {
	provider := &AdminDownloadsStatsProvider{
		pool:  pool,
		cache: cache.NewTTLCache[*AdminDownloadsStats](),
		ttl:   adminDownloadsStatsCacheTTL,
	}

	if bus == nil || ctx == nil {
		return provider, nil
	}

	if err := bus.Subscribe(ctx, cache.ChannelAdmin, func(cache.Event) {
		provider.Invalidate()
	}); err != nil {
		provider.Close()
		return nil, fmt.Errorf("subscribing admin downloads stats provider to %s: %w", cache.ChannelAdmin, err)
	}

	return provider, nil
}

// Get returns the cached stats when available, otherwise queries Postgres.
func (p *AdminDownloadsStatsProvider) Get(ctx context.Context, limit int) (*AdminDownloadsStats, error) {
	if p == nil || p.pool == nil {
		return nil, fmt.Errorf("admin downloads stats provider is not configured")
	}
	limit = clampQueryInt(limit, adminDownloadsStatsMinLimit, adminDownloadsStatsMaxLimit)
	key := adminDownloadsStatsCachePrefix + strconv.Itoa(limit)
	if stats, ok := p.cache.Get(key); ok {
		return stats, nil
	}

	stats, err := queryAdminDownloadsStats(ctx, p.pool, limit)
	if err != nil {
		return nil, err
	}
	p.cache.Set(key, stats, p.ttl)
	return stats, nil
}

// Invalidate drops every cached variant.
func (p *AdminDownloadsStatsProvider) Invalidate() {
	if p == nil || p.cache == nil {
		return
	}
	p.cache.InvalidatePrefix(adminDownloadsStatsCachePrefix)
}

// Close stops the background TTL sweeper.
func (p *AdminDownloadsStatsProvider) Close() {
	if p == nil || p.cache == nil {
		return
	}
	p.cache.Close()
}

// HandleGetDownloadsStats handles GET /admin/stats/downloads.
func (h *AdminHandler) HandleGetDownloadsStats(w http.ResponseWriter, r *http.Request) {
	limit, err := parseClampedIntQuery(
		r, "limit",
		adminDownloadsStatsDefaultLimit,
		adminDownloadsStatsMinLimit,
		adminDownloadsStatsMaxLimit,
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	var stats *AdminDownloadsStats
	switch {
	case h.DownloadsStatsSource != nil:
		if isTruthyQuery(r.URL.Query().Get("refresh")) {
			h.DownloadsStatsSource.Invalidate()
		}
		stats, err = h.DownloadsStatsSource.Get(r.Context(), limit)
	case h.pool != nil:
		stats, err = queryAdminDownloadsStats(r.Context(), h.pool, limit)
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "Database not configured")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to get downloads stats")
		return
	}

	writeJSON(w, http.StatusOK, stats)
}

// queryAdminDownloadsStats reads the aggregate straight from the downloads
// table (migrations 042 + 20260619020213). The table exists on every deployment
// regardless of whether the downloads service is wired, so a server with the
// feature off answers zeros rather than an error.
func queryAdminDownloadsStats(ctx context.Context, pool *pgxpool.Pool, limit int) (*AdminDownloadsStats, error) {
	if pool == nil {
		return nil, fmt.Errorf("database not configured")
	}

	stats := &AdminDownloadsStats{
		Limit:    limit,
		TopUsers: []AdminDownloadsUser{},
	}

	// One pass over the table for every scalar. GREATEST guards file sizes
	// recorded before a transfer finished sizing (the column defaults to 0 and
	// is never negative in practice, but a SUM must not depend on that).
	if err := pool.QueryRow(ctx, `
		SELECT
			COUNT(DISTINCT user_id) FILTER (WHERE `+downloadsActiveFilter("")+`),
			COUNT(*)                FILTER (WHERE `+downloadsActiveFilter("")+`),
			COALESCE(SUM(GREATEST(file_size, 0)) FILTER (WHERE device_id IS NOT NULL AND status = 'completed'), 0)::bigint,
			COUNT(*) FILTER (WHERE created_at >= now() - interval '24 hours'),
			COUNT(*) FILTER (WHERE status = 'completed' AND completed_at >= now() - interval '24 hours')
		FROM downloads
	`).Scan(
		&stats.UsersWithDownloads,
		&stats.ActiveDownloads,
		&stats.TotalBytes,
		&stats.DownloadsStarted24h,
		&stats.DownloadsCompleted24h,
	); err != nil {
		return nil, fmt.Errorf("querying downloads stats: %w", err)
	}

	// The top list ranks accounts, not profiles: a device entry belongs to a
	// profile, but quota and the download policy hang off the account, so the
	// admin-facing ranking follows the same line.
	rows, err := pool.Query(ctx, `
		SELECT d.user_id,
		       COALESCE(u.username, '') AS username,
		       COUNT(*)::bigint AS downloads,
		       COALESCE(SUM(GREATEST(d.file_size, 0)) FILTER (WHERE d.status = 'completed'), 0)::bigint AS total_bytes
		FROM downloads d
		LEFT JOIN users u ON u.id = d.user_id
		WHERE `+downloadsActiveFilter("d.")+`
		GROUP BY d.user_id, u.username
		ORDER BY downloads DESC, total_bytes DESC, d.user_id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("querying top download users: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var user AdminDownloadsUser
		if err := rows.Scan(&user.UserID, &user.Username, &user.Downloads, &user.TotalBytes); err != nil {
			return nil, fmt.Errorf("scanning top download user: %w", err)
		}
		stats.TopUsers = append(stats.TopUsers, user)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating top download users: %w", err)
	}

	return stats, nil
}
