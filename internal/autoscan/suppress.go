package autoscan

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

// Suppressor debounces repeated reports of the same observed change. A claim
// is keyed by (folder ID, reported path), matching the scanqueue dedup
// granularity so two distinct paths under one library folder are never
// collapsed into a single claim, and it records the path's observed state.
//
// The window drops a report only when the path still looks exactly as it did
// at the live claim: a duplicate delivery of a change that a scan has already
// been queued for. A report that observes a different state (the file was
// deleted, replaced, or re-imported since the claim) claims again and scans.
type Suppressor interface {
	// ShouldScan atomically claims key for the observed state. It returns
	// true, recording state with a ttl expiry, when no claim is live or the
	// live claim recorded a different state; it returns false when the live
	// claim recorded this same state. A duplicate does not extend the window.
	ShouldScan(ctx context.Context, key, state string, ttl time.Duration) (bool, error)
	// Release drops the claim on key if it still records state (used when the
	// scan enqueue fails). A newer claim another report wrote is kept.
	Release(ctx context.Context, key, state string) error
}

type redisSuppressor struct{ client *redis.Client }

func NewRedisSuppressor(client *redis.Client) Suppressor { return &redisSuppressor{client: client} }

// claimStateScript claims KEYS[1] for state ARGV[1] with a PX expiry of
// ARGV[2] milliseconds unless the live claim already holds that state.
// Values written by older releases ("1") never equal a state token, so a
// mixed-version cluster only ever scans more, never less.
var claimStateScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return 0
end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
return 1
`)

func (s *redisSuppressor) ShouldScan(ctx context.Context, key, state string, ttl time.Duration) (bool, error) {
	if s.client == nil || ttl <= 0 {
		return true, nil // no suppression configured -> always scan
	}
	ttlMillis := max(ttl.Milliseconds(), 1)
	claimed, err := claimStateScript.Run(ctx, s.client, []string{suppressRedisKey(key)}, state, ttlMillis).Int()
	if err != nil {
		return true, nil // fail open: a Redis hiccup should not block scanning
	}
	return claimed == 1, nil
}

// releaseStateScript deletes KEYS[1] only while it still holds state ARGV[1].
var releaseStateScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`)

func (s *redisSuppressor) Release(ctx context.Context, key, state string) error {
	if s.client == nil {
		return nil
	}
	return releaseStateScript.Run(ctx, s.client, []string{suppressRedisKey(key)}, state).Err()
}

// suppressKeyPrefix keeps the claims under the server's silo: namespace, so a
// Redis user limited to ~silo:* can write them.
const suppressKeyPrefix = "silo:autoscan:scanned:"

func suppressRedisKey(key string) string { return suppressKeyPrefix + key }

// stateAbsent is the observed state of a path that does not exist.
const stateAbsent = "absent"

// stateUnobserved is recorded for a report that cannot be debounced (a
// directory, or a path that could not be inspected). No observation produces
// it, so it replaces any earlier claim on the path and the next debounceable
// report of that path always claims again.
const stateUnobserved = "unobserved"

// observePathState returns the debounce state of a local path and whether a
// repeated report of it may be debounced at all. A missing path is "absent";
// a regular file is identified by its size, modification time, and (on Unix)
// inode number, as "file:<size>:<mtimeNs>:<inode>". A delete, a rewrite, or a
// replacement at the same path reads as a new state, including a replacement
// that keeps the old size and modification time. Claims recorded in an older
// token format never match, so they only ever cause an extra scan.
//
// Anything else is never debounced. A directory's own metadata does not
// change when a file deeper in its tree does, so a repeated directory report
// cannot be told apart from a new change below it; such reports always claim
// and the scan queue's run reuse and follow-up absorb bursts. The same holds
// for a path whose state could not be inspected.
func observePathState(path string) (string, bool) {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return stateAbsent, true
	case err != nil:
		return "", false
	case info.Mode().IsRegular():
		state := fmt.Sprintf("file:%d:%d", info.Size(), info.ModTime().UnixNano())
		if id := fileIdentity(info); id != "" {
			state += ":" + id
		}
		return state, true
	default:
		return "", false
	}
}
