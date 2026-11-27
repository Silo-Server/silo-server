package auth

import (
	"context"
	"fmt"
)

// revokeCredentialsIssuedToNonAdmin ends, when an account becomes an
// administrator or the Owner, everything it set going while it was ordinary
// that could still produce a session or a key: nothing it planted may sign
// anyone in as an administrator. That is its API keys; its password reset
// links; its app keys (the person key, device keys, waiting device keys) and
// the sessions they opened; every device sign-in it approved, in advance or
// at the moment, that no device has collected; its sign-in links with their
// codes and set-password tokens; and its unredeemed app codes of every kind.
// Other admins may have minted some of them and kept them; afterwards they
// would carry authority that only the Owner grants. An admin's app keys also
// need the admin's own approval, which keys enrolled before were never
// given. Order: session rows (the key sessions), then request rows (the
// withdrawal every sign-out-everywhere uses, in id order), then link and code
// rows, which reference no session or request. The withdrawal and the code
// deletion are direct writes, not side writes of ending a session. Add any
// new kind of pending credential here and to the list in
// docs/architecture/app-keys.md.
func revokeCredentialsIssuedToNonAdmin(ctx context.Context, db sessionExecQuerier, id int) error {
	if _, err := db.Exec(ctx, `DELETE FROM api_keys WHERE user_id = $1`, id); err != nil {
		return fmt.Errorf("revoking the promoted account's API keys: %w", err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM password_reset_tokens WHERE user_id = $1`, id); err != nil {
		return fmt.Errorf("deleting the promoted account's reset links: %w", err)
	}
	if err := revokeAccountKeysTx(ctx, db, id); err != nil {
		return err
	}
	if _, err := withdrawDeviceSignInApprovalsInTransaction(ctx, db, []int{id}); err != nil {
		return err
	}
	if err := EndSignInLinksTx(ctx, db, id); err != nil {
		return err
	}
	if _, err := db.Exec(ctx, `DELETE FROM auth_app_codes WHERE user_id = $1 AND redeemed_at IS NULL`, id); err != nil {
		return fmt.Errorf("deleting the promoted account's app codes: %w", err)
	}
	return nil
}
