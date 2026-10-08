package downloads

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
)

// BandwidthRates reads only the two non-secret authoritative settings. Reading
// each chunk avoids stale config caches or missed pubsub on an active transfer.
func BandwidthRates(pool *pgxpool.Pool) func(context.Context) (int64, int64, error) {
	return func(ctx context.Context) (int64, int64, error) {
		rows, err := pool.Query(ctx, `SELECT key,value FROM server_settings WHERE key IN ('download.server_bandwidth_mbps','download.user_bandwidth_mbps')`)
		if err != nil {
			return 0, 0, err
		}
		defer rows.Close()
		var server, user int64
		for rows.Next() {
			var key, value string
			if err := rows.Scan(&key, &value); err != nil {
				return 0, 0, err
			}
			mbps, err := strconv.ParseInt(value, 10, 64)
			if err != nil || mbps < 0 || mbps > 9223372036854775807/125000 {
				return 0, 0, fmt.Errorf("invalid download bandwidth setting %s", key)
			}
			if key == "download.server_bandwidth_mbps" {
				server = mbps * 125000
			} else {
				user = mbps * 125000
			}
		}
		return server, user, rows.Err()
	}
}
