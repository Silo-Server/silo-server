package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/config"
)

// connectInfoSignIn answers PasswordSignInFor for account 7.
type connectInfoSignIn struct {
	answer auth.PasswordSignIn
	err    error
}

func (s connectInfoSignIn) PasswordSignInFor(_ context.Context, userID int) (auth.PasswordSignIn, error) {
	if userID != 7 {
		return auth.PasswordSignIn{}, auth.ErrNotFound
	}
	return s.answer, s.err
}

// The decision itself is auth.Service.PasswordSignInFor's
// (TestPasswordSignInForDB); a failed lookup must not read as "this account
// cannot sign in".
func TestCompatConnectInfoAccount(t *testing.T) {
	tests := []struct {
		name   string
		signIn connectInfoSignIn
		want   CompatConnectAccountInfo
	}{
		{name: "directory account", signIn: connectInfoSignIn{answer: auth.PasswordSignIn{Directory: true, DirectoryUsername: "alice"}},
			want: CompatConnectAccountInfo{DirectoryLoginAvailable: true, DirectoryUsername: "alice"}},
		{name: "no password signs in", signIn: connectInfoSignIn{}, want: CompatConnectAccountInfo{}},
		{name: "failed lookup keeps the password instructions", signIn: connectInfoSignIn{err: errors.New("database unavailable")},
			want: CompatConnectAccountInfo{PasswordLoginAvailable: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewCompatConnectInfoHandler(&config.Config{}, &fakeServerSettingsStore{}, tt.signIn)
			ctx := middleware.SetClaims(t.Context(), &auth.Claims{UserID: 7})

			if got := handler.GetConnectInfo(ctx).Account; got != tt.want {
				t.Fatalf("account = %+v, want %+v", got, tt.want)
			}
		})
	}
}
