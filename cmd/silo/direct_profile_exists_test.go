package main

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// profileStore answers GetProfile with a fixed result; any other method panics
// on the nil embedded store.
type profileStore struct {
	userstore.UserStore
	profile *userstore.Profile
	err     error
}

func (s profileStore) GetProfile(context.Context, string) (*userstore.Profile, error) {
	return s.profile, s.err
}

// A profile store returns no profile and no error for a deleted profile, so
// "gone" is the nil profile, and a read failure is an error, never "gone".
func TestProfileExistsTreatsANilProfileAsDeletedAndAnErrorAsUnknown(t *testing.T) {
	for _, tc := range []struct {
		name    string
		store   profileStore
		want    bool
		wantErr bool
	}{
		{name: "present", store: profileStore{profile: &userstore.Profile{ID: "p"}}, want: true},
		{name: "deleted", store: profileStore{}},
		{name: "read fails", store: profileStore{err: errors.New("postgres down")}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := profileExists(singleStoreProvider{store: tc.store})(t.Context(), 7, "p")

			if got != tc.want || (err != nil) != tc.wantErr {
				t.Fatalf("profileExists = %t, %v; want %t, error %t", got, err, tc.want, tc.wantErr)
			}
		})
	}
}
