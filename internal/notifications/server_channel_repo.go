package notifications

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ServerChannelRepository owns notification_server_channels.
type ServerChannelRepository struct {
	pool *pgxpool.Pool
}

// NewServerChannelRepository creates a ServerChannelRepository.
func NewServerChannelRepository(pool *pgxpool.Pool) *ServerChannelRepository {
	return &ServerChannelRepository{pool: pool}
}

const serverChannelColumns = `
	id, name, type, url_ciphertext, url_host, signing_secret_ciphertext, enabled,
	notify_new_movies, notify_new_episodes,
	notify_new_audiobooks, notify_new_ebooks,
	notify_request_submitted, notify_request_approved,
	notify_request_declined, notify_request_fulfilled,
	watermark_created_at, watermark_id,
	last_attempt_at, consecutive_failures, disabled_reason,
	last_success_at, last_failure_at, last_failure_status, last_failure_message,
	created_by_user_id, created_at, updated_at`

func scanServerChannel(row pgx.Row) (*ServerChannel, error) {
	var ch ServerChannel
	err := row.Scan(
		&ch.ID, &ch.Name, &ch.Type, &ch.URLCiphertext, &ch.URLHost,
		&ch.SigningSecretCiphertext, &ch.Enabled,
		&ch.NotifyNewMovies, &ch.NotifyNewEpisodes,
		&ch.NotifyNewAudiobooks, &ch.NotifyNewEbooks,
		&ch.NotifyRequestSubmitted, &ch.NotifyRequestApproved,
		&ch.NotifyRequestDeclined, &ch.NotifyRequestFulfilled,
		&ch.WatermarkCreatedAt, &ch.WatermarkID,
		&ch.LastAttemptAt, &ch.ConsecutiveFailures, &ch.DisabledReason,
		&ch.LastSuccessAt, &ch.LastFailureAt, &ch.LastFailureStatus, &ch.LastFailureMessage,
		&ch.CreatedByUserID, &ch.CreatedAt, &ch.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &ch, nil
}

func scanServerChannels(rows pgx.Rows) ([]ServerChannel, error) {
	defer rows.Close()
	channels := make([]ServerChannel, 0, 4)
	for rows.Next() {
		ch, err := scanServerChannel(rows)
		if err != nil {
			return nil, fmt.Errorf("scan server channel: %w", err)
		}
		channels = append(channels, *ch)
	}
	return channels, rows.Err()
}

// List returns every server channel for the admin listing endpoint.
func (r *ServerChannelRepository) List(ctx context.Context) ([]ServerChannel, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+serverChannelColumns+` FROM notification_server_channels ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list server channels: %w", err)
	}
	return scanServerChannels(rows)
}

// GetByID returns one server channel; (nil, nil) when absent.
func (r *ServerChannelRepository) GetByID(ctx context.Context, id string) (*ServerChannel, error) {
	ch, err := scanServerChannel(r.pool.QueryRow(ctx,
		`SELECT `+serverChannelColumns+` FROM notification_server_channels WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get server channel: %w", err)
	}
	return ch, nil
}

// ErrServerChannelNameTaken is returned when a channel with the requested
// name already exists.
var ErrServerChannelNameTaken = errors.New("a server channel with this name already exists")

func isServerChannelNameViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		pgErr.Code == "23505" &&
		pgErr.ConstraintName == "notification_server_channels_name_key"
}

// InsertWithLimit persists a new channel unless the server is already at
// maxChannels. The count and insert run under an advisory transaction lock so
// concurrent creates cannot both pass the check.
func (r *ServerChannelRepository) InsertWithLimit(ctx context.Context, ch ServerChannel, maxChannels int) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin server channel insert: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('notification_server_channels', 0))`); err != nil {
		return fmt.Errorf("lock server channel quota: %w", err)
	}
	var count int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM notification_server_channels`).Scan(&count); err != nil {
		return fmt.Errorf("count server channels: %w", err)
	}
	if count >= maxChannels {
		return ErrServerChannelLimit
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO notification_server_channels
			(id, name, type, url_ciphertext, url_host, signing_secret_ciphertext, enabled,
			 notify_new_movies, notify_new_episodes,
			 notify_new_audiobooks, notify_new_ebooks,
			 notify_request_submitted, notify_request_approved,
			 notify_request_declined, notify_request_fulfilled,
			 created_by_user_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		ch.ID, ch.Name, ch.Type, ch.URLCiphertext, ch.URLHost, ch.SigningSecretCiphertext, ch.Enabled,
		ch.NotifyNewMovies, ch.NotifyNewEpisodes,
		ch.NotifyNewAudiobooks, ch.NotifyNewEbooks,
		ch.NotifyRequestSubmitted, ch.NotifyRequestApproved,
		ch.NotifyRequestDeclined, ch.NotifyRequestFulfilled,
		ch.CreatedByUserID); err != nil {
		if isServerChannelNameViolation(err) {
			return ErrServerChannelNameTaken
		}
		return fmt.Errorf("insert server channel: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit server channel insert: %w", err)
	}
	return nil
}

// Update persists the admin-mutable fields (name, URL, secret, enabled state,
// event toggles) and bumps updated_at. Watermark and failure bookkeeping are
// owned by the sweep/send paths; the service resets them explicitly through
// ResetDispatchState on enable transitions.
func (r *ServerChannelRepository) Update(ctx context.Context, ch ServerChannel) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE notification_server_channels SET
			name = $2, url_ciphertext = $3, url_host = $4,
			signing_secret_ciphertext = $5, enabled = $6,
			notify_new_movies = $7, notify_new_episodes = $8,
			notify_new_audiobooks = $9, notify_new_ebooks = $10,
			notify_request_submitted = $11, notify_request_approved = $12,
			notify_request_declined = $13, notify_request_fulfilled = $14,
			updated_at = now()
		WHERE id = $1`,
		ch.ID, ch.Name, ch.URLCiphertext, ch.URLHost,
		ch.SigningSecretCiphertext, ch.Enabled,
		ch.NotifyNewMovies, ch.NotifyNewEpisodes,
		ch.NotifyNewAudiobooks, ch.NotifyNewEbooks,
		ch.NotifyRequestSubmitted, ch.NotifyRequestApproved,
		ch.NotifyRequestDeclined, ch.NotifyRequestFulfilled)
	if err != nil {
		if isServerChannelNameViolation(err) {
			return ErrServerChannelNameTaken
		}
		return fmt.Errorf("update server channel: %w", err)
	}
	return nil
}

// ResetDispatchState clears failure backoff/auto-disable state and
// fast-forwards the content watermark to now. Called when a channel is
// (re-)enabled or its URL is replaced: a channel that was dead for days must
// resume from the present, not replay the gap.
func (r *ServerChannelRepository) ResetDispatchState(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE notification_server_channels SET
			consecutive_failures = 0, disabled_reason = NULL, last_attempt_at = NULL,
			watermark_created_at = now(), watermark_id = '',
			updated_at = now()
		WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("reset server channel dispatch state: %w", err)
	}
	return nil
}

// Delete removes a server channel. Idempotent.
func (r *ServerChannelRepository) Delete(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM notification_server_channels WHERE id = $1`, id)
	return err
}

// ListEnabledForContent returns enabled, non-auto-disabled channels with at
// least one content kind toggled on, for the sweep pre-scan.
func (r *ServerChannelRepository) ListEnabledForContent(ctx context.Context) ([]ServerChannel, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+serverChannelColumns+`
		FROM notification_server_channels
		WHERE enabled AND disabled_reason IS NULL
		  AND (notify_new_movies OR notify_new_episodes
		       OR notify_new_audiobooks OR notify_new_ebooks)
		ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list content server channels: %w", err)
	}
	return scanServerChannels(rows)
}

// ListEnabledForRequests returns enabled, non-auto-disabled channels with at
// least one request lifecycle toggle on; the caller filters per event.
func (r *ServerChannelRepository) ListEnabledForRequests(ctx context.Context) ([]ServerChannel, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+serverChannelColumns+`
		FROM notification_server_channels
		WHERE enabled AND disabled_reason IS NULL
		  AND (notify_request_submitted OR notify_request_approved
		       OR notify_request_declined OR notify_request_fulfilled)
		ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list request server channels: %w", err)
	}
	return scanServerChannels(rows)
}

// ClaimForSweep locks one channel row for a sweep attempt with FOR UPDATE
// SKIP LOCKED; (nil, nil) means another node holds it. Must run inside the
// caller's transaction.
func (r *ServerChannelRepository) ClaimForSweep(ctx context.Context, tx pgx.Tx, id string) (*ServerChannel, error) {
	ch, err := scanServerChannel(tx.QueryRow(ctx,
		`SELECT `+serverChannelColumns+`
		 FROM notification_server_channels
		 WHERE id = $1 AND enabled AND disabled_reason IS NULL
		 FOR UPDATE SKIP LOCKED`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim server channel: %w", err)
	}
	return ch, nil
}

// MarkSwept advances the content watermark past everything the sweep covered
// and resets failure backoff. Must run inside the claim transaction.
func (r *ServerChannelRepository) MarkSwept(ctx context.Context, tx pgx.Tx, id string, watermark Cursor) error {
	_, err := tx.Exec(ctx, `
		UPDATE notification_server_channels SET
			watermark_created_at = $2, watermark_id = $3,
			last_attempt_at = now(), last_success_at = now(),
			consecutive_failures = 0,
			updated_at = now()
		WHERE id = $1`,
		id, watermark.CreatedAt, watermark.ID)
	if err != nil {
		return fmt.Errorf("mark server channel swept: %w", err)
	}
	return nil
}

// serverChannelMaxConsecutiveFailures auto-disables a channel after this many
// consecutive failed sends (sweep and request paths share the counter). With
// the sweep's exponential backoff capped at 6h, 20 failures spans multiple
// days of a consistently dead destination.
const serverChannelMaxConsecutiveFailures = 20

// serverChannelAutoDisableReason is the admin-visible auto-disable text.
const serverChannelAutoDisableReason = "Deliveries failed repeatedly; check the destination and re-enable the channel"

// recordFailure increments the failure streak, stores the diagnostic, and
// auto-disables the channel when the streak crosses the threshold. The
// watermark stays put so the next eligible pass retries the same events.
func serverChannelRecordFailure(ctx context.Context, q interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}, id string, httpStatus *int, message string,
) error {
	_, err := q.Exec(ctx, `
		UPDATE notification_server_channels SET
			last_attempt_at = now(), last_failure_at = now(),
			last_failure_status = $2,
			last_failure_message = left($3, 256),
			consecutive_failures = consecutive_failures + 1,
			disabled_reason = CASE
				WHEN consecutive_failures + 1 >= $4 THEN left($5, 256)
				ELSE disabled_reason
			END,
			updated_at = now()
		WHERE id = $1`,
		id, httpStatus, message,
		serverChannelMaxConsecutiveFailures, serverChannelAutoDisableReason)
	if err != nil {
		return fmt.Errorf("record server channel failure: %w", err)
	}
	return nil
}

// MarkSweepFailure records a failed sweep send inside the claim transaction.
func (r *ServerChannelRepository) MarkSweepFailure(ctx context.Context, tx pgx.Tx, id string, httpStatus *int, message string) error {
	return serverChannelRecordFailure(ctx, tx, id, httpStatus, message)
}

// RecordSendSuccess resets the failure streak after a successful request-path
// send (the watermark is content-sweep state and is not touched).
func (r *ServerChannelRepository) RecordSendSuccess(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE notification_server_channels SET
			last_attempt_at = now(), last_success_at = now(),
			consecutive_failures = 0,
			updated_at = now()
		WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("record server channel success: %w", err)
	}
	return nil
}

// RecordSendFailure records a failed request-path send.
func (r *ServerChannelRepository) RecordSendFailure(ctx context.Context, id string, httpStatus *int, message string) error {
	return serverChannelRecordFailure(ctx, r.pool, id, httpStatus, message)
}
