package main

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/workerwire"
)

type workerReportCursor struct {
	BootID   string           `json:"boot_id"`
	Sequence uint64           `json:"sequence"`
	Retired  map[string]int64 `json:"retired,omitempty"`
}

// Reports can arrive on different API replicas. Serialize their writes and
// keep the boot/sequence cursor in Redis so a delayed batch cannot roll back
// newer ownership or telemetry. The request deadline is much shorter than
// the lock lease, and every storage operation uses that deadline.
func lockWorkerReport(ctx context.Context, rdb *redis.Client, node int, batch workerwire.Report) (context.Context, func(), bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	if rdb == nil {
		cancel()
		return ctx, nil, false, errors.New("worker report storage unavailable")
	}
	key := "silo:http-workers:report:" + strconv.Itoa(node)
	lock := key + ":lock"
	owner := uuid.NewString()
	acquired, err := rdb.SetArgs(ctx, lock, owner, redis.SetArgs{Mode: "NX", TTL: 30 * time.Second}).Result()
	if err != nil || acquired != "OK" {
		cancel()
		return ctx, nil, false, errors.New("worker report already publishing or storage unavailable")
	}
	release := func() {
		cancel()
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer stop()
		_ = rdb.Eval(cleanup, `if redis.call('GET',KEYS[1])==ARGV[1] then return redis.call('DEL',KEYS[1]) end return 0`, []string{lock}, owner).Err()
	}
	var cursor workerReportCursor
	raw, err := rdb.Get(ctx, key).Bytes()
	if err != nil && !errors.Is(err, redis.Nil) {
		release()
		return ctx, nil, false, err
	}
	if len(raw) > 16<<10 || len(raw) != 0 && json.Unmarshal(raw, &cursor) != nil {
		release()
		return ctx, nil, false, errors.New("invalid worker report cursor")
	}
	now := time.Now().Unix()
	if cursor.Retired == nil {
		cursor.Retired = make(map[string]int64)
	}
	for boot, until := range cursor.Retired {
		if until <= now {
			delete(cursor.Retired, boot)
		}
	}
	if cursor.BootID == batch.BootID && batch.Sequence <= cursor.Sequence || cursor.Retired[batch.BootID] > now {
		return ctx, release, false, nil
	}
	if cursor.BootID != "" && cursor.BootID != batch.BootID {
		if len(cursor.Retired) >= 32 {
			release()
			return ctx, nil, false, errors.New("worker restarted too often to publish")
		}
		// Proofs expire within 30 seconds. Retain superseded boots longer
		// than that window, including clock skew, while bounding history.
		cursor.Retired[cursor.BootID] = now + 120
	}
	cursor.BootID, cursor.Sequence = batch.BootID, batch.Sequence
	encoded, err := json.Marshal(cursor)
	if err == nil {
		err = rdb.Set(ctx, key, encoded, 2*time.Minute).Err()
	}
	if err != nil {
		release()
		return ctx, nil, false, err
	}
	return ctx, release, true, nil
}
