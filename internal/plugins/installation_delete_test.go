package plugins

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestDeleteThenRefusesCallbackInTransaction: on a store bound to a caller's
// transaction the delete commits nothing, so an after-commit callback would
// run too early. DeleteThen refuses before touching the database.
func TestDeleteThenRefusesCallbackInTransaction(t *testing.T) {
	store := (&InstallationStore{}).WithTx(struct{ pgx.Tx }{})
	ran := false
	err := store.DeleteThen(t.Context(), 1, func() { ran = true })
	if !errors.Is(err, errDeleteThenInTx) || ran {
		t.Fatalf("DeleteThen in a transaction = %v, callback ran = %v; want errDeleteThenInTx and no callback", err, ran)
	}
}
