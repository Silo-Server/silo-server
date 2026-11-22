package opslog

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/logstream"
	"github.com/Silo-Server/silo-server/internal/logthrottle"
)

// Consumer batch-inserts one node's operational log entries into Postgres and
// fans them out to the admin live tail.
type Consumer struct {
	pool      *pgxpool.Pool
	batchSize int
	interval  time.Duration
	streamHub *logstream.Hub
	// rejectedWarnings spaces InsertNodeBatch's warning per node.
	rejectedWarnings *logthrottle.Throttle[string]
}

func NewConsumer(pool *pgxpool.Pool, streamHub *logstream.Hub) *Consumer {
	return &Consumer{
		pool:      pool,
		batchSize: 100,
		interval:  2 * time.Second,
		streamHub: streamHub,

		rejectedWarnings: logthrottle.New[string](rejectedWarnInterval),
	}
}

// Run persists entries from ch until ctx ends, then flushes what is already
// buffered; see logstream.Drain for retries and drops. The consumer's own
// records reach stderr and OTLP but are not captured: a warning about a failed
// batch must not queue another entry behind it.
func (c *Consumer) Run(ctx context.Context, ch <-chan Entry) {
	Ship(ctx, ch, c.batchSize, c.interval, c.insertBatch)
}

// Ship hands entries from ch to insert in batches of at most size, flushing a
// partial batch every interval, until ctx ends; see logstream.Drain for
// retries and drops. It is Run with a caller-supplied sink: a transcode node,
// which has no database, ships its entries to the core with it. Records the
// pipeline logs about itself reach stderr and OTLP but are not captured, so a
// warning about a failed batch never queues another entry behind it.
func Ship(ctx context.Context, ch <-chan Entry, size int, interval time.Duration, insert func(context.Context, []Entry) error) {
	logstream.Drain[Entry]{
		Stream:   logstream.StreamApp,
		Size:     size,
		Interval: interval,
		Insert:   insert,
		Failed: func(ctx context.Context, f logstream.InsertFailure) {
			if f.RetryIn > 0 {
				slog.WarnContext(ctx, "opslog batch insert failed; retrying", "component", "opslog",
					"entries", f.Entries, "attempt", f.Attempt, "retry_in", f.RetryIn, "error", f.Err)
				return
			}
			slog.ErrorContext(ctx, "opslog batch insert failed; entries dropped", "component", "opslog",
				"entries", f.Entries, "attempt", f.Attempt, "error", f.Err)
		},
	}.Run(withoutCapture(ctx), ch)
}

// Bounds on the timestamp of an entry a transcode node shipped, which the
// node's clock set. Later than nodeEntryMaxAhead past the core's clock is a
// clock running ahead; older than nodeEntryMaxAge is more than a node keeps
// queued (logstream.Drain drops what it cannot deliver within minutes). Both
// are moved to the bound, so a bad clock or a stolen node key cannot file
// rows outside retention, into a partition not yet created (the default
// partition), or ahead of everything the live tail shows.
const (
	nodeEntryMaxAhead = 30 * time.Second
	nodeEntryMaxAge   = 24 * time.Hour
)

// Bounds on the short text fields of an entry a transcode node shipped. The
// columns are unbounded text, but these fields are labels the viewer filters
// and groups by: a level is a slog level name, a component a package name,
// and the ids are request, session and playback session ids. A longer value
// is cut at the bound. Message and attrs are left to the batch's byte bound.
const (
	nodeEntryMaxLevel     = 16
	nodeEntryMaxComponent = 64
	nodeEntryMaxID        = 256
)

// NormalizeNodeEntry makes an entry a transcode node shipped storable as the
// core's own handler would have built it: it files the entry under nodeID,
// whatever node id it carried, keeps its timestamp within nodeEntryMaxAge
// before and nodeEntryMaxAhead after now, cuts its short text fields to their
// bounds, clears a client_ip that is not an IP address (an address with a
// port keeps the address) and a user_id outside the column's integer range.
// What is left are values Postgres accepts, so one bad field does not cost
// the batch its single insert.
func NormalizeNodeEntry(entry Entry, nodeID string, now time.Time) Entry {
	entry.NodeID = nodeID
	if latest := now.Add(nodeEntryMaxAhead); entry.Timestamp.After(latest) {
		entry.Timestamp = latest
	} else if earliest := now.Add(-nodeEntryMaxAge); entry.Timestamp.Before(earliest) {
		entry.Timestamp = earliest
	}
	entry.Level = truncateText(entry.Level, nodeEntryMaxLevel)
	entry.Component = truncateText(entry.Component, nodeEntryMaxComponent)
	entry.RequestID = truncateText(entry.RequestID, nodeEntryMaxID)
	entry.SessionID = truncateText(entry.SessionID, nodeEntryMaxID)
	entry.PlaybackSessionID = truncateText(entry.PlaybackSessionID, nodeEntryMaxID)
	entry.ClientIP = normalizeClientIP(entry.ClientIP)
	if entry.UserID != nil && (*entry.UserID < math.MinInt32 || *entry.UserID > math.MaxInt32) {
		entry.UserID = nil
	}
	return entry
}

// normalizeClientIP is raw as an inet value, or "" when it is not an IP
// address. A zone, which inet does not accept, is dropped.
func normalizeClientIP(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		addrPort, portErr := netip.ParseAddrPort(raw)
		if portErr != nil {
			return ""
		}
		addr = addrPort.Addr()
	}
	return addr.WithZone("").String()
}

// truncateText cuts s to at most limit bytes without splitting a UTF-8
// sequence.
func truncateText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// rejectedWarnInterval spaces the warning about one node's rejected entries:
// a node retries nothing it was told was stored, but every batch it sends
// with the same bad value would log again.
const rejectedWarnInterval = time.Minute

// InsertNodeBatch stores a batch a transcode node shipped, in the shape the
// node's own consumer stored it: every entry is normalized by
// NormalizeNodeEntry, and the rows go to the admin live tail in one insert.
// When Postgres still rejects a value in the batch, the rows are inserted one
// at a time so only the rejected ones are lost, as logstream.Drain does for a
// local batch; the warning about them is logged at most once a minute per
// node.
func (c *Consumer) InsertNodeBatch(ctx context.Context, nodeID string, entries []Entry) error {
	now := time.Now().UTC()
	stamped := make([]Entry, len(entries))
	for i, entry := range entries {
		stamped[i] = NormalizeNodeEntry(entry, nodeID, now)
	}
	err := c.insertBatch(ctx, stamped)
	if err == nil || len(stamped) < 2 || !logstream.ValueRejected(err) {
		return err
	}
	var rejected int
	for i := range stamped {
		if rowErr := c.insertBatch(ctx, stamped[i:i+1]); rowErr != nil {
			if !logstream.ValueRejected(rowErr) {
				return rowErr
			}
			rejected++
		}
	}
	if rejected > 0 && c.rejectedWarnings.Allow(nodeID) {
		slog.WarnContext(withoutCapture(ctx), "opslog: dropped node log entries Postgres rejected", "component", "opslog",
			"node_id", nodeID, "rejected", rejected, "entries", len(stamped))
	}
	return nil
}

func (c *Consumer) insertBatch(ctx context.Context, entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}

	var b strings.Builder
	b.WriteString("INSERT INTO operational_logs (timestamp, level, component, message, request_id, user_id, session_id, playback_session_id, client_ip, node_id, attrs) VALUES ")

	args := make([]any, 0, len(entries)*11)
	for i, e := range entries {
		if i > 0 {
			b.WriteString(", ")
		}
		base := i * 11
		fmt.Fprintf(&b, "($%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, NULLIF($%d, '')::inet, $%d, $%d::jsonb)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9, base+10, base+11)
		attrsJSON, err := json.Marshal(e.Attrs)
		if err != nil {
			attrsJSON = []byte(`{}`)
		}
		// Messages and attrs carry paths, headers and file names.
		args = append(args, e.Timestamp, logstream.SafeText(e.Level), logstream.SafeText(e.Component),
			logstream.SafeText(e.Message), logstream.SafeText(e.RequestID), e.UserID, logstream.SafeText(e.SessionID),
			logstream.SafeText(e.PlaybackSessionID), logstream.SafeText(e.ClientIP), logstream.SafeText(e.NodeID),
			string(logstream.SafeJSON(attrsJSON)))
	}
	b.WriteString(" RETURNING id, timestamp, level, component, message, COALESCE(request_id, ''), user_id, COALESCE(session_id, ''), COALESCE(playback_session_id, ''), COALESCE(host(client_ip), ''), COALESCE(node_id, ''), attrs")

	rows, err := c.pool.Query(ctx, b.String(), args...)
	if err != nil {
		return fmt.Errorf("batch insert: %w", err)
	}
	defer rows.Close()

	inserted := make([]EntryRow, 0, len(entries))
	for rows.Next() {
		var entry EntryRow
		var attrsJSON []byte
		if err := rows.Scan(
			&entry.ID,
			&entry.Timestamp,
			&entry.Level,
			&entry.Component,
			&entry.Message,
			&entry.RequestID,
			&entry.UserID,
			&entry.SessionID,
			&entry.PlaybackSessionID,
			&entry.ClientIP,
			&entry.NodeID,
			&attrsJSON,
		); err != nil {
			return fmt.Errorf("scan inserted operational log row: %w", err)
		}
		if len(attrsJSON) > 0 {
			if err := json.Unmarshal(attrsJSON, &entry.Attrs); err != nil {
				return fmt.Errorf("decode inserted operational log attrs: %w", err)
			}
		}
		inserted = append(inserted, entry)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate inserted operational log rows: %w", err)
	}

	if failed, err := logstream.PublishAppends(c.streamHub, logstream.StreamApp, inserted); failed > 0 {
		slog.WarnContext(ctx, "opslog: failed to publish log stream appends", "component", "opslog", "error", err, "failed", failed, "entries", len(inserted))
	}

	return nil
}
