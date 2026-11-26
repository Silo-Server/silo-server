package auth

import (
	"context"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/models"
)

// Audit fields of the operations that add a way in to an account. Each names
// a key by its server ID (a device key) or its public key ID (a person key),
// an app code by its kind and expiry, and an approval in advance by its
// request ID and device name. None carries a code, a hash, a secret or key
// material: the request's address is the row's client_ip, and the key row
// keeps its own copy (enrolled_ip_address).
const (
	auditFieldDeviceKeyStatus    = "device_key_status"
	auditFieldAppCodeKind        = "app_code_kind"
	auditFieldExpiresAt          = "expires_at"
	auditFieldDeviceName         = "device_name"
	auditFieldDeviceLoginRequest = "device_login_request_id"
	// auditFieldSessionOpenedVia is how the session that made the change
	// was opened (auth_sessions.opened_via): whether a device key stands
	// behind it (app_key) or a password or provider sign-in.
	auditFieldSessionOpenedVia = "session_opened_via"
)

// keyAdded are the values a key-adding audit row records, all on the after
// side: what came into existence.
type keyAdded map[string]any

// withDevice adds the device key a call enrolled.
func (v keyAdded) withDevice(key *models.AppDeviceKey) keyAdded {
	v[auditFieldDeviceKey] = key.ID
	return v
}

// withPerson adds the person key a call enrolled, when it enrolled one.
func (v keyAdded) withPerson(person *models.AppPersonKey, created bool) keyAdded {
	if created {
		v[auditFieldPersonKey] = person.KeyID
	}
	return v
}

// recordKeyAdded records action on userID's account in tx, the transaction
// that made the change, so a rollback leaves no row. The caller commits the
// entry (auditmutation.CommitMutation) after tx commits. status is the
// operation's success status.
func recordKeyAdded(ctx context.Context, tx pgx.Tx, action string, userID, status int, values keyAdded) (*auditmutation.Entry, error) {
	return auditmutation.RecordMutation(ctx, tx, action, auditmutation.TargetUser, strconv.Itoa(userID), status,
		auditmutation.Changes(nil, values))
}

// auditInstant is how an audit row spells a time.
func auditInstant(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}
