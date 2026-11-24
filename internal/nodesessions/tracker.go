package nodesessions

import (
	"bytes"
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
	// cleanupTimeout bounds the writes refresh sends under the tracker lock, so
	// a slow Redis cannot stall Hold, release, or ActiveCount for long.
	cleanupTimeout = time.Second
)

// SessionInfo represents an active streaming session stored in Redis.
type SessionInfo struct {
	SessionID         string `json:"session_id"`
	NodeURL           string `json:"node_url"`
	NodeName          string `json:"node_name"`
	UserID            string `json:"user_id,omitempty"`
	MediaItemID       string `json:"media_item_id,omitempty"`
	MediaTitle        string `json:"media_title,omitempty"`
	Type              string `json:"type"` // "direct_play", "remux", "transcode", "download_prepare", "download", "theme_audio"
	CodecVideo        string `json:"codec_video,omitempty"`
	CodecAudio        string `json:"codec_audio,omitempty"`
	Resolution        string `json:"resolution,omitempty"`
	HWAccel           string `json:"hw_accel,omitempty"`
	ToneMapMode       string `json:"tone_map_mode,omitempty"`
	StartedAt         string `json:"started_at"`
	StartedAtUnixNano int64  `json:"started_at_unix_nano,omitempty"`
	StartedAtSource   string `json:"started_at_source,omitempty"`

	// AuthUserID / ProfileID / MediaFileID are the numeric ownership keys the
	// node copies from the verified stream token. They enrich the live admin
	// "active streams" view (served by SCANning these records) so it can answer
	// *who* is watching *what* on each node, not just session id + node + type;
	// the string UserID/MediaItemID/MediaTitle fields remain the display labels.
	AuthUserID  int    `json:"auth_user_id,omitempty"`
	ProfileID   string `json:"profile_id,omitempty"`
	MediaFileID int    `json:"media_file_id,omitempty"`

	// StreamEndSeconds is where an encoded transcode's media ends, once a
	// run of it read the source to the end. A node fills it in each report,
	// and the core lowers the file's stored duration to it
	// (playback.StreamEndRecorder).
	StreamEndSeconds float64 `json:"stream_end_seconds,omitempty"`
	// StreamPlannedSeconds is the stored duration that transcode was planned
	// with; the core lowers the duration only while it is still this.
	StreamPlannedSeconds float64 `json:"stream_planned_seconds,omitempty"`
}

// Tracker manages session lifecycle in Redis for a single node.
type Tracker struct {
	rdb      *redis.Client
	nodeURL  string
	nodeName string
	nodeType string
	nodeHash string // first 8 chars of SHA-256 of nodeURL

	mu        sync.Mutex
	sessions  map[string]*trackedSession // active sessions by ID
	touched   map[string]time.Time       // ephemeral sessions by last-activity time
	delivered map[string]time.Time       // last delivery record written per playback session
	lingering map[string][]byte          // record of a session in touched by a lingering release
}

// trackedSession counts the in-flight requests holding one session. A session
// registered with Track and never held has holds == 0 and ends only through
// Remove or Cleanup. record is the latest Redis record a hold wrote, which the
// refresh loop writes back for held sessions.
type trackedSession struct {
	holds  int
	record []byte
	// delivered is set when a lingering hold on this session reported that its
	// request delivered media. The last release then lingers, whichever
	// request it belongs to.
	delivered bool
	// plain says the latest hold on the session was a plain Hold: the session
	// is on a transport whose requests span the stream, so its last release
	// ends it. A lingering hold taken after it makes the session a direct play
	// again.
	plain bool
}

// NewTracker creates a session tracker for the given node.
// rdb may be nil, in which case all operations are no-ops.
func NewTracker(rdb *redis.Client, nodeURL, nodeName, nodeType string) *Tracker {
	h := sha256.Sum256([]byte(nodeURL))
	return &Tracker{
		rdb:       rdb,
		nodeURL:   nodeURL,
		nodeName:  nodeName,
		nodeType:  nodeType,
		nodeHash:  hex.EncodeToString(h[:4]), // 8 hex chars
		sessions:  make(map[string]*trackedSession),
		touched:   make(map[string]time.Time),
		delivered: make(map[string]time.Time),
		lingering: make(map[string][]byte),
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
	if _, ok := tr.sessions[info.SessionID]; !ok {
		tr.sessions[info.SessionID] = &trackedSession{}
	}
	tr.mu.Unlock()
}

// Hold registers one in-flight request for a session and returns the func
// that ends it. Overlapping requests for the same session, such as a HEAD
// probe or a second range request while a GET plays, each take a hold, and the
// session stays tracked until the last one is released. Remove and Cleanup
// still end the session outright; a hold taken before them releases as a
// no-op. Calling the returned func more than once has no further effect.
func (tr *Tracker) Hold(ctx context.Context, info SessionInfo) func(context.Context) {
	entry := tr.hold(ctx, info, false)
	if entry == nil {
		return func(context.Context) {}
	}
	var once sync.Once
	return func(ctx context.Context) {
		once.Do(func() { tr.release(ctx, info.SessionID, entry, false) })
	}
}

// HoldLingering is Hold for requests that arrive as a series with gaps between
// them, such as the range requests of one direct-play stream. The returned
// func takes whether the request delivered media. When any request holding the
// session did, the last release keeps the session counted for sessionTTL, and
// its record alive until then, so a gap between requests does not end the
// session. When none did, the last release adds no linger: it ends the
// session, unless an earlier linger is still running, which it leaves as it
// was. A plain Hold for the session ends a running linger: the session moved
// to another transport, whose own hold carries it. While holds of both kinds
// overlap, the latest one decides which the session is. The first Touch for the
// session after a lingering release writes its own record.
func (tr *Tracker) HoldLingering(ctx context.Context, info SessionInfo) func(ctx context.Context, delivered bool) {
	entry := tr.hold(ctx, info, true)
	if entry == nil {
		return func(context.Context, bool) {}
	}
	var once sync.Once
	return func(ctx context.Context, delivered bool) {
		once.Do(func() { tr.release(ctx, info.SessionID, entry, delivered) })
	}
}

// hold registers one in-flight request for the session and writes its record.
// It returns nil when the tracker has no Redis.
func (tr *Tracker) hold(ctx context.Context, info SessionInfo, lingering bool) *trackedSession {
	if tr.rdb == nil {
		return nil
	}
	data, _ := json.Marshal(info) // SessionInfo holds only strings and ints
	tr.mu.Lock()
	entry, ok := tr.sessions[info.SessionID]
	if !ok {
		entry = &trackedSession{}
		tr.sessions[info.SessionID] = entry
	}
	entry.holds++
	entry.record = data
	entry.plain = !lingering
	if !lingering {
		entry.delivered = false
		if _, lingers := tr.lingering[info.SessionID]; lingers {
			delete(tr.lingering, info.SessionID)
			delete(tr.touched, info.SessionID)
		}
	}
	tr.mu.Unlock()

	if err := tr.rdb.Set(ctx, tr.redisKey(info.SessionID), data, sessionTTL).Err(); err != nil {
		slog.DebugContext(ctx, "session hold set failed", "component", "nodesessions", "error", err, "session", info.SessionID)
	}
	return entry
}

// release drops one hold and ends the session when it was the last one. When
// the session is a direct play (its latest hold was a lingering one) and a
// request holding it delivered media, the last release moves it to the touched
// set instead, and its record expires when its count does.
func (tr *Tracker) release(ctx context.Context, sessionID string, entry *trackedSession, delivered bool) {
	// The request that held the session may be canceled by now, and its end
	// must still reach Redis, within a bound.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	tr.mu.Lock()
	if tr.sessions[sessionID] != entry {
		// Remove or Cleanup already ended the session this hold belonged to.
		tr.mu.Unlock()
		return
	}
	if delivered {
		entry.delivered = true
	}
	entry.holds--
	if entry.holds > 0 {
		tr.mu.Unlock()
		return
	}
	delete(tr.sessions, sessionID)
	if record, ttl, ok := tr.lingerAfterLocked(sessionID, entry); ok {
		tr.mu.Unlock()
		tr.writeLinger(ctx, sessionID, record, ttl)
		return
	}
	delete(tr.touched, sessionID)
	delete(tr.lingering, sessionID)
	tr.mu.Unlock()

	if err := tr.rdb.Del(ctx, tr.redisKey(sessionID)).Err(); err != nil {
		slog.DebugContext(ctx, "session release failed", "component", "nodesessions", "error", err, "session", sessionID)
	}
}

// lingerAfterLocked reports whether a session whose last hold was just
// released lingers, with the record to keep and for how long. A direct play
// that delivered media starts a new linger. One that delivered nothing leaves
// an earlier linger as it was: its hold gave the record a full TTL, which goes
// back to the time that linger has left. The caller holds the lock.
func (tr *Tracker) lingerAfterLocked(sessionID string, entry *trackedSession) ([]byte, time.Duration, bool) {
	if entry.plain {
		return nil, 0, false
	}
	if entry.delivered {
		tr.touched[sessionID] = time.Now()
		tr.lingering[sessionID] = entry.record
		return entry.record, sessionTTL, true
	}
	record, lingers := tr.lingering[sessionID]
	left := sessionTTL - time.Since(tr.touched[sessionID])
	if !lingers || left <= 0 {
		return nil, 0, false
	}
	return record, left, true
}

// writeLinger writes a lingering session's record with the time it has left.
// The write runs outside the lock, so Remove or Cleanup can end the session
// before it lands: a record written for a session the tracker no longer knows
// is deleted again.
func (tr *Tracker) writeLinger(ctx context.Context, sessionID string, record []byte, ttl time.Duration) {
	key := tr.redisKey(sessionID)
	if err := tr.rdb.Set(ctx, key, record, ttl).Err(); err != nil {
		slog.DebugContext(ctx, "session linger set failed", "component", "nodesessions", "error", err, "session", sessionID)
	}
	// The check and the delete run under the lock, so a Hold or Touch that
	// registers the session again sends its SET after them.
	tr.mu.Lock()
	defer tr.mu.Unlock()
	_, held := tr.sessions[sessionID]
	_, touched := tr.touched[sessionID]
	if held || touched {
		return
	}
	if err := tr.rdb.Del(ctx, key).Err(); err != nil {
		slog.DebugContext(ctx, "session linger cleanup failed", "component", "nodesessions", "error", err, "session", sessionID)
	}
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
	if _, lingered := tr.lingering[info.SessionID]; lingered {
		// The record in Redis is the lingering release's, not this Touch's.
		known = false
		delete(tr.lingering, info.SessionID)
	}
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

// Remove deletes a session from Redis and the in-memory set, regardless of
// any holds on it.
func (tr *Tracker) Remove(ctx context.Context, sessionID string) {
	if tr.rdb == nil {
		return
	}
	tr.mu.Lock()
	delete(tr.sessions, sessionID)
	delete(tr.touched, sessionID)
	delete(tr.lingering, sessionID)
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
	tr.sessions = make(map[string]*trackedSession)
	tr.touched = make(map[string]time.Time)
	tr.lingering = make(map[string][]byte)
	// Delivery records stay in Redis: the deliveries happened, and the records
	// expire on their own within DeliveryWindow.
	tr.delivered = make(map[string]time.Time)
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

// refreshAll extends the TTL of every active session. A held session's record
// is written again rather than extended, so one deleted while a request still
// holds it comes back within a refresh interval: the last release's DEL for an
// earlier request can land after the next request's SET.
func (tr *Tracker) refreshAll(ctx context.Context) {
	now := time.Now()
	tr.mu.Lock()
	ids := make([]string, 0, len(tr.sessions)+len(tr.touched))
	held := make(map[string][]byte)
	for id, entry := range tr.sessions {
		if entry.holds > 0 {
			held[id] = entry.record
			continue
		}
		ids = append(ids, id)
	}
	for id, last := range tr.touched {
		if now.Sub(last) > sessionTTL {
			// Idle ephemeral session: stop refreshing and let the Redis
			// key expire on its own.
			delete(tr.touched, id)
			delete(tr.lingering, id)
			continue
		}
		if _, dup := tr.sessions[id]; dup {
			continue
		}
		if _, lingers := tr.lingering[id]; lingers {
			// The lingering release gave the record the time left to its
			// count's end; a full TTL here would keep a stopped play listed
			// after the node stopped counting it.
			continue
		}
		ids = append(ids, id)
	}
	tr.pruneDeliveredLocked(now)
	tr.mu.Unlock()

	if len(ids) == 0 && len(held) == 0 {
		return
	}

	pipe := tr.rdb.Pipeline()
	for _, id := range ids {
		pipe.Expire(ctx, tr.redisKey(id), sessionTTL)
	}
	for id, record := range held {
		pipe.Set(ctx, tr.redisKey(id), record, sessionTTL)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		// Some writes may still have landed, so reconcile them anyway.
		slog.DebugContext(ctx, "session refresh pipeline failed", "component", "nodesessions", "error", err)
	}
	if len(held) > 0 {
		tr.reconcileAfterRefresh(ctx, held)
	}
}

// reconcileAfterRefresh repairs the records refresh wrote back for sessions
// that changed while the write was in flight, since that write can land after
// a newer one. A session that ended (a last release, Remove, or Cleanup) has
// its record deleted. A session Hold registered again, or held with a newer
// record, gets its current record written. A session only Touch registered
// again has its record deleted and its touch forgotten, so the next Touch
// writes its own record. A session a lingering last release moved to the
// touched set keeps its touch and gets the release's record written again
// with the time its linger has left. The writes run under the lock so a Hold or Touch that
// registers the session again sends its SET after them.
func (tr *Tracker) reconcileAfterRefresh(ctx context.Context, written map[string][]byte) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	var stale []string
	current := make(map[string][]byte)
	lingering := make(map[string]lingerWrite)
	for id, record := range written {
		if entry, ok := tr.sessions[id]; ok {
			if entry.record != nil && !bytes.Equal(entry.record, record) {
				current[id] = entry.record
			}
			continue
		}
		if lingered, ok := tr.lingering[id]; ok {
			// The refresh write gave the record a full TTL, whatever it
			// holds: it goes back to the time the linger has left.
			if left := sessionTTL - time.Since(tr.touched[id]); left > 0 {
				lingering[id] = lingerWrite{record: lingered, ttl: left}
				continue
			}
			delete(tr.lingering, id)
		}
		delete(tr.touched, id)
		stale = append(stale, tr.redisKey(id))
	}
	if len(stale) == 0 && len(current) == 0 && len(lingering) == 0 {
		return
	}
	// Refresh's own context can be canceled once its writes are out; the
	// repair still has to land.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	pipe := tr.rdb.Pipeline()
	if len(stale) > 0 {
		pipe.Del(ctx, stale...)
	}
	for id, record := range current {
		pipe.Set(ctx, tr.redisKey(id), record, sessionTTL)
	}
	for id, write := range lingering {
		pipe.Set(ctx, tr.redisKey(id), write.record, write.ttl)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		slog.DebugContext(ctx, "session refresh reconcile failed", "component", "nodesessions", "error", err)
	}
}

// lingerWrite is a lingering session's record and the time its linger has left.
type lingerWrite struct {
	record []byte
	ttl    time.Duration
}
