package nodesessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// nodeIndexPrefix keys, per transcode node id, the set of session keys the
// core last wrote from that node's report, so the next report can delete the
// ones it no longer lists without scanning the keyspace.
const nodeIndexPrefix = "silo:nodesessions:"

// maxSessionIDLen bounds a reported session id, which becomes part of a Redis
// key.
const maxSessionIDLen = 256

// ErrInvalidSession is a reported session record the core refuses.
var ErrInvalidSession = errors.New("nodesessions: invalid session record")

// nodeKeyPrefix is the prefix of the session keys the core writes for
// transcode node nodeID: silo:sessions:n<id>:. It shares keyPrefix, so every
// reader that scans silo:sessions:* finds these records next to the ones
// proxies write under the hash of their URL, and it cannot collide with those:
// a URL hash is hex, which has no "n".
func nodeKeyPrefix(nodeID int) string {
	return keyPrefix + "n" + strconv.Itoa(nodeID) + ":"
}

// replaceNodeSessions makes one node's session records the given set in one
// step: it reads the node's index, deletes the indexed keys the set no longer
// has, writes each record with the TTL, and replaces the index. Run as one
// script, two replicas ingesting reports of the same node cannot each read
// the same index and leave a record no index lists; the later one always sees
// and replaces what the earlier one wrote.
//
// KEYS[1] is the index; KEYS[2..] are the session keys, with the records in
// ARGV[2..] at the same positions. ARGV[1] is the TTL in milliseconds. Only
// keys under the node's own prefix (KEYS[1]'s owner, ARGV[#ARGV]) are deleted.
var replaceNodeSessions = redis.NewScript(`
local prefix = ARGV[#ARGV]
local keep = {}
for i = 2, #KEYS do keep[KEYS[i]] = true end
for _, key in ipairs(redis.call('SMEMBERS', KEYS[1])) do
  if not keep[key] and string.sub(key, 1, #prefix) == prefix then
    redis.call('DEL', key)
  end
end
redis.call('DEL', KEYS[1])
for i = 2, #KEYS do
  redis.call('SET', KEYS[i], ARGV[i], 'PX', ARGV[1])
  redis.call('SADD', KEYS[1], KEYS[i])
end
if #KEYS > 1 then redis.call('PEXPIRE', KEYS[1], ARGV[1]) end
return #KEYS - 1
`)

// IngestNodeSessions makes sessions the complete set of session records of
// transcode node nodeID, as the node's last report listed them. It writes
// each record under the authenticated node id
// (silo:sessions:n<id>:<session>, 60 s TTL), in the record shape a proxy's
// Tracker writes, so readers of silo:sessions:* find them unchanged, and it
// deletes the records of this node that the report no longer lists. Keying by
// node id rather than by the hash of the row URL keeps one node from
// overwriting or deleting another's records when two URLs share a hash.
//
// The node does not choose its identity: nodeURL and nodeName are its
// registered row's, and replace whatever the records carry. A start time
// later than latest is clamped to latest, so a node's clock cannot make a
// session look like it starts in the future. It keeps no process state, so
// any API replica can ingest any report.
func IngestNodeSessions(ctx context.Context, rdb *redis.Client, nodeID int, nodeURL, nodeName string, sessions []SessionInfo, latest time.Time) error {
	if rdb == nil {
		return nil
	}
	prefix := nodeKeyPrefix(nodeID)
	keys := make([]string, 1, len(sessions)+1)
	keys[0] = nodeIndexPrefix + strconv.Itoa(nodeID)
	args := make([]any, 1, len(sessions)+2)
	args[0] = sessionTTL.Milliseconds()
	seen := make(map[string]struct{}, len(sessions))
	for _, info := range sessions {
		if info.SessionID == "" || len(info.SessionID) > maxSessionIDLen {
			return fmt.Errorf("%w: session id must be 1-%d bytes", ErrInvalidSession, maxSessionIDLen)
		}
		if _, dup := seen[info.SessionID]; dup {
			continue
		}
		seen[info.SessionID] = struct{}{}
		info.NodeURL, info.NodeName = nodeURL, nodeName
		clampStartedAt(&info, latest)
		data, err := json.Marshal(info)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidSession, err)
		}
		keys = append(keys, prefix+info.SessionID)
		args = append(args, data)
	}
	args = append(args, prefix)
	if err := replaceNodeSessions.Run(ctx, rdb, keys, args...).Err(); err != nil {
		return fmt.Errorf("write node sessions: %w", err)
	}
	return nil
}

// clampStartedAt moves a start time later than latest back to latest, in both
// the nanosecond field and the RFC 3339 one.
func clampStartedAt(info *SessionInfo, latest time.Time) {
	if info.StartedAtUnixNano > latest.UnixNano() {
		info.StartedAtUnixNano = latest.UnixNano()
	}
	if t, err := time.Parse(time.RFC3339, info.StartedAt); err == nil && t.After(latest) {
		info.StartedAt = latest.UTC().Format(time.RFC3339)
	}
}
