package requests

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
)

// unboundTargetError is the last error a live target keeps when its server is
// deleted while no plugin installation backs it.
const unboundTargetError = "request server deleted while its plugin was not installed"

// RebindOrphanedIntegrations points every server whose plugin installation no
// longer exists at the current installation of the same plugin, when that
// installation provides the server's request router capability. Uninstalling
// a plugin and installing it again gives it a new installation id, and
// request_integrations.installation_id has no foreign key, so without this
// the servers keep the old id: the editor finds no settings, status checks
// fail, and their live requests never settle.
//
// Servers follow their own plugin_id only: another plugin offering the same
// capability never receives a server, or its API key. The rebind advances the
// row's revision, so an editor opened on the old id saves as stale. It is
// idempotent and returns the ids of the servers it rebound.
func (r *Repository) RebindOrphanedIntegrations(ctx context.Context) ([]string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin rebind request integrations: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockRoutingMode(ctx, tx); err != nil {
		return nil, err
	}
	rebound, err := rebindOrphanedIntegrations(ctx, tx, nil)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit rebind request integrations: %w", err)
	}
	return rebound, nil
}

// rebindOrphanedIntegrations runs the rebind for every server, or for the one
// server id names. The caller holds the routing mode lock.
func rebindOrphanedIntegrations(ctx context.Context, tx pgx.Tx, id *string) ([]string, error) {
	// Lock the orphaned servers in id order first, as a route save does with
	// FOR SHARE, so rebinding several servers cannot deadlock with it. The
	// update then touches only the servers locked here.
	rows, err := tx.Query(ctx, `
		SELECT i.id FROM request_integrations i
		WHERE i.plugin_id <> '' AND i.installation_id IS NOT NULL
			AND ($1::text IS NULL OR i.id = $1)
			AND NOT EXISTS (SELECT 1 FROM plugin_installations p WHERE p.id = i.installation_id)
		ORDER BY i.id
		FOR UPDATE`, id)
	if err != nil {
		return nil, fmt.Errorf("lock orphaned request integrations: %w", err)
	}
	orphaned, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("lock orphaned request integrations: %w", err)
	}
	if len(orphaned) == 0 {
		return nil, nil
	}
	rows, err = tx.Query(ctx, `
		WITH candidates AS (
			SELECT i.id, min(p.id) AS installation_id, count(DISTINCT p.id) AS matches
			FROM request_integrations i
			JOIN plugin_installations p ON p.plugin_id = i.plugin_id
			JOIN plugin_capabilities pc
				ON pc.plugin_installation_id = p.id
				AND pc.capability_type = 'request_router.v1'
				AND pc.capability_id = i.capability_id
			WHERE i.id = ANY($1)
				AND NOT EXISTS (SELECT 1 FROM plugin_installations p WHERE p.id = i.installation_id)
			GROUP BY i.id
		)
		UPDATE request_integrations i
		SET installation_id = c.installation_id, updated_at = now()
		FROM candidates c
		WHERE i.id = c.id AND c.matches = 1
		RETURNING i.id`, orphaned)
	if err != nil {
		return nil, fmt.Errorf("rebind request integrations: %w", err)
	}
	rebound, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("rebind request integrations: %w", err)
	}
	return rebound, nil
}

// failUnboundTargets fails the queued and downloading targets of a server no
// plugin installation backs, so deleting it does not leave requests that no
// plugin will ever report on. A bound server's live targets are left alone:
// its plugin still tracks them, and the delete refuses. The caller holds the
// routing mode lock and the server's row lock, and rebinds the server first.
func (r *Repository) failUnboundTargets(ctx context.Context, tx pgx.Tx, id string) error {
	var unbound bool
	if err := tx.QueryRow(ctx, `
		SELECT NOT EXISTS (
			SELECT 1 FROM request_integrations i
			JOIN plugin_installations p ON p.id = i.installation_id
			WHERE i.id = $1
		)`, id).Scan(&unbound); err != nil {
		return fmt.Errorf("check request integration installation: %w", err)
	}
	if !unbound {
		return nil
	}
	// Targets before their requests, the order UpdateTargetStatus takes them
	// in, which the stalled-target sweep still runs on these targets.
	rows, err := tx.Query(ctx, `
		UPDATE media_request_targets
		SET status = 'failed', last_error = $2, updated_at = now(), `+clearTargetDownload+`
		WHERE integration_id = $1 AND status IN ('queued', 'downloading')
		RETURNING request_id`, id, unboundTargetError)
	if err != nil {
		return fmt.Errorf("fail unbound integration targets: %w", err)
	}
	requestIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("fail unbound integration targets: %w", err)
	}
	slices.Sort(requestIDs)
	for _, requestID := range slices.Compact(requestIDs) {
		if _, err := r.recomputeAggregate(ctx, tx, requestID, Viewer{}); err != nil {
			return err
		}
	}
	return nil
}
