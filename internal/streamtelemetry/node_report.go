package streamtelemetry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// NodeSnapshot is a transcode node's telemetry as it travels in the node's
// report to the core. Sessions and transfers are the codec's own records, the
// bytes a publisher would store, so the core stores exactly what an in-process
// publisher would have. The node's publisher identity, epoch, sequence and
// capture time are not on the wire: the core supplies all four.
type NodeSnapshot struct {
	Truncated                bool              `json:"truncated,omitempty"`
	DroppedObservations      int64             `json:"dropped_observations,omitempty"`
	DroppedBytes             int64             `json:"dropped_bytes,omitempty"`
	UnattributedObservations int64             `json:"unattributed_observations,omitempty"`
	UnattributedBytes        int64             `json:"unattributed_bytes,omitempty"`
	Sessions                 []json.RawMessage `json:"sessions,omitempty"`
	Transfers                []json.RawMessage `json:"transfers,omitempty"`
}

// ErrInvalidNodeSnapshot is reported telemetry the core refuses.
var ErrInvalidNodeSnapshot = errors.New("streamtelemetry: invalid node snapshot")

// EncodeNodeSnapshot puts a node's swept snapshot on the wire, keeping the
// maxSessions sessions and maxTransfers transfers with the most recent
// activity. omitted reports whether any were left out; Truncated is set then
// too, so the stored view says it is partial.
func EncodeNodeSnapshot(snapshot Snapshot, maxSessions, maxTransfers int) (wire NodeSnapshot, omitted bool, err error) {
	sessions := slices.Clone(snapshot.Sessions)
	transfers := slices.Clone(snapshot.Transfers)
	if len(sessions) > maxSessions {
		sort.SliceStable(sessions, func(i, j int) bool {
			return sessionActivity(sessions[i]).After(sessionActivity(sessions[j]))
		})
		sessions, omitted = sessions[:max(maxSessions, 0)], true
	}
	if len(transfers) > maxTransfers {
		sort.SliceStable(transfers, func(i, j int) bool {
			return latestOf(transfers[i].LastByteAccepted, transfers[i].LastObservationEnd).
				After(latestOf(transfers[j].LastByteAccepted, transfers[j].LastObservationEnd))
		})
		transfers, omitted = transfers[:max(maxTransfers, 0)], true
	}
	wire = NodeSnapshot{
		Truncated:           snapshot.Truncated || omitted,
		DroppedObservations: snapshot.DroppedObservations, DroppedBytes: snapshot.DroppedBytes,
		UnattributedObservations: snapshot.UnattributedObservations, UnattributedBytes: snapshot.UnattributedBytes,
	}
	for _, session := range sessions {
		encoded, encodeErr := encodeSession(session)
		if encodeErr != nil {
			return NodeSnapshot{}, false, fmt.Errorf("encode session %q: %w", session.SessionID, encodeErr)
		}
		wire.Sessions = append(wire.Sessions, encoded)
	}
	for _, transfer := range transfers {
		encoded, encodeErr := encodeTransfer(transfer)
		if encodeErr != nil {
			return NodeSnapshot{}, false, fmt.Errorf("encode transfer %q: %w", transfer.ID, encodeErr)
		}
		wire.Transfers = append(wire.Transfers, encoded)
	}
	return wire, omitted, nil
}

func sessionActivity(s SessionView) time.Time {
	return latestOf(latestOf(s.LastByteAccepted, s.LastObservationEnd), s.StartedAt)
}

func latestOf(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// NodeSnapshotStore returns the store a core ingests transcode nodes'
// telemetry into: registry's Redis store, where every publisher's snapshot
// lives. It is nil when telemetry is off or not distributed; a node's
// telemetry then has nowhere a reader would find it.
func NodeSnapshotStore(registry *Registry) *RedisStore {
	if !registry.Enabled() {
		return nil
	}
	store, _ := registry.Store().(*RedisStore)
	return store
}

// IngestNodeSnapshot stores a transcode node's reported telemetry as the
// complete snapshot of publisher publisherID, replacing whatever that
// publisher had. It uses only the store's client and configuration, never the
// state Publish keeps for this process's own publisher, so any API replica can
// ingest any report.
//
// Identity and time belong to the core: publisherID, nodeName (the snapshot's
// node id), epoch and sequence are the caller's, the capture time and roster
// score are now, and every reported timestamp later than latest is moved back
// to latest. A node can therefore neither write another publisher's key nor
// push the roster cutoff forward to evict other publishers.
func (s *RedisStore) IngestNodeSnapshot(ctx context.Context, publisherID, nodeName string, epoch int64, sequence uint64, wire NodeSnapshot, now, latest time.Time) error {
	if s == nil || s.client == nil {
		return fmt.Errorf("stream telemetry redis client is nil")
	}
	if wire.DroppedObservations < 0 || wire.DroppedBytes < 0 || wire.UnattributedObservations < 0 || wire.UnattributedBytes < 0 {
		return fmt.Errorf("%w: negative counter", ErrInvalidNodeSnapshot)
	}
	snapshot := Snapshot{PublisherID: publisherID, NodeID: nodeName, PublisherEpoch: epoch, Sequence: sequence, CapturedAt: now,
		Truncated: wire.Truncated, DroppedObservations: wire.DroppedObservations, DroppedBytes: wire.DroppedBytes,
		UnattributedObservations: wire.UnattributedObservations, UnattributedBytes: wire.UnattributedBytes}
	seenSessions := make(map[string]bool, len(wire.Sessions))
	for _, raw := range wire.Sessions {
		session, err := decodeSession(raw)
		if err != nil {
			return fmt.Errorf("%w: session: %w", ErrInvalidNodeSnapshot, err)
		}
		if seenSessions[session.SessionID] {
			return fmt.Errorf("%w: duplicate session %q", ErrInvalidNodeSnapshot, session.SessionID)
		}
		seenSessions[session.SessionID] = true
		clampSession(&session, latest)
		snapshot.Sessions = append(snapshot.Sessions, session)
	}
	seenTransfers := make(map[string]bool, len(wire.Transfers))
	for _, raw := range wire.Transfers {
		transfer, err := decodeTransfer(raw)
		if err != nil {
			return fmt.Errorf("%w: transfer: %w", ErrInvalidNodeSnapshot, err)
		}
		if seenTransfers[transfer.ID] {
			return fmt.Errorf("%w: duplicate transfer %q", ErrInvalidNodeSnapshot, transfer.ID)
		}
		seenTransfers[transfer.ID] = true
		transfer.LastByteAccepted = clampTime(transfer.LastByteAccepted, latest)
		transfer.LastObservationEnd = clampTime(transfer.LastObservationEnd, latest)
		snapshot.Transfers = append(snapshot.Transfers, transfer)
	}
	fields, err := snapshotHashFields(snapshot)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidNodeSnapshot, err)
	}
	names := make([]string, 0, len(fields))
	for field := range fields {
		if field != publisherMetaField {
			names = append(names, field)
		}
	}
	sort.Strings(names)
	key := s.snapshotKey(publisherID)
	_, err = s.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Del(ctx, key)
		for start := 0; start < len(names); start += redisFieldChunk {
			end := min(start+redisFieldChunk, len(names))
			values := make([]any, 0, (end-start)*2)
			for _, field := range names[start:end] {
				values = append(values, field, fields[field])
			}
			pipe.HSet(ctx, key, values...)
		}
		pipe.HSet(ctx, key, publisherMetaField, fields[publisherMetaField])
		pipe.PExpire(ctx, key, s.cfg.MembershipTTL)
		pipe.ZAdd(ctx, s.rosterKey(), redis.Z{Score: float64(now.UnixNano()), Member: publisherID})
		cutoff := now.Add(-2 * s.cfg.MembershipTTL).UnixNano()
		pipe.ZRemRangeByScore(ctx, s.rosterKey(), "-inf", "("+strconv.FormatInt(cutoff, 10))
		pipe.PExpire(ctx, s.rosterKey(), 10*s.cfg.MembershipTTL)
		return nil
	})
	return err
}

func clampSession(session *SessionView, latest time.Time) {
	session.StartedAt = clampTime(session.StartedAt, latest)
	session.LastByteAccepted = clampTime(session.LastByteAccepted, latest)
	session.LastObservationEnd = clampTime(session.LastObservationEnd, latest)
	for i := range session.Routes {
		session.Routes[i].LastByteAccepted = clampTime(session.Routes[i].LastByteAccepted, latest)
		session.Routes[i].LastObservationEnd = clampTime(session.Routes[i].LastObservationEnd, latest)
	}
	for i := range session.TokenIssuedAts {
		session.TokenIssuedAts[i] = clampTime(session.TokenIssuedAts[i], latest)
	}
	for i := range session.IdentityConflicts {
		session.IdentityConflicts[i].ObservedAt = clampTime(session.IdentityConflicts[i].ObservedAt, latest)
	}
}

// clampTime moves t back to latest when it is later; a zero time stays zero.
func clampTime(t, latest time.Time) time.Time {
	if t.After(latest) {
		return latest
	}
	return t
}
