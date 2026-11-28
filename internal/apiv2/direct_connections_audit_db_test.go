package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/darthShadow/shunpike/coordination/server"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/auditmutation/audittest"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/directconn"
	"github.com/Silo-Server/silo-server/internal/models"
)

// auditPool is the database the enrollment audit writes to, with a switch that
// makes it refuse every transaction.
type auditPool struct {
	*pgxpool.Pool
	down atomic.Bool
}

func (p *auditPool) Begin(ctx context.Context) (pgx.Tx, error) {
	if p.down.Load() {
		return nil, errors.New("audit database unavailable")
	}
	return p.Pool.Begin(ctx)
}

// The first enrollment of a node key for a device, a key replacement included,
// records one domain row naming the device and the key, never the ticket. A
// renewal of the same key records none, and an audit write that fails ends the
// enrollment: the call errors and the new entry is withdrawn.
func TestDirectConnectionEnrollmentAuditDB(t *testing.T) {
	pool := &auditPool{Pool: scopedRecoveryDB(t)}
	users := auth.NewUserRepository(pool.Pool)
	user, err := users.Create(t.Context(), models.CreateUserInput{Username: "direct-audit-" + uuid.NewString(), Email: uuid.NewString() + "@example.test", Password: "original-password", Role: models.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = users.Delete(context.WithoutCancel(t.Context()), user.ID) })
	store := directconn.NewCoordinationStore(nil, "")
	coord, err := directconn.NewCoordination(directconn.CoordinationConfig{Store: store, AuthSecret: directTestSecret, Audit: directconn.NewEnrollmentAudit(pool)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, _ := audittest.Context(t, pool.Pool, audittest.Request{Method: http.MethodPost, Pattern: Prefix + "/direct-connections/enrollment", Path: Prefix + "/direct-connections/enrollment", UserID: &user.ID, OptIn: true})
	enroll := func(device string, key int) (directconn.Enrollment, error) {
		return coord.Enroll(ctx, directconn.EnrollRequest{UserID: user.ID, ProfileID: "p-owner", SessionID: "s1", DeviceID: device, NodeKey: directTestKey(key), ConnectionPath: "automatic"})
	}
	rows := func(device string) []audittest.Row {
		return audittest.Rows(t, pool.Pool, auditmutation.ActionDirectConnectionEnrolled, auditmutation.TargetDirectConnection, device)
	}

	first, err := enroll("tv", 1)
	if err != nil {
		t.Fatal(err)
	}
	row := audittest.One(t, pool.Pool, auditmutation.ActionDirectConnectionEnrolled, auditmutation.TargetDirectConnection, "tv")
	if row.UserID == nil || *row.UserID != user.ID {
		t.Errorf("row user = %v, want %d", row.UserID, user.ID)
	}
	for field, want := range map[string]string{"device_id": strconv.Quote("tv"), "node_key": strconv.Quote(directTestKey(1).String())} {
		change := row.Change(t, field)
		if change.Before != nil || change.After == nil || *change.After != want {
			t.Errorf("change %s = before %v after %v, want only after %s", field, change.Before, change.After, want)
		}
	}
	if len(row.Changes) != 2 {
		t.Errorf("changes = %+v, want the device ID and the node key only", row.Changes)
	}
	row.AssertNoSecrets(t, first.Ticket)

	if _, err := enroll("tv", 1); err != nil {
		t.Fatal(err)
	}
	if got := len(rows("tv")); got != 1 {
		t.Fatalf("rows after a renewal of the same key = %d, want 1", got)
	}

	if _, err := enroll("tv", 2); err != nil {
		t.Fatal(err)
	}
	got := rows("tv")
	if len(got) != 2 {
		t.Fatalf("rows after a key replacement = %d, want 2", len(got))
	}
	if change := got[1].Change(t, "node_key"); change.After == nil || *change.After != strconv.Quote(directTestKey(2).String()) {
		t.Errorf("replacement row node_key = %s, want the new key", audittest.Value(change.After))
	}

	pool.down.Store(true)
	ticket, err := enroll("phone", 3)
	if err == nil || ticket.Ticket != "" {
		t.Fatalf("enrollment with a failing audit write: ticket issued = %t, err = %v; want an error and no ticket", ticket.Ticket != "", err)
	}
	if _, err := store.Device(ctx, server.DeviceRef{Account: server.AccountID(strconv.Itoa(user.ID)), Device: "phone"}, time.Now()); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("entry after a failed audit write: %v, want not found", err)
	}
	pool.down.Store(false)
	if got := len(rows("phone")); got != 0 {
		t.Fatalf("rows for the failed enrollment = %d, want 0", got)
	}
}
