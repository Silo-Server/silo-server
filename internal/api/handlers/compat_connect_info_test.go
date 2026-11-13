package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// connectInfoUsers serves one account by id.
type connectInfoUsers struct {
	user *models.User
}

func (r connectInfoUsers) GetByID(_ context.Context, id int) (*models.User, error) {
	if r.user == nil || r.user.ID != id {
		return nil, auth.ErrNotFound
	}
	return r.user, nil
}

// connectInfoSignIn answers for the account it is asked about and records it.
// Which accounts can sign in is auth.Service's rule, pinned by
// TestPasswordSignInAvailableDB.
type connectInfoSignIn struct {
	available bool
	err       error
	asked     *int
}

func (s connectInfoSignIn) PasswordSignInAvailable(_ context.Context, user *models.User) (bool, error) {
	*s.asked = user.ID
	return s.available, s.err
}

func TestCompatConnectInfoPasswordLoginAvailable(t *testing.T) {
	account := models.User{ID: 7, PasswordHash: "$2a$hash"}
	otherAccount := account
	otherAccount.ID = 8

	tests := []struct {
		name      string
		user      *models.User
		available bool
		err       error
		noSignIn  bool
		want      bool
	}{
		{name: "account that can sign in", user: &account, available: true, want: true},
		{name: "account that cannot sign in", user: &account, available: false, want: false},
		{name: "unreadable answer keeps the password instructions", user: &account, err: errors.New("settings unavailable"), want: true},
		{name: "unknown account keeps the password instructions", user: &otherAccount, want: true},
		{name: "no sign-in service keeps the password instructions", user: &account, noSignIn: true, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asked := 0
			var signIn CompatPasswordSignIn = connectInfoSignIn{available: tt.available, err: tt.err, asked: &asked}
			if tt.noSignIn {
				signIn = nil
			}
			handler := NewCompatConnectInfoHandler(&config.Config{}, &fakeServerSettingsStore{}, connectInfoUsers{user: tt.user}, signIn)
			ctx := middleware.SetClaims(t.Context(), &auth.Claims{UserID: 7})

			got := handler.GetConnectInfo(ctx).Account.PasswordLoginAvailable
			if got != tt.want {
				t.Fatalf("password_login_available = %v, want %v", got, tt.want)
			}
			if tt.user.ID == 7 && !tt.noSignIn && asked != 7 {
				t.Fatalf("asked about account %d, want the requesting account 7", asked)
			}
		})
	}
}
