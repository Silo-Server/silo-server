package workerregistration

import (
	"context"
	"crypto/ed25519"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
)

// RegistrySnapshotTTL must remain shorter than the database mutation drain.
const RegistrySnapshotTTL = 2 * time.Second

type RegistrySnapshot struct {
	Nodes      []workercontrolcore.Node
	Origins    []string
	ValidUntil time.Time
}

// Snapshot excludes protocol mutations until the complete classification has
// been read. The shared lock has its own statement: READ COMMITTED must take the
// data snapshot after a waiting writer commits, never before the lock wait.
func (r *Repository) Snapshot(ctx context.Context) (RegistrySnapshot, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadWrite})
	if err != nil {
		return RegistrySnapshot{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared($1,$2)", nodepool.ConfigurationLockNamespace, nodepool.WorkerRegistryLockKey); err != nil {
		return RegistrySnapshot{}, err
	}
	// Certify only after the lock wait and before any data read. A writer must
	// acquire the exclusive lock after this transaction releases its shared
	// lock, then drain longer than this certificate can remain valid.
	validUntil := time.Now().Add(RegistrySnapshotTTL)
	rows, err := tx.Query(ctx, "SELECT n.id,n.url,n.enabled,w.public_key,w.allow_http FROM stream_nodes n JOIN worker_http_nodes w ON w.node_id=n.id WHERE n.type='transcode' ORDER BY n.id")
	if err != nil {
		return RegistrySnapshot{}, err
	}
	snapshot := RegistrySnapshot{ValidUntil: validUntil}
	for rows.Next() {
		var node workercontrolcore.Node
		var key []byte
		if err = rows.Scan(&node.ID, &node.URL, &node.Enabled, &key, &node.AllowHTTP); err != nil {
			rows.Close()
			return RegistrySnapshot{}, err
		}
		node.PublicKey = ed25519.PublicKey(key)
		if canonical, canonicalErr := workeridentity.ValidateOrigin(node.URL, node.AllowHTTP); canonicalErr == nil {
			node.URL = canonical
		}
		snapshot.Nodes = append(snapshot.Nodes, node)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return RegistrySnapshot{}, err
	}
	rows, err = tx.Query(ctx, "SELECT raw_url FROM worker_http_origin_history")
	if err != nil {
		return RegistrySnapshot{}, err
	}
	snapshot.Origins, err = pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return RegistrySnapshot{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return RegistrySnapshot{}, err
	}
	return snapshot, nil
}
