package api

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

type discordWiringStore struct{ userstore.UserStore }

func (discordWiringStore) GetProfile(_ context.Context, id string) (*userstore.Profile, error) {
	return &userstore.Profile{ID: id, IsPrimary: id == "parent", PINHash: "synthetic-hash", PINRevision: 2}, nil
}

type discordWiringStores struct{ userstore.UserStoreProvider }

func (discordWiringStores) ForUser(context.Context, int) (userstore.UserStore, error) {
	return discordWiringStore{}, nil
}

// The Discord destination is account-wide, so only the household manager may
// change it: the primary profile, with its PIN proof when it has a PIN.
func TestDiscordNotificationConstructorWiresHouseholdAuthority(t *testing.T) {
	h := newDiscordNotificationsHandler(Dependencies{UserStoreProvider: discordWiringStores{}})
	errBadProof := errors.New("bad proof")
	for _, tc := range []struct {
		profile string
		verify  func(string) error
		allowed bool
	}{
		{"parent", func(string) error { return nil }, true},
		{"parent", func(string) error { return errBadProof }, false},
		{"member", func(string) error { return nil }, false},
	} {
		allowed, err := h.MayManageDiscordNotifications(context.Background(), 1, tc.profile, tc.verify)
		if allowed != tc.allowed || (err != nil && !errors.Is(err, errBadProof)) {
			t.Fatalf("profile %s: allowed=%v err=%v", tc.profile, allowed, err)
		}
	}
}
