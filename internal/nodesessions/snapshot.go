package nodesessions

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	snapshotKeyPrefix   = "silo:session-snapshots:"
	sessionOwnerPrefix  = "silo:session-owners:"
	maxSnapshotSessions = 128
)

var ErrSnapshotFenced = errors.New("session snapshot lease replaced")

// SnapshotFence identifies the Redis lease held by a snapshot publisher.
type SnapshotFence struct {
	Key, Value string
}

var replaceSnapshot = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
if redis.call('SCARD', KEYS[2]) > tonumber(ARGV[4]) then
  return redis.error_reply('session snapshot membership exceeds limit')
end
local present = {}
local count = tonumber(ARGV[7])
for i = 1, count do present[ARGV[8 + (i - 1) * 2]] = true end
local removals = {}
for _, member in ipairs(redis.call('SMEMBERS', KEYS[2])) do
  if #member < 10 or #member > 265 or not string.match(member, '^%x%x%x%x%x%x%x%x:') then
    return redis.error_reply('invalid session snapshot member')
  end
  if not present[member] and redis.call('GET', ARGV[6] .. member) == ARGV[2] then
    table.insert(removals, member)
  end
end
for _, member in ipairs(removals) do
  redis.call('DEL', ARGV[5] .. member, ARGV[6] .. member)
end
redis.call('DEL', KEYS[2])
for i = 1, count do
  local member = ARGV[8 + (i - 1) * 2]
  redis.call('SET', ARGV[5] .. member, ARGV[9 + (i - 1) * 2], 'EX', ARGV[3])
  redis.call('SET', ARGV[6] .. member, ARGV[2], 'EX', ARGV[3])
  redis.call('SADD', KEYS[2], member)
end
redis.call('EXPIRE', KEYS[2], ARGV[3])
return 1
`)

// ReplaceSnapshot publishes a bounded, complete node session snapshot. Membership
// is shared across API replicas. The lease check and all writes are atomic, and
// removal cannot delete a record taken over by another publisher or legacy node.
func (tr *Tracker) ReplaceSnapshot(ctx context.Context, publisher string, fence SnapshotFence, infos []SessionInfo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if tr == nil || tr.rdb == nil || publisher == "" || len(publisher) > 128 || fence.Key == "" || fence.Value == "" || len(infos) > maxSnapshotSessions {
		return errors.New("invalid session snapshot")
	}
	args := []any{fence.Value, publisher, int(sessionTTL / time.Second), maxSnapshotSessions, keyPrefix, sessionOwnerPrefix, len(infos)}
	present := make(map[string]struct{}, len(infos))
	for _, info := range infos {
		if info.SessionID == "" || len(info.SessionID) > 256 || info.NodeURL != tr.nodeURL {
			return errors.New("invalid session snapshot member")
		}
		if _, duplicate := present[info.SessionID]; duplicate {
			return errors.New("duplicate session snapshot member")
		}
		present[info.SessionID] = struct{}{}
		data, _ := json.Marshal(info)
		args = append(args, tr.nodeHash+":"+info.SessionID, data)
	}
	accepted, err := replaceSnapshot.Run(ctx, tr.rdb, []string{fence.Key, snapshotKeyPrefix + publisher}, args...).Int()
	if err != nil {
		return err
	}
	if accepted != 1 {
		return ErrSnapshotFenced
	}
	tr.mu.Lock()
	tr.sessions = present
	tr.mu.Unlock()
	return nil
}

// A legacy writer replaces the snapshot record's ownership at the same time as
// its value, so a later HTTP worker snapshot cannot remove the replacement.
func (tr *Tracker) writeSession(ctx context.Context, sessionID string, data []byte) error {
	_, err := tr.rdb.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Del(ctx, sessionOwnerPrefix+tr.nodeHash+":"+sessionID)
		pipe.Set(ctx, tr.redisKey(sessionID), data, sessionTTL)
		return nil
	})
	return err
}
