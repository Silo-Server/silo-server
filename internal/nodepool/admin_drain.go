package nodepool

import (
	"context"
	"errors"
	"strings"

	"github.com/Silo-Server/silo-server/internal/serveridentity"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrNodeDrainConflict = errors.New("node drain configuration changed")
var ErrNodeDrainAuthorityUnavailable = errors.New("node drain deployment identity unavailable")

type DrainConfiguration struct {
	Node           *Node
	FenceID        string
	NativeServerID string
}

func readDrainServerIdentity(ctx context.Context, tx pgx.Tx) (string, error) {
	var id string
	if err := tx.QueryRow(ctx, `SELECT value FROM server_settings WHERE key=$1`, serveridentity.Key).Scan(&id); err != nil {
		return "", ErrNodeDrainAuthorityUnavailable
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return "", ErrNodeDrainAuthorityUnavailable
	}
	return id, nil
}

// ReadDrain pairs the configuration validator and fence in one snapshot. A
// health sample cannot change either part of this retirement authority.
func (s *AdminConfigurationStore) ReadDrain(ctx context.Context, id int) (DrainConfiguration, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return DrainConfiguration{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	node, err := NewTransactionalRepository(tx).GetByID(ctx, id)
	if err != nil {
		return DrainConfiguration{}, err
	}
	if err = tx.QueryRow(ctx, `SELECT GREATEST(admin_revision,drain_revision) FROM stream_nodes WHERE id=$1`, id).Scan(&node.AdminRevision); err != nil {
		return DrainConfiguration{}, err
	}
	var fence string
	err = tx.QueryRow(ctx, `SELECT fence_id FROM stream_node_drain_fences WHERE node_id=$1`, id).Scan(&fence)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return DrainConfiguration{}, err
	}
	realm, err := readDrainServerIdentity(ctx, tx)
	if err != nil {
		return DrainConfiguration{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return DrainConfiguration{}, err
	}
	return DrainConfiguration{Node: node, FenceID: fence, NativeServerID: realm}, nil
}

// BeginDrain disables placement and publishes the worker admission fence in
// the same guarded transaction. The fence survives API and worker restarts.
func (s *AdminConfigurationStore) BeginDrain(ctx context.Context, id int, guard func(int64) error) (DrainConfiguration, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return DrainConfiguration{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	revision, err := readConfigurationRevision(ctx, tx, id)
	if err != nil {
		return DrainConfiguration{}, err
	}
	if err = guard(revision); err != nil {
		return DrainConfiguration{}, err
	}
	realm, err := readDrainServerIdentity(ctx, tx)
	if err != nil {
		return DrainConfiguration{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE stream_nodes SET enabled=false WHERE id=$1`, id); err != nil {
		return DrainConfiguration{}, err
	}
	var fence string
	err = tx.QueryRow(ctx, `SELECT fence_id FROM stream_node_drain_fences WHERE node_id=$1`, id).Scan(&fence)
	if errors.Is(err, pgx.ErrNoRows) {
		fence = uuid.NewString()
		if _, err = tx.Exec(ctx, `INSERT INTO stream_node_drain_fences(node_id,fence_id) VALUES($1,$2)`, id, fence); err != nil {
			return DrainConfiguration{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE stream_nodes SET drain_revision=nextval('stream_node_admin_revision_seq') WHERE id=$1`, id); err != nil {
			return DrainConfiguration{}, err
		}
	} else if err != nil {
		return DrainConfiguration{}, err
	}
	node, err := NewTransactionalRepository(tx).GetByID(ctx, id)
	if err != nil {
		return DrainConfiguration{}, err
	}
	node.AdminRevision, err = readConfigurationRevision(ctx, tx, id)
	if err != nil {
		return DrainConfiguration{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return DrainConfiguration{}, err
	}
	return DrainConfiguration{Node: node, FenceID: fence, NativeServerID: realm}, nil
}

// CancelDrain removes the fence under the caller's original configuration
// validator. It deliberately leaves placement disabled; reenable is separate.
func (s *AdminConfigurationStore) CancelDrain(ctx context.Context, id int, guard func(int64) error) (*Node, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	revision, err := readConfigurationRevision(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err = guard(revision); err != nil {
		return nil, err
	}
	deleted, err := tx.Exec(ctx, `DELETE FROM stream_node_drain_fences WHERE node_id=$1`, id)
	if err != nil {
		return nil, err
	}
	if deleted.RowsAffected() > 0 {
		if _, err = tx.Exec(ctx, `UPDATE stream_nodes SET drain_revision=nextval('stream_node_admin_revision_seq') WHERE id=$1`, id); err != nil {
			return nil, err
		}
	}
	node, err := NewTransactionalRepository(tx).GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	node.AdminRevision, err = readConfigurationRevision(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return node, nil
}
