package auth

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

// revokeCredentialsIssuedToNonAdmin deletes the API keys, reset and sign-in
// links of an account being made an admin or the Owner, revokes its app keys,
// and withdraws the device sign-ins it approved in advance that no device has
// collected. Other admins may have minted them and kept them; afterwards they
// would carry authority that only the Owner grants, and a device collecting
// such a sign-in would open an administrator's session. An admin's app keys
// also need the admin's own approval, which keys enrolled before were never
// given. The withdrawal marks those requests denied, as the person's own
// withdrawal does, after the sessions (session rows before request rows),
// with the request rows locked in id order. It is a direct write, not a
// side write of ending a session. The sign-in links and their codes go last;
// their rows reference no session or request.
func revokeCredentialsIssuedToNonAdmin(ctx context.Context, db interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, id int) error {
	if _, err := db.Exec(ctx, `DELETE FROM api_keys WHERE user_id = $1`, id); err != nil {
		return fmt.Errorf("revoking the promoted account's API keys: %w", err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM password_reset_tokens WHERE user_id = $1`, id); err != nil {
		return fmt.Errorf("deleting the promoted account's reset links: %w", err)
	}
	if err := revokeAccountKeysTx(ctx, db, id); err != nil {
		return err
	}
	if _, err := db.Exec(ctx, `SELECT id FROM device_login_requests
		WHERE approved_by_user_id = $1 AND preapproved_at IS NOT NULL AND status = $2 ORDER BY id FOR UPDATE`,
		id, DeviceLoginStatusApproved); err != nil {
		return fmt.Errorf("locking the promoted account's device sign-ins approved in advance: %w", err)
	}
	if _, err := db.Exec(ctx, `UPDATE device_login_requests SET status = $2, denied_at = NOW(), updated_at = NOW()
		WHERE approved_by_user_id = $1 AND preapproved_at IS NOT NULL AND status = $3`,
		id, DeviceLoginStatusDenied, DeviceLoginStatusApproved); err != nil {
		return fmt.Errorf("withdrawing the promoted account's device sign-ins approved in advance: %w", err)
	}
	return EndSignInLinksTx(ctx, db, id)
}
