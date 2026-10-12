package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EventDelivery is one event waiting to reach one installation's
// event_consumer.v1 capability: a plugin_event_deliveries row.
type EventDelivery struct {
	ID             int64
	EventID        string
	EventName      string
	Payload        json.RawMessage
	InstallationID int
	CapabilityID   string
	// Attempts counts claims, including the one that returned the row.
	Attempts int
}

// EventDeliveryStore owns plugin_event_deliveries, the outbox the
// EventDispatcher delivers from. Every API server claims from it, so each row
// is delivered by one server at a time.
type EventDeliveryStore struct {
	pool *pgxpool.Pool
}

func NewEventDeliveryStore(pool *pgxpool.Pool) *EventDeliveryStore {
	return &EventDeliveryStore{pool: pool}
}

// Enqueue inserts deliveries, due at once. A delivery for an installation
// uninstalled since the caller listed its subscribers is skipped rather than
// failing the others' insert. FOR KEY SHARE holds each installation row until
// the insert commits, so an uninstall cannot remove it between the join and
// the foreign-key check.
func (s *EventDeliveryStore) Enqueue(ctx context.Context, deliveries []EventDelivery) error {
	if len(deliveries) == 0 {
		return nil
	}
	var sb strings.Builder
	sb.WriteString(`INSERT INTO plugin_event_deliveries
		(event_id, event_name, payload, plugin_installation_id, capability_id)
		SELECT v.event_id, v.event_name, v.payload, v.installation_id, v.capability_id
		FROM (VALUES `)
	args := make([]any, 0, len(deliveries)*5)
	for i, delivery := range deliveries {
		if i > 0 {
			sb.WriteString(", ")
		}
		base := len(args)
		fmt.Fprintf(&sb, "($%d::text, $%d::text, $%d::json, $%d::bigint, $%d::text)", base+1, base+2, base+3, base+4, base+5)
		var payload any
		if len(delivery.Payload) > 0 {
			payload = string(delivery.Payload)
		}
		args = append(args, delivery.EventID, delivery.EventName, payload, delivery.InstallationID, delivery.CapabilityID)
	}
	sb.WriteString(`) AS v(event_id, event_name, payload, installation_id, capability_id)
		JOIN plugin_installations i ON i.id = v.installation_id
		FOR KEY SHARE OF i`)
	if _, err := s.pool.Exec(ctx, sb.String(), args...); err != nil {
		return fmt.Errorf("enqueue plugin event deliveries: %w", err)
	}
	return nil
}

// Claim takes up to limit due deliveries, counts the attempt, and hides each
// from other claimers for lease. A claimer that dies mid-delivery leaves the
// row to be claimed again once the lease runs out.
//
// The MATERIALIZED CTE runs the locking subquery once; as a plain
// `id IN (SELECT ... LIMIT n FOR UPDATE SKIP LOCKED)` the planner may run it
// more than once and claim more than limit rows.
func (s *EventDeliveryStore) Claim(ctx context.Context, limit int, lease time.Duration) ([]EventDelivery, error) {
	rows, err := s.pool.Query(ctx, `
		WITH due AS MATERIALIZED (
			SELECT id FROM plugin_event_deliveries
			WHERE next_attempt_at <= now()
			ORDER BY next_attempt_at, id
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE plugin_event_deliveries d
		SET attempts = d.attempts + 1,
		    next_attempt_at = now() + $2::interval
		FROM due
		WHERE d.id = due.id
		RETURNING d.id, d.event_id, d.event_name, d.payload, d.plugin_installation_id, d.capability_id, d.attempts`,
		limit, lease)
	if err != nil {
		return nil, fmt.Errorf("claim plugin event deliveries: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (EventDelivery, error) {
		var delivery EventDelivery
		var payload []byte
		err := row.Scan(&delivery.ID, &delivery.EventID, &delivery.EventName, &payload,
			&delivery.InstallationID, &delivery.CapabilityID, &delivery.Attempts)
		delivery.Payload = payload
		return delivery, err
	})
}

// Delete removes a delivery that succeeded or will not be retried. attempts
// is the count Claim returned: a claimer whose lease ran out and whose row
// another server claimed since leaves the row to that server.
func (s *EventDeliveryStore) Delete(ctx context.Context, id int64, attempts int) error {
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM plugin_event_deliveries WHERE id = $1 AND attempts = $2`, id, attempts); err != nil {
		return fmt.Errorf("delete plugin event delivery: %w", err)
	}
	return nil
}

// Retry makes a failed delivery due again after delay. attempts guards it
// the way it guards Delete.
func (s *EventDeliveryStore) Retry(ctx context.Context, id int64, attempts int, delay time.Duration, lastError string) error {
	if _, err := s.pool.Exec(ctx, `
		UPDATE plugin_event_deliveries
		SET next_attempt_at = now() + $3::interval, last_error = left($4, 512)
		WHERE id = $1 AND attempts = $2`, id, attempts, delay, lastError); err != nil {
		return fmt.Errorf("reschedule plugin event delivery: %w", err)
	}
	return nil
}
