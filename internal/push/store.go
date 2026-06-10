package push

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("push: not found")

// hiddenForChild are categories never pushed to a child profile (mirrors the
// web toast suppression rule).
var hiddenForChild = []string{"request", "system", "admin"}

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// EligibleDevices returns push-eligible devices for a notification addressed to
// userID (profileID empty = all of the user's devices). A device is excluded
// when category is child-hidden and the device's profile is a child.
func (s *Store) EligibleDevices(ctx context.Context, userID int, profileID, category string) ([]Device, error) {
	q := `
		SELECT d.user_id, d.profile_id, d.device_id, d.push_transport, d.push_token
		FROM user_devices d
		JOIN user_profiles p ON p.user_id = d.user_id AND p.id = d.profile_id
		WHERE d.user_id = $1
		  AND d.push_token IS NOT NULL
		  AND d.push_enabled = true
		  AND ($2 = '' OR d.profile_id = $2)
		  AND NOT (p.is_child AND $3 = ANY($4::text[]))`
	rows, err := s.pool.Query(ctx, q, userID, profileID, category, hiddenForChild)
	if err != nil {
		return nil, fmt.Errorf("eligible devices: %w", err)
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var d Device
		var transport, token *string
		if err := rows.Scan(&d.UserID, &d.ProfileID, &d.DeviceID, &transport, &token); err != nil {
			return nil, fmt.Errorf("scan device: %w", err)
		}
		if transport != nil {
			d.Transport = *transport
		}
		if token != nil {
			d.Token = *token
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// EnqueueDelivery writes one pending delivery row.
func (s *Store) EnqueueDelivery(ctx context.Context, notificationID int64, d Device, notBefore time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO push_deliveries (notification_id, user_id, device_id, transport, status, not_before)
		VALUES ($1,$2,$3,$4,'pending',$5)`,
		notificationID, d.UserID, d.DeviceID, d.Transport, notBefore)
	if err != nil {
		return fmt.Errorf("enqueue delivery: %w", err)
	}
	return nil
}

// claimedDelivery bundles a delivery row with the data needed to send it.
type claimedDelivery struct {
	Delivery
	Token   string
	Payload Payload
}

// ClaimDue opens a tx, locks up to limit due deliveries FOR UPDATE SKIP LOCKED,
// joining device token + notification content. The caller records outcomes via
// the Mark*Tx methods on the returned tx, then Commit/Rollback and Release the
// conn.
func (s *Store) ClaimDue(ctx context.Context, now time.Time, limit int) (*pgxpool.Conn, pgx.Tx, []claimedDelivery, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("acquire: %w", err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		conn.Release()
		return nil, nil, nil, fmt.Errorf("begin: %w", err)
	}
	rows, err := tx.Query(ctx, `
		SELECT pd.id, pd.notification_id, pd.user_id, pd.device_id, pd.transport, pd.status, pd.attempts, pd.not_before,
		       d.push_token, n.title, n.body, COALESCE(n.link,''), n.category
		FROM push_deliveries pd
		JOIN user_devices d ON d.user_id = pd.user_id AND d.device_id = pd.device_id
		JOIN notifications n ON n.id = pd.notification_id
		WHERE pd.status IN ('pending','failed') AND pd.not_before <= $1
		ORDER BY pd.not_before
		LIMIT $2
		FOR UPDATE OF pd SKIP LOCKED`, now, limit)
	if err != nil {
		_ = tx.Rollback(ctx)
		conn.Release()
		return nil, nil, nil, fmt.Errorf("claim query: %w", err)
	}
	defer rows.Close()
	var out []claimedDelivery
	for rows.Next() {
		var c claimedDelivery
		var token *string
		if err := rows.Scan(&c.ID, &c.NotificationID, &c.UserID, &c.DeviceID, &c.Transport,
			&c.Status, &c.Attempts, &c.NotBefore,
			&token, &c.Payload.Title, &c.Payload.Body, &c.Payload.Link, &c.Payload.Category); err != nil {
			_ = tx.Rollback(ctx)
			conn.Release()
			return nil, nil, nil, fmt.Errorf("scan claim: %w", err)
		}
		if token != nil {
			c.Token = *token
		}
		c.Payload.NotificationID = c.NotificationID
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		_ = tx.Rollback(ctx)
		conn.Release()
		return nil, nil, nil, err
	}
	return conn, tx, out, nil
}

func (s *Store) MarkSentTx(ctx context.Context, tx pgx.Tx, id int64) error {
	_, err := tx.Exec(ctx, `UPDATE push_deliveries SET status='sent', attempts=attempts+1, updated_at=now() WHERE id=$1`, id)
	return err
}

func (s *Store) MarkSkippedTx(ctx context.Context, tx pgx.Tx, id int64, reason string) error {
	_, err := tx.Exec(ctx, `UPDATE push_deliveries SET status='skipped', last_error=$2, updated_at=now() WHERE id=$1`, id, reason)
	return err
}

func (s *Store) MarkFailedTx(ctx context.Context, tx pgx.Tx, id int64, nextNotBefore time.Time, errMsg string) error {
	_, err := tx.Exec(ctx, `UPDATE push_deliveries SET status='failed', attempts=attempts+1, not_before=$2, last_error=$3, updated_at=now() WHERE id=$1`, id, nextNotBefore, errMsg)
	return err
}

func (s *Store) MarkDeadTx(ctx context.Context, tx pgx.Tx, id int64, errMsg string) error {
	_, err := tx.Exec(ctx, `UPDATE push_deliveries SET status='dead', attempts=attempts+1, last_error=$2, updated_at=now() WHERE id=$1`, id, errMsg)
	return err
}

// PruneTokenTx clears a dead token from the device row (within the claim tx).
func (s *Store) PruneTokenTx(ctx context.Context, tx pgx.Tx, userID int, deviceID string) error {
	_, err := tx.Exec(ctx, `UPDATE user_devices SET push_token=NULL, push_enabled=false WHERE user_id=$1 AND device_id=$2`, userID, deviceID)
	return err
}

// RegisterToken upserts a push token onto the device row.
func (s *Store) RegisterToken(ctx context.Context, userID int, profileID, deviceID, transport, token string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO user_devices (user_id, profile_id, device_id, push_token, push_transport, push_enabled, push_token_at, push_failures)
		VALUES ($1,$2,$3,$4,$5,true,now(),0)
		ON CONFLICT (user_id, profile_id, device_id) DO UPDATE
		SET push_token=$4, push_transport=$5, push_enabled=true, push_token_at=now(), push_failures=0`,
		userID, profileID, deviceID, token, transport)
	if err != nil {
		return fmt.Errorf("register token: %w", err)
	}
	return nil
}

func (s *Store) RevokeToken(ctx context.Context, userID int, profileID, deviceID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE user_devices SET push_token=NULL, push_enabled=false
		WHERE user_id=$1 AND profile_id=$2 AND device_id=$3`, userID, profileID, deviceID)
	return err
}

func (s *Store) SetDeviceEnabled(ctx context.Context, userID int, deviceID string, enabled bool) error {
	tag, err := s.pool.Exec(ctx, `UPDATE user_devices SET push_enabled=$3 WHERE user_id=$1 AND device_id=$2`, userID, deviceID, enabled)
	if err != nil {
		return fmt.Errorf("set device enabled: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

type DeviceInfo struct {
	DeviceID     string     `json:"device_id"`
	Name         string     `json:"name"`
	Platform     string     `json:"platform"`
	Transport    string     `json:"transport"`
	PushEnabled  bool       `json:"push_enabled"`
	RegisteredAt *time.Time `json:"registered_at,omitempty"`
}

func (s *Store) ListDevices(ctx context.Context, userID int) ([]DeviceInfo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT device_id, device_name, device_platform, COALESCE(push_transport,''), push_enabled, push_token_at
		FROM user_devices
		WHERE user_id=$1 AND push_token IS NOT NULL
		ORDER BY last_seen_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	defer rows.Close()
	var out []DeviceInfo
	for rows.Next() {
		var d DeviceInfo
		if err := rows.Scan(&d.DeviceID, &d.Name, &d.Platform, &d.Transport, &d.PushEnabled, &d.RegisteredAt); err != nil {
			return nil, fmt.Errorf("scan device info: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// PurgeTerminal deletes terminal deliveries older than before.
func (s *Store) PurgeTerminal(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM push_deliveries
		WHERE status IN ('sent','skipped','dead') AND updated_at < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("purge terminal: %w", err)
	}
	return tag.RowsAffected(), nil
}
