package planstore

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/google/uuid"
)

func TestInitialReconciliationGlobalPagesAndEligibility(t *testing.T) {
	f := newInitialActivationFixture(t)
	f.begin(t)
	rows, err := f.store.ListInitialReconciliation(t.Context(), "", 1)
	if err != nil || len(rows) != 0 {
		t.Fatalf("live pending included: %v %v", rows, err)
	}
	f.acknowledge(t)
	// Additional attempts share the same account/registration but never a session.
	for range 2 {
		req := f.reservation
		req.ExpectedAdmissionID = f.binding.AdmissionID
		req.PlaybackAttemptID = uuid.NewString()
		reserved, err := f.store.ReserveAttempt(t.Context(), req)
		if err != nil {
			t.Fatal(err)
		}
		b := f.binding
		b.IntentID = uuid.NewString()
		b.Scope.SessionID = uuid.NewString()
		b.Fence.AttemptID = req.PlaybackAttemptID
		b.Fence.Incarnation = reserved.Authority.Incarnation
		if _, err := f.store.BeginInitialActivation(t.Context(), b); err != nil {
			t.Fatal(err)
		}
	}
	// Withdrawal is eligible even with a live owner and unchanged source ID.
	if _, err := f.pool.Exec(t.Context(), `UPDATE playback_source_registrations SET admission_state='retiring' WHERE user_id=$1`, f.userID); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	after := ""
	for {
		page, err := f.store.ListInitialReconciliation(t.Context(), after, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		id := page[0].Binding.Fence.AttemptID
		if seen[id] || id <= after || page[0].Binding.Source.AccountID != f.userID {
			t.Fatal("bad keyset or account")
		}
		seen[id] = true
		after = id
	}
	if len(seen) != 3 {
		t.Fatalf("pages=%d", len(seen))
	}
	other := newInitialActivationFixture(t)
	other.begin(t)
	if rows, err := f.store.ListInitialReconciliation(t.Context(), "", 100); err != nil || len(rows) != 3 {
		t.Fatalf("live second account must stay excluded: %v %v", rows, err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE playback_source_registrations SET admission_state='admitting' WHERE user_id=$1`, f.userID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE playback_v3_attempts SET control_lease_expires_at=clock_timestamp()-interval '1 hour',expires_at=clock_timestamp()-interval '1 minute' WHERE user_id=$1`, f.userID); err != nil {
		t.Fatal(err)
	}
	rows, err = f.store.ListInitialReconciliation(t.Context(), "", 100)
	if err != nil || len(rows) != 3 {
		t.Fatalf("expired retention omitted: %d %v", len(rows), err)
	}
	abortID := uuid.NewString()
	if _, err := f.store.AbortInitialActivation(t.Context(), f.binding, abortID); err != nil {
		t.Fatal(err)
	}
	rows, err = f.store.ListInitialReconciliation(t.Context(), "", 100)
	if err != nil || len(rows) != 3 {
		t.Fatalf("aborting omitted: %d %v", len(rows), err)
	}
	if _, err := f.store.CompleteInitialAbort(t.Context(), f.binding, abortID, f.receipt(t, terminalInitialReceipt(t, f, abortID))); err != nil {
		t.Fatal(err)
	}
	rows, err = f.store.ListInitialReconciliation(t.Context(), "", 100)
	if err != nil || len(rows) != 2 {
		t.Fatalf("terminal included: %d %v", len(rows), err)
	}
	for _, limit := range []int{0, 101} {
		if _, err := f.store.ListInitialReconciliation(t.Context(), "", limit); err == nil {
			t.Fatal("invalid limit")
		}
	}
}

func TestInitialReconciliationIncludesStoppingNotActivated(t *testing.T) {
	f := activatedLifecycleFixture(t)
	rows, err := f.store.ListInitialReconciliation(t.Context(), "", 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("activated included: %v %v", rows, err)
	}
	if _, err := f.store.BeginBoundStop(t.Context(), f.binding, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE playback_v3_attempts SET expires_at=clock_timestamp()-interval '1 second' WHERE user_id=$1`, f.userID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CleanupExpired(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	rows, err = f.store.ListInitialReconciliation(t.Context(), "", 10)
	if err != nil || len(rows) != 1 || rows[0].Phase != playback.InitialActivationStoppingV3 {
		t.Fatalf("stopping missing: %v %v", rows, err)
	}
}
