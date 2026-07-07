package worker

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/cache"
	evt "github.com/Silo-Server/silo-server/internal/events"
)

// SessionSync represents the data needed to sync a playback session to the
// playback_sessions_sync table in PostgreSQL.
type SessionSync struct {
	SessionID            string
	UserID               int
	ProfileID            string
	MediaFileID          int
	RequestedMediaFileID int
	PlayMethod           string // current live transport method for admin session views
	ReportingNode        string
	ClientIP             string
	ClientName           string
	ClientVersion        string
	ClientUserAgent      string
	AudioTrackIndex      int
	TranscodeAudio       bool
	StreamBitrateKbps    int
	TranscodeNodeURL     string
	TargetResolution     string
	TargetVideoCodec     string
	TargetAudioCodec     string
	TargetBitrateKbps    int
	TranscodeHWAccel     string
	StartedAt            time.Time
	UpdatedAt            time.Time
	PositionSeconds      float64
	IsPaused             bool
	HasWebSocket         bool
}

// AggregateData represents the aggregate counts for a single user that are
// synced to the user_aggregates table in PostgreSQL.
type AggregateData struct {
	TotalWatched   int
	FavoritesCount int
	WatchlistCount int
	ActiveNode     string
}

// SessionSyncProvider returns the current set of active sessions to reconcile.
// This is typically a closure that reads from the SessionManager.
type SessionSyncProvider func() []SessionSync

// PreSyncHook is called before each reconciliation cycle. Implementations
// can use it to expire idle sessions from the in-memory manager so they are
// no longer included in the snapshot sent to the database.
type PreSyncHook func()

// Reconciler performs background reconciliation of user data from per-user
// SQLite databases into the central PostgreSQL instance.
type Reconciler struct {
	pool            *pgxpool.Pool
	nodeName        string
	sessionProvider SessionSyncProvider
	interval        time.Duration
	stop            chan struct{}
	EventBus        cache.EventBus
	EventsHub       *evt.Hub
	PreSync         PreSyncHook
	// syncMu guards syncRunning/syncPending. Session syncs are coalesced onto a
	// single owner so concurrent callers (the periodic tick plus request-path
	// start/stop triggers) can never commit an older session snapshot after a
	// newer one — which would resurrect stopped sessions or drop freshly
	// started ones — and so request goroutines never queue behind a slow sync.
	syncMu      sync.Mutex
	syncRunning bool
	syncPending bool
}

// NewReconciler creates a new Reconciler with sensible defaults. The default
// reconciliation interval is 30 seconds. The sessionProvider may be nil if
// session sync is not needed (e.g. in tests).
func NewReconciler(pool *pgxpool.Pool, nodeName string, sp SessionSyncProvider) *Reconciler {
	return &Reconciler{
		pool:            pool,
		nodeName:        strings.TrimSpace(nodeName),
		sessionProvider: sp,
		interval:        15 * time.Second,
		stop:            make(chan struct{}),
	}
}

// ReconcileSessions upserts the given sessions into the playback_sessions_sync
// table. Each session is inserted or updated based on its session_id primary
// key, making the operation idempotent.
func (r *Reconciler) ReconcileSessions(ctx context.Context, sessions []SessionSync) error {
	if len(sessions) == 0 {
		return nil
	}

	grouped := make(map[string][]SessionSync)
	for _, session := range sessions {
		grouped[session.ReportingNode] = append(grouped[session.ReportingNode], session)
	}

	for reportingNode, nodeSessions := range grouped {
		if err := r.ReconcileNodeSessions(ctx, reportingNode, nodeSessions); err != nil {
			return err
		}
	}

	return nil
}

// ReconcileNodeSessions upserts the sessions currently active on one reporting
// node, then removes any stale rows for that same node that are no longer
// present in the provided snapshot.
func (r *Reconciler) ReconcileNodeSessions(ctx context.Context, reportingNode string, sessions []SessionSync) error {
	reportingNode = strings.TrimSpace(reportingNode)

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	currentSessions, err := loadNodeSessionsSnapshot(ctx, tx, reportingNode)
	if err != nil {
		return fmt.Errorf("loading existing sessions for node %s: %w", reportingNode, err)
	}
	normalizedIncoming := normalizeSessionSyncs(reportingNode, sessions)
	changed := !sessionSnapshotsEqual(currentSessions, normalizedIncoming)

	sessionIDs := make([]string, 0, len(sessions))
	for _, s := range sessions {
		sessionIDs = append(sessionIDs, s.SessionID)
		sessionNode := strings.TrimSpace(s.ReportingNode)
		if sessionNode == "" {
			sessionNode = reportingNode
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO playback_sessions_sync
				(session_id, user_id, profile_id, media_file_id, requested_media_file_id, play_method,
				 reporting_node, started_at, updated_at, last_sync_at, client_ip,
				 client_name, client_version, client_user_agent,
				 audio_track_index, transcode_audio, stream_bitrate_kbps, transcode_node_url,
				 target_resolution, target_video_codec, target_audio_codec, target_bitrate_kbps,
				 transcode_hw_accel, position_seconds, is_paused, has_websocket)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW(), $10::inet, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25)
			ON CONFLICT (session_id) DO UPDATE SET
				user_id             = EXCLUDED.user_id,
				profile_id          = EXCLUDED.profile_id,
				media_file_id       = EXCLUDED.media_file_id,
				requested_media_file_id = EXCLUDED.requested_media_file_id,
				play_method         = EXCLUDED.play_method,
				reporting_node      = EXCLUDED.reporting_node,
				started_at          = EXCLUDED.started_at,
				updated_at          = EXCLUDED.updated_at,
				client_ip           = EXCLUDED.client_ip,
				client_name         = EXCLUDED.client_name,
				client_version      = EXCLUDED.client_version,
				client_user_agent   = EXCLUDED.client_user_agent,
				audio_track_index   = EXCLUDED.audio_track_index,
				transcode_audio     = EXCLUDED.transcode_audio,
				stream_bitrate_kbps = EXCLUDED.stream_bitrate_kbps,
				transcode_node_url  = EXCLUDED.transcode_node_url,
				target_resolution   = EXCLUDED.target_resolution,
				target_video_codec  = EXCLUDED.target_video_codec,
				target_audio_codec  = EXCLUDED.target_audio_codec,
				target_bitrate_kbps = EXCLUDED.target_bitrate_kbps,
				transcode_hw_accel  = EXCLUDED.transcode_hw_accel,
				position_seconds    = EXCLUDED.position_seconds,
				is_paused           = EXCLUDED.is_paused,
				has_websocket       = EXCLUDED.has_websocket,
				last_sync_at        = NOW()
		`, s.SessionID, s.UserID, s.ProfileID, s.MediaFileID, nullableInt(s.RequestedMediaFileID), s.PlayMethod,
			sessionNode, s.StartedAt, s.UpdatedAt, nullableIP(s.ClientIP),
			nullableString(s.ClientName), nullableString(s.ClientVersion), nullableString(s.ClientUserAgent),
			s.AudioTrackIndex, s.TranscodeAudio, nullableInt(s.StreamBitrateKbps), nullableString(s.TranscodeNodeURL),
			nullableString(s.TargetResolution), nullableString(s.TargetVideoCodec),
			nullableString(s.TargetAudioCodec), nullableInt(s.TargetBitrateKbps),
			nullableString(s.TranscodeHWAccel), normalizePositionSeconds(s.PositionSeconds),
			s.IsPaused, s.HasWebSocket)
		if err != nil {
			return fmt.Errorf("upserting session %s: %w", s.SessionID, err)
		}
	}

	if len(sessionIDs) == 0 {
		if _, err := tx.Exec(ctx, `
			DELETE FROM playback_sessions_sync
			WHERE COALESCE(reporting_node, '') = $1
		`, reportingNode); err != nil {
			return fmt.Errorf("deleting empty snapshot for node %s: %w", reportingNode, err)
		}
	} else {
		if _, err := tx.Exec(ctx, `
			DELETE FROM playback_sessions_sync
			WHERE COALESCE(reporting_node, '') = $1
			  AND NOT (session_id = ANY($2))
		`, reportingNode, sessionIDs); err != nil {
			return fmt.Errorf("deleting missing sessions for node %s: %w", reportingNode, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}
	if changed && r.EventsHub != nil {
		if err := r.EventsHub.PublishJSON(
			ctx,
			evt.ChannelSessions,
			"sessions.replaced",
			nil,
			evt.PublishOptions{AdminOnly: true},
		); err != nil {
			log.Printf("reconciler: failed to publish session event for node %s: %v", reportingNode, err)
		}
	} else if changed && r.EventBus != nil {
		if err := r.EventBus.Publish(ctx, cache.ChannelPlayback, cache.Event{
			Type:    cache.EventPlaybackSessionsChanged,
			Payload: reportingNode,
		}); err != nil {
			log.Printf("reconciler: failed to publish playback invalidation event for node %s: %v", reportingNode, err)
		}
	}

	return nil
}

func loadNodeSessionsSnapshot(ctx context.Context, tx pgx.Tx, reportingNode string) ([]SessionSync, error) {
	rows, err := tx.Query(ctx, `
		SELECT
			session_id,
			user_id,
			COALESCE(profile_id, ''),
			media_file_id,
			COALESCE(requested_media_file_id, media_file_id, 0),
			COALESCE(play_method, ''),
			COALESCE(reporting_node, ''),
			COALESCE(HOST(client_ip), ''),
			COALESCE(client_name, ''),
			COALESCE(client_version, ''),
			COALESCE(client_user_agent, ''),
			COALESCE(audio_track_index, 0),
			COALESCE(transcode_audio, FALSE),
			COALESCE(stream_bitrate_kbps, 0),
			COALESCE(transcode_node_url, ''),
			COALESCE(target_resolution, ''),
			COALESCE(target_video_codec, ''),
			COALESCE(target_audio_codec, ''),
			COALESCE(target_bitrate_kbps, 0),
			COALESCE(transcode_hw_accel, ''),
			started_at,
			updated_at,
			COALESCE(position_seconds, 0),
			COALESCE(is_paused, FALSE),
			COALESCE(has_websocket, FALSE)
		FROM playback_sessions_sync
		WHERE COALESCE(reporting_node, '') = $1
		ORDER BY session_id
	`, reportingNode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []SessionSync
	for rows.Next() {
		var s SessionSync
		if err := rows.Scan(
			&s.SessionID,
			&s.UserID,
			&s.ProfileID,
			&s.MediaFileID,
			&s.RequestedMediaFileID,
			&s.PlayMethod,
			&s.ReportingNode,
			&s.ClientIP,
			&s.ClientName,
			&s.ClientVersion,
			&s.ClientUserAgent,
			&s.AudioTrackIndex,
			&s.TranscodeAudio,
			&s.StreamBitrateKbps,
			&s.TranscodeNodeURL,
			&s.TargetResolution,
			&s.TargetVideoCodec,
			&s.TargetAudioCodec,
			&s.TargetBitrateKbps,
			&s.TranscodeHWAccel,
			&s.StartedAt,
			&s.UpdatedAt,
			&s.PositionSeconds,
			&s.IsPaused,
			&s.HasWebSocket,
		); err != nil {
			return nil, err
		}
		sessions = append(sessions, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return sessions, nil
}

func normalizeSessionSyncs(reportingNode string, sessions []SessionSync) []SessionSync {
	normalized := make([]SessionSync, len(sessions))
	for i, s := range sessions {
		cp := s
		if strings.TrimSpace(cp.ReportingNode) == "" {
			cp.ReportingNode = reportingNode
		}
		normalized[i] = cp
	}
	sort.Slice(normalized, func(i, j int) bool {
		return normalized[i].SessionID < normalized[j].SessionID
	})
	return normalized
}

func sessionSnapshotsEqual(left, right []SessionSync) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].SessionID != right[i].SessionID ||
			left[i].UserID != right[i].UserID ||
			left[i].ProfileID != right[i].ProfileID ||
			left[i].MediaFileID != right[i].MediaFileID ||
			left[i].RequestedMediaFileID != right[i].RequestedMediaFileID ||
			left[i].PlayMethod != right[i].PlayMethod ||
			left[i].ReportingNode != right[i].ReportingNode ||
			left[i].ClientIP != right[i].ClientIP ||
			left[i].ClientName != right[i].ClientName ||
			left[i].ClientVersion != right[i].ClientVersion ||
			left[i].ClientUserAgent != right[i].ClientUserAgent ||
			left[i].AudioTrackIndex != right[i].AudioTrackIndex ||
			left[i].TranscodeAudio != right[i].TranscodeAudio ||
			left[i].StreamBitrateKbps != right[i].StreamBitrateKbps ||
			left[i].TranscodeNodeURL != right[i].TranscodeNodeURL ||
			left[i].TargetResolution != right[i].TargetResolution ||
			left[i].TargetVideoCodec != right[i].TargetVideoCodec ||
			left[i].TargetAudioCodec != right[i].TargetAudioCodec ||
			left[i].TargetBitrateKbps != right[i].TargetBitrateKbps ||
			left[i].TranscodeHWAccel != right[i].TranscodeHWAccel ||
			!left[i].StartedAt.Equal(right[i].StartedAt) ||
			!left[i].UpdatedAt.Equal(right[i].UpdatedAt) ||
			normalizePositionSeconds(left[i].PositionSeconds) != normalizePositionSeconds(right[i].PositionSeconds) ||
			left[i].IsPaused != right[i].IsPaused ||
			left[i].HasWebSocket != right[i].HasWebSocket {
			return false
		}
	}
	return true
}

func normalizePositionSeconds(position float64) float64 {
	if position < 0 {
		return 0
	}
	return position
}

func nullableIP(ip string) any {
	if strings.TrimSpace(ip) == "" {
		return nil
	}
	return ip
}

func nullableInt(value int) any {
	if value <= 0 {
		return nil
	}
	return value
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

// ReconcileAggregates upserts the aggregate counts for a single user into the
// user_aggregates table. The operation is idempotent.
func (r *Reconciler) ReconcileAggregates(ctx context.Context, userID int, totals AggregateData) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO user_aggregates
			(user_id, total_watched, favorites_count, watchlist_count, active_node, last_sync_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (user_id) DO UPDATE SET
			total_watched   = EXCLUDED.total_watched,
			favorites_count = EXCLUDED.favorites_count,
			watchlist_count = EXCLUDED.watchlist_count,
			active_node     = EXCLUDED.active_node,
			last_sync_at    = NOW()
	`, userID, totals.TotalWatched, totals.FavoritesCount, totals.WatchlistCount, totals.ActiveNode)
	if err != nil {
		return fmt.Errorf("upserting aggregates for user %d: %w", userID, err)
	}

	return nil
}

// Start begins the background reconciliation loop. It runs until Stop is
// called. On each tick it syncs active playback sessions to PostgreSQL.
func (r *Reconciler) Start() {
	go func() {
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()

		for {
			select {
			case <-r.stop:
				return
			case <-ticker.C:
				r.tick()
			}
		}
	}()
}

// tick runs one reconciliation cycle.
func (r *Reconciler) tick() {
	if r.PreSync != nil {
		r.PreSync()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := r.SyncNow(ctx); err != nil {
		log.Printf("reconciler: session sync error: %v", err)
	}
}

// SyncNow reconciles the current local session snapshot into the shared
// table. When nodeName is configured, an empty snapshot still clears any rows
// previously reported by that node.
//
// Syncs are coalesced: only one reconciliation runs at a time, and a call that
// arrives while one is in flight returns immediately after asking the running
// owner for one follow-up pass with a fresh snapshot. The follow-up capture
// happens after the caller's state change, so its effect is never lost, and
// snapshots always commit in capture order.
func (r *Reconciler) SyncNow(ctx context.Context) error {
	if r.sessionProvider == nil {
		return nil
	}

	r.syncMu.Lock()
	if r.syncRunning {
		// The in-flight sync may have captured a snapshot that predates this
		// caller's state change; have the owner run one more pass.
		r.syncPending = true
		r.syncMu.Unlock()
		return nil
	}
	r.syncRunning = true
	// The fresh capture below supersedes any pass queued before ownership.
	r.syncPending = false
	r.syncMu.Unlock()

	err := r.syncOnce(ctx)
	for {
		r.syncMu.Lock()
		// Leave a queued pass for the next caller (the periodic tick at the
		// latest) rather than burning it on an already-expired context.
		if !r.syncPending || ctx.Err() != nil {
			r.syncRunning = false
			r.syncMu.Unlock()
			return err
		}
		r.syncPending = false
		r.syncMu.Unlock()
		if passErr := r.syncOnce(ctx); err == nil {
			err = passErr
		}
	}
}

// syncOnce captures one session snapshot and reconciles it.
func (r *Reconciler) syncOnce(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sessions := r.sessionProvider()
	if len(sessions) == 0 {
		if r.nodeName == "" {
			return nil
		}
		return r.ReconcileNodeSessions(ctx, r.nodeName, nil)
	}
	if r.nodeName != "" {
		for i := range sessions {
			if strings.TrimSpace(sessions[i].ReportingNode) == "" {
				sessions[i].ReportingNode = r.nodeName
			}
		}
	}
	return r.ReconcileSessions(ctx, sessions)
}

// Stop signals the reconciliation loop to stop.
func (r *Reconciler) Stop() {
	close(r.stop)
}
