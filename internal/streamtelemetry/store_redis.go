package streamtelemetry

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const redisFieldChunk = 512

type RedisStore struct {
	client *redis.Client
	cfg    Config
	logger *slog.Logger

	mu             sync.Mutex
	publisherID    string
	published      map[string][16]byte
	needFullResync bool
	publishCount   uint64
}

func NewRedisStore(client *redis.Client, cfg Config, logger *slog.Logger) *RedisStore {
	if logger == nil {
		logger = slog.Default()
	}
	return &RedisStore{client: client, cfg: cfg, logger: logger, publisherID: cfg.PublisherID, published: make(map[string][16]byte), needFullResync: true}
}

func (s *RedisStore) snapshotKey(publisherID string) string {
	return s.cfg.KeyPrefix + ":snap:" + publisherID
}

func (s *RedisStore) rosterKey() string { return s.cfg.KeyPrefix + ":roster" }

func digest128(value []byte) [16]byte {
	full := sha256.Sum256(value)
	var digest [16]byte
	copy(digest[:], full[:16])
	return digest
}

func snapshotHashFields(snapshot Snapshot) (map[string][]byte, error) {
	fields := make(map[string][]byte, len(snapshot.Sessions)+len(snapshot.Transfers)+1)
	meta, err := encodeMeta(publisherMeta{
		PublisherID: snapshot.PublisherID, NodeID: snapshot.NodeID, Epoch: snapshot.PublisherEpoch,
		Sequence: snapshot.Sequence, CapturedAtUnixNano: timeToUnixNano(snapshot.CapturedAt), Truncated: snapshot.Truncated,
		DroppedObservations: snapshot.DroppedObservations, DroppedBytes: snapshot.DroppedBytes,
		UnattributedObservations: snapshot.UnattributedObservations, UnattributedBytes: snapshot.UnattributedBytes,
		SessionCount: len(snapshot.Sessions), TransferCount: len(snapshot.Transfers),
	})
	if err != nil {
		return nil, err
	}
	fields[publisherMetaField] = meta
	for _, session := range snapshot.Sessions {
		encoded, encodeErr := encodeSession(session)
		if encodeErr != nil {
			return nil, fmt.Errorf("encode session %q: %w", session.SessionID, encodeErr)
		}
		fields["s:"+session.SessionID] = encoded
	}
	for _, transfer := range snapshot.Transfers {
		encoded, encodeErr := encodeTransfer(transfer)
		if encodeErr != nil {
			return nil, fmt.Errorf("encode transfer %q: %w", transfer.ID, encodeErr)
		}
		fields["t:"+transfer.ID] = encoded
	}
	return fields, nil
}

func (s *RedisStore) plan(snapshot Snapshot) (sets map[string][]byte, dels []string, fields map[string][]byte, full bool, err error) {
	fields, err = snapshotHashFields(snapshot)
	if err != nil {
		return nil, nil, nil, false, err
	}
	full = s.needFullResync || s.publishCount == 0 || (s.cfg.FullResyncEvery > 0 && s.publishCount%uint64(s.cfg.FullResyncEvery) == 0)
	sets = make(map[string][]byte, len(fields))
	if full {
		for field, value := range fields {
			sets[field] = value
		}
		return sets, nil, fields, true, nil
	}
	for field, value := range fields {
		if old, ok := s.published[field]; !ok || old != digest128(value) {
			sets[field] = value
		}
	}
	sets[publisherMetaField] = fields[publisherMetaField]
	for field := range s.published {
		if _, ok := fields[field]; !ok {
			dels = append(dels, field)
		}
	}
	sort.Strings(dels)
	return sets, dels, fields, false, nil
}

func (s *RedisStore) Publish(ctx context.Context, snapshot Snapshot) error {
	if s == nil || s.client == nil {
		return fmt.Errorf("stream telemetry redis client is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.publisherID == "" {
		s.publisherID = snapshot.PublisherID
	} else if s.publisherID != snapshot.PublisherID {
		return fmt.Errorf("stream telemetry publisher id changed from %q to %q", s.publisherID, snapshot.PublisherID)
	}
	sets, dels, fields, full, err := s.plan(snapshot)
	if err != nil {
		return err
	}
	key := s.snapshotKey(snapshot.PublisherID)
	// A delta publish rewrites only the fields whose local digest changed, so it
	// silently assumes the hash still holds everything else. It may not: a
	// maxmemory eviction, an out-of-band DEL, a failover to a replica missing
	// the key, or a publish gap long enough for PExpire to lapse all drop it
	// without an error. HLEN runs inside the same transaction, so it reports the
	// post-write field count; a mismatch means the key was reconstructed from
	// the delta alone and the next publish must be full. Costs one pipelined
	// command and self-heals in one sweep instead of up to FullResyncEvery.
	var fieldCount *redis.IntCmd
	_, err = s.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		if full {
			pipe.Del(ctx, key)
		}
		for start := 0; start < len(dels); start += redisFieldChunk {
			end := min(start+redisFieldChunk, len(dels))
			pipe.HDel(ctx, key, dels[start:end]...)
		}
		setNames := make([]string, 0, len(sets))
		for field := range sets {
			if field != publisherMetaField {
				setNames = append(setNames, field)
			}
		}
		sort.Strings(setNames)
		for start := 0; start < len(setNames); start += redisFieldChunk {
			end := min(start+redisFieldChunk, len(setNames))
			values := make([]any, 0, (end-start)*2)
			for _, field := range setNames[start:end] {
				values = append(values, field, sets[field])
			}
			pipe.HSet(ctx, key, values...)
		}
		pipe.HSet(ctx, key, publisherMetaField, sets[publisherMetaField])
		pipe.PExpire(ctx, key, s.cfg.MembershipTTL)
		pipe.ZAdd(ctx, s.rosterKey(), redis.Z{Score: float64(snapshot.CapturedAt.UnixNano()), Member: snapshot.PublisherID})
		cutoff := snapshot.CapturedAt.Add(-2 * s.cfg.MembershipTTL).UnixNano()
		pipe.ZRemRangeByScore(ctx, s.rosterKey(), "-inf", "("+strconv.FormatInt(cutoff, 10))
		pipe.PExpire(ctx, s.rosterKey(), 10*s.cfg.MembershipTTL)
		if !full {
			fieldCount = pipe.HLen(ctx, key)
		}
		return nil
	})
	if err != nil {
		s.needFullResync = true
		clear(s.published)
		return err
	}
	clear(s.published)
	for field, value := range fields {
		s.published[field] = digest128(value)
	}
	s.needFullResync = false
	if fieldCount != nil {
		if held, hlenErr := fieldCount.Result(); hlenErr != nil || held != int64(len(fields)) {
			s.needFullResync = true
		}
	}
	s.publishCount++
	return nil
}

func (s *RedisStore) Load(ctx context.Context) (Snapshot, error) {
	if s == nil || s.client == nil {
		return Snapshot{}, fmt.Errorf("stream telemetry redis client is nil")
	}
	s.mu.Lock()
	publisherID := s.publisherID
	s.mu.Unlock()
	if publisherID == "" {
		return Snapshot{}, nil
	}
	fields, err := s.client.HGetAll(ctx, s.snapshotKey(publisherID)).Result()
	if err != nil {
		return Snapshot{}, err
	}
	snapshot, _, err := decodeSnapshotHash(publisherID, fields, s.cfg.MaxMergedSessions, s.cfg.MaxMergedTransfers)
	return snapshot, err
}

func (s *RedisStore) LoadAll(ctx context.Context) (PublisherSet, error) {
	if s == nil || s.client == nil {
		return PublisherSet{}, fmt.Errorf("stream telemetry redis client is nil")
	}
	set := PublisherSet{}
	minimum := "(" + strconv.FormatInt(now().Add(-s.cfg.MembershipTTL).UnixNano(), 10)
	readLimit := int64(s.cfg.MaxPublishers)
	if readLimit < int64(^uint64(0)>>1) {
		readLimit++
	}
	members, err := s.client.ZRangeByScoreWithScores(ctx, s.rosterKey(), &redis.ZRangeBy{Min: minimum, Max: "+inf", Offset: 0, Count: readLimit}).Result()
	if err != nil {
		return set, err
	}
	if len(members) > s.cfg.MaxPublishers {
		set.Truncated = true
		set.Errors = append(set.Errors, PublisherError{Reason: "publisher_cap"})
		members = members[:s.cfg.MaxPublishers]
	}
	for _, member := range members {
		publisherID, ok := member.Member.(string)
		if !ok {
			continue
		}
		set.Members = append(set.Members, Member{PublisherID: publisherID, LastHeartbeat: time.Unix(0, int64(member.Score))})
	}
	sort.Slice(set.Members, func(i, j int) bool { return set.Members[i].PublisherID < set.Members[j].PublisherID })

	pipe := s.client.Pipeline()
	hlens := make(map[string]*redis.IntCmd, len(set.Members))
	for _, member := range set.Members {
		hlens[member.PublisherID] = pipe.HLen(ctx, s.snapshotKey(member.PublisherID))
	}
	if _, err = pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return set, err
	}
	maxFields := s.maxFieldsPerPublisher()
	readPipe := s.client.Pipeline()
	reads := make(map[string]*redis.MapStringStringCmd, len(set.Members))
	for _, member := range set.Members {
		length, lengthErr := hlens[member.PublisherID].Result()
		if lengthErr != nil && !errors.Is(lengthErr, redis.Nil) {
			return set, lengthErr
		}
		if length > maxFields {
			set.Errors = append(set.Errors, PublisherError{PublisherID: member.PublisherID, Reason: publisherReasonOversized})
			continue
		}
		reads[member.PublisherID] = readPipe.HGetAll(ctx, s.snapshotKey(member.PublisherID))
	}
	if len(reads) > 0 {
		if _, err = readPipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
			return set, err
		}
	}
	remainingSessions, remainingTransfers := s.cfg.MaxMergedSessions, s.cfg.MaxMergedTransfers
	for _, member := range set.Members {
		read := reads[member.PublisherID]
		if read == nil {
			continue
		}
		fields, readErr := read.Result()
		if readErr != nil && !errors.Is(readErr, redis.Nil) {
			return set, readErr
		}
		snapshot, publisherErr, decodeErr := decodeSnapshotHash(member.PublisherID, fields, remainingSessions, remainingTransfers)
		if decodeErr != nil {
			set.Errors = append(set.Errors, PublisherError{PublisherID: member.PublisherID, DecodeErrors: 1, Reason: publisherReasonDecode})
			continue
		}
		if publisherErr.Reason != "" || publisherErr.DecodeErrors > 0 {
			set.Errors = append(set.Errors, publisherErr)
		}
		if snapshot.PublisherID == "" {
			continue
		}
		if len(snapshot.Sessions) == remainingSessions && countFields(fields, "s:") > remainingSessions {
			set.Truncated = true
		}
		if len(snapshot.Transfers) == remainingTransfers && countFields(fields, "t:") > remainingTransfers {
			set.Truncated = true
		}
		remainingSessions -= len(snapshot.Sessions)
		remainingTransfers -= len(snapshot.Transfers)
		set.Snapshots = append(set.Snapshots, snapshot)
	}
	sort.Slice(set.Snapshots, func(i, j int) bool { return set.Snapshots[i].PublisherID < set.Snapshots[j].PublisherID })
	sort.Slice(set.Errors, func(i, j int) bool {
		if set.Errors[i].PublisherID == set.Errors[j].PublisherID {
			return set.Errors[i].Reason < set.Errors[j].Reason
		}
		return set.Errors[i].PublisherID < set.Errors[j].PublisherID
	})
	return set, nil
}

func (s *RedisStore) maxFieldsPerPublisher() int64 {
	maximum := s.cfg.MaxSessions + s.cfg.MaxTransfers + 16
	if maximum < 16 {
		return int64(^uint64(0) >> 1)
	}
	return maximum
}

func countFields(fields map[string]string, prefix string) int {
	count := 0
	for field := range fields {
		if strings.HasPrefix(field, prefix) {
			count++
		}
	}
	return count
}

func decodeSnapshotHash(publisherID string, fields map[string]string, maxSessions, maxTransfers int) (Snapshot, PublisherError, error) {
	problem := PublisherError{PublisherID: publisherID}
	metaBytes, ok := fields[publisherMetaField]
	if !ok {
		problem.Reason = publisherReasonMetaMissing
		return Snapshot{}, problem, nil
	}
	meta, err := decodeMeta([]byte(metaBytes))
	if err != nil {
		problem.DecodeErrors = 1
		problem.Reason = publisherReasonDecode
		return Snapshot{}, problem, err
	}
	if meta.PublisherID != publisherID {
		problem.Reason = publisherReasonIdentityMismatch
		return Snapshot{}, problem, nil
	}
	snapshot := Snapshot{PublisherID: meta.PublisherID, NodeID: meta.NodeID, PublisherEpoch: meta.Epoch, Sequence: meta.Sequence,
		CapturedAt: timeFromUnixNano(meta.CapturedAtUnixNano), Truncated: meta.Truncated, DroppedObservations: meta.DroppedObservations,
		DroppedBytes: meta.DroppedBytes, UnattributedObservations: meta.UnattributedObservations, UnattributedBytes: meta.UnattributedBytes}
	names := make([]string, 0, len(fields))
	for field := range fields {
		if field != publisherMetaField {
			names = append(names, field)
		}
	}
	sort.Strings(names)
	decodedSessions, decodedTransfers := 0, 0
	for _, field := range names {
		switch {
		case strings.HasPrefix(field, "s:"):
			decodedSessions++
			if len(snapshot.Sessions) >= maxSessions {
				continue
			}
			value, decodeErr := decodeSession([]byte(fields[field]))
			if decodeErr != nil {
				problem.DecodeErrors++
				continue
			}
			snapshot.Sessions = append(snapshot.Sessions, value)
		case strings.HasPrefix(field, "t:"):
			decodedTransfers++
			if len(snapshot.Transfers) >= maxTransfers {
				continue
			}
			value, decodeErr := decodeTransfer([]byte(fields[field]))
			if decodeErr != nil {
				problem.DecodeErrors++
				continue
			}
			snapshot.Transfers = append(snapshot.Transfers, value)
		default:
			problem.DecodeErrors++
		}
	}
	if decodedSessions != meta.SessionCount || decodedTransfers != meta.TransferCount {
		problem.Reason = publisherReasonCountMismatch
	} else if problem.DecodeErrors > 0 {
		problem.Reason = publisherReasonDecode
	}
	sort.Slice(snapshot.Sessions, func(i, j int) bool { return snapshot.Sessions[i].SessionID < snapshot.Sessions[j].SessionID })
	sort.Slice(snapshot.Transfers, func(i, j int) bool { return snapshot.Transfers[i].ID < snapshot.Transfers[j].ID })
	return snapshot, problem, nil
}

func (s *RedisStore) Leave(ctx context.Context) error {
	if s == nil || s.client == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.publisherID == "" {
		return nil
	}
	_, err := s.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.ZRem(ctx, s.rosterKey(), s.publisherID)
		pipe.Del(ctx, s.snapshotKey(s.publisherID))
		return nil
	})
	if errors.Is(err, redis.Nil) {
		err = nil
	}
	if err != nil {
		return err
	}
	clear(s.published)
	s.needFullResync = true
	return nil
}
