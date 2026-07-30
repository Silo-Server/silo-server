package notifications

import (
	"context"
	"database/sql"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type preferenceTransactionTestProvider struct {
	store userstore.UserStore
}

func (p preferenceTransactionTestProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func (preferenceTransactionTestProvider) Close() error { return nil }

func TestInterestTrackingStorePreservesPreferenceSettingsTransactions(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	provider := WrapUserStoreProvider(
		preferenceTransactionTestProvider{store: userdb.NewSQLiteUserStore(db)},
		&System{},
	)
	wrapped, err := provider.ForUser(context.Background(), 1)
	if err != nil {
		t.Fatalf("ForUser: %v", err)
	}
	transactioner, ok := wrapped.(userstore.PreferenceSettingsTransactioner)
	if !ok {
		t.Fatal("interest-tracking wrapper dropped PreferenceSettingsTransactioner")
	}

	called := false
	if err := transactioner.WithPreferenceSettingsTransaction(context.Background(),
		func(userstore.PreferenceSettingsWriter) error {
			called = true
			return nil
		}); err != nil {
		t.Fatalf("WithPreferenceSettingsTransaction: %v", err)
	}
	if !called {
		t.Fatal("transaction callback was not invoked")
	}
}
