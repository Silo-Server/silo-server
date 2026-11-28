package directconn

import (
	"context"
	"fmt"
	"net/http"

	"github.com/darthShadow/shunpike/coordination/protocol"
	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/auditmutation"
)

// TxBeginner opens the transaction an audit row is written in; a
// *pgxpool.Pool satisfies it.
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// NewEnrollmentAudit returns the EnrollmentAudit that writes the domain row of
// a first enrollment in a transaction of its own: action
// direct_connection.enrolled, target the device ID, changes the device ID and
// the node key. The row never holds a ticket. A request that records no domain
// rows writes nothing.
func NewEnrollmentAudit(db TxBeginner) EnrollmentAudit {
	return func(ctx context.Context, deviceID string, nodeKey protocol.NodeKey) error {
		if !auditmutation.Recording(ctx) {
			return nil
		}
		tx, err := db.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin enrollment audit: %w", err)
		}
		defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // rollback after commit is a no-op
		changes := auditmutation.Changes(nil, map[string]any{"device_id": deviceID, "node_key": nodeKey.String()})
		entry, err := auditmutation.RecordMutation(ctx, tx, auditmutation.ActionDirectConnectionEnrolled, auditmutation.TargetDirectConnection, deviceID, http.StatusOK, changes)
		if err != nil {
			return fmt.Errorf("record enrollment audit: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit enrollment audit: %w", err)
		}
		auditmutation.CommitMutation(ctx, entry)
		return nil
	}
}
