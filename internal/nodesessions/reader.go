package nodesessions

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/redis/go-redis/v9"
)

// ListResult is the outcome of reading every node's live session records.
type ListResult struct {
	Sessions []SessionInfo
	// Undecodable counts keys whose value did not parse as a SessionInfo. A
	// mixed-version fleet can legitimately write a record this binary cannot
	// read; surfacing the count keeps that visible instead of quietly shrinking
	// the result.
	Undecodable int
	// Truncated reports that the scan stopped at limit. A caller that ignores
	// this would read a partial answer as a complete one.
	Truncated bool
}

// ListAll reads the live session records of every proxy and transcode node:
// silo:sessions:{nodeHash}:{sessionID}, which proxies write, and
// silo:sessions:n{nodeID}:{sessionID}, which the core writes from transcode
// node reports.
//
// It exists here rather than in a handler because this package owns the key
// format and the record shape. Note that internal/api/handlers/nodes.go
// deliberately keeps its own scan: that endpoint passes the stored JSON through
// opaquely so an older node's extra fields survive the round trip, which a
// decode-and-re-encode reader would silently drop.
func ListAll(ctx context.Context, rdb *redis.Client, limit int) (ListResult, error) {
	var result ListResult
	if rdb == nil {
		return result, nil
	}
	if limit <= 0 {
		limit = 50_000
	}

	var cursor uint64
	for {
		keys, next, err := rdb.Scan(ctx, cursor, keyPrefix+"*", 200).Result()
		if err != nil {
			return result, err
		}
		for _, key := range keys {
			if len(result.Sessions) >= limit {
				result.Truncated = true
				return result, nil
			}
			value, err := rdb.Get(ctx, key).Result()
			if err != nil {
				// A key that expired between the SCAN and the GET is normal:
				// these records carry a 60s TTL.
				continue
			}
			var info SessionInfo
			if err := json.Unmarshal([]byte(value), &info); err != nil || info.SessionID == "" {
				result.Undecodable++
				continue
			}
			result.Sessions = append(result.Sessions, info)
		}
		cursor = next
		if cursor == 0 {
			return result, nil
		}
	}
}

// ListNode reads the live session records of one node, nodeID registered at
// nodeURL, as ListAll reads every node's. Undecodable counts the records that
// could be this node's and do not parse.
func ListNode(ctx context.Context, rdb *redis.Client, nodeID int, nodeURL string, limit int) (ListResult, error) {
	var result ListResult
	if limit <= 0 {
		limit = 50_000
	}
	err := eachNodeRecord(ctx, rdb, nodeID, nodeURL, func(_ string, info SessionInfo, decoded bool) bool {
		if !decoded {
			result.Undecodable++
			return true
		}
		if len(result.Sessions) >= limit {
			result.Truncated = true
			return false
		}
		result.Sessions = append(result.Sessions, info)
		return true
	})
	return result, err
}

// NodeRecords returns the stored JSON of one node's live session records, as
// written, for a caller that passes them through without decoding them.
func NodeRecords(ctx context.Context, rdb *redis.Client, nodeID int, nodeURL string) ([]json.RawMessage, error) {
	var records []json.RawMessage
	err := eachNodeRecord(ctx, rdb, nodeID, nodeURL, func(value string, _ SessionInfo, decoded bool) bool {
		if decoded {
			records = append(records, json.RawMessage(value))
		}
		return true
	})
	return records, err
}

// eachNodeRecord calls fn with each live session record node nodeID,
// registered at nodeURL, owns, until fn returns false. A node owns every record
// under its id (the core's writes from its reports) and the records under the
// hash of its URL (a proxy's, or a node's from before reports) that name that
// URL: URL hashes are 32 bits and collide, so the hash alone does not name a
// node. decoded is false for a value that does not parse as a SessionInfo;
// under the hash its owner is unknown.
func eachNodeRecord(ctx context.Context, rdb *redis.Client, nodeID int, nodeURL string, fn func(value string, info SessionInfo, decoded bool) bool) error {
	if rdb == nil {
		return nil
	}
	byID := nodeKeyPrefix(nodeID)
	patterns := []string{byID + "*"}
	if nodeURL != "" {
		patterns = append(patterns, keyPrefix+nodeHash(nodeURL)+":*")
	}
	for _, pattern := range patterns {
		var cursor uint64
		for {
			keys, next, err := rdb.Scan(ctx, cursor, pattern, 200).Result()
			if err != nil {
				return err
			}
			for _, key := range keys {
				value, err := rdb.Get(ctx, key).Result()
				if err != nil {
					// Expired between the SCAN and the GET.
					continue
				}
				var info SessionInfo
				decoded := json.Unmarshal([]byte(value), &info) == nil && info.SessionID != ""
				if decoded && !strings.HasPrefix(key, byID) && info.NodeURL != nodeURL {
					continue
				}
				if !fn(value, info, decoded) {
					return nil
				}
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
	}
	return nil
}
