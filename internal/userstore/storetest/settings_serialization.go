package storetest

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// RunSettingsWriterSerialization exercises read/merge/write plans across real
// connections. Every successful plan must observe the previous committed value,
// whether it enters through the preference API or the canonical settings API.
func RunSettingsWriterSerialization(t *testing.T, newStore func(*testing.T) userstore.UserStore) {
	for _, mixed := range []bool{false, true} {
		t.Run(fmt.Sprintf("canonical_writers_%t", mixed), func(t *testing.T) {
			ctx := t.Context()
			store := newStore(t)
			if err := store.CreateProfile(ctx, userstore.Profile{ID: "p1", Name: "Main"}); err != nil {
				t.Fatal(err)
			}
			id := userstore.SettingIdentity{Key: "playback.max_bitrate_kbps", Scope: settingscontract.ScopeProfile, ProfileID: "p1"}
			if _, err := store.UpsertSettingValue(ctx, id, json.RawMessage(`0`)); err != nil {
				t.Fatal(err)
			}
			preference, ok := store.(userstore.PreferenceSettingsTransactioner)
			if !ok {
				t.Fatal("store lacks preference transactions")
			}
			canonical, ok := store.(userstore.SettingMutationTransactioner)
			if !ok {
				t.Fatal("store lacks canonical setting transactions")
			}
			const updates = 32
			start := make(chan struct{})
			errs := make(chan error, updates)
			var wg sync.WaitGroup
			for i := range updates {
				wg.Go(func() {
					<-start
					// The two interfaces intentionally share the read and write methods
					// whose serialization protects omitted fields in preference plans.
					increment := func(writer interface {
						GetSettingValue(ctx context.Context, id userstore.SettingIdentity) (*userstore.SettingValue, error)
						UpsertSettingValue(ctx context.Context, id userstore.SettingIdentity, value json.RawMessage) (*userstore.SettingValue, error)
					}) error {
						row, err := writer.GetSettingValue(ctx, id)
						if err != nil {
							return err
						}
						var count int
						if err := json.Unmarshal(row.Value, &count); err != nil {
							return err
						}
						next, err := json.Marshal(count + 1)
						if err != nil {
							return err
						}
						_, err = writer.UpsertSettingValue(ctx, id, next)
						return err
					}
					if mixed && i%2 == 0 {
						// Empty mutation IDs are the v2 canonical path; v1 receipt IDs must
						// participate in the same account lock as well.
						mutationID := ""
						if i%4 == 0 {
							mutationID = fmt.Sprintf("mutation-%d", i)
						}
						errs <- canonical.WithSettingMutationTransaction(ctx, mutationID, func(w userstore.SettingMutationWriter) error { return increment(w) })
					} else {
						errs <- preference.WithPreferenceSettingsTransaction(ctx, func(w userstore.PreferenceSettingsWriter) error { return increment(w) })
					}
				})
			}
			close(start)
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Errorf("concurrent plan: %v", err)
				}
			}
			row, err := store.GetSettingValue(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if string(row.Value) != fmt.Sprint(updates) {
				t.Fatalf("committed value=%s, want %d successful serialized plans", row.Value, updates)
			}
		})
	}
}
