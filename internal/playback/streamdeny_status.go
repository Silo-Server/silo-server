package playback

import (
	"context"
	"errors"

	"github.com/redis/go-redis/v9"
)

// DeniedStatus is the authority-refresh reader. It preserves immediate local
// revocation, but reads live negative decisions and never caches an outage as
// permission to issue a new signed worker lease.
func (d *StreamDeny) DeniedStatus(ctx context.Context, sessionID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if d == nil || sessionID == "" {
		return false, nil
	}
	if denied, found := d.cached(sessionID); found && denied {
		return true, nil
	}
	lookup, cancel := context.WithTimeout(ctx, streamDenyLookupTimeout)
	defer cancel()
	err := d.client.Get(lookup, StreamDenyKey(sessionID)).Err()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	d.remember(sessionID, true)
	return true, nil
}
