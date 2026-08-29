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
	adminTopActivityCacheTTL    = 5 * time.Minute
	adminTopActivityCachePrefix = "days="

	adminTopActivityDefaultDays = 7
	adminTopActivityMinDays     = 1
	adminTopActivityMaxDays     = 30

	adminTopActivityDefaultLimit = 10
	adminTopActivityMinLimit     = 1
	adminTopActivityMaxLimit     = 25
)

// adminTopActivityWatchSourceFilter keeps only history that originated on this
// server, so the leaderboards describe what people actually played here.
// `manual` (marked-watched) stays in as an on-server action, and `jellycompat`
// is real playback through the Jellyfin surface. This is an allowlist rather
// than a denylist of known providers because plugin watch providers store
// their own arbitrary keys in `source` — a denylist would silently count any
// newly installed provider's imported backlog as local plays.
const adminTopActivityWatchSourceFilter = `COALESCE(h.source, 'legacy') IN ('legacy', 'manual', 'playback', 'jellycompat')`

// AdminTopTitle is one row of the most-watched-titles list. Episodes are rolled
// up to their series, so media_item_id is a series content id for TV.
type AdminTopTitle struct {
	MediaItemID string `json:"media_item_id"`
	Title       string `json:"title"`
	MediaType   string `json:"media_type"`
	Plays       int64  `json:"plays"`
	// TotalSeconds is watched time summed from finalized playback sessions, not
	// the runtime of the titles played. A title that was only ever marked
	// watched has no sessions and reports 0.
	TotalSeconds int64 `json:"total_seconds"`
}

// AdminTopProfile is one row of the most-active-profiles list.
type AdminTopProfile struct {
	UserID      int    `json:"user_id"`
	Username    string `json:"username"`
	ProfileID   string `json:"profile_id"`
	ProfileName string `json:"profile_name"`
	Plays       int64  `json:"plays"`
	// TotalSeconds is watched time summed from this profile's finalized
	// playback sessions, not the runtime of what it played.
	TotalSeconds int64 `json:"total_seconds"`
}

// AdminTopActivity is the GET /admin/stats/top-activity body.
type AdminTopActivity struct {
	Days     int               `json:"days"`
	Limit    int               `json:"limit"`
	Titles   []AdminTopTitle   `json:"titles"`
	Profiles []AdminTopProfile `json:"profiles"`
}

// AdminTopActivitySource returns cached or freshly queried leaderboards.
type AdminTopActivitySource interface {
	Get(ctx context.Context, days, limit int) (*AdminTopActivity, error)
	Invalidate()
}

// AdminTopActivityProvider serves the leaderboards with a longer TTL than the
// other dashboard aggregates: a seven-day ranking barely moves within minutes,
// and the query is the most expensive one on the page.
//
// The cached payload is a pointer because cache.TTLCache requires a comparable
// value type and this struct carries slices.
type AdminTopActivityProvider struct {
	pool  *pgxpool.Pool
	cache *cache.TTLCache[*AdminTopActivity]
	ttl   time.Duration
}

var _ AdminTopActivitySource = (*AdminTopActivityProvider)(nil)

// NewAdminTopActivityProvider creates a cached provider and subscribes it to
// the playback/admin invalidation channels when an event bus is configured.
func NewAdminTopActivityProvider(ctx context.Context, pool *pgxpool.Pool, bus cache.EventBus) (*AdminTopActivityProvider, error) {
	provider := &AdminTopActivityProvider{
		pool:  pool,
		cache: cache.NewTTLCache[*AdminTopActivity](),
		ttl:   adminTopActivityCacheTTL,
	}

	if bus == nil || ctx == nil {
		return provider, nil
	}

	handler := func(cache.Event) {
		provider.Invalidate()
	}
	for _, channel := range []string{cache.ChannelAdmin, cache.ChannelPlayback} {
		if err := bus.Subscribe(ctx, channel, handler); err != nil {
			provider.Close()
			return nil, fmt.Errorf("subscribing admin top activity provider to %s: %w", channel, err)
		}
	}

	return provider, nil
}

// Get returns the cached leaderboards when available, otherwise queries Postgres.
func (p *AdminTopActivityProvider) Get(ctx context.Context, days, limit int) (*AdminTopActivity, error) {
	if p == nil || p.pool == nil {
		return nil, fmt.Errorf("admin top activity provider is not configured")
	}
	days = clampQueryInt(days, adminTopActivityMinDays, adminTopActivityMaxDays)
	limit = clampQueryInt(limit, adminTopActivityMinLimit, adminTopActivityMaxLimit)
	key := adminTopActivityCachePrefix + strconv.Itoa(days) + "&limit=" + strconv.Itoa(limit)
	if activity, ok := p.cache.Get(key); ok {
		return activity, nil
	}

	activity, err := queryAdminTopActivity(ctx, p.pool, days, limit)
	if err != nil {
		return nil, err
	}
	p.cache.Set(key, activity, p.ttl)
	return activity, nil
}

// Invalidate drops every cached window.
func (p *AdminTopActivityProvider) Invalidate() {
	if p == nil || p.cache == nil {
		return
	}
	p.cache.InvalidatePrefix(adminTopActivityCachePrefix)
}

// Close stops the background TTL sweeper.
func (p *AdminTopActivityProvider) Close() {
	if p == nil || p.cache == nil {
		return
	}
	p.cache.Close()
}

// HandleGetTopActivity handles GET /admin/stats/top-activity.
func (h *AdminHandler) HandleGetTopActivity(w http.ResponseWriter, r *http.Request) {
	days, err := parseClampedIntQuery(
		r, "days",
		adminTopActivityDefaultDays,
		adminTopActivityMinDays,
		adminTopActivityMaxDays,
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	limit, err := parseClampedIntQuery(
		r, "limit",
		adminTopActivityDefaultLimit,
		adminTopActivityMinLimit,
		adminTopActivityMaxLimit,
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	var activity *AdminTopActivity
	switch {
	case h.TopActivitySource != nil:
		if isTruthyQuery(r.URL.Query().Get("refresh")) {
			h.TopActivitySource.Invalidate()
		}
		activity, err = h.TopActivitySource.Get(r.Context(), days, limit)
	case h.pool != nil:
		activity, err = queryAdminTopActivity(r.Context(), h.pool, days, limit)
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "Database not configured")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to get top activity")
		return
	}

	writeJSON(w, http.StatusOK, activity)
}

func queryAdminTopActivity(ctx context.Context, pool *pgxpool.Pool, days, limit int) (*AdminTopActivity, error) {
	if pool == nil {
		return nil, fmt.Errorf("database not configured")
	}

	activity := &AdminTopActivity{
		Days:     days,
		Limit:    limit,
		Titles:   []AdminTopTitle{},
		Profiles: []AdminTopProfile{},
	}

	// Episodes roll up to their series: a season binge should read as one show,
	// not twelve one-play entries.
	//
	// Plays come from user_watch_history (marked-watched counts as a play),
	// while total_seconds is watched time summed from finalized playback
	// sessions — user_watch_history.duration_seconds is the media's full
	// runtime, so summing it would report three hours for a movie someone
	// abandoned after a minute. A title that was only ever marked watched has
	// no session rows and reports 0 seconds. Sessions are windowed on ended_at
	// — the same stop instant watched_at records — so a session that started
	// before the cutoff but stopped inside the window counts toward both plays
	// and watch time rather than only the former.
	//
	// watched_seconds records the final absolute position, not elapsed viewing
	// time, so a session resumed at the one-hour mark would claim the first
	// hour again; each session's contribution is therefore capped at its
	// wall-clock length. Still an estimate — recording true elapsed playback
	// needs a session start position, which does not exist yet.
	//
	// The ranking is computed and limited first so the title lookup and the
	// watched-seconds aggregate only run over the rows that survive.
	titleRows, err := pool.Query(ctx, `
		WITH ranked AS (
			SELECT COALESCE(ep.series_id, h.media_item_id) AS item_id,
			       bool_or(ep.content_id IS NOT NULL) AS is_series,
			       COUNT(*)::bigint AS plays
			FROM user_watch_history h
			LEFT JOIN episodes ep ON ep.content_id = h.media_item_id
			WHERE h.watched_at >= now() - make_interval(days => $1)
			  AND `+adminTopActivityWatchSourceFilter+`
			GROUP BY 1
			ORDER BY plays DESC, item_id
			LIMIT $2
		),
		watched AS (
			SELECT COALESCE(ep.series_id, p.media_item_id) AS item_id,
			       SUM(LEAST(GREATEST(p.watched_seconds, 0),
			                 GREATEST(EXTRACT(EPOCH FROM (p.ended_at - p.started_at)), 0))) AS total_seconds
			FROM playback_history_admin p
			LEFT JOIN episodes ep ON ep.content_id = p.media_item_id
			WHERE p.ended_at >= now() - make_interval(days => $1)
			  AND COALESCE(ep.series_id, p.media_item_id) IN (SELECT item_id FROM ranked)
			GROUP BY 1
		)
		SELECT r.item_id,
		       COALESCE(mi.title, '') AS title,
		       COALESCE(CASE WHEN r.is_series THEN 'series' ELSE mi.type END, '') AS media_type,
		       r.plays,
		       COALESCE(w.total_seconds, 0)::bigint AS total_seconds
		FROM ranked r
		LEFT JOIN media_items mi ON mi.content_id = r.item_id
		LEFT JOIN watched w ON w.item_id = r.item_id
		ORDER BY r.plays DESC, total_seconds DESC, r.item_id
	`, days, limit)
	if err != nil {
		return nil, fmt.Errorf("querying top titles: %w", err)
	}
	defer titleRows.Close()

	for titleRows.Next() {
		var title AdminTopTitle
		if err := titleRows.Scan(
			&title.MediaItemID,
			&title.Title,
			&title.MediaType,
			&title.Plays,
			&title.TotalSeconds,
		); err != nil {
			return nil, fmt.Errorf("scanning top title: %w", err)
		}
		activity.Titles = append(activity.Titles, title)
	}
	if err := titleRows.Err(); err != nil {
		return nil, fmt.Errorf("iterating top titles: %w", err)
	}
	titleRows.Close()

	// Profile display names live in the per-user stores, not in
	// user_watch_history, so they are read back from the most recent admin
	// playback-history row for that profile. A profile that has only ever been
	// marked-watched has no such row and falls back to its id.
	//
	// The ranking groups on (user_id, profile_id) alone: a profile that was
	// renamed mid-window would otherwise split into two rows. The name lookup
	// and the watched-seconds aggregate run after the limit, once per surviving
	// profile rather than once per history row. As above, total_seconds is
	// watched time from finalized playback sessions.
	profileRows, err := pool.Query(ctx, `
		WITH ranked AS (
			SELECT h.user_id,
			       h.profile_id,
			       COUNT(*)::bigint AS plays
			FROM user_watch_history h
			WHERE h.watched_at >= now() - make_interval(days => $1)
			  AND `+adminTopActivityWatchSourceFilter+`
			GROUP BY 1, 2
			ORDER BY plays DESC, h.user_id, h.profile_id
			LIMIT $2
		),
		watched AS (
			SELECT p.user_id,
			       p.profile_id,
			       SUM(LEAST(GREATEST(p.watched_seconds, 0),
			                 GREATEST(EXTRACT(EPOCH FROM (p.ended_at - p.started_at)), 0))) AS total_seconds
			FROM playback_history_admin p
			WHERE p.ended_at >= now() - make_interval(days => $1)
			  AND EXISTS (
				SELECT 1 FROM ranked r
				WHERE r.user_id = p.user_id AND r.profile_id = p.profile_id
			  )
			GROUP BY 1, 2
		)
		SELECT r.user_id,
		       COALESCE(u.username, '') AS username,
		       r.profile_id,
		       COALESCE(pn.profile_name, r.profile_id) AS profile_name,
		       r.plays,
		       COALESCE(w.total_seconds, 0)::bigint AS total_seconds
		FROM ranked r
		LEFT JOIN users u ON u.id = r.user_id
		LEFT JOIN watched w ON w.user_id = r.user_id AND w.profile_id = r.profile_id
		LEFT JOIN LATERAL (
			SELECT p.profile_name
			FROM playback_history_admin p
			WHERE p.user_id = r.user_id
			  AND p.profile_id = r.profile_id
			  AND p.profile_name <> ''
			ORDER BY p.ended_at DESC
			LIMIT 1
		) pn ON TRUE
		ORDER BY r.plays DESC, total_seconds DESC, r.user_id, r.profile_id
	`, days, limit)
	if err != nil {
		return nil, fmt.Errorf("querying top profiles: %w", err)
	}
	defer profileRows.Close()

	for profileRows.Next() {
		var profile AdminTopProfile
		if err := profileRows.Scan(
			&profile.UserID,
			&profile.Username,
			&profile.ProfileID,
			&profile.ProfileName,
			&profile.Plays,
			&profile.TotalSeconds,
		); err != nil {
			return nil, fmt.Errorf("scanning top profile: %w", err)
		}
		activity.Profiles = append(activity.Profiles, profile)
	}
	if err := profileRows.Err(); err != nil {
		return nil, fmt.Errorf("iterating top profiles: %w", err)
	}

	return activity, nil
}
