package notifications

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/oklog/ulid/v2"
)

const (
	PushTriggerDelivery = "delivery"
	PushTriggerTest     = "test"

	PushOutcomePending   = "pending"
	PushOutcomeDelivered = "delivered"
	PushOutcomeRetrying  = "retrying"
	PushOutcomeFailed    = "failed"
)

var (
	ErrPushDeliveryUnavailable = errors.New("apple push delivery unavailable")
	ErrPushDeliveryInvalid     = errors.New("invalid apple push delivery request")
	ErrPushDeliveryNotFound    = errors.New("apple push device not found")
)

// PushDeliveryAttempt is one row in the APNs relay outbox/retry log.
type PushDeliveryAttempt struct {
	ID                     string
	NotificationDeliveryID *string
	PushDeviceID           string
	TriggerType            string
	Provider               string
	Platform               string
	AttemptNumber          int
	AttemptedAt            time.Time
	NextRetryAt            *time.Time
	Outcome                string
	RelayRequestID         *string
	UpstreamStatus         *int
	UpstreamReason         *string
	FailureMessage         *string
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// newPushDeliveryAttempts builds the pending outbox rows fanning one delivery
// out to its profile's eligible devices. Shared by the fanout worker and
// operational dispatch so both outbox paths enqueue identical rows.
func newPushDeliveryAttempts(deliveryID string, devices []PushDevice) []PushDeliveryAttempt {
	attempts := make([]PushDeliveryAttempt, 0, len(devices))
	for _, device := range devices {
		attempts = append(attempts, PushDeliveryAttempt{
			ID:                     ulid.Make().String(),
			NotificationDeliveryID: &deliveryID,
			PushDeviceID:           device.ID,
			TriggerType:            PushTriggerDelivery,
			Provider:               device.Provider,
			Platform:               device.Platform,
		})
	}
	return attempts
}

const pushAttemptReturning = `
		RETURNING id, notification_delivery_id, push_device_id, trigger_type, provider, platform,
		          attempt_number, attempted_at, next_retry_at, outcome, relay_request_id,
		          upstream_status, upstream_reason, failure_message, created_at, updated_at`

func scanPushDeliveryAttempts(rows pgx.Rows) ([]PushDeliveryAttempt, error) {
	defer rows.Close()
	attempts := make([]PushDeliveryAttempt, 0, 8)
	for rows.Next() {
		var attempt PushDeliveryAttempt
		if err := rows.Scan(
			&attempt.ID,
			&attempt.NotificationDeliveryID,
			&attempt.PushDeviceID,
			&attempt.TriggerType,
			&attempt.Provider,
			&attempt.Platform,
			&attempt.AttemptNumber,
			&attempt.AttemptedAt,
			&attempt.NextRetryAt,
			&attempt.Outcome,
			&attempt.RelayRequestID,
			&attempt.UpstreamStatus,
			&attempt.UpstreamReason,
			&attempt.FailureMessage,
			&attempt.CreatedAt,
			&attempt.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan push attempt: %w", err)
		}
		attempts = append(attempts, attempt)
	}
	return attempts, rows.Err()
}

// ListEnabledPushByProfiles loads delivery-eligible push devices for the
// admin-enabled platforms, keyed by profile.
func (r *PushDeviceRepository) ListEnabledPushByProfiles(ctx context.Context, tx pgx.Tx, profileIDs, platforms []string) (map[string][]PushDevice, error) {
	out := make(map[string][]PushDevice, len(profileIDs))
	if len(profileIDs) == 0 || len(platforms) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT `+pushDeviceColumns+`
		FROM push_devices
		WHERE profile_id = ANY($1)
		  AND platform = ANY($2)
		  AND provider = $3
		  AND push_mode = $4
		  AND enabled`,
		profileIDs, platforms, PushProviderSiloRelay, PushModePrivatePush)
	if err != nil {
		return nil, fmt.Errorf("list enabled push devices: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		device, err := scanPushDevice(rows)
		if err != nil {
			return nil, fmt.Errorf("scan enabled push device: %w", err)
		}
		out[device.ProfileID] = append(out[device.ProfileID], *device)
	}
	return out, rows.Err()
}

// EnqueuePushAttempts inserts pending APNs relay attempts in the fanout transaction.
func (r *PushDeviceRepository) EnqueuePushAttempts(ctx context.Context, tx pgx.Tx, attempts []PushDeliveryAttempt) error {
	if len(attempts) == 0 {
		return nil
	}
	var sb strings.Builder
	sb.WriteString(`
		INSERT INTO push_delivery_attempts
			(id, notification_delivery_id, push_device_id, trigger_type, provider, platform, attempt_number, outcome)
		VALUES `)
	args := make([]any, 0, len(attempts)*8)
	for i, attempt := range attempts {
		if i > 0 {
			sb.WriteString(", ")
		}
		base := len(args)
		sb.WriteString(fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8))
		args = append(args,
			attempt.ID,
			attempt.NotificationDeliveryID,
			attempt.PushDeviceID,
			defaultString(attempt.TriggerType, PushTriggerDelivery),
			defaultString(attempt.Provider, PushProviderSiloRelay),
			defaultString(attempt.Platform, PushPlatformApple),
			0,
			PushOutcomePending,
		)
	}
	sb.WriteString(" ON CONFLICT DO NOTHING")
	if _, err := tx.Exec(ctx, sb.String(), args...); err != nil {
		return fmt.Errorf("enqueue push attempts: %w", err)
	}
	return nil
}

// EnqueueTestAttempt creates a pending diagnostic attempt for one enabled
// device on the given platform.
func (r *PushDeviceRepository) EnqueueTestAttempt(ctx context.Context, platform, profileID, serverDeviceID string) (*PushDeliveryAttempt, *PushDevice, error) {
	if r == nil || r.pool == nil {
		return nil, nil, ErrPushDeliveryUnavailable
	}
	profileID = strings.TrimSpace(profileID)
	serverDeviceID = strings.TrimSpace(serverDeviceID)
	if profileID == "" {
		return nil, nil, fmt.Errorf("%w: profile_id is required", ErrPushDeliveryInvalid)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("begin push test attempt: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	query := `SELECT ` + pushDeviceColumns + `
		FROM push_devices
		WHERE profile_id = $1
		  AND platform = $2
		  AND provider = $3
		  AND push_mode = $4
		  AND enabled`
	args := []any{profileID, platform, PushProviderSiloRelay, PushModePrivatePush}
	if serverDeviceID != "" {
		args = append(args, serverDeviceID)
		query += fmt.Sprintf(" AND server_device_id = $%d", len(args))
	}
	query += ` ORDER BY last_seen_at DESC NULLS LAST, created_at DESC LIMIT 1 FOR UPDATE`

	device, err := scanPushDevice(tx.QueryRow(ctx, query, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrPushDeliveryNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("select push test device: %w", err)
	}

	attemptID := ulid.Make().String()
	rows, err := tx.Query(ctx, `
		INSERT INTO push_delivery_attempts
			(id, notification_delivery_id, push_device_id, trigger_type, provider, platform, attempt_number, outcome)
		VALUES ($1, NULL, $2, $3, $4, $5, 0, $6)`+pushAttemptReturning,
		attemptID, device.ID, PushTriggerTest, PushProviderSiloRelay, platform, PushOutcomePending)
	if err != nil {
		return nil, nil, fmt.Errorf("insert push test attempt: %w", err)
	}
	attempts, err := scanPushDeliveryAttempts(rows)
	if err != nil {
		return nil, nil, err
	}
	if len(attempts) != 1 {
		return nil, nil, fmt.Errorf("insert push test attempt returned %d rows", len(attempts))
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, fmt.Errorf("commit push test attempt: %w", err)
	}
	return &attempts[0], device, nil
}

func (r *PushDeviceRepository) GetPushAttempt(ctx context.Context, id string) (*PushDeliveryAttempt, error) {
	rows, err := r.pool.Query(ctx, `SELECT * FROM (
		SELECT id, notification_delivery_id, push_device_id, trigger_type, provider, platform,
		       attempt_number, attempted_at, next_retry_at, outcome, relay_request_id,
		       upstream_status, upstream_reason, failure_message, created_at, updated_at
		FROM push_delivery_attempts
		WHERE id = $1
	) attempt`, id)
	if err != nil {
		return nil, fmt.Errorf("get push attempt: %w", err)
	}
	attempts, err := scanPushDeliveryAttempts(rows)
	if err != nil {
		return nil, err
	}
	if len(attempts) == 0 {
		return nil, nil
	}
	return &attempts[0], nil
}

func (r *PushDeviceRepository) getPushDeviceByID(ctx context.Context, id string) (*PushDevice, error) {
	device, err := scanPushDevice(r.pool.QueryRow(ctx,
		`SELECT `+pushDeviceColumns+` FROM push_devices WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get push device: %w", err)
	}
	return device, nil
}

func (r *PushDeviceRepository) ClaimPendingPushForDelivery(ctx context.Context, deliveryID string) ([]PushDeliveryAttempt, error) {
	return r.claimPushAttempts(ctx, `
		UPDATE push_delivery_attempts SET outcome = 'retrying', next_retry_at = now() + $2, updated_at = now()
		WHERE id IN (
			SELECT id FROM push_delivery_attempts
			WHERE notification_delivery_id = $1 AND outcome = 'pending'
			FOR UPDATE SKIP LOCKED
		)`+pushAttemptReturning,
		deliveryID, webhookClaimLease)
}

func (r *PushDeviceRepository) ClaimPushAttemptByID(ctx context.Context, attemptID string) ([]PushDeliveryAttempt, error) {
	return r.claimPushAttempts(ctx, `
		UPDATE push_delivery_attempts SET outcome = 'retrying', next_retry_at = now() + $2, updated_at = now()
		WHERE id IN (
			SELECT id FROM push_delivery_attempts
			WHERE id = $1 AND outcome = 'pending'
			FOR UPDATE SKIP LOCKED
		)`+pushAttemptReturning,
		attemptID, webhookClaimLease)
}

func (r *PushDeviceRepository) ClaimDuePushAttempts(ctx context.Context, limit int) ([]PushDeliveryAttempt, error) {
	return r.claimPushAttempts(ctx, `
		UPDATE push_delivery_attempts SET outcome = 'retrying', next_retry_at = now() + $2, updated_at = now()
		WHERE id IN (
			SELECT id FROM push_delivery_attempts
			WHERE (outcome = 'retrying' AND next_retry_at <= now())
			   OR (outcome = 'pending' AND attempted_at <= now() - interval '60 seconds')
			ORDER BY next_retry_at NULLS FIRST
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)`+pushAttemptReturning,
		limit, webhookClaimLease)
}

func (r *PushDeviceRepository) claimPushAttempts(ctx context.Context, query string, args ...any) ([]PushDeliveryAttempt, error) {
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("claim push attempts: %w", err)
	}
	return scanPushDeliveryAttempts(rows)
}

func (r *PushDeviceRepository) FinalizePushAttempt(ctx context.Context, attemptID, outcome string, attemptNumber int, relayRequestID string, upstreamStatus *int, upstreamReason, failureMessage string, nextRetryAt *time.Time) (*PushDeliveryAttempt, error) {
	var relayRequestIDPtr *string
	if relayRequestID != "" {
		relayRequestIDPtr = &relayRequestID
	}
	var upstreamReasonPtr *string
	if upstreamReason != "" {
		upstreamReasonPtr = &upstreamReason
	}
	var failureMessagePtr *string
	if failureMessage != "" {
		failureMessagePtr = &failureMessage
	}
	rows, err := r.pool.Query(ctx, `
		UPDATE push_delivery_attempts
		SET outcome = $2,
		    attempt_number = $3,
		    attempted_at = now(),
		    next_retry_at = $4,
		    relay_request_id = $5,
		    upstream_status = $6,
		    upstream_reason = left($7, 256),
		    failure_message = left($8, 256),
		    updated_at = now()
		WHERE id = $1`+pushAttemptReturning,
		attemptID, outcome, attemptNumber, nextRetryAt, relayRequestIDPtr, upstreamStatus, upstreamReasonPtr, failureMessagePtr)
	if err != nil {
		return nil, fmt.Errorf("finalize push attempt: %w", err)
	}
	attempts, err := scanPushDeliveryAttempts(rows)
	if err != nil {
		return nil, err
	}
	if len(attempts) == 0 {
		return nil, nil
	}
	return &attempts[0], nil
}

func (r *PushDeviceRepository) RecordPushSuccess(ctx context.Context, deviceID string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE push_devices
		SET last_success_at = now(), last_failure_at = NULL, last_failure_code = NULL, updated_at = now()
		WHERE id = $1`, deviceID)
	return err
}

func (r *PushDeviceRepository) RecordPushFailure(ctx context.Context, deviceID, code string, disable bool) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE push_devices
		SET last_failure_at = now(),
		    last_failure_code = left($2, 128),
		    enabled = CASE WHEN $3 THEN false ELSE enabled END,
		    updated_at = now()
		WHERE id = $1`, deviceID, code, disable)
	return err
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
