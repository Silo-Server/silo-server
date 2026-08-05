package notifications

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
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

func TestInterestTrackingStorePreservesSettingCapabilities(t *testing.T) {
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

	cas, ok := wrapped.(userstore.SettingValueCompareAndSetter)
	if !ok {
		t.Fatal("interest-tracking wrapper dropped SettingValueCompareAndSetter")
	}
	identity := userstore.SettingIdentity{Key: "ui.test", Scope: settingscontract.ScopeAccount}
	first, err := cas.CompareAndSetSettingValue(
		context.Background(), identity, json.RawMessage(`"first"`), 0)
	if err != nil {
		t.Fatalf("CompareAndSetSettingValue: %v", err)
	}

	mutationTx, ok := wrapped.(userstore.SettingMutationTransactioner)
	if !ok {
		t.Fatal("interest-tracking wrapper dropped SettingMutationTransactioner")
	}
	called = false
	if err := mutationTx.WithSettingMutationTransaction(context.Background(), "wrapped-mutation",
		func(writer userstore.SettingMutationWriter) error {
			called = true
			if _, err := writer.CompareAndSetSettingValue(
				context.Background(), identity, json.RawMessage(`"second"`), first.Revision); err != nil {
				return err
			}
			_, _, err := writer.PutSettingMutation(context.Background(), userstore.SettingMutationRecord{
				MutationID: "wrapped-mutation", RequestHash: "hash",
				Result: json.RawMessage(`{"ok":true}`), ExpiresAt: time.Now().UTC().Add(time.Hour),
			})
			return err
		}); err != nil {
		t.Fatalf("WithSettingMutationTransaction: %v", err)
	}
	if !called {
		t.Fatal("mutation transaction callback was not invoked")
	}
	stored, err := wrapped.GetSettingValue(context.Background(), identity)
	if err != nil || stored == nil || stored.Revision != 2 {
		t.Fatalf("wrapped setting after transaction = %+v (%v), want revision 2", stored, err)
	}
	receipt, err := wrapped.GetSettingMutation(context.Background(), "wrapped-mutation")
	if err != nil || receipt == nil || receipt.RequestHash != "hash" {
		t.Fatalf("wrapped receipt = %+v (%v)", receipt, err)
	}
}
