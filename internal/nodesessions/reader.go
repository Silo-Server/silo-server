package nodesessions

import (
	"context"
	"encoding/json"

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

// ListAll reads the live session records every proxy and transcode node
// publishes under silo:sessions:{nodeHash}:{sessionID}.
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
