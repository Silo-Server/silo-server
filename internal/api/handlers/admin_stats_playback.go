package handlers

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/playback"
)

const (
	adminPlaybackActivityCacheTTL    = 60 * time.Second
	adminPlaybackActivityCachePrefix = "hours="

	adminPlaybackActivityDefaultHours = 24
	adminPlaybackActivityMinHours     = 1
	adminPlaybackActivityMaxHours     = 744

	// adminPlaybackActivityHourlyMaxHours is the widest window still bucketed by
	// hour. Past two days an hourly chart is more columns than a widget has
	// pixels, so the buckets become days.
	adminPlaybackActivityHourlyMaxHours = 48

	adminPlaybackActivityHourSeconds = 3600
	adminPlaybackActivityDaySeconds  = 86400
)

// playbackActivityBucketSeconds picks the bucket width for a window: hourly up
// to two days, daily beyond it. The response reports the choice in
// bucket_seconds so the client zero-fills the same shape the server grouped by.
func playbackActivityBucketSeconds(hours int) int {
	if hours <= adminPlaybackActivityHourlyMaxHours {
		return adminPlaybackActivityHourSeconds
	}
	return adminPlaybackActivityDaySeconds
}

// date_trunc field names for the two bucket widths.
const (
	playbackActivityTruncDay  = "day"
	playbackActivityTruncHour = "hour"
)

// playbackActivityTruncField is the date_trunc field matching a bucket width.
func playbackActivityTruncField(bucketSeconds int) string {
	if bucketSeconds >= adminPlaybackActivityDaySeconds {
		return playbackActivityTruncDay
	}
	return playbackActivityTruncHour
}

// AdminPlaybackActivityBucket is one bucket of playback starts, split by the
// play method that was resolved for the session. Only buckets with at least one
// session are present; the dashboard zero-fills the rest of the window so a
// quiet server draws an empty column rather than a shorter chart.
//
// The field stays `hour` for every bucket width: it is the bucket's start
// instant, and renaming it would break every client for no new information —
// bucket_seconds already says how wide the bucket is.
type AdminPlaybackActivityBucket struct {
	Hour      time.Time `json:"hour"`
	Direct    int64     `json:"direct"`
	Remux     int64     `json:"remux"`
	Transcode int64     `json:"transcode"`
}

// AdminPlaybackReliability summarizes how playback went over the window.
//
// Time-to-first-frame and failed-start counts are deliberately absent: nothing
// records a playback *start* event today (playback_history_admin only gains a
// row when a session finalizes), so both would have to be guessed from log
// parsing. They need client telemetry first — see docs/admin-api.md.
type AdminPlaybackReliability struct {
	SessionsStarted   int64   `json:"sessions_started"`
	TranscodeStarts   int64   `json:"transcode_starts"`
	FinalizedSessions int64   `json:"finalized_sessions"`
	CompletedSessions int64   `json:"completed_sessions"`
	CompletionRate    float64 `json:"completion_rate"`
	UniqueProfiles    int64   `json:"unique_profiles"`
}

// AdminPlaybackActivity is the GET /admin/stats/playback-activity body.
//
// Buckets are hourly or daily depending on the window (BucketSeconds says
// which). Reliability is computed over the whole requested window, while
// ProfilesActive24h is a fixed rolling-24h tile that ignores the window — it
// answers "who watched today", which does not become "who watched this month"
// because a chart next to it got wider.
type AdminPlaybackActivity struct {
	Hours         int `json:"hours"`
	BucketSeconds int `json:"bucket_seconds"`
	// From/To are the window on the database clock, which is the clock the
	// bucket filter ran against. The dashboard anchors its bucket grid on To
	// rather than the browser clock: a few seconds of client/server skew
	// around an hour or day boundary would otherwise discard the newest
	// bucket and show a stale one.
	From              time.Time                     `json:"from"`
	To                time.Time                     `json:"to"`
	Buckets           []AdminPlaybackActivityBucket `json:"buckets"`
	Reliability       AdminPlaybackReliability      `json:"reliability"`
	ProfilesActive24h int64                         `json:"profiles_active_24h"`
}

// AdminPlaybackActivitySource returns cached or freshly queried playback
// activity for a window of hours.
type AdminPlaybackActivitySource interface {
	Get(ctx context.Context, hours int) (*AdminPlaybackActivity, error)
	Invalidate()
}

// AdminPlaybackActivityProvider serves playback activity with a short in-process
// TTL and optional cross-node invalidation via the shared event bus, mirroring
// AdminStatsProvider.
//
// The cached payload is a pointer because cache.TTLCache requires a comparable
// value type and this struct carries a slice.
type AdminPlaybackActivityProvider struct {
	pool  *pgxpool.Pool
	cache *cache.TTLCache[*AdminPlaybackActivity]
	ttl   time.Duration
}

var _ AdminPlaybackActivitySource = (*AdminPlaybackActivityProvider)(nil)

// NewAdminPlaybackActivityProvider creates a cached provider and subscribes it
// to the playback/admin invalidation channels when an event bus is configured.
func NewAdminPlaybackActivityProvider(ctx context.Context, pool *pgxpool.Pool, bus cache.EventBus) (*AdminPlaybackActivityProvider, error) {
	provider := &AdminPlaybackActivityProvider{
		pool:  pool,
		cache: cache.NewTTLCache[*AdminPlaybackActivity](),
		ttl:   adminPlaybackActivityCacheTTL,
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
			return nil, fmt.Errorf("subscribing admin playback activity provider to %s: %w", channel, err)
		}
	}

	return provider, nil
}

// Get returns the cached window when available, otherwise queries Postgres.
func (p *AdminPlaybackActivityProvider) Get(ctx context.Context, hours int) (*AdminPlaybackActivity, error) {
	if p == nil || p.pool == nil {
		return nil, fmt.Errorf("admin playback activity provider is not configured")
	}
	hours = clampQueryInt(hours, adminPlaybackActivityMinHours, adminPlaybackActivityMaxHours)
	key := adminPlaybackActivityCachePrefix + strconv.Itoa(hours)
	if activity, ok := p.cache.Get(key); ok {
		return activity, nil
	}

	activity, err := queryAdminPlaybackActivity(ctx, p.pool, hours)
	if err != nil {
		return nil, err
	}
	p.cache.Set(key, activity, p.ttl)
	return activity, nil
}

// Invalidate drops every cached window.
func (p *AdminPlaybackActivityProvider) Invalidate() {
	if p == nil || p.cache == nil {
		return
	}
	p.cache.InvalidatePrefix(adminPlaybackActivityCachePrefix)
}

// Close stops the background TTL sweeper.
func (p *AdminPlaybackActivityProvider) Close() {
	if p == nil || p.cache == nil {
		return
	}
	p.cache.Close()
}

// HandleGetPlaybackActivity handles GET /admin/stats/playback-activity.
func (h *AdminHandler) HandleGetPlaybackActivity(w http.ResponseWriter, r *http.Request) {
	hours, err := parseClampedIntQuery(
		r, "hours",
		adminPlaybackActivityDefaultHours,
		adminPlaybackActivityMinHours,
		adminPlaybackActivityMaxHours,
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	var activity *AdminPlaybackActivity
	switch {
	case h.PlaybackActivitySource != nil:
		if isTruthyQuery(r.URL.Query().Get("refresh")) {
			h.PlaybackActivitySource.Invalidate()
		}
		activity, err = h.PlaybackActivitySource.Get(r.Context(), hours)
	case h.pool != nil:
		activity, err = queryAdminPlaybackActivity(r.Context(), h.pool, hours)
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "Database not configured")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to get playback activity")
		return
	}

	writeJSON(w, http.StatusOK, activity)
}

// playbackActivityRow is one (bucket, play_method) group from the union of
// finalized history and live sessions.
type playbackActivityRow struct {
	Hour       time.Time
	PlayMethod string
	Count      int64
}

// assemblePlaybackBuckets folds the grouped rows into one bucket per instant.
// Play methods the resolver does not produce (empty or unknown strings on old
// rows) are dropped from the stacked series but still counted in
// reliability.sessions_started, which is measured by SQL over the same union.
func assemblePlaybackBuckets(rows []playbackActivityRow) []AdminPlaybackActivityBucket {
	buckets := make([]AdminPlaybackActivityBucket, 0, len(rows))
	index := make(map[time.Time]int, len(rows))

	for _, row := range rows {
		hour := row.Hour.UTC()
		pos, ok := index[hour]
		if !ok {
			buckets = append(buckets, AdminPlaybackActivityBucket{Hour: hour})
			pos = len(buckets) - 1
			index[hour] = pos
		}
		switch playback.PlayMethod(row.PlayMethod) {
		case playback.PlayDirect:
			buckets[pos].Direct += row.Count
		case playback.PlayRemux:
			buckets[pos].Remux += row.Count
		case playback.PlayTranscode:
			buckets[pos].Transcode += row.Count
		}
	}

	return buckets
}

// completionRate is completed over finalized sessions. Live sessions are
// excluded from both sides by the caller: a session that is still playing has
// not failed to complete, so counting it as a miss would drag the rate down
// whenever someone is watching.
func completionRate(completed, finalized int64) float64 {
	if finalized <= 0 {
		return 0
	}
	return math.Round(float64(completed)/float64(finalized)*10000) / 10000
}

// adminPlaybackSessionsCTE is the union both activity queries aggregate over.
//
// playback_history_admin only gains a row when a session finalizes, so the
// current hour would be under-counted without the live sessions. A finalizing
// session briefly exists on both sides — history is written before the sync
// row is deleted, and the deletion can fail until stale-session cleanup — so
// live rows whose session already reached history are excluded rather than
// counted twice. playback_sessions_sync.started_at is nullable for sessions
// reconstructed after a restart, hence the COALESCE onto updated_at.
const adminPlaybackSessionsCTE = `
	WITH history AS (
		SELECT session_id, started_at, play_method, completed, user_id, profile_id, FALSE AS live
		FROM playback_history_admin
		WHERE started_at >= now() - make_interval(hours => $1)
	),
	sessions AS (
		SELECT started_at, play_method, completed, user_id, profile_id, live FROM history
		UNION ALL
		SELECT COALESCE(s.started_at, s.updated_at) AS started_at, s.play_method, FALSE, s.user_id, s.profile_id, TRUE
		FROM playback_sessions_sync s
		WHERE COALESCE(s.started_at, s.updated_at) >= now() - make_interval(hours => $1)
		  AND NOT EXISTS (SELECT 1 FROM history h WHERE h.session_id = s.session_id)
	)`

func queryAdminPlaybackActivity(ctx context.Context, pool *pgxpool.Pool, hours int) (*AdminPlaybackActivity, error) {
	if pool == nil {
		return nil, fmt.Errorf("database not configured")
	}

	bucketSeconds := playbackActivityBucketSeconds(hours)

	// Both statements run inside one repeatable-read transaction so they see a
	// single snapshot and a single clock: read committed would let a session
	// that starts or finalizes between them make reliability describe a
	// different session set from the buckets, and now() — which is the
	// transaction timestamp — would otherwise differ between the bucket filter
	// and the reported window, moving the client's grid off the buckets around
	// an hour or day boundary.
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("beginning playback activity read: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Truncation is pinned to UTC: date_trunc otherwise cuts on the session
	// TimeZone's boundaries, and the dashboard zero-fills on epoch-aligned UTC
	// buckets — a daily bucket cut in another zone would land in the wrong
	// column (or between columns) client-side.
	rows, err := tx.Query(ctx, adminPlaybackSessionsCTE+`
		SELECT date_trunc($2, started_at, 'UTC') AS bucket,
		       COALESCE(play_method, '') AS play_method,
		       COUNT(*)::bigint AS sessions
		FROM sessions
		WHERE started_at IS NOT NULL
		GROUP BY 1, 2
		ORDER BY 1
	`, hours, playbackActivityTruncField(bucketSeconds))
	if err != nil {
		return nil, fmt.Errorf("querying playback activity buckets: %w", err)
	}
	defer rows.Close()

	grouped := make([]playbackActivityRow, 0, 64)
	for rows.Next() {
		var row playbackActivityRow
		if err := rows.Scan(&row.Hour, &row.PlayMethod, &row.Count); err != nil {
			return nil, fmt.Errorf("scanning playback activity bucket: %w", err)
		}
		grouped = append(grouped, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating playback activity buckets: %w", err)
	}

	activity := &AdminPlaybackActivity{
		Hours:         hours,
		BucketSeconds: bucketSeconds,
		Buckets:       assemblePlaybackBuckets(grouped),
	}

	// profiles_active_24h deliberately excludes imported and provider-synced
	// history so the tile means "watched on this server", not "appeared in
	// someone's Trakt backlog". Manual marks stay in: they are on-server
	// actions.
	row := tx.QueryRow(ctx, adminPlaybackSessionsCTE+`,
		reliability AS (
			SELECT
				COUNT(*)::bigint AS sessions_started,
				COUNT(*) FILTER (WHERE play_method = 'transcode')::bigint AS transcode_starts,
				COUNT(*) FILTER (WHERE NOT live)::bigint AS finalized_sessions,
				COUNT(*) FILTER (WHERE NOT live AND completed)::bigint AS completed_sessions,
				COUNT(DISTINCT (user_id, profile_id))::bigint AS unique_profiles
			FROM sessions
		),
		active_profiles AS (
			SELECT COUNT(DISTINCT (user_id, profile_id))::bigint AS profiles_active_24h
			FROM user_watch_history
			WHERE watched_at >= now() - interval '24 hours'
			  AND COALESCE(source, 'legacy') IN ('legacy', 'manual', 'playback', 'jellycompat')
		)
		SELECT
			reliability.sessions_started,
			reliability.transcode_starts,
			reliability.finalized_sessions,
			reliability.completed_sessions,
			reliability.unique_profiles,
			active_profiles.profiles_active_24h,
			now() - make_interval(hours => $1) AS window_from,
			now() AS window_to
		FROM reliability
		CROSS JOIN active_profiles
	`, hours)
	if err := row.Scan(
		&activity.Reliability.SessionsStarted,
		&activity.Reliability.TranscodeStarts,
		&activity.Reliability.FinalizedSessions,
		&activity.Reliability.CompletedSessions,
		&activity.Reliability.UniqueProfiles,
		&activity.ProfilesActive24h,
		&activity.From,
		&activity.To,
	); err != nil {
		return nil, fmt.Errorf("querying playback reliability: %w", err)
	}
	activity.From = activity.From.UTC()
	activity.To = activity.To.UTC()
	activity.Reliability.CompletionRate = completionRate(
		activity.Reliability.CompletedSessions,
		activity.Reliability.FinalizedSessions,
	)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("closing playback activity read: %w", err)
	}
	return activity, nil
}
