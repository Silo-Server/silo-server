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

// connectInfoUsers serves one account by id and the server-wide local
// password switch.
type connectInfoUsers struct {
	user         *models.User
	switchOff    bool
	switchFailed bool
}

func (r connectInfoUsers) GetByID(_ context.Context, id int) (*models.User, error) {
	if r.user == nil || r.user.ID != id {
		return nil, auth.ErrNotFound
	}
	return r.user, nil
}

func (r connectInfoUsers) LocalPasswordLoginAllowed(context.Context) (bool, error) {
	if r.switchFailed {
		return false, errors.New("settings unavailable")
	}
	return !r.switchOff, nil
}

func TestCompatConnectInfoPasswordLoginAvailable(t *testing.T) {
	passwordAccount := models.User{ID: 7, LocalPasswordLoginEnabled: true, PasswordHash: "$2a$hash"}
	providerAccount := models.User{ID: 7, PasswordHash: "$2a$unusable"}
	breakGlass := passwordAccount
	breakGlass.BreakGlass = true
	noHash := passwordAccount
	noHash.PasswordHash = ""
	otherAccount := passwordAccount
	otherAccount.ID = 8

	tests := []struct {
		name  string
		users connectInfoUsers
		want  bool
	}{
		{name: "password account", users: connectInfoUsers{user: &passwordAccount}, want: true},
		{name: "provider account", users: connectInfoUsers{user: &providerAccount}, want: false},
		{name: "password account, switch off", users: connectInfoUsers{user: &passwordAccount, switchOff: true}, want: false},
		{name: "break-glass account, switch off", users: connectInfoUsers{user: &breakGlass, switchOff: true}, want: true},
		{name: "account without a password", users: connectInfoUsers{user: &noHash}, want: false},
		{name: "unreadable switch counts as on", users: connectInfoUsers{user: &passwordAccount, switchFailed: true}, want: true},
		{name: "unknown account keeps the password instructions", users: connectInfoUsers{user: &otherAccount, switchOff: true}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewCompatConnectInfoHandler(&config.Config{}, &fakeServerSettingsStore{}, tt.users)
			ctx := middleware.SetClaims(t.Context(), &auth.Claims{UserID: 7})

			got := handler.GetConnectInfo(ctx).Account.PasswordLoginAvailable
			if got != tt.want {
				t.Fatalf("password_login_available = %v, want %v", got, tt.want)
			}
		})
	}
}
