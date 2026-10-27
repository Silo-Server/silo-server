package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

type discordAuthorityStore struct {
	userstore.UserStore
	profiles []userstore.Profile
	err      error
}

func (s discordAuthorityStore) GetProfile(_ context.Context, id string) (*userstore.Profile, error) {
	if s.err != nil {
		return nil, s.err
	}
	for i := range s.profiles {
		if s.profiles[i].ID == id {
			return &s.profiles[i], nil
		}
	}
	return nil, nil
}
func (s discordAuthorityStore) ListProfiles(context.Context) ([]userstore.Profile, error) {
	return s.profiles, s.err
}

type discordAuthorityStores struct {
	userstore.UserStoreProvider
	store userstore.UserStore
	err   error
}

func (p discordAuthorityStores) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, p.err
}

// The Discord destination is account-wide, so an unreadable household never
// admits a manager: no store denies, and a failed read is an error.
func TestDiscordHouseholdAdmissionFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stores  userstore.UserStoreProvider
		wantErr bool
	}{
		{"unwired", nil, false},
		{"nil store", discordAuthorityStores{}, false},
		{"open failure", discordAuthorityStores{err: errors.New("synthetic store failure")}, true},
		{"read failure", discordAuthorityStores{store: discordAuthorityStore{err: errors.New("synthetic read failure")}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewDiscordNotificationsHandler(nil, "")
			h.Stores = tc.stores
			allowed, err := h.MayManageDiscordNotifications(t.Context(), 1, "primary", func(string) error { return nil })
			if allowed {
				t.Fatal("admitted a manager without reading the household")
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
		})
	}
}
