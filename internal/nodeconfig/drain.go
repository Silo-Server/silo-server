package nodeconfig

import (
	"context"
	"errors"
	"strings"

	"github.com/Silo-Server/silo-server/internal/serveridentity"
	"github.com/Silo-Server/silo-server/internal/workerdrain"
	"github.com/jackc/pgx/v5"
)

func (w *Watcher) HasDrainAuthority() bool { return w != nil && w.pool != nil }

// ReadDrainFence uses the worker's remembered stable registration, never a
// hostname match or a stale configuration cache. Missing registration and DB
// errors deny new admission and prevent a retirement receipt.
func (w *Watcher) ReadDrainFence(ctx context.Context) (int, string, string, error) {
	if !w.HasDrainAuthority() {
		return 0, "", "", workerdrain.ErrUnavailable
	}
	id, ok := w.NodeRowID()
	if !ok {
		return 0, "", "", workerdrain.ErrIdentityUnavailable
	}
	var fence, realm string
	err := w.pool.QueryRow(ctx, `SELECT COALESCE(f.fence_id,''), s.value FROM stream_nodes n
		LEFT JOIN stream_node_drain_fences f ON f.node_id=n.id
		JOIN server_settings s ON s.key=$2 WHERE n.id=$1`, id, serveridentity.Key).Scan(&fence, &realm)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", "", workerdrain.ErrIdentityUnavailable
	}
	if err != nil {
		return 0, "", "", err
	}
	if strings.TrimSpace(realm) == "" {
		return 0, "", "", workerdrain.ErrIdentityUnavailable
	}
	return id, fence, strings.TrimSpace(realm), err
}
