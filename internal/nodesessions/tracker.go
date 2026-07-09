package nodesessions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	keyPrefix  = "silo:sessions:"
	sessionTTL = 60 * time.Second
	refreshInt = 30 * time.Second
)

// SessionInfo represents an active streaming session stored in Redis.
type SessionInfo struct {
	SessionID   string `json:"session_id"`
	NodeURL     string `json:"node_url"`
	NodeName    string `json:"node_name"`
	UserID      string `json:"user_id,omitempty"`
	MediaItemID string `json:"media_item_id,omitempty"`
	MediaTitle  string `json:"media_title,omitempty"`
	Type        string `json:"type"` // "direct_play", "remux", "transcode"
	CodecVideo  string `json:"codec_video,omitempty"`
	CodecAudio  string `json:"codec_audio,omitempty"`
	Resolution  string `json:"resolution,omitempty"`
	HWAccel     string `json:"hw_accel,omitempty"`
	StartedAt   string `json:"started_at"`

	// AuthUserID / ProfileID / MediaFileID are the numeric ownership keys the
	// node copies from the verified stream token. They enrich the live admin
	// "active streams" view (served by SCANning these records) so it can answer
	// *who* is watching *what* on each node, not just session id + node + type;
	// the string UserID/MediaItemID/MediaTitle fields remain the display labels.
	AuthUserID  int    `json:"auth_user_id,omitempty"`
	ProfileID   string `json:"profile_id,omitempty"`
	MediaFileID int    `json:"media_file_id,omitempty"`
}

// Tracker manages session lifecycle in Redis for a single node.
type Tracker struct {
	rdb      *redis.Client
	nodeURL  string
	nodeName string
	nodeType string
	nodeHash string // first 8 chars of SHA-256 of nodeURL

	mu       sync.Mutex
	sessions map[string]struct{}  // set of active session IDs
	touched  map[string]time.Time // ephemeral sessions by last-activity time
}

// NewTracker creates a session tracker for the given node.
// rdb may be nil, in which case all operations are no-ops.
func NewTracker(rdb *redis.Client, nodeURL, nodeName, nodeType string) *Tracker {
	h := sha256.Sum256([]byte(nodeURL))
	return &Tracker{
		rdb:      rdb,
		nodeURL:  nodeURL,
		nodeName: nodeName,
		nodeType: nodeType,
		nodeHash: hex.EncodeToString(h[:4]), // 8 hex chars
		sessions: make(map[string]struct{}),
		touched:  make(map[string]time.Time),
	}
}

// redisKey returns the full Redis key for a session.
func (tr *Tracker) redisKey(sessionID string) string {
	return keyPrefix + tr.nodeHash + ":" + sessionID
}

// NodeHash returns the node's hash prefix used in Redis keys.
func (tr *Tracker) NodeHash() string {
	return tr.nodeHash
}

// NodeURL returns the node's URL.
func (tr *Tracker) NodeURL() string {
	return tr.nodeURL
}

// NodeName returns the node's display name.
func (tr *Tracker) NodeName() string {
	return tr.nodeName
}

// ActiveCount returns the number of active sessions tracked by this node,
// including ephemeral sessions touched within the session TTL.
func (tr *Tracker) ActiveCount() int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	now := time.Now()
	count := len(tr.sessions)
	for id, last := range tr.touched {
		if _, dup := tr.sessions[id]; dup {
			continue
		}
		if now.Sub(last) <= sessionTTL {
			count++
		}
	}
	return count
}

// Track registers an active session in Redis with a TTL.
func (tr *Tracker) Track(ctx context.Context, info SessionInfo) {
	if tr.rdb == nil {
		return
	}
	data, err := json.Marshal(info)
	if err != nil {
		slog.DebugContext(ctx, "session track marshal failed", "component", "nodesessions", "error", err)
		return
	}
	key := tr.redisKey(info.SessionID)
	if err := tr.rdb.Set(ctx, key, data, sessionTTL).Err(); err != nil {
		slog.DebugContext(ctx, "session track set failed", "component", "nodesessions", "error", err, "session", info.SessionID)
		return
	}

	tr.mu.Lock()
	tr.sessions[info.SessionID] = struct{}{}
	tr.mu.Unlock()
}

// Touch registers or refreshes an ephemeral session that has no explicit end,
// such as HLS manifest/segment fetches flowing through a proxy. The session is
// written to Redis on first touch and drops out of the active count after
// sessionTTL without further touches (pruned by the refresh loop).
func (tr *Tracker) Touch(ctx context.Context, info SessionInfo) {
	if tr.rdb == nil {
		return
	}
	tr.mu.Lock()
	_, known := tr.touched[info.SessionID]
	tr.touched[info.SessionID] = time.Now()
	tr.mu.Unlock()
	if known {
		return
	}

	data, err := json.Marshal(info)
	if err != nil {
		slog.DebugContext(ctx, "session touch marshal failed", "component", "nodesessions", "error", err)
		return
	}
	if err := tr.rdb.Set(ctx, tr.redisKey(info.SessionID), data, sessionTTL).Err(); err != nil {
		slog.DebugContext(ctx, "session touch set failed", "component", "nodesessions", "error", err, "session", info.SessionID)
	}
}

// Remove deletes a session from Redis and the in-memory set.
func (tr *Tracker) Remove(ctx context.Context, sessionID string) {
	if tr.rdb == nil {
		return
	}
	tr.mu.Lock()
	delete(tr.sessions, sessionID)
	delete(tr.touched, sessionID)
	tr.mu.Unlock()

	if err := tr.rdb.Del(ctx, tr.redisKey(sessionID)).Err(); err != nil {
		slog.DebugContext(ctx, "session remove failed", "component", "nodesessions", "error", err, "session", sessionID)
	}
}

// Cleanup deletes all session keys for this node. Called on graceful shutdown.
func (tr *Tracker) Cleanup(ctx context.Context) {
	if tr.rdb == nil {
		return
	}
	tr.mu.Lock()
	ids := make([]string, 0, len(tr.sessions)+len(tr.touched))
	for id := range tr.sessions {
		ids = append(ids, id)
	}
	for id := range tr.touched {
		if _, dup := tr.sessions[id]; !dup {
			ids = append(ids, id)
		}
	}
	tr.sessions = make(map[string]struct{})
	tr.touched = make(map[string]time.Time)
	tr.mu.Unlock()

	if len(ids) == 0 {
		return
	}

	pipe := tr.rdb.Pipeline()
	for _, id := range ids {
		pipe.Del(ctx, tr.redisKey(id))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		slog.DebugContext(ctx, "session cleanup pipeline failed", "component", "nodesessions", "error", err)
	}
}

// StartRefresh starts a background goroutine that refreshes TTLs for all
// active sessions every 30 seconds. Stops when ctx is cancelled.
func (tr *Tracker) StartRefresh(ctx context.Context) {
	if tr.rdb == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(refreshInt)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				tr.refreshAll(ctx)
			}
		}
	}()
}

func (tr *Tracker) refreshAll(ctx context.Context) {
	now := time.Now()
	tr.mu.Lock()
	ids := make([]string, 0, len(tr.sessions)+len(tr.touched))
	for id := range tr.sessions {
		ids = append(ids, id)
	}
	for id, last := range tr.touched {
		if now.Sub(last) > sessionTTL {
			// Idle ephemeral session: stop refreshing and let the Redis
			// key expire on its own.
			delete(tr.touched, id)
			continue
		}
		if _, dup := tr.sessions[id]; !dup {
			ids = append(ids, id)
		}
	}
	tr.mu.Unlock()

	if len(ids) == 0 {
		return
	}

	pipe := tr.rdb.Pipeline()
	for _, id := range ids {
		pipe.Expire(ctx, tr.redisKey(id), sessionTTL)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		slog.DebugContext(ctx, "session refresh pipeline failed", "component", "nodesessions", "error", err)
	}
}
